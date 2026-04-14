package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log"
	"math/rand"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf16"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"trigger-admin-bot/internal/engine"
	"trigger-admin-bot/internal/gpt"
	"trigger-admin-bot/internal/spotifymusic"
	"trigger-admin-bot/internal/trigger"
	"trigger-admin-bot/internal/vk"
)

var chatErrorLogEnabled bool
var debugTriggerLogEnabled bool
var debugGPTLogEnabled bool

type chatAllowList struct {
	enabled bool
	ids     map[int64]struct{}
}

func parseAllowedChatIDs(raw string) (chatAllowList, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return chatAllowList{enabled: false}, nil
	}
	parts := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || r == ' ' || r == '\n' || r == '\t'
	})
	ids := make(map[int64]struct{}, len(parts))
	for _, part := range parts {
		v := strings.TrimSpace(part)
		if v == "" {
			continue
		}
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return chatAllowList{}, fmt.Errorf("invalid ALLOWED_CHAT_IDS value %q: %w", v, err)
		}
		ids[id] = struct{}{}
	}
	if len(ids) == 0 {
		return chatAllowList{enabled: false}, nil
	}
	return chatAllowList{enabled: true, ids: ids}, nil
}

func (a chatAllowList) Allows(chatID int64) bool {
	if !a.enabled {
		return true
	}
	_, ok := a.ids[chatID]
	return ok
}

type adminCacheEntry struct {
	isAdmin   bool
	expiresAt time.Time
}

type adminStatusCache struct {
	mu     sync.RWMutex
	ttl    time.Duration
	store  *Store
	values map[string]adminCacheEntry
	chats  map[int64]time.Time
}

type recentChatMessage struct {
	MessageID int
	UserName  string
	Text      string
	At        time.Time
}

type chatRecentStore struct {
	mu       sync.RWMutex
	maxPer   int
	maxAge   time.Duration
	messages map[int64][]recentChatMessage
}

type chatUserIndex struct {
	mu      sync.RWMutex
	byChat  map[int64]map[string]int64
	byID    map[int64]map[int64]string
	maxSize int
}

type readonlyManager struct {
	mu     sync.Mutex
	on     map[int64]bool
	timers map[int64]*time.Timer
}

type moderationRequest struct {
	Action      string
	Silent      bool
	Targets     []string
	Duration    time.Duration
	DurationRaw string
	Reason      string
}

type rawMessageEntity struct {
	Type          string `json:"type"`
	CustomEmojiID string `json:"custom_emoji_id"`
	Offset        int    `json:"offset"`
	Length        int    `json:"length"`
}

type rawMessageWithEmoji struct {
	Entities        []rawMessageEntity   `json:"entities"`
	CaptionEntities []rawMessageEntity   `json:"caption_entities"`
	Text            string               `json:"text"`
	Caption         string               `json:"caption"`
	ReplyToMessage  *rawMessageWithEmoji `json:"reply_to_message"`
}

type rawUpdateWithEmoji struct {
	Message      *rawMessageWithEmoji  `json:"message"`
	ChatMember   *rawChatMemberUpdated `json:"chat_member"`
	MyChatMember *rawChatMemberUpdated `json:"my_chat_member"`
}

type updateWithEmojiMeta struct {
	Update          tgbotapi.Update
	RawMessage      *rawMessageWithEmoji
	RawChatMember   *rawChatMemberUpdated
	RawMyChatMember *rawChatMemberUpdated
}

type rawChatMemberUpdated struct {
	Chat          *rawChat       `json:"chat"`
	From          *rawUser       `json:"from"`
	Date          int64          `json:"date"`
	OldChatMember *rawChatMember `json:"old_chat_member"`
	NewChatMember *rawChatMember `json:"new_chat_member"`
}

type rawChat struct {
	ID    int64  `json:"id"`
	Type  string `json:"type"`
	Title string `json:"title"`
}

type rawUser struct {
	ID        int64  `json:"id"`
	IsBot     bool   `json:"is_bot"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	UserName  string `json:"username"`
}

type rawChatMember struct {
	User   *rawUser `json:"user"`
	Status string   `json:"status"`
}

type customEmojiHit struct {
	ID       string
	Fallback string
}

func newChatRecentStore(maxPer int, maxAge time.Duration) *chatRecentStore {
	if maxPer <= 0 {
		maxPer = 8
	}
	if maxAge <= 0 {
		maxAge = 30 * time.Minute
	}
	return &chatRecentStore{
		maxPer:   maxPer,
		maxAge:   maxAge,
		messages: make(map[int64][]recentChatMessage),
	}
}

func (s *chatRecentStore) Add(chatID int64, msg recentChatMessage) {
	if s == nil || chatID == 0 || strings.TrimSpace(msg.Text) == "" {
		return
	}
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	items := s.messages[chatID]
	filtered := make([]recentChatMessage, 0, len(items)+1)
	for _, it := range items {
		if now.Sub(it.At) <= s.maxAge {
			filtered = append(filtered, it)
		}
	}
	filtered = append(filtered, msg)
	if len(filtered) > s.maxPer {
		filtered = filtered[len(filtered)-s.maxPer:]
	}
	s.messages[chatID] = filtered
}

func (s *chatRecentStore) RecentText(chatID int64, limit int) string {
	if s == nil || chatID == 0 {
		return ""
	}
	if limit <= 0 {
		limit = 4
	}
	now := time.Now()
	s.mu.RLock()
	items := s.messages[chatID]
	s.mu.RUnlock()
	if len(items) == 0 {
		return ""
	}

	start := len(items) - limit
	if start < 0 {
		start = 0
	}
	lines := make([]string, 0, len(items)-start)
	for _, it := range items[start:] {
		if now.Sub(it.At) > s.maxAge {
			continue
		}
		txt := strings.TrimSpace(it.Text)
		if txt == "" {
			continue
		}
		txt = clipText(txt, 220)
		user := strings.TrimSpace(it.UserName)
		if user == "" {
			user = "участник"
		}
		lines = append(lines, fmt.Sprintf("- %s: %s", user, txt))
	}
	return strings.Join(lines, "\n")
}

func executeGPTPromptTask(task gpt.PromptTask) {
	if task.Bot == nil || task.Msg == nil {
		return
	}
	tmplCtx := newTemplateContext(task.Bot, task.Msg, &task.Trigger, task.TemplateLookup)
	out, err := generateChatGPTReply(tmplCtx, pickResponseVariantText(task.Trigger.ResponseText), task.RecentContext)
	if err != nil {
		log.Printf("gpt prompt failed: %v", err)
		reportChatFailure(task.Bot, task.Msg.Chat.ID, "ошибка запроса к ChatGPT", err)
		return
	}
	out = expandTemplateCalls(out, task.TemplateLookup)
	replyTo := 0
	if task.Trigger.Reply || task.Trigger.TriggerMode == "command_reply" {
		replyTo = task.Msg.MessageID
	}
	rawOut := out
	if debugGPTLogEnabled {
		log.Printf("gpt flow trigger=%d chat=%d msg=%d raw_len=%d raw_tgemoji=%d raw=%q",
			task.Trigger.ID, task.Msg.Chat.ID, task.Msg.MessageID, len(rawOut), countTGEmojiTags(rawOut), clipText(rawOut, 1400))
	}
	out = canonicalizeTGEmojiTags(out)
	if debugGPTLogEnabled {
		log.Printf("gpt flow trigger=%d canonical_len=%d canonical_tgemoji=%d canonical=%q",
			task.Trigger.ID, len(out), countTGEmojiTags(out), clipText(out, 1400))
	}
	sent := false
	sendMode := "markdown"
	hasHTML := containsTelegramHTMLMarkup(out)
	if debugGPTLogEnabled {
		log.Printf("gpt flow trigger=%d has_html=%v", task.Trigger.ID, hasHTML)
	}
	sendCtx := sendContext{Bot: task.Bot, ChatID: task.Msg.Chat.ID, ReplyTo: replyTo}
	if hasHTML {
		htmlOut := markdownToTelegramHTMLLite(out)
		if debugGPTLogEnabled {
			log.Printf("gpt flow trigger=%d html_len=%d html_tgemoji=%d html=%q",
				task.Trigger.ID, len(htmlOut), countTGEmojiTags(htmlOut), clipText(htmlOut, 1400))
		}
		if ok := sendHTML(sendCtx, htmlOut, task.Trigger.Preview); ok {
			sent = true
			sendMode = "html"
		} else {
			fallbackText := replaceTGEmojiTagsWithFallback(out)
			if debugGPTLogEnabled {
				log.Printf("gpt flow trigger=%d html_send_failed fallback_len=%d fallback_tgemoji=%d fallback=%q",
					task.Trigger.ID, len(fallbackText), countTGEmojiTags(fallbackText), clipText(fallbackText, 1400))
			}
			if ok := sendMarkdownV2(sendCtx, fallbackText, task.Trigger.Preview); ok {
				sent = true
				sendMode = "markdown(fallback)"
			}
		}
	} else if ok := sendMarkdownV2(sendCtx, out, task.Trigger.Preview); ok {
		sent = true
	}
	if sent {
		if task.IdleMarkActivity != nil {
			task.IdleMarkActivity(task.ChatID, time.Now())
		}
		deleteTriggerSourceMessage(task.Bot, task.Msg, &task.Trigger)
	}
	if debugTriggerLogEnabled {
		log.Printf("send gpt/%s attempted trigger=%d replyTo=%d", sendMode, task.Trigger.ID, replyTo)
	}
}

func deleteTriggerSourceMessage(bot *tgbotapi.BotAPI, msg *tgbotapi.Message, tr *Trigger) {
	if bot == nil || msg == nil || tr == nil {
		return
	}
	if !tr.DeleteSource {
		return
	}
	if tr.ActionType == "delete" {
		return
	}
	_, err := bot.Request(tgbotapi.DeleteMessageConfig{
		ChatID:    msg.Chat.ID,
		MessageID: msg.MessageID,
	})
	if err != nil && debugTriggerLogEnabled {
		log.Printf("delete source msg failed trigger=%d chat=%d msg=%d err=%v", tr.ID, msg.Chat.ID, msg.MessageID, err)
	}
}

var tgEmojiLooseRe = regexp.MustCompile(`(?is)"?<tg-emoji\s+emoji-id\s*=\s*"?(?P<id>\d+)"?\s*>"?(?P<fallback>.*?)"?</tg-emoji>"?`)
var tgEmojiCanonicalRe = regexp.MustCompile(`(?is)<tg-emoji[^>]*>(.*?)</tg-emoji>`)
var telegramHTMLTagRe = regexp.MustCompile(`(?is)<\s*/?\s*(b|strong|i|em|u|ins|s|strike|del|code|pre|blockquote|a|tg-spoiler|tg-emoji)\b`)
var templateCallPattern = regexp.MustCompile(`\{\{\s*template\s+\"([^\"]+)\"\s*\}\}`)

func canonicalizeTGEmojiTags(s string) string {
	if strings.TrimSpace(s) == "" {
		return s
	}
	s = strings.ReplaceAll(s, `\"`, `"`)
	s = strings.ReplaceAll(s, "&lt;", "<")
	s = strings.ReplaceAll(s, "&gt;", ">")
	s = strings.ReplaceAll(s, "&quot;", `"`)
	if !strings.Contains(strings.ToLower(s), "tg-emoji") {
		return s
	}
	return tgEmojiLooseRe.ReplaceAllStringFunc(s, func(m string) string {
		sub := tgEmojiLooseRe.FindStringSubmatch(m)
		if len(sub) < 3 {
			return m
		}
		id := strings.TrimSpace(sub[1])
		fallback := strings.TrimSpace(sub[2])
		if fallback == "" {
			fallback = "🙂"
		}
		return fmt.Sprintf(`<tg-emoji emoji-id="%s">%s</tg-emoji>`, id, fallback)
	})
}

func replaceTGEmojiTagsWithFallback(s string) string {
	if strings.TrimSpace(s) == "" {
		return s
	}
	return tgEmojiCanonicalRe.ReplaceAllStringFunc(s, func(m string) string {
		sub := tgEmojiCanonicalRe.FindStringSubmatch(m)
		if len(sub) < 2 {
			return "🙂"
		}
		fallback := strings.TrimSpace(sub[1])
		if fallback == "" {
			return "🙂"
		}
		return fallback
	})
}

func containsTelegramHTMLMarkup(s string) bool {
	if strings.TrimSpace(s) == "" {
		return false
	}
	return telegramHTMLTagRe.FindStringIndex(s) != nil
}

func countTGEmojiTags(s string) int {
	if strings.TrimSpace(s) == "" {
		return 0
	}
	return len(tgEmojiCanonicalRe.FindAllString(s, -1))
}

var mdFenceRe = regexp.MustCompile("(?s)```([a-zA-Z0-9_+-]*)\\n(.*?)```")
var mdInlineCodeRe = regexp.MustCompile("`([^`\\n]+)`")
var mdLinkRe = regexp.MustCompile(`\[(.*?)\]\((https?://[^\s)]+)\)`)
var mdSpoilerRe = regexp.MustCompile(`\|\|(.+?)\|\|`)

func markdownToTelegramHTMLLite(s string) string {
	if strings.TrimSpace(s) == "" {
		return s
	}
	// Fenced code blocks first.
	s = mdFenceRe.ReplaceAllStringFunc(s, func(m string) string {
		sub := mdFenceRe.FindStringSubmatch(m)
		if len(sub) < 3 {
			return m
		}
		lang := strings.TrimSpace(sub[1])
		code := html.EscapeString(sub[2])
		if lang != "" {
			return `<pre><code class="language-` + html.EscapeString(lang) + `">` + code + `</code></pre>`
		}
		return `<pre><code>` + code + `</code></pre>`
	})
	// Inline code.
	s = mdInlineCodeRe.ReplaceAllStringFunc(s, func(m string) string {
		sub := mdInlineCodeRe.FindStringSubmatch(m)
		if len(sub) < 2 {
			return m
		}
		return `<code>` + html.EscapeString(sub[1]) + `</code>`
	})
	// Links.
	s = mdLinkRe.ReplaceAllStringFunc(s, func(m string) string {
		sub := mdLinkRe.FindStringSubmatch(m)
		if len(sub) < 3 {
			return m
		}
		txt := html.EscapeString(strings.TrimSpace(sub[1]))
		u := strings.TrimSpace(sub[2])
		if txt == "" || u == "" {
			return m
		}
		return `<a href="` + html.EscapeString(u) + `">` + txt + `</a>`
	})
	// Spoiler.
	s = mdSpoilerRe.ReplaceAllStringFunc(s, func(m string) string {
		sub := mdSpoilerRe.FindStringSubmatch(m)
		if len(sub) < 2 {
			return m
		}
		return `<tg-spoiler>` + html.EscapeString(sub[1]) + `</tg-spoiler>`
	})
	return s
}

func buildTemplateLookup(store *Store) func(string) string {
	var mu sync.Mutex
	cache := map[string]string{}
	var lastLoad time.Time
	load := func() {
		items, err := store.ListTemplates()
		if err != nil {
			return
		}
		next := map[string]string{}
		for _, it := range items {
			k := strings.TrimSpace(it.Key)
			if k == "" {
				continue
			}
			next[k] = it.Text
		}
		cache = next
		lastLoad = time.Now()
	}
	return func(key string) string {
		key = strings.TrimSpace(key)
		if key == "" || store == nil {
			return ""
		}
		mu.Lock()
		if lastLoad.IsZero() || time.Since(lastLoad) > 10*time.Second {
			load()
		}
		val := cache[key]
		if val == "" {
			load()
			val = cache[key]
		}
		mu.Unlock()
		return val
	}
}

func expandTemplateCalls(input string, lookup func(string) string) string {
	if strings.TrimSpace(input) == "" || lookup == nil {
		return input
	}
	const maxDepth = 3
	out := input
	for i := 0; i < maxDepth; i++ {
		changed := false
		out = templateCallPattern.ReplaceAllStringFunc(out, func(m string) string {
			sub := templateCallPattern.FindStringSubmatch(m)
			if len(sub) < 2 {
				return m
			}
			key := strings.TrimSpace(sub[1])
			if key == "" {
				return ""
			}
			val := lookup(key)
			if val == "" {
				return ""
			}
			changed = true
			return val
		})
		if !changed {
			break
		}
	}
	return out
}

func newAdminStatusCache(ttl time.Duration, store *Store) *adminStatusCache {
	if ttl <= 0 {
		ttl = 2 * time.Minute
	}
	return &adminStatusCache{
		ttl:    ttl,
		store:  store,
		values: make(map[string]adminCacheEntry),
		chats:  make(map[int64]time.Time),
	}
}

func adminCacheKey(chatID, userID int64) string {
	return strconv.FormatInt(chatID, 10) + ":" + strconv.FormatInt(userID, 10)
}

func (c *adminStatusCache) setCached(chatID, userID int64, isAdmin bool, now time.Time) {
	if c == nil {
		return
	}
	key := adminCacheKey(chatID, userID)
	c.mu.Lock()
	c.values[key] = adminCacheEntry{
		isAdmin:   isAdmin,
		expiresAt: now.Add(c.ttl),
	}
	c.mu.Unlock()
}

func (c *adminStatusCache) chatFresh(chatID int64, now time.Time) bool {
	if c == nil || chatID == 0 {
		return false
	}
	c.mu.RLock()
	exp, ok := c.chats[chatID]
	c.mu.RUnlock()
	return ok && now.Before(exp)
}

func (c *adminStatusCache) markChatFresh(chatID int64, now time.Time) {
	if c == nil || chatID == 0 {
		return
	}
	c.mu.Lock()
	c.chats[chatID] = now.Add(c.ttl)
	c.mu.Unlock()
}

func (c *adminStatusCache) EnsureChatAdminsFresh(bot *tgbotapi.BotAPI, chatID int64) (int, error) {
	if c == nil {
		return 0, errors.New("admin cache is nil")
	}
	if bot == nil || chatID == 0 {
		return 0, errors.New("invalid chat preload params")
	}
	now := time.Now()
	if c.chatFresh(chatID, now) {
		return 0, nil
	}

	if c.store != nil {
		updatedAt, count, ok, err := c.store.GetChatAdminSync(chatID)
		if err == nil && ok {
			if now.Sub(time.Unix(updatedAt, 0)) < c.ttl {
				c.markChatFresh(chatID, now)
				return count, nil
			}
		}
	}
	return c.ReloadChatAdmins(bot, chatID)
}

func (c *adminStatusCache) IsChatAdmin(bot *tgbotapi.BotAPI, chatID, userID int64) bool {
	if c == nil {
		return fetchChatAdminStatus(bot, chatID, userID)
	}
	key := adminCacheKey(chatID, userID)
	now := time.Now()

	c.mu.RLock()
	if cached, ok := c.values[key]; ok && now.Before(cached.expiresAt) {
		c.mu.RUnlock()
		return cached.isAdmin
	}
	c.mu.RUnlock()

	_, _ = c.EnsureChatAdminsFresh(bot, chatID)

	c.mu.RLock()
	if cached, ok := c.values[key]; ok && now.Before(cached.expiresAt) {
		c.mu.RUnlock()
		return cached.isAdmin
	}
	c.mu.RUnlock()

	if c.store != nil {
		isAdmin, updatedAt, ok, err := c.store.GetChatAdminCache(chatID, userID)
		if err == nil && ok {
			ts := time.Unix(updatedAt, 0)
			if now.Sub(ts) < c.ttl {
				expiresAt := ts.Add(c.ttl)
				if expiresAt.Before(now) {
					expiresAt = now.Add(c.ttl)
				}
				c.mu.Lock()
				c.values[key] = adminCacheEntry{
					isAdmin:   isAdmin,
					expiresAt: expiresAt,
				}
				c.mu.Unlock()
				return isAdmin
			}
		}
	}

	return false
}

func (c *adminStatusCache) ClearChat(chatID int64) error {
	if c == nil || chatID == 0 {
		return nil
	}
	prefix := strconv.FormatInt(chatID, 10) + ":"
	c.mu.Lock()
	for key := range c.values {
		if strings.HasPrefix(key, prefix) {
			delete(c.values, key)
		}
	}
	delete(c.chats, chatID)
	c.mu.Unlock()
	if c.store != nil {
		return c.store.ClearChatAdminCache(chatID)
	}
	return nil
}

func (c *adminStatusCache) ReloadChatAdmins(bot *tgbotapi.BotAPI, chatID int64) (int, error) {
	if c == nil {
		return 0, errors.New("admin cache is nil")
	}
	if bot == nil || chatID == 0 {
		return 0, errors.New("invalid chat reload params")
	}
	if err := c.ClearChat(chatID); err != nil {
		return 0, err
	}
	admins, err := bot.GetChatAdministrators(tgbotapi.ChatAdministratorsConfig{
		ChatConfig: tgbotapi.ChatConfig{ChatID: chatID},
	})
	if err != nil {
		return 0, err
	}
	now := time.Now()
	count := 0
	for _, member := range admins {
		if member.User == nil || member.User.ID == 0 {
			continue
		}
		uid := member.User.ID
		c.setCached(chatID, uid, true, now)
		if c.store != nil {
			_ = c.store.UpsertChatAdminCache(chatID, uid, true, now.Unix())
		}
		count++
	}
	c.markChatFresh(chatID, now)
	if c.store != nil {
		_ = c.store.UpsertChatAdminSync(chatID, now.Unix(), count)
	}
	return count, nil
}

func envOr(key, fallback string) string {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	return v
}

func envBool(key string, fallback bool) bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	if v == "" {
		return fallback
	}
	return v == "1" || v == "true" || v == "yes"
}

func envInt(key string, fallback int) int {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fallback
	}
	return n
}

func newChatUserIndex(maxSize int) *chatUserIndex {
	if maxSize <= 0 {
		maxSize = 500
	}
	return &chatUserIndex{
		byChat:  make(map[int64]map[string]int64),
		byID:    make(map[int64]map[int64]string),
		maxSize: maxSize,
	}
}

func (i *chatUserIndex) remember(chatID int64, u *tgbotapi.User) {
	if i == nil || chatID == 0 || u == nil || u.ID == 0 {
		return
	}
	uname := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(u.UserName), "@"))
	label := strings.TrimSpace(u.FirstName)
	if label == "" {
		label = strings.TrimSpace(u.UserName)
	}
	if label == "" {
		label = strconv.FormatInt(u.ID, 10)
	}

	i.mu.Lock()
	defer i.mu.Unlock()
	um := i.byChat[chatID]
	if um == nil {
		um = make(map[string]int64)
		i.byChat[chatID] = um
	}
	if uname != "" {
		um[uname] = u.ID
	}
	idm := i.byID[chatID]
	if idm == nil {
		idm = make(map[int64]string)
		i.byID[chatID] = idm
	}
	idm[u.ID] = label
	if len(idm) > i.maxSize {
		for k := range idm {
			delete(idm, k)
			break
		}
	}
}

func (i *chatUserIndex) RememberFromMessage(msg *tgbotapi.Message) {
	if i == nil || msg == nil || msg.Chat == nil {
		return
	}
	i.remember(msg.Chat.ID, msg.From)
	if msg.ReplyToMessage != nil {
		i.remember(msg.Chat.ID, msg.ReplyToMessage.From)
	}
}

func (i *chatUserIndex) Resolve(chatID int64, raw string) (int64, string, bool) {
	token := strings.TrimSpace(raw)
	if token == "" {
		return 0, "", false
	}
	token = strings.Trim(token, ",;")
	if token == "" {
		return 0, "", false
	}
	if id, err := strconv.ParseInt(token, 10, 64); err == nil && id != 0 {
		return id, token, true
	}
	name := strings.ToLower(strings.TrimPrefix(token, "@"))
	if name == "" {
		return 0, "", false
	}
	if i == nil {
		return 0, "", false
	}
	i.mu.RLock()
	defer i.mu.RUnlock()
	um := i.byChat[chatID]
	if um == nil {
		return 0, "", false
	}
	id, ok := um[name]
	if !ok {
		return 0, "", false
	}
	return id, "@" + name, true
}

func (i *chatUserIndex) Display(chatID, userID int64) string {
	if i == nil || chatID == 0 || userID == 0 {
		return strconv.FormatInt(userID, 10)
	}
	i.mu.RLock()
	defer i.mu.RUnlock()
	if m := i.byID[chatID]; m != nil {
		if v := strings.TrimSpace(m[userID]); v != "" {
			return v
		}
	}
	return strconv.FormatInt(userID, 10)
}

func newReadonlyManager() *readonlyManager {
	return &readonlyManager{
		on:     make(map[int64]bool),
		timers: make(map[int64]*time.Timer),
	}
}

func (m *readonlyManager) IsOn(chatID int64) bool {
	if m == nil || chatID == 0 {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.on[chatID]
}

func (m *readonlyManager) Set(chatID int64, on bool) {
	if m == nil || chatID == 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.on[chatID] = on
	if !on {
		if t := m.timers[chatID]; t != nil {
			t.Stop()
		}
		delete(m.timers, chatID)
	}
}

func (m *readonlyManager) ScheduleOff(chatID int64, d time.Duration, fn func()) {
	if m == nil || chatID == 0 || d <= 0 || fn == nil {
		return
	}
	m.mu.Lock()
	if t := m.timers[chatID]; t != nil {
		t.Stop()
	}
	m.timers[chatID] = time.AfterFunc(d, fn)
	m.mu.Unlock()
}

func parseModerationDurationToken(raw string) (time.Duration, bool) {
	raw = strings.ToLower(strings.TrimSpace(raw))
	if raw == "" {
		return 0, false
	}
	if len(raw) < 2 {
		return 0, false
	}
	unit := raw[len(raw)-1]
	num, err := strconv.Atoi(raw[:len(raw)-1])
	if err != nil || num <= 0 {
		return 0, false
	}
	switch unit {
	case 'm':
		return time.Duration(num) * time.Minute, true
	case 'h':
		return time.Duration(num) * time.Hour, true
	case 'd':
		return time.Duration(num) * 24 * time.Hour, true
	case 'w':
		return time.Duration(num) * 7 * 24 * time.Hour, true
	default:
		return 0, false
	}
}

func parseModerationCommand(text string) (moderationRequest, bool, error) {
	raw := strings.TrimSpace(text)
	if raw == "" || !strings.HasPrefix(raw, "!") {
		return moderationRequest{}, false, nil
	}
	firstLine := raw
	reason := ""
	if nl := strings.IndexByte(raw, '\n'); nl >= 0 {
		firstLine = strings.TrimSpace(raw[:nl])
		reason = strings.TrimSpace(raw[nl+1:])
	}
	parts := strings.Fields(firstLine)
	if len(parts) == 0 {
		return moderationRequest{}, false, nil
	}
	cmd := strings.ToLower(strings.TrimSpace(parts[0]))
	args := parts[1:]
	out := moderationRequest{Reason: reason}

	switch cmd {
	case "!ban":
		out.Action = "ban"
	case "!sban":
		out.Action = "ban"
		out.Silent = true
	case "!unban":
		out.Action = "unban"
	case "!sunban":
		out.Action = "unban"
		out.Silent = true
	case "!mute":
		out.Action = "mute"
	case "!smute":
		out.Action = "mute"
		out.Silent = true
	case "!unmute":
		out.Action = "unmute"
	case "!kick":
		out.Action = "kick"
	case "!skick":
		out.Action = "kick"
		out.Silent = true
	case "!readonly", "!ro", "!channelmode":
		out.Action = "readonly"
	case "!reload_admins":
		out.Action = "reload_admins"
	default:
		return moderationRequest{}, false, nil
	}

	if out.Action == "reload_admins" {
		return out, true, nil
	}

	if out.Action == "readonly" {
		if len(args) > 0 {
			if d, ok := parseModerationDurationToken(args[0]); ok {
				out.Duration = d
				out.DurationRaw = strings.ToLower(strings.TrimSpace(args[0]))
			}
		}
		return out, true, nil
	}

	if len(args) > 0 {
		if d, ok := parseModerationDurationToken(args[len(args)-1]); ok && (out.Action == "ban" || out.Action == "mute") {
			out.Duration = d
			out.DurationRaw = strings.ToLower(strings.TrimSpace(args[len(args)-1]))
			args = args[:len(args)-1]
		}
	}
	for _, a := range args {
		v := strings.TrimSpace(strings.Trim(a, ",;"))
		if v != "" {
			out.Targets = append(out.Targets, v)
		}
	}
	return out, true, nil
}

func htmlUserLabel(label string, userID int64) string {
	label = strings.TrimSpace(label)
	if label == "" {
		label = strconv.FormatInt(userID, 10)
	}
	return html.EscapeString(label) + ` (<code>` + strconv.FormatInt(userID, 10) + `</code>)`
}

func htmlUserLink(label string, userID int64) string {
	name := strings.TrimSpace(label)
	if name == "" {
		name = "Участник без имени"
	}
	return fmt.Sprintf(`<a href="tg://user?id=%d">%s</a>`, userID, html.EscapeString(name))
}

func applyReadonly(bot *tgbotapi.BotAPI, chatID int64, on bool) error {
	if bot == nil || chatID == 0 {
		return errors.New("invalid readonly params")
	}
	perm := &tgbotapi.ChatPermissions{}
	if !on {
		perm = &tgbotapi.ChatPermissions{
			CanSendMessages:       true,
			CanSendMediaMessages:  true,
			CanSendPolls:          true,
			CanSendOtherMessages:  true,
			CanAddWebPagePreviews: true,
			CanChangeInfo:         true,
			CanInviteUsers:        true,
			CanPinMessages:        true,
		}
	}
	_, err := bot.Request(tgbotapi.SetChatPermissionsConfig{
		ChatConfig:  tgbotapi.ChatConfig{ChatID: chatID},
		Permissions: perm,
	})
	return err
}

type moderationContext struct {
	Bot        *tgbotapi.BotAPI
	AdminCache *adminStatusCache
	UserIndex  *chatUserIndex
	Readonly   *readonlyManager
}

func handleModerationCommand(ctx moderationContext, msg *tgbotapi.Message, text string) bool {
	req, ok, err := parseModerationCommand(text)
	if !ok {
		return false
	}
	if err != nil {
		return true
	}
	if ctx.Bot == nil || msg == nil || msg.Chat == nil || msg.From == nil {
		return true
	}
	sendCtx := sendContext{Bot: ctx.Bot, ChatID: msg.Chat.ID}
	if msg.Chat.IsPrivate() {
		reply(sendCtx.WithReply(msg.MessageID), "Мод-команды работают только в группах.", false)
		return true
	}
	if ctx.AdminCache == nil || !ctx.AdminCache.IsChatAdmin(ctx.Bot, msg.Chat.ID, msg.From.ID) {
		reply(sendCtx.WithReply(msg.MessageID), "Только администраторы могут использовать эту команду.", false)
		return true
	}

	deleteCmd := func() {
		_, _ = ctx.Bot.Request(tgbotapi.DeleteMessageConfig{
			ChatID:    msg.Chat.ID,
			MessageID: msg.MessageID,
		})
	}

	actionVerb := map[string]string{
		"ban":    "забанил(а)",
		"unban":  "разбанил(а)",
		"mute":   "замьютил(а)",
		"unmute": "размьютил(а)",
		"kick":   "кикнул(а)",
	}

	if req.Action == "reload_admins" {
		count, err := ctx.AdminCache.ReloadChatAdmins(ctx.Bot, msg.Chat.ID)
		if err != nil {
			reportChatFailure(ctx.Bot, msg.Chat.ID, "ошибка обновления кэша админов", err)
			return true
		}
		deleteCmd()
		reply(sendCtx, fmt.Sprintf("Кэш админов обновлён: %d.", count), false)
		return true
	}

	if req.Action == "readonly" {
		turnOn := true
		if ctx.Readonly != nil && ctx.Readonly.IsOn(msg.Chat.ID) {
			turnOn = false
		}
		if err := applyReadonly(ctx.Bot, msg.Chat.ID, turnOn); err != nil {
			reportChatFailure(ctx.Bot, msg.Chat.ID, "ошибка переключения readonly", err)
			return true
		}
		if ctx.Readonly != nil {
			ctx.Readonly.Set(msg.Chat.ID, turnOn)
		}
		deleteCmd()
		if turnOn && req.Duration > 0 && ctx.Readonly != nil {
			chatID := msg.Chat.ID
			ctx.Readonly.ScheduleOff(chatID, req.Duration, func() {
				_ = applyReadonly(ctx.Bot, chatID, false)
				ctx.Readonly.Set(chatID, false)
				reply(sendCtx.WithReply(0), "Режим только чтения автоматически выключен.", false)
			})
		}
		if !req.Silent {
			modLabel := ctx.UserIndex.Display(msg.Chat.ID, msg.From.ID)
			modLink := htmlUserLink(modLabel, msg.From.ID)
			state := "включил(а) режим только чтения"
			if !turnOn {
				state = "выключил(а) режим только чтения"
			}
			var b strings.Builder
			b.WriteString(modLink)
			b.WriteByte(' ')
			b.WriteString(state)
			if req.DurationRaw != "" && turnOn {
				b.WriteString(" на ")
				b.WriteString(html.EscapeString(req.DurationRaw))
			}
			if req.Reason != "" {
				b.WriteString(" — ")
				b.WriteString(html.EscapeString(req.Reason))
			}
			sendHTML(sendCtx, b.String(), false)
		}
		return true
	}

	seen := make(map[int64]struct{})
	targets := make([]int64, 0, 4)
	targetLabels := make([]string, 0, 4)
	addTarget := func(id int64, label string) {
		if id == 0 {
			return
		}
		if _, ok := seen[id]; ok {
			return
		}
		seen[id] = struct{}{}
		targets = append(targets, id)
		targetLabels = append(targetLabels, label)
	}

	if msg.ReplyToMessage != nil && msg.ReplyToMessage.From != nil {
		u := msg.ReplyToMessage.From
		addTarget(u.ID, ctx.UserIndex.Display(msg.Chat.ID, u.ID))
	}
	for _, tok := range req.Targets {
		id, label, ok := ctx.UserIndex.Resolve(msg.Chat.ID, tok)
		if !ok {
			reply(sendCtx.WithReply(msg.MessageID), "Не удалось распознать участника: "+tok, false)
			return true
		}
		if label == "" {
			label = ctx.UserIndex.Display(msg.Chat.ID, id)
		}
		addTarget(id, label)
	}
	if len(targets) == 0 {
		reply(sendCtx.WithReply(msg.MessageID), "Нужен reply на сообщение участника или список @username/ID.", false)
		return true
	}

	var firstErr error
	for _, uid := range targets {
		cfgMember := tgbotapi.ChatMemberConfig{ChatID: msg.Chat.ID, UserID: uid}
		switch req.Action {
		case "ban":
			cfg := tgbotapi.BanChatMemberConfig{
				ChatMemberConfig: cfgMember,
				RevokeMessages:   true,
			}
			if req.Duration > 0 {
				cfg.UntilDate = time.Now().Add(req.Duration).Unix()
			}
			_, err = ctx.Bot.Request(cfg)
		case "unban":
			_, err = ctx.Bot.Request(tgbotapi.UnbanChatMemberConfig{
				ChatMemberConfig: cfgMember,
				OnlyIfBanned:     false,
			})
		case "mute":
			cfg := tgbotapi.RestrictChatMemberConfig{
				ChatMemberConfig: cfgMember,
				Permissions:      &tgbotapi.ChatPermissions{},
			}
			if req.Duration > 0 {
				cfg.UntilDate = time.Now().Add(req.Duration).Unix()
			}
			_, err = ctx.Bot.Request(cfg)
		case "unmute":
			_, err = ctx.Bot.Request(tgbotapi.RestrictChatMemberConfig{
				ChatMemberConfig: cfgMember,
				Permissions: &tgbotapi.ChatPermissions{
					CanSendMessages:       true,
					CanSendMediaMessages:  true,
					CanSendPolls:          true,
					CanSendOtherMessages:  true,
					CanAddWebPagePreviews: true,
				},
			})
		case "kick":
			_, err = ctx.Bot.Request(tgbotapi.BanChatMemberConfig{
				ChatMemberConfig: cfgMember,
				UntilDate:        time.Now().Add(45 * time.Second).Unix(),
				RevokeMessages:   false,
			})
			if err == nil {
				_, err = ctx.Bot.Request(tgbotapi.UnbanChatMemberConfig{
					ChatMemberConfig: cfgMember,
					OnlyIfBanned:     true,
				})
			}
		}
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if firstErr != nil {
		reportChatFailure(ctx.Bot, msg.Chat.ID, "ошибка модерации", firstErr)
		return true
	}

	deleteCmd()
	if req.Silent {
		return true
	}
	modLabel := ctx.UserIndex.Display(msg.Chat.ID, msg.From.ID)
	modLink := htmlUserLink(modLabel, msg.From.ID)
	verb := actionVerb[req.Action]
	if verb == "" {
		verb = "изменил(а)"
	}
	targetLinks := make([]string, 0, len(targets))
	for i, uid := range targets {
		lbl := ctx.UserIndex.Display(msg.Chat.ID, uid)
		if i < len(targetLabels) && strings.TrimSpace(targetLabels[i]) != "" {
			lbl = targetLabels[i]
		}
		targetLinks = append(targetLinks, htmlUserLink(lbl, uid))
	}
	var b strings.Builder
	b.WriteString(modLink)
	b.WriteByte(' ')
	b.WriteString(verb)
	b.WriteByte(' ')
	b.WriteString(strings.Join(targetLinks, ", "))
	if req.DurationRaw != "" && (req.Action == "ban" || req.Action == "mute") {
		b.WriteString(" на ")
		b.WriteString(html.EscapeString(req.DurationRaw))
	}
	if req.Reason != "" {
		b.WriteString(" — ")
		b.WriteString(html.EscapeString(req.Reason))
	}
	sendHTML(sendCtx, strings.TrimSpace(b.String()), false)
	return true
}

func main() {
	rand.Seed(time.Now().UnixNano())

	token := strings.TrimSpace(os.Getenv("TELEGRAM_BOT_TOKEN"))
	if token == "" {
		log.Fatal("TELEGRAM_BOT_TOKEN is required")
	}

	dbTarget := strings.TrimSpace(os.Getenv("MONGO_URI"))
	if dbTarget == "" {
		log.Printf("MONGO_URI is required (SQLite support removed); exiting")
		return
	}
	store, err := OpenStore(dbTarget)
	if err != nil {
		log.Printf("open db failed: %v", err)
		return
	}
	defer store.Close()
	log.Printf("storage backend: mongodb")

	bot, err := tgbotapi.NewBotAPI(token)
	if err != nil {
		log.Fatalf("create bot failed: %v", err)
	}
	var spotifyMusicClient *spotifymusic.Client
	spotifyClientID := strings.TrimSpace(firstNonEmptyEnv("SPOTIPY_CLIENT_ID", "SPOTIFY_CLIENT_ID"))
	spotifyClientSecret := strings.TrimSpace(firstNonEmptyEnv("SPOTIPY_CLIENT_SECRET", "SPOTIFY_CLIENT_SECRET"))
	if spotifyClientID != "" && spotifyClientSecret != "" {
		spotifyMusicClient = spotifymusic.New(spotifyClientID, spotifyClientSecret)
		log.Printf("spotify music client enabled")
	} else {
		log.Printf("spotify music client disabled: set SPOTIPY_CLIENT_ID/SPOTIPY_CLIENT_SECRET")
	}
	spotifyDownloader := spotifymusic.Downloader{
		YTDLPBin:     strings.TrimSpace(os.Getenv("YTDLP_BIN")),
		ProxySocks:   strings.TrimSpace(os.Getenv("FIXIE_SOCKS_HOST")),
		AudioFormat:  strings.TrimSpace(firstNonEmptyEnv("AUDIO_FORMAT", "VK_AUDIO_FORMAT")),
		AudioQuality: strings.TrimSpace(firstNonEmptyEnv("AUDIO_QUALITY", "VK_AUDIO_QUALITY")),
	}
	chatErrorLogEnabled = envBool("CHAT_ERROR_LOG", true)
	debugTriggerLogEnabled = envBool("DEBUG_TRIGGER_LOG", false)
	debugGPTLogEnabled = envBool("DEBUG_GPT_LOG", false)
	log.Printf("Bot started as @%s", bot.Self.UserName)

	allowedChats, err := parseAllowedChatIDs(os.Getenv("ALLOWED_CHAT_IDS"))
	if err != nil {
		log.Fatalf("ALLOWED_CHAT_IDS parse failed: %v", err)
	}
	if allowedChats.enabled {
		ids := make([]string, 0, len(allowedChats.ids))
		for id := range allowedChats.ids {
			ids = append(ids, strconv.FormatInt(id, 10))
		}
		log.Printf("chat allow-list enabled, allowed chat IDs: %s", strings.Join(ids, ","))
	} else {
		log.Printf("chat allow-list is disabled (ALLOWED_CHAT_IDS is empty)")
	}
	adminCacheTTL := time.Duration(envInt("ADMIN_CACHE_TTL_SEC", 120)) * time.Second
	adminCache := newAdminStatusCache(adminCacheTTL, store)
	userIndex := newChatUserIndex(envInt("USER_INDEX_MAX", 800))
	readonly := newReadonlyManager()
	chatRecent := newChatRecentStore(envInt("CHAT_RECENT_MAX_MESSAGES", 8), time.Duration(envInt("CHAT_RECENT_MAX_AGE_SEC", 1800))*time.Second)
	idleTracker := trigger.NewIdleTracker()
	gptDebounceSec := envInt("GPT_PROMPT_DEBOUNCE_SEC", 0)
	gptDebouncer := gpt.NewDebouncer(time.Duration(gptDebounceSec)*time.Second, executeGPTPromptTask)
	if gptDebounceSec > 0 {
		log.Printf("gpt prompt debounce enabled: %ds (leading+trailing per chat)", gptDebounceSec)
	}

	adminBind := envOr("ADMIN_BIND", ":8090")
	adminEnabled := envBool("ADMIN_ENABLED", true)
	if adminEnabled {
		admin := NewWebAdmin(store, os.Getenv("ADMIN_TOKEN"))
		go func() {
			log.Printf("Web admin listening on %s", adminBind)
			if err := http.ListenAndServe(adminBind, admin.routes()); err != nil {
				log.Printf("web admin stopped: %v", err)
			}
		}()
	}

	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60
	u.AllowedUpdates = []string{"message", "callback_query", "chat_member"}
	updates := getUpdatesChanWithEmojiMeta(bot, u)
	triggerEngine := engine.NewTriggerEngine()
	templateLookup := buildTemplateLookup(store)
	audioQueue := newAudioSendQueue(envInt("VK_AUDIO_WORKERS", 1), envInt("VK_AUDIO_QUEUE", 8))
	handlerDeps := triggerHandlerDeps{
		triggerActionDeps: triggerActionDeps{
			Bot:               bot,
			IdleTracker:       idleTracker,
			GPTDebouncer:      gptDebouncer,
			SpotifyMusic:      spotifyMusicClient,
			SpotifyDownloader: spotifyDownloader,
			TemplateLookup:    templateLookup,
			AudioQueue:        audioQueue,
		},
		Allowed:    allowedChats,
		Engine:     triggerEngine,
		Store:      store,
		AdminCache: adminCache,
		ChatRecent: chatRecent,
	}

	for update := range updates {
		if update.RawChatMember != nil {
			handleNewMemberUpdate(handlerDeps, update.RawChatMember)
		}
		if update.RawMyChatMember != nil {
			handleNewMemberUpdate(handlerDeps, update.RawMyChatMember)
		}
		if update.Update.CallbackQuery != nil {
			if vk.HandlePickCallback(
				bot,
				update.Update.CallbackQuery,
				func(chatID int64, title string, err error) {
					reportChatFailure(bot, chatID, title, err)
				},
				func(ctx context.Context, req vk.PickRequest) error {
					return processSpotifyPick(ctx, sendContext{Bot: bot, ChatID: req.ChatID, ReplyTo: req.ReplyTo}, spotifyMusicClient, spotifyDownloader, req)
				},
			) {
				continue
			}
		}
		if update.Update.Message == nil {
			continue
		}
		msg := update.Update.Message
		rawMsg := update.RawMessage
		if msg.Chat == nil || msg.From == nil || msg.From.IsBot {
			continue
		}
		isPrivateChat := msg.Chat.IsPrivate()
		if !isPrivateChat && !allowedChats.Allows(msg.Chat.ID) {
			if debugTriggerLogEnabled {
				log.Printf("skip message from disallowed chat chat=%d msg=%d", msg.Chat.ID, msg.MessageID)
			}
			continue
		}
		userIndex.RememberFromMessage(msg)
		if !isPrivateChat {
			if _, err := adminCache.EnsureChatAdminsFresh(bot, msg.Chat.ID); err != nil && debugTriggerLogEnabled {
				log.Printf("admin cache warmup failed chat=%d: %v", msg.Chat.ID, err)
			}
		}

		if handleModerationCommand(moderationContext{
			Bot:        bot,
			AdminCache: adminCache,
			UserIndex:  userIndex,
			Readonly:   readonly,
		}, msg, strings.TrimSpace(msg.Text)) {
			continue
		}

		cmdSendCtx := sendContext{Bot: bot, ChatID: msg.Chat.ID}
		if msg.IsCommand() {
			cmd := msg.Command()
			switch cmd {
			case "start", "help":
				s := "Триггер-бот активен.\n\n" +
					"Админка: /trigger_bot\n" +
					"Команды: /start /help /emojiid /vksearch\n" +
					"Мод-команды: !ban !unban !mute !unmute !kick !readonly !reload_admins (+ тихие !sban !smute !skick)\n\n" +
					"Теги для ChatGPT-промпта:\n" +
					"{{message}} / {{user_text}} — текст сообщения\n" +
					"{{user_id}}, {{user_first_name}}, {{user_username}}\n" +
					"{{user_display_name}}, {{user_label}}\n" +
					"{{sender_tag}}\n" +
					"{{chat_id}}, {{chat_title}}\n" +
					"{{reply_text}}\n" +
					"{{capturing_text}}\n" +
					"{{capturing_choice}} / {{capturing_option}}\n" +
					"{{reply_user_id}}, {{reply_first_name}}, {{reply_username}}\n" +
					"{{reply_display_name}}, {{reply_label}}, {{reply_user_link}}\n" +
					"{{reply_sender_tag}}\n\n" +
					"Кастомный emoji ID:\n" +
					"— команда /emojiid\n" +
					"— или просто отправьте кастомный emoji в личку боту."
				reply(cmdSendCtx.WithReply(msg.MessageID), s, false)
				continue
			case "emojiid", "emoji_id":
				hits, entityCount := extractCustomEmojiFromRaw(rawMsg)
				if len(hits) == 0 && rawMsg != nil && rawMsg.ReplyToMessage != nil {
					hits, entityCount = extractCustomEmojiFromRaw(rawMsg.ReplyToMessage)
				}
				if len(hits) == 0 {
					if entityCount > 0 {
						reply(cmdSendCtx.WithReply(msg.MessageID), "Нашла кастомный эмодзи, но не смогла извлечь его ID. Попробуйте отправить другой эмодзи ещё раз.", false)
						continue
					}
					reply(cmdSendCtx.WithReply(msg.MessageID), "Кастомный emoji не найден. Отправьте сообщение с premium-эмодзи.", false)
					continue
				}
				lines := make([]string, 0, len(hits)+2)
				lines = append(lines, "Готовый код для вставки:")
				for _, hit := range hits {
					snippet := buildTGEmojiSnippet(hit.ID, hit.Fallback)
					lines = append(lines, "<code>"+html.EscapeString(snippet)+"</code>")
				}
				sendHTML(cmdSendCtx.WithReply(msg.MessageID), strings.Join(lines, "\n"), false)
				continue
			case "spsearch", "spfind", "vksearch", "vkfind":
				query := strings.TrimSpace(msg.CommandArguments())
				if query == "" {
					reply(cmdSendCtx.WithReply(msg.MessageID), "Использование: /spsearch исполнитель или трек", false)
					continue
				}
				if spotifyMusicClient == nil || !spotifyMusicClient.Enabled() {
					reply(cmdSendCtx.WithReply(msg.MessageID), "Spotify-поиск не настроен (добавьте SPOTIPY_CLIENT_ID и SPOTIPY_CLIENT_SECRET в .env).", false)
					continue
				}
				ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
				tracks, err := spotifyMusicClient.SearchTracks(ctx, query, 10)
				cancel()
				if err != nil {
					reply(cmdSendCtx.WithReply(msg.MessageID), "Ошибка Spotify-поиска: "+clipText(err.Error(), 240), false)
					continue
				}
				if len(tracks) == 0 {
					reply(cmdSendCtx.WithReply(msg.MessageID), "Ничего не найдено в Spotify.", false)
					continue
				}
				var b strings.Builder
				b.WriteString("Spotify поиск:\n")
				for i, tr := range tracks {
					fmt.Fprintf(&b, "%d. %s — %s (<code>%s</code>)\n", i+1, strings.TrimSpace(tr.Artist), strings.TrimSpace(tr.Title), strings.TrimSpace(tr.ID))
				}
				sendHTML(cmdSendCtx.WithReply(msg.MessageID), strings.TrimSpace(b.String()), false)
				continue
			}
		}
		if isPrivateChat {
			hits, entityCount := extractCustomEmojiFromRaw(rawMsg)
			if len(hits) > 0 {
				lines := make([]string, 0, len(hits)+2)
				lines = append(lines, "Готовый код для вставки:")
				for _, hit := range hits {
					snippet := buildTGEmojiSnippet(hit.ID, hit.Fallback)
					lines = append(lines, "<code>"+html.EscapeString(snippet)+"</code>")
				}
				sendHTML(cmdSendCtx.WithReply(msg.MessageID), strings.Join(lines, "\n"), false)
				continue
			}
			if entityCount > 0 {
				reply(cmdSendCtx.WithReply(msg.MessageID), "Нашла кастомный эмодзи, но не смогла извлечь его ID. Попробуйте отправить другой эмодзи ещё раз.", false)
				continue
			}
		}
		if isPrivateChat {
			if debugTriggerLogEnabled {
				log.Printf("skip non-command message in private chat chat=%d msg=%d", msg.Chat.ID, msg.MessageID)
			}
			continue
		}

		text := strings.TrimSpace(msg.Text)
		if text == "" {
			continue
		}
		now := time.Now()
		idleTracker.Seen(msg.Chat.ID, now)

		recentBefore := ""
		if text != "" {
			recentBefore = chatRecent.RecentText(msg.Chat.ID, envInt("OLENYAM_CONTEXT_MESSAGES", 4))
			displayName := strings.TrimSpace(msg.From.FirstName)
			if displayName == "" {
				displayName = strings.TrimSpace(msg.From.UserName)
			}
			chatRecent.Add(msg.Chat.ID, recentChatMessage{
				MessageID: msg.MessageID,
				UserName:  displayName,
				Text:      text,
				At:        time.Now(),
			})
		}

		if debugTriggerLogEnabled {
			log.Printf("update chat=%d msg=%d from=%d user=%s text=%q",
				msg.Chat.ID, msg.MessageID, msg.From.ID, msg.From.UserName, clipText(text, 220))
		}

		items, err := store.ListTriggersCached()
		if err != nil {
			log.Printf("list triggers failed: %v", err)
			reportChatFailure(bot, msg.Chat.ID, "ошибка загрузки триггеров", err)
			continue
		}
		if debugTriggerLogEnabled {
			log.Printf("triggers loaded (cached): %d", len(items))
		}
		tr := triggerEngine.Select(engine.SelectInput{
			Bot:      bot,
			Msg:      msg,
			Text:     text,
			Triggers: items,
			IsAdminFn: func() bool {
				return adminCache.IsChatAdmin(bot, msg.Chat.ID, msg.From.ID)
			},
		})
		if tr == nil {
			if debugTriggerLogEnabled {
				log.Printf("no trigger matched for msg=%d", msg.MessageID)
			}
			if idleTracker != nil {
				autoTr, idleAfter := trigger.SelectIdleAutoReplyTrigger(bot, msg, items, func() bool {
					return adminCache.IsChatAdmin(bot, msg.Chat.ID, msg.From.ID)
				})
				if autoTr != nil && idleTracker.ShouldAutoReply(msg.Chat.ID, idleAfter, now) {
					ctx := ""
					if isOlenyamTrigger(autoTr) {
						ctx = recentBefore
					}
					rawTemplate := pickResponseVariantText(autoTr.ResponseText)
					resolvedTemplate := expandTemplateCalls(rawTemplate, templateLookup)
					trCopy := *autoTr
					trCopy.ResponseText = []ResponseTextItem{{Text: resolvedTemplate}}
					task := gpt.PromptTask{
						Bot:            bot,
						Trigger:        trCopy,
						Msg:            msg,
						RecentContext:  ctx,
						TemplateLookup: templateLookup,
						IdleMarkActivity: func(chatID int64, now time.Time) {
							if idleTracker != nil {
								idleTracker.MarkActivity(chatID, now)
							}
						},
						ChatID: msg.Chat.ID,
					}
					if gptDebouncer != nil {
						gptDebouncer.Schedule(msg.Chat.ID, task)
					} else {
						executeGPTPromptTask(task)
					}
					if debugTriggerLogEnabled {
						log.Printf("idle auto-reply queued trigger=%d chat=%d msg=%d idle_after=%s", autoTr.ID, msg.Chat.ID, msg.MessageID, idleAfter)
					}
					continue
				}
			}
			continue
		}
		msgForTrigger := msg
		rawTemplate := pickResponseVariantText(tr.ResponseText)
		resolvedTemplate := expandTemplateCalls(rawTemplate, templateLookup)
		tmplCtx := newTemplateContext(bot, msgForTrigger, tr, templateLookup)
		if debugTriggerLogEnabled {
			log.Printf("pick id=%d title=%q mode=%s action=%s", tr.ID, tr.Title, tr.TriggerMode, tr.ActionType)
		}

		switch tr.ActionType {
		case "delete":
			cfg := tgbotapi.DeleteMessageConfig{
				ChatID:    msg.Chat.ID,
				MessageID: msg.MessageID,
			}
			if _, err := bot.Request(cfg); err != nil {
				log.Printf("delete message failed: %v", err)
				reportChatFailure(bot, msg.Chat.ID, "ошибка удаления сообщения", err)
			} else {
				idleTracker.MarkActivity(msg.Chat.ID, time.Now())
				if debugTriggerLogEnabled {
					log.Printf("delete ok msg=%d by trigger=%d", msg.MessageID, tr.ID)
				}
			}
		case "gpt_prompt":
			ctx := ""
			if isOlenyamTrigger(tr) {
				ctx = recentBefore
			}
			if gptDebouncer != nil {
				trCopy := *tr
				trCopy.ResponseText = []ResponseTextItem{{Text: resolvedTemplate}}
				gptDebouncer.Schedule(msg.Chat.ID, gpt.PromptTask{
					Bot:            bot,
					Trigger:        trCopy,
					Msg:            msgForTrigger,
					RecentContext:  ctx,
					TemplateLookup: templateLookup,
					IdleMarkActivity: func(chatID int64, now time.Time) {
						if idleTracker != nil {
							idleTracker.MarkActivity(chatID, now)
						}
					},
					ChatID: msg.Chat.ID,
				})
				if debugTriggerLogEnabled {
					log.Printf("gpt prompt queued (debounce) trigger=%d chat=%d msg=%d", tr.ID, msg.Chat.ID, msg.MessageID)
				}
				continue
			}
			trCopy := *tr
			trCopy.ResponseText = []ResponseTextItem{{Text: resolvedTemplate}}
			executeGPTPromptTask(gpt.PromptTask{
				Bot:            bot,
				Trigger:        trCopy,
				Msg:            msgForTrigger,
				RecentContext:  ctx,
				TemplateLookup: templateLookup,
				IdleMarkActivity: func(chatID int64, now time.Time) {
					if idleTracker != nil {
						idleTracker.MarkActivity(chatID, now)
					}
				},
				ChatID: msg.Chat.ID,
			})
		case "gpt_image":
			img, err := generateChatGPTImage(tmplCtx, resolvedTemplate)
			if err != nil {
				log.Printf("gpt image failed: %v", err)
				reportChatFailure(bot, msg.Chat.ID, "ошибка генерации картинки в ChatGPT", err)
				continue
			}
			replyTo := 0
			if tr.Reply || tr.TriggerMode == "command_reply" {
				replyTo = msg.MessageID
			}
			sendCtx := sendContext{Bot: bot, ChatID: msg.Chat.ID, ReplyTo: replyTo}
			if ok := sendPhoto(sendCtx, img, "CW: сгенерено нейросетью", true); ok {
				idleTracker.MarkActivity(msg.Chat.ID, time.Now())
				deleteTriggerSourceMessage(bot, msg, tr)
			}
			if debugTriggerLogEnabled {
				log.Printf("send gpt/image attempted trigger=%d replyTo=%d", tr.ID, replyTo)
			}
		case "search_image":
			query := buildImageSearchQueryFromMessage(tmplCtx, resolvedTemplate)
			img, err := searchImageInSerpAPI(query)
			if err != nil {
				log.Printf("search image failed: %v", err)
				reportChatFailure(bot, msg.Chat.ID, "ошибка поиска картинки", err)
				continue
			}
			replyTo := 0
			if tr.Reply || tr.TriggerMode == "command_reply" {
				replyTo = msg.MessageID
			}
			sendCtx := sendContext{Bot: bot, ChatID: msg.Chat.ID, ReplyTo: replyTo}
			if ok := sendPhoto(sendCtx, img, "", false); ok {
				idleTracker.MarkActivity(msg.Chat.ID, time.Now())
				deleteTriggerSourceMessage(bot, msg, tr)
			}
			if debugTriggerLogEnabled {
				log.Printf("send search/image attempted trigger=%d replyTo=%d query=%q", tr.ID, replyTo, clipText(query, 220))
			}
		case "spotify_music_audio", "vk_music_audio":
			if spotifyMusicClient == nil || !spotifyMusicClient.Enabled() {
				reportChatFailure(bot, msg.Chat.ID, "ошибка Spotify-музыки", errors.New("SPOTIPY_CLIENT_ID/SPOTIPY_CLIENT_SECRET не настроены"))
				continue
			}
			query := buildSpotifyMusicQueryFromMessage(tmplCtx, resolvedTemplate)
			if query == "" {
				query = strings.TrimSpace(msg.Text)
			}
			if query == "" {
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			tracks, err := spotifyMusicClient.SearchTracks(ctx, query, 10)
			cancel()
			if err != nil {
				log.Printf("spotify music search failed: %v", err)
				reportChatFailure(bot, msg.Chat.ID, "ошибка поиска музыки Spotify", err)
				continue
			}
			if len(tracks) == 0 {
				if debugTriggerLogEnabled {
					log.Printf("spotify music search empty trigger=%d query=%q", tr.ID, clipText(query, 220))
				}
				continue
			}
			replyTo := 0
			if tr.Reply || tr.TriggerMode == "command_reply" {
				replyTo = msg.MessageID
			}
			if envBool("SPOTIFY_AUDIO_INTERACTIVE", envBool("VK_AUDIO_INTERACTIVE", true)) {
				maxResults := 10
				if len(tracks) > maxResults {
					tracks = tracks[:maxResults]
				}
				pickTracks := make([]vk.PickTrack, 0, len(tracks))
				for _, track := range tracks {
					pickTracks = append(pickTracks, vk.PickTrack{
						ID:          track.ID,
						Artist:      track.Artist,
						Title:       track.Title,
						DurationSec: track.DurationSec,
					})
				}
				m := tgbotapi.NewMessage(msg.Chat.ID, "🎵 Результаты поиска:")
				m.ReplyMarkup = vk.BuildPickKeyboard(msg, tr != nil && tr.DeleteSource, pickTracks)
				if replyTo > 0 {
					m.ReplyToMessageID = replyTo
					m.AllowSendingWithoutReply = true
				}
				if _, err := bot.Send(m); err != nil {
					reportChatFailure(bot, msg.Chat.ID, "ошибка отправки списка Spotify", err)
					continue
				}
				idleTracker.MarkActivity(msg.Chat.ID, time.Now())
				continue
			}
			req := vk.PickRequest{
				TrackID:      tracks[0].ID,
				Artist:       tracks[0].Artist,
				Title:        tracks[0].Title,
				ChatID:       msg.Chat.ID,
				ReplyTo:      replyTo,
				SourceMsgID:  msg.MessageID,
				DeleteSource: tr != nil && tr.DeleteSource,
				UserID:       msg.From.ID,
			}
			ctxSend, cancelSend := context.WithTimeout(context.Background(), 3*time.Minute)
			err = processSpotifyPick(ctxSend, sendContext{Bot: bot, ChatID: msg.Chat.ID, ReplyTo: replyTo}, spotifyMusicClient, spotifyDownloader, req)
			cancelSend()
			if err != nil {
				reportChatFailure(bot, msg.Chat.ID, "ошибка отправки аудио Spotify", err)
				continue
			}
			if idleTracker != nil {
				idleTracker.MarkActivity(msg.Chat.ID, time.Now())
			}
			deleteTriggerSourceMessage(bot, msg, tr)
			if debugTriggerLogEnabled {
				log.Printf("send spotify/audio attempted trigger=%d replyTo=%d query=%q", tr.ID, replyTo, clipText(query, 160))
			}
		default:
			replyTo := 0
			if tr.Reply || tr.TriggerMode == "command_reply" {
				replyTo = msg.MessageID
			}
			out := buildResponseFromMessage(tmplCtx, resolvedTemplate)
			sendCtx := sendContext{Bot: bot, ChatID: msg.Chat.ID, ReplyTo: replyTo}
			if ok := sendHTML(sendCtx, out, tr.Preview); ok {
				idleTracker.MarkActivity(msg.Chat.ID, time.Now())
				deleteTriggerSourceMessage(bot, msg, tr)
			}
			if debugTriggerLogEnabled {
				log.Printf("send static/html attempted trigger=%d replyTo=%d", tr.ID, replyTo)
			}
		}
	}
}

type sendContext struct {
	Bot     *tgbotapi.BotAPI
	ChatID  int64
	ReplyTo int
}

func (c sendContext) WithReply(replyTo int) sendContext {
	c.ReplyTo = replyTo
	return c
}

func reply(ctx sendContext, text string, preview bool) {
	m := tgbotapi.NewMessage(ctx.ChatID, text)
	m.DisableWebPagePreview = !preview
	if ctx.ReplyTo > 0 {
		m.ReplyToMessageID = ctx.ReplyTo
		m.AllowSendingWithoutReply = true
	}
	sent, err := ctx.Bot.Send(m)
	if err != nil {
		log.Printf("send failed: %v", err)
		reportChatFailure(ctx.Bot, ctx.ChatID, "ошибка отправки сообщения", err)
		return
	}
	if debugTriggerLogEnabled {
		log.Printf("send ok chat=%d msg=%d replyTo=%d text=%q", ctx.ChatID, sent.MessageID, ctx.ReplyTo, clipText(text, 120))
	}
}

func sendHTML(ctx sendContext, html string, preview bool) bool {
	html = normalizeTelegramLineBreaks(html)
	m := tgbotapi.NewMessage(ctx.ChatID, html)
	m.ParseMode = "HTML"
	m.DisableWebPagePreview = !preview
	if ctx.ReplyTo > 0 {
		m.ReplyToMessageID = ctx.ReplyTo
		m.AllowSendingWithoutReply = true
	}
	if len(m.Text) > 4096 {
		m.Text = m.Text[:4096]
	}
	sent, err := ctx.Bot.Send(m)
	if err != nil {
		log.Printf("send html failed: %v", err)
		reportChatFailure(ctx.Bot, ctx.ChatID, "ошибка отправки HTML-сообщения", err)
		return false
	}
	if debugTriggerLogEnabled {
		log.Printf("send html ok chat=%d msg=%d replyTo=%d text=%q", ctx.ChatID, sent.MessageID, ctx.ReplyTo, clipText(m.Text, 120))
	}
	return true
}

func sendMarkdownV2(ctx sendContext, text string, preview bool) bool {
	text = normalizeTelegramLineBreaks(text)
	text = escapeMarkdownV2PreservingFences(text)
	m := tgbotapi.NewMessage(ctx.ChatID, text)
	m.ParseMode = "MarkdownV2"
	m.DisableWebPagePreview = !preview
	if ctx.ReplyTo > 0 {
		m.ReplyToMessageID = ctx.ReplyTo
		m.AllowSendingWithoutReply = true
	}
	if len(m.Text) > 4096 {
		m.Text = m.Text[:4096]
	}
	sent, err := ctx.Bot.Send(m)
	if err != nil {
		log.Printf("send markdown failed: %v", err)
		reportChatFailure(ctx.Bot, ctx.ChatID, "ошибка отправки Markdown-сообщения", err)
		return false
	}
	if debugTriggerLogEnabled {
		log.Printf("send markdown ok chat=%d msg=%d replyTo=%d text=%q", ctx.ChatID, sent.MessageID, ctx.ReplyTo, clipText(m.Text, 120))
	}
	return true
}

type generatedImage struct {
	URL   string
	Bytes []byte
}

func sendPhoto(ctx sendContext, img generatedImage, caption string, spoiler bool) bool {
	if spoiler {
		if err := sendPhotoWithSpoilerAPI(ctx, img, caption); err != nil {
			log.Printf("send photo (spoiler) failed: %v", err)
			reportChatFailure(ctx.Bot, ctx.ChatID, "ошибка отправки картинки", err)
			return false
		}
		if debugTriggerLogEnabled {
			log.Printf("send photo (spoiler) ok chat=%d replyTo=%d", ctx.ChatID, ctx.ReplyTo)
		}
		return true
	}

	var file tgbotapi.RequestFileData
	switch {
	case strings.TrimSpace(img.URL) != "":
		file = tgbotapi.FileURL(strings.TrimSpace(img.URL))
	case len(img.Bytes) > 0:
		file = tgbotapi.FileBytes{Name: "generated.png", Bytes: img.Bytes}
	default:
		reportChatFailure(ctx.Bot, ctx.ChatID, "ошибка отправки картинки", errors.New("empty image payload"))
		return false
	}

	m := tgbotapi.NewPhoto(ctx.ChatID, file)
	if ctx.ReplyTo > 0 {
		m.ReplyToMessageID = ctx.ReplyTo
		m.AllowSendingWithoutReply = true
	}
	m.Caption = strings.TrimSpace(caption)
	sent, err := ctx.Bot.Send(m)
	if err != nil {
		log.Printf("send photo failed: %v", err)
		reportChatFailure(ctx.Bot, ctx.ChatID, "ошибка отправки картинки", err)
		return false
	}
	if debugTriggerLogEnabled {
		if strings.TrimSpace(img.URL) != "" {
			log.Printf("send photo ok chat=%d msg=%d replyTo=%d source=url", ctx.ChatID, sent.MessageID, ctx.ReplyTo)
		} else {
			log.Printf("send photo ok chat=%d msg=%d replyTo=%d source=bytes size=%d", ctx.ChatID, sent.MessageID, ctx.ReplyTo, len(img.Bytes))
		}
	}
	return true
}

func sendAudioFromURL(ctx sendContext, audioURL, performer, title string) error {
	audioURL = strings.TrimSpace(audioURL)
	if ctx.Bot == nil || ctx.ChatID == 0 || audioURL == "" {
		return errors.New("invalid audio send params")
	}
	tmpPath, err := downloadAudioToTempFile(audioURL)
	if err != nil {
		return err
	}
	defer func() {
		if rmErr := os.Remove(tmpPath); rmErr != nil && debugTriggerLogEnabled {
			log.Printf("audio temp cleanup failed path=%q err=%v", tmpPath, rmErr)
		}
	}()

	m := tgbotapi.NewAudio(ctx.ChatID, tgbotapi.FilePath(tmpPath))
	if ctx.ReplyTo > 0 {
		m.ReplyToMessageID = ctx.ReplyTo
		m.AllowSendingWithoutReply = true
	}
	m.Performer = strings.TrimSpace(performer)
	m.Title = strings.TrimSpace(title)
	if caption := buildAudioCaption(tmpPath); caption != "" {
		m.Caption = caption
	}
	sent, err := ctx.Bot.Send(m)
	if err != nil {
		return err
	}
	if debugTriggerLogEnabled {
		log.Printf("send audio ok chat=%d msg=%d replyTo=%d performer=%q title=%q", ctx.ChatID, sent.MessageID, ctx.ReplyTo, m.Performer, m.Title)
	}
	return nil
}

func sendAudioFromFile(ctx sendContext, filePath, performer, title string) error {
	filePath = strings.TrimSpace(filePath)
	if ctx.Bot == nil || ctx.ChatID == 0 || filePath == "" {
		return errors.New("invalid audio file send params")
	}
	m := tgbotapi.NewAudio(ctx.ChatID, tgbotapi.FilePath(filePath))
	if ctx.ReplyTo > 0 {
		m.ReplyToMessageID = ctx.ReplyTo
		m.AllowSendingWithoutReply = true
	}
	m.Performer = strings.TrimSpace(performer)
	m.Title = strings.TrimSpace(title)
	if caption := buildAudioCaption(filePath); caption != "" {
		m.Caption = caption
	}
	sent, err := ctx.Bot.Send(m)
	if err != nil {
		return err
	}
	if debugTriggerLogEnabled {
		log.Printf("send audio file ok chat=%d msg=%d replyTo=%d performer=%q title=%q", ctx.ChatID, sent.MessageID, ctx.ReplyTo, m.Performer, m.Title)
	}
	return nil
}

func processSpotifyPick(ctx context.Context, sendCtx sendContext, spClient *spotifymusic.Client, dl spotifymusic.Downloader, req vk.PickRequest) error {
	if spClient == nil || !spClient.Enabled() {
		return errors.New("spotify client is not configured")
	}
	if strings.TrimSpace(req.TrackID) == "" {
		return errors.New("empty track id")
	}
	trackCtx, cancelTrack := context.WithTimeout(ctx, 20*time.Second)
	track, err := spClient.GetTrack(trackCtx, req.TrackID)
	cancelTrack()
	if err != nil {
		return err
	}
	query := spotifymusic.BuildSearchQuery(track)
	if query == "" {
		query = strings.TrimSpace(req.Artist + " " + req.Title)
	}
	if query == "" {
		return errors.New("empty track search query")
	}
	dlCtx, cancelDl := context.WithTimeout(ctx, 3*time.Minute)
	filePath, err := dl.DownloadByQuery(dlCtx, query)
	cancelDl()
	if err != nil {
		return err
	}
	defer func() {
		if rmErr := os.Remove(filePath); rmErr != nil && debugTriggerLogEnabled {
			log.Printf("spotify temp cleanup failed path=%q err=%v", filePath, rmErr)
		}
	}()
	performer := strings.TrimSpace(track.Artist)
	title := strings.TrimSpace(track.Title)
	if performer == "" {
		performer = strings.TrimSpace(req.Artist)
	}
	if title == "" {
		title = strings.TrimSpace(req.Title)
	}
	return sendAudioFromFile(sendCtx, filePath, performer, title)
}

type audioSendTask struct {
	Ctx       sendContext
	AudioURL  string
	Performer string
	Title     string
	Msg       *tgbotapi.Message
	Trigger   *Trigger
	Idle      *trigger.IdleTracker
}

type audioSendQueue struct {
	ch chan audioSendTask
}

func newAudioSendQueue(workers, size int) *audioSendQueue {
	if workers < 1 {
		workers = 1
	}
	if size < 1 {
		size = workers * 2
	}
	q := &audioSendQueue{ch: make(chan audioSendTask, size)}
	for i := 0; i < workers; i++ {
		go func() {
			for task := range q.ch {
				err := sendAudioFromURL(task.Ctx, task.AudioURL, task.Performer, task.Title)
				if err != nil {
					log.Printf("audio send failed chat=%d err=%v", task.Ctx.ChatID, err)
					reportChatFailure(task.Ctx.Bot, task.Ctx.ChatID, "ошибка отправки аудио VK", err)
					continue
				}
				if task.Idle != nil {
					task.Idle.MarkActivity(task.Ctx.ChatID, time.Now())
				}
				if task.Msg != nil && task.Trigger != nil {
					deleteTriggerSourceMessage(task.Ctx.Bot, task.Msg, task.Trigger)
				}
			}
		}()
	}
	return q
}

func (q *audioSendQueue) enqueue(task audioSendTask) bool {
	if q == nil {
		return false
	}
	select {
	case q.ch <- task:
		return true
	default:
		return false
	}
}

func buildAudioCaption(path string) string {
	stats, ok := probeAudioStats(path)
	if !ok || stats.SizeBytes <= 0 {
		return ""
	}
	sizeMB := float64(stats.SizeBytes) / 1_000_000.0
	dur := vk.FormatDuration(stats.DurationSec)
	bitrateKbps := stats.BitrateKbps
	if bitrateKbps <= 0 && stats.DurationSec > 0 {
		bitrateKbps = int64(float64(stats.SizeBytes*8)/stats.DurationSec/1000.0 + 0.5)
	}
	if dur == "" || bitrateKbps <= 0 {
		return fmt.Sprintf("🎧 %.2f MB", sizeMB)
	}
	return fmt.Sprintf("🎧 %s | %.2fMB | %dKbps", dur, sizeMB, bitrateKbps)
}

type audioStats struct {
	SizeBytes   int64
	DurationSec float64
	BitrateKbps int64
}

func probeAudioStats(path string) (audioStats, bool) {
	info, err := os.Stat(path)
	if err != nil {
		return audioStats{}, false
	}
	stats := audioStats{SizeBytes: info.Size()}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		return stats, true
	}
	out, err := exec.Command("ffprobe",
		"-v", "error",
		"-show_entries", "format=duration,bit_rate",
		"-of", "default=nw=1:nk=1",
		path,
	).Output()
	if err != nil {
		return stats, true
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) > 0 {
		if v, err := strconv.ParseFloat(strings.TrimSpace(lines[0]), 64); err == nil && v > 0 {
			stats.DurationSec = v
		}
	}
	if len(lines) > 1 {
		if v, err := strconv.ParseInt(strings.TrimSpace(lines[1]), 10, 64); err == nil && v > 0 {
			stats.BitrateKbps = v / 1000
		}
	}
	return stats, true
}

func downloadAudioToTempFile(audioURL string) (string, error) {
	tmp, err := os.CreateTemp("", "vk-audio-*.mp3")
	if err != nil {
		return "", err
	}
	tmpPath := tmp.Name()
	_ = tmp.Close()
	tmpSrcPath := ""
	maxMB := envInt("VK_AUDIO_MAX_MB", 60)
	if maxMB < 5 {
		maxMB = 5
	}
	ffmpegTimeout := envInt("VK_AUDIO_FFMPEG_TIMEOUT_SEC", 120)
	if ffmpegTimeout < 30 {
		ffmpegTimeout = 30
	}
	ua := strings.TrimSpace(os.Getenv("VK_USER_AGENT"))
	if ua == "" {
		ua = "VKAndroidApp/8.120-13180 (Android 13; SDK 33; arm64-v8a; Google Pixel 6 Pro; ru; 320dpi)"
	}
	retries := envInt("VK_AUDIO_RETRY_COUNT", 3)
	if retries < 1 {
		retries = 1
	}
	dlThreads := envInt("VK_AUDIO_DL_THREADS", 1)
	if dlThreads < 1 {
		dlThreads = 1
	}
	useMultiDownload := dlThreads > 1 && !strings.Contains(strings.ToLower(audioURL), ".m3u8")
	var runErr error
	for attempt := 1; attempt <= retries; attempt++ {
		_ = os.Remove(tmpPath)
		ctx, cancel := context.WithTimeout(context.Background(), time.Duration(ffmpegTimeout)*time.Second)
		var err error
		if useMultiDownload {
			if tmpSrcPath == "" {
				if f, ferr := os.CreateTemp("", "vk-audio-src-*.bin"); ferr == nil {
					tmpSrcPath = f.Name()
					_ = f.Close()
				}
			}
			if tmpSrcPath != "" {
				_ = os.Remove(tmpSrcPath)
			}
			err = downloadAudioMultiPart(ctx, audioDownloadRequest{
				AudioURL:  audioURL,
				OutPath:   tmpSrcPath,
				Threads:   dlThreads,
				UserAgent: ua,
			})
			if err == nil {
				err = runFFmpegAudioDownloadFromFile(ctx, tmpSrcPath, tmpPath)
			}
		} else {
			err = runFFmpegAudioDownload(ctx, audioURL, tmpPath, ua)
		}
		cancel()
		if err == nil {
			runErr = nil
			break
		}
		runErr = err
		if debugTriggerLogEnabled {
			log.Printf("ffmpeg audio attempt=%d/%d failed: %v", attempt, retries, err)
		}
		time.Sleep(time.Duration(attempt) * 500 * time.Millisecond)
	}
	if runErr != nil {
		_ = os.Remove(tmpPath)
		if tmpSrcPath != "" {
			_ = os.Remove(tmpSrcPath)
		}
		return "", runErr
	}
	st, err := os.Stat(tmpPath)
	if err != nil {
		_ = os.Remove(tmpPath)
		if tmpSrcPath != "" {
			_ = os.Remove(tmpSrcPath)
		}
		return "", err
	}
	size := st.Size()
	limit := int64(maxMB) << 20
	if size <= 0 {
		_ = os.Remove(tmpPath)
		if tmpSrcPath != "" {
			_ = os.Remove(tmpSrcPath)
		}
		return "", errors.New("ffmpeg produced empty audio file")
	}
	if size > limit {
		_ = os.Remove(tmpPath)
		if tmpSrcPath != "" {
			_ = os.Remove(tmpSrcPath)
		}
		return "", fmt.Errorf("audio too large: %d bytes (limit %d MB)", size, maxMB)
	}
	if tmpSrcPath != "" {
		_ = os.Remove(tmpSrcPath)
	}
	return tmpPath, nil
}

func runFFmpegAudioDownload(ctx context.Context, audioURL, outPath, userAgent string) error {
	if strings.Contains(strings.ToLower(audioURL), ".m3u8") {
		return runFFmpegAudioDownloadFromM3U8(ctx, audioURL, outPath, userAgent)
	}
	return runFFmpegAudioDownloadDirect(ctx, audioURL, outPath, userAgent)
}

func runFFmpegAudioDownloadFromM3U8(ctx context.Context, audioURL, outPath, userAgent string) error {
	tmpTS, err := os.CreateTemp("", "vk-audio-*.ts")
	if err != nil {
		return err
	}
	tmpTSPath := tmpTS.Name()
	_ = tmpTS.Close()
	defer os.Remove(tmpTSPath)

	var stderr1 bytes.Buffer
	copyCmd := exec.CommandContext(ctx,
		"ffmpeg",
		"-nostdin",
		"-hide_banner",
		"-loglevel", "warning",
		"-y",
		"-http_persistent", "false",
		"-reconnect", "1",
		"-reconnect_streamed", "1",
		"-reconnect_at_eof", "1",
		"-reconnect_delay_max", "5",
		"-rw_timeout", "15000000",
		"-user_agent", userAgent,
		"-protocol_whitelist", "file,http,https,tcp,tls,crypto",
		"-allowed_extensions", "ALL",
		"-i", audioURL,
		"-vn",
		"-c", "copy",
		tmpTSPath,
	)
	copyCmd.Stderr = &stderr1
	if err := copyCmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr1.String())
		if msg == "" {
			msg = err.Error()
		}
		if ctx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("ffmpeg timeout")
		}
		return fmt.Errorf("ffmpeg m3u8 copy failed: %s", clipText(msg, 400))
	}

	var stderr2 bytes.Buffer
	transcodeCmd := exec.CommandContext(ctx,
		"ffmpeg",
		"-nostdin",
		"-hide_banner",
		"-loglevel", "warning",
		"-y",
		"-i", tmpTSPath,
		"-vn",
		"-acodec", "libmp3lame",
		"-b:a", "192k",
		outPath,
	)
	transcodeCmd.Stderr = &stderr2
	if err := transcodeCmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr2.String())
		if msg == "" {
			msg = err.Error()
		}
		if ctx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("ffmpeg timeout")
		}
		return fmt.Errorf("ffmpeg m3u8 transcode failed: %s", clipText(msg, 400))
	}
	return nil
}

func runFFmpegAudioDownloadDirect(ctx context.Context, audioURL, outPath, userAgent string) error {
	var stderr bytes.Buffer
	headers := "Referer: https://vk.com/\r\nOrigin: https://vk.com\r\nAccept: */*\r\n"
	cmd := exec.CommandContext(ctx,
		"ffmpeg",
		"-nostdin",
		"-hide_banner",
		"-loglevel", "warning",
		"-y",
		// Network resilience for HLS.
		"-reconnect", "1",
		"-reconnect_streamed", "1",
		"-reconnect_at_eof", "1",
		"-reconnect_delay_max", "5",
		"-rw_timeout", "15000000",
		"-http_persistent", "0",
		"-headers", headers,
		"-user_agent", userAgent,
		"-protocol_whitelist", "file,http,https,tcp,tls,crypto",
		"-allowed_extensions", "ALL",
		"-http_seekable", "0",
		"-fflags", "+discardcorrupt",
		"-i", audioURL,
		// Be tolerant to damaged frames in VK-protected streams.
		"-err_detect", "ignore_err",
		"-vn",
		"-acodec", "libmp3lame",
		"-b:a", "192k",
		outPath,
	)
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		if ctx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("ffmpeg timeout")
		}
		return fmt.Errorf("ffmpeg failed: %s", clipText(msg, 400))
	}
	return nil
}

func runFFmpegAudioDownloadFromFile(ctx context.Context, inPath, outPath string) error {
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx,
		"ffmpeg",
		"-nostdin",
		"-hide_banner",
		"-loglevel", "warning",
		"-y",
		"-i", inPath,
		"-vn",
		"-acodec", "libmp3lame",
		"-b:a", "192k",
		outPath,
	)
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		if ctx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("ffmpeg timeout")
		}
		return fmt.Errorf("ffmpeg file transcode failed: %s", clipText(msg, 400))
	}
	return nil
}

type audioDownloadRequest struct {
	AudioURL  string
	OutPath   string
	Threads   int
	UserAgent string
}

func downloadAudioMultiPart(ctx context.Context, req audioDownloadRequest) error {
	if strings.TrimSpace(req.OutPath) == "" {
		return errors.New("empty temp path for multipart download")
	}
	if req.Threads < 2 {
		return errors.New("multipart download requires threads >= 2")
	}
	if _, err := exec.LookPath("aria2c"); err != nil {
		return fmt.Errorf("aria2c not found")
	}
	headerUA := "User-Agent: " + req.UserAgent
	args := []string{
		"-c",
		"-x", strconv.Itoa(req.Threads),
		"-s", strconv.Itoa(req.Threads),
		"-k", "1M",
		"--max-tries=3",
		"--retry-wait=1",
		"--timeout=15",
		"--connect-timeout=10",
		"--file-allocation=none",
		"--header", headerUA,
		"-o", filepath.Base(req.OutPath),
		"-d", filepath.Dir(req.OutPath),
		req.AudioURL,
	}
	cmd := exec.CommandContext(ctx, "aria2c", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		if ctx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("aria2c timeout")
		}
		return fmt.Errorf("aria2c failed: %s", clipText(msg, 400))
	}
	return nil
}

func sendPhotoWithSpoilerAPI(ctx sendContext, img generatedImage, caption string) error {
	if ctx.Bot == nil || strings.TrimSpace(ctx.Bot.Token) == "" {
		return errors.New("bot token is empty")
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	_ = writer.WriteField("chat_id", strconv.FormatInt(ctx.ChatID, 10))
	if ctx.ReplyTo > 0 {
		_ = writer.WriteField("reply_to_message_id", strconv.Itoa(ctx.ReplyTo))
		_ = writer.WriteField("allow_sending_without_reply", "true")
	}
	if strings.TrimSpace(caption) != "" {
		_ = writer.WriteField("caption", strings.TrimSpace(caption))
	}
	_ = writer.WriteField("has_spoiler", "true")

	switch {
	case strings.TrimSpace(img.URL) != "":
		_ = writer.WriteField("photo", strings.TrimSpace(img.URL))
	case len(img.Bytes) > 0:
		part, err := writer.CreateFormFile("photo", "generated.png")
		if err != nil {
			_ = writer.Close()
			return err
		}
		if _, err := part.Write(img.Bytes); err != nil {
			_ = writer.Close()
			return err
		}
	default:
		_ = writer.Close()
		return errors.New("empty image payload")
	}

	if err := writer.Close(); err != nil {
		return err
	}

	endpoint := fmt.Sprintf("https://api.telegram.org/bot%s/sendPhoto", strings.TrimSpace(ctx.Bot.Token))
	req, err := http.NewRequest("POST", endpoint, &body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())

	client := &http.Client{Timeout: 35 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("telegram status=%d body=%s", resp.StatusCode, clipText(string(respBody), 600))
	}
	var tg struct {
		OK bool `json:"ok"`
	}
	if err := json.Unmarshal(respBody, &tg); err != nil {
		return nil
	}
	if !tg.OK {
		return fmt.Errorf("telegram response not ok: %s", clipText(string(respBody), 600))
	}
	return nil
}

func normalizeTelegramLineBreaks(s string) string {
	s = strings.ReplaceAll(s, "\\r\\n", "\n")
	s = strings.ReplaceAll(s, "\\n", "\n")
	s = strings.ReplaceAll(s, "\\r", "\n")
	s = strings.ReplaceAll(s, "<br>", "\n")
	s = strings.ReplaceAll(s, "<br/>", "\n")
	s = strings.ReplaceAll(s, "<br />", "\n")
	return s
}

func escapeMarkdownV2Text(s string) string {
	replacer := strings.NewReplacer(
		`\\`, `\\\\`,
		"_", `\_`,
		"*", `\*`,
		"[", `\[`,
		"]", `\]`,
		"(", `\(`,
		")", `\)`,
		"~", `\~`,
		"`", "\\`",
		">", `\>`,
		"#", `\#`,
		"+", `\+`,
		"-", `\-`,
		"=", `\=`,
		"|", `\|`,
		"{", `\{`,
		"}", `\}`,
		".", `\.`,
		"!", `\!`,
	)
	return replacer.Replace(s)
}

func escapeMarkdownV2Code(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, "`", "\\`")
	return s
}

func escapeMarkdownV2PreservingFences(s string) string {
	var out strings.Builder
	for {
		start := strings.Index(s, "```")
		if start < 0 {
			out.WriteString(escapeMarkdownV2Text(s))
			break
		}
		out.WriteString(escapeMarkdownV2Text(s[:start]))
		rest := s[start+3:]
		end := strings.Index(rest, "```")
		if end < 0 {
			// broken fence: treat all as plain text
			out.WriteString(escapeMarkdownV2Text("```" + rest))
			break
		}
		block := rest[:end]
		nl := strings.Index(block, "\n")
		code := block
		if nl >= 0 {
			code = block[nl+1:]
		}
		out.WriteString("```\n")
		out.WriteString(escapeMarkdownV2Code(code))
		out.WriteString("\n```")
		s = rest[end+3:]
	}
	return out.String()
}

func fetchChatAdminStatus(bot *tgbotapi.BotAPI, chatID int64, userID int64) bool {
	cfg := tgbotapi.GetChatMemberConfig{
		ChatConfigWithUser: tgbotapi.ChatConfigWithUser{
			ChatID: chatID,
			UserID: userID,
		},
	}
	member, err := bot.GetChatMember(cfg)
	if err != nil {
		return false
	}
	return member.Status == "administrator" || member.Status == "creator"
}

type templateContext struct {
	Bot            *tgbotapi.BotAPI
	Msg            *tgbotapi.Message
	CapturingText  string
	MatchText      string
	CaseSensitive  bool
	TemplateLookup func(string) string
}

func newTemplateContext(bot *tgbotapi.BotAPI, msg *tgbotapi.Message, tr *Trigger, lookup func(string) string) templateContext {
	if tr == nil {
		return templateContext{Bot: bot, Msg: msg, TemplateLookup: lookup}
	}
	return templateContext{
		Bot:            bot,
		Msg:            msg,
		CapturingText:  tr.CapturingText,
		MatchText:      tr.MatchText,
		CaseSensitive:  tr.CaseSensitive,
		TemplateLookup: lookup,
	}
}

func buildPromptFromMessage(ctx templateContext, promptTemplate string) string {
	prompt := strings.TrimSpace(promptTemplate)
	if prompt == "" {
		prompt = "Ответь коротко и по делу."
	}

	if ctx.Msg == nil {
		return prompt
	}

	if strings.Contains(promptTemplate, "{{") {
		return renderTemplateWithMessage(ctx, prompt)
	}

	replacements := buildMessageTemplateReplacements(ctx.Bot, ctx.Msg)
	return prompt + "\n\nСообщение пользователя:\n" + replacements["{{message}}"]
}

var regexQuantifierPattern = regexp.MustCompile(`\{[^}]*\}`)
var regexSpacePattern = regexp.MustCompile(`\\s\+|\\s\*|\\s`)
var templateExprPattern = regexp.MustCompile(`\{\{([^}]+)\}\}`)

func applyCapturingTemplate(s, capture, matchText string, caseSensitive bool) string {
	if strings.TrimSpace(s) == "" {
		return s
	}
	cleanCapture := strings.TrimSpace(capture)
	choice := deriveCapturingChoice(matchText, cleanCapture, caseSensitive)
	vars := map[string]string{
		"capturing_text":   cleanCapture,
		"capturing_choice": choice,
		"capturing_option": choice,
	}
	out := applyTemplatePipes(s, vars)
	for key, val := range vars {
		out = strings.ReplaceAll(out, "{{"+key+"}}", val)
	}
	return out
}

func deriveCapturingChoice(matchText, capture string, caseSensitive bool) string {
	capture = strings.TrimSpace(capture)
	if capture == "" || strings.TrimSpace(matchText) == "" {
		return ""
	}
	groups := extractRegexGroups(matchText)
	for _, g := range groups {
		if !strings.Contains(g, "|") {
			continue
		}
		g = trimGroupPrefix(g)
		parts := strings.Split(g, "|")
		for _, part := range parts {
			clean := cleanRegexAlt(part)
			if clean == "" {
				continue
			}
			if containsMatch(clean, capture, caseSensitive) {
				return clean
			}
		}
	}
	return ""
}

func applyTemplatePipes(s string, vars map[string]string) string {
	if strings.TrimSpace(s) == "" {
		return s
	}
	return templateExprPattern.ReplaceAllStringFunc(s, func(m string) string {
		expr := strings.TrimSpace(m[2 : len(m)-2])
		if expr == "" || !strings.Contains(expr, "|") {
			return m
		}
		out, ok := evalTemplatePipe(expr, vars)
		if !ok {
			return m
		}
		return out
	})
}

func evalTemplatePipe(expr string, vars map[string]string) (string, bool) {
	parts := strings.Split(expr, "|")
	if len(parts) == 0 {
		return "", false
	}
	base := strings.TrimSpace(parts[0])
	val, ok := vars[base]
	if !ok {
		return "", false
	}
	var list []string
	for _, raw := range parts[1:] {
		op := strings.TrimSpace(raw)
		name, argStr := splitPipeOp(op)
		args := parsePipeArgs(argStr)
		switch name {
		case "split":
			delim := ""
			if len(args) > 0 {
				delim = args[0]
			}
			if delim == "" {
				list = []string{val}
				continue
			}
			items := strings.Split(val, delim)
			for i := range items {
				items[i] = strings.TrimSpace(items[i])
			}
			list = items
			continue
		case "index":
			if len(args) == 0 {
				return "", true
			}
			idx, err := strconv.Atoi(args[0])
			if err != nil || idx < 0 {
				return "", true
			}
			if list == nil {
				return "", true
			}
			if idx >= len(list) {
				return "", true
			}
			val = list[idx]
			list = nil
			continue
		case "contains":
			if len(args) == 0 {
				val = "false"
				continue
			}
			if strings.Contains(val, args[0]) {
				val = "true"
			} else {
				val = "false"
			}
			continue
		case "istrue":
			if isTruthy(val) {
				if len(args) > 0 {
					val = args[0]
				} else {
					val = ""
				}
			} else {
				if len(args) > 1 {
					val = args[1]
				} else {
					val = ""
				}
			}
			continue
		case "gender":
			var variants genderVariants
			if len(args) > 0 {
				variants.Male = args[0]
			}
			if len(args) > 1 {
				variants.Female = args[1]
			}
			if len(args) > 2 {
				variants.Neuter = args[2]
			}
			if len(args) > 3 {
				variants.Plural = args[3]
			}
			if len(args) > 4 {
				variants.Unknown = args[4]
			}
			val = resolveGenderVariant(val, variants)
			continue
		default:
			continue
		}
	}
	return strings.TrimSpace(val), true
}

type pronounFlags struct {
	any     bool
	none    bool
	he      bool
	she     bool
	it      bool
	neutral bool
	they    bool
}

type genderVariants struct {
	Male    string
	Female  string
	Neuter  string
	Plural  string
	Unknown string
}

func resolveGenderVariant(tag string, variants genderVariants) string {
	flags := detectPronounFlags(tag)
	if flags.they {
		return variants.Plural
	}
	if flags.he {
		return variants.Male
	}
	if flags.she {
		return variants.Female
	}
	if flags.it {
		return variants.Neuter
	}
	return variants.Unknown
}

func detectPronounFlags(raw string) pronounFlags {
	raw = strings.ToLower(strings.TrimSpace(raw))
	if raw == "" {
		return pronounFlags{}
	}
	tokens := splitPronounTokens(raw)
	flags := pronounFlags{}
	for _, tok := range tokens {
		if tok == "" {
			continue
		}
		if tok == "any" || strings.HasPrefix(tok, "люб") {
			flags.any = true
		}
		if isPronounNoneToken(tok) {
			flags.none = true
		}
		if isPronounHeToken(tok) {
			flags.he = true
		}
		if isPronounSheToken(tok) {
			flags.she = true
		}
		if isPronounItToken(tok) {
			flags.it = true
		}
		if isPronounNeutralToken(tok) {
			flags.neutral = true
		}
		if isPronounTheyToken(tok) {
			flags.they = true
		}
	}
	return flags
}

func splitPronounTokens(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}

func isPronounNoneToken(tok string) bool {
	switch tok {
	case "0", "vo", "nul", "null", "no", "нет", "избег":
		return true
	default:
		return false
	}
}

func isPronounHeToken(tok string) bool {
	switch tok {
	case "1", "him", "boy", "mas", "его", "па", "му", "тот", "he", "он", "mal", "male", "man":
		return true
	default:
		if strings.HasPrefix(tok, "mas") {
			return true
		}
		return false
	}
}

func isPronounSheToken(tok string) bool {
	switch tok {
	case "2", "she", "her", "wom", "woman", "gir", "girl", "fem", "female", "она", "её", "ее", "де", "же", "та", "фем":
		return true
	default:
		if strings.HasPrefix(tok, "fem") || strings.HasPrefix(tok, "wom") || strings.HasPrefix(tok, "gir") {
			return true
		}
		return false
	}
}

func isPronounItToken(tok string) bool {
	switch tok {
	case "3", "it":
		return true
	default:
		return false
	}
}

func isPronounNeutralToken(tok string) bool {
	switch tok {
	case "4", "one", "neu", "оно", "то":
		return true
	default:
		if strings.HasPrefix(tok, "neu") {
			return true
		}
		return false
	}
}

func isPronounTheyToken(tok string) bool {
	switch tok {
	case "5", "the", "они", "их", "эти", "те":
		return true
	default:
		return false
	}
}

func splitPipeOp(s string) (string, string) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", ""
	}
	if idx := strings.IndexFunc(s, func(r rune) bool { return r == ' ' || r == '\t' }); idx >= 0 {
		return strings.TrimSpace(s[:idx]), strings.TrimSpace(s[idx+1:])
	}
	return s, ""
}

func parsePipeArgs(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	args := make([]string, 0, 2)
	for len(s) > 0 {
		s = strings.TrimLeft(s, " \t")
		if s == "" {
			break
		}
		if s[0] == '"' {
			end := strings.Index(s[1:], "\"")
			if end < 0 {
				args = append(args, s[1:])
				break
			}
			args = append(args, s[1:1+end])
			s = s[2+end:]
			continue
		}
		next := len(s)
		for i, r := range s {
			if r == ' ' || r == '\t' {
				next = i
				break
			}
		}
		args = append(args, s[:next])
		if next >= len(s) {
			break
		}
		s = s[next:]
	}
	return args
}

func isTruthy(v string) bool {
	v = strings.ToLower(strings.TrimSpace(v))
	if v == "" {
		return false
	}
	switch v {
	case "true", "1", "yes", "y", "да", "истина":
		return true
	default:
		return false
	}
}

func containsMatch(candidate, capture string, caseSensitive bool) bool {
	if !caseSensitive {
		candidate = strings.ToLower(candidate)
		capture = strings.ToLower(capture)
	}
	return strings.Contains(candidate, capture) || strings.Contains(capture, candidate)
}

func trimGroupPrefix(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "?") {
		if idx := strings.Index(s, ":"); idx >= 0 {
			s = s[idx+1:]
		}
	}
	return strings.TrimSpace(s)
}

func extractRegexGroups(pattern string) []string {
	if pattern == "" {
		return nil
	}
	out := make([]string, 0, 4)
	var stack []int
	escaped := false
	for i := 0; i < len(pattern); i++ {
		ch := pattern[i]
		if escaped {
			escaped = false
			continue
		}
		if ch == '\\' {
			escaped = true
			continue
		}
		if ch == '(' {
			stack = append(stack, i)
			continue
		}
		if ch == ')' && len(stack) > 0 {
			start := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if start+1 < i {
				out = append(out, pattern[start+1:i])
			}
		}
	}
	return out
}

func cleanRegexAlt(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	s = strings.ReplaceAll(s, "(?:", "")
	s = strings.ReplaceAll(s, "?:", "")
	s = strings.ReplaceAll(s, "(", "")
	s = strings.ReplaceAll(s, ")", "")
	s = strings.ReplaceAll(s, "^", "")
	s = strings.ReplaceAll(s, "$", "")
	s = regexSpacePattern.ReplaceAllString(s, " ")
	s = regexQuantifierPattern.ReplaceAllString(s, "")
	s = strings.ReplaceAll(s, "\\", "")
	s = strings.ReplaceAll(s, "?", "")
	s = strings.ReplaceAll(s, "*", "")
	s = strings.ReplaceAll(s, "+", "")
	s = strings.Join(strings.Fields(s), " ")
	return strings.TrimSpace(s)
}

func pickResponseVariantText(items []ResponseTextItem) string {
	if len(items) == 0 {
		return ""
	}
	nonEmpty := make([]string, 0, len(items))
	for _, it := range items {
		val := strings.TrimSpace(it.Text)
		if val != "" {
			nonEmpty = append(nonEmpty, val)
		}
	}
	if len(nonEmpty) == 0 {
		return ""
	}
	return nonEmpty[rand.Intn(len(nonEmpty))]
}

func renderTemplateWithMessage(ctx templateContext, template string) string {
	if strings.TrimSpace(template) == "" {
		return template
	}
	template = expandTemplateCalls(template, ctx.TemplateLookup)
	vars := buildTemplateVars(ctx)
	out := applyTemplatePipes(template, vars)
	for key, val := range vars {
		out = strings.ReplaceAll(out, "{{"+key+"}}", val)
	}
	return out
}

func buildResponseFromMessage(ctx templateContext, template string) string {
	return renderTemplateWithMessage(ctx, template)
}

func isOlenyamTrigger(tr *Trigger) bool {
	if tr == nil {
		return false
	}
	title := strings.ToLower(strings.TrimSpace(tr.Title))
	return strings.Contains(title, "оле-ням") || strings.Contains(title, "оленям") || strings.Contains(title, "оле ням")
}

func buildTemplateVars(ctx templateContext) map[string]string {
	vars := make(map[string]string, 24)
	if ctx.Msg != nil {
		replacements := buildMessageTemplateReplacements(ctx.Bot, ctx.Msg)
		for tag, value := range replacements {
			name := strings.TrimSpace(strings.TrimPrefix(tag, "{{"))
			name = strings.TrimSuffix(name, "}}")
			if name != "" {
				vars[name] = value
			}
		}
	}
	cleanCapture := strings.TrimSpace(ctx.CapturingText)
	choice := deriveCapturingChoice(ctx.MatchText, cleanCapture, ctx.CaseSensitive)
	vars["capturing_text"] = cleanCapture
	vars["capturing_choice"] = choice
	vars["capturing_option"] = choice
	return vars
}

func buildImageSearchQueryFromMessage(ctx templateContext, queryTemplate string) string {
	query := strings.TrimSpace(renderTemplateWithMessage(ctx, queryTemplate))
	if ctx.Msg == nil {
		return query
	}
	if query == "" {
		replacements := buildMessageTemplateReplacements(ctx.Bot, ctx.Msg)
		return strings.TrimSpace(replacements["{{message}}"])
	}
	return strings.TrimSpace(query)
}

func buildSpotifyMusicQueryFromMessage(ctx templateContext, queryTemplate string) string {
	query := strings.TrimSpace(renderTemplateWithMessage(ctx, queryTemplate))
	if ctx.Msg == nil {
		return query
	}
	if query == "" {
		replacements := buildMessageTemplateReplacements(ctx.Bot, ctx.Msg)
		return strings.TrimSpace(replacements["{{message}}"])
	}
	return strings.TrimSpace(query)
}

func firstNonEmptyEnv(keys ...string) string {
	for _, key := range keys {
		if v := strings.TrimSpace(os.Getenv(strings.TrimSpace(key))); v != "" {
			return v
		}
	}
	return ""
}

func buildMessageTemplateReplacements(bot *tgbotapi.BotAPI, msg *tgbotapi.Message) map[string]string {
	if msg == nil || msg.From == nil || msg.Chat == nil {
		return map[string]string{
			"{{message}}":   "",
			"{{user_text}}": "",
		}
	}
	extractLabel := func(s string) string {
		s = strings.TrimSpace(s)
		if s == "" {
			return ""
		}
		re := regexp.MustCompile(`\(([^()]{1,80})\)`)
		m := re.FindStringSubmatch(s)
		if len(m) > 1 {
			return strings.TrimSpace(m[1])
		}
		return ""
	}
	buildDisplayName := func(u *tgbotapi.User) string {
		return buildUserDisplayName(u)
	}

	userText := strings.TrimSpace(msg.Text)
	if userText == "" {
		userText = strings.TrimSpace(msg.Caption)
	}
	userDisplayName := buildDisplayName(msg.From)
	userUsername := strings.TrimSpace(msg.From.UserName)
	userLabel := extractLabel(userDisplayName)
	senderTag := ""
	if bot != nil && msg.From != nil {
		senderTag = getChatMemberTagRaw(bot.Token, msg.Chat.ID, msg.From.ID)
	}
	senderTagDisplay := senderTag
	if strings.TrimSpace(senderTagDisplay) == "" {
		senderTagDisplay = "не указан"
	}

	replyText := ""
	replyUserID := ""
	replyFirstName := ""
	replyUsername := ""
	replyDisplayName := ""
	replyLabel := ""
	replyUserLink := ""
	replySenderTag := ""
	if msg.ReplyToMessage != nil {
		replyText = strings.TrimSpace(msg.ReplyToMessage.Text)
		if replyText == "" {
			replyText = strings.TrimSpace(msg.ReplyToMessage.Caption)
		}
		if msg.ReplyToMessage.From != nil {
			replyUserID = strconv.FormatInt(msg.ReplyToMessage.From.ID, 10)
			replyFirstName = strings.TrimSpace(msg.ReplyToMessage.From.FirstName)
			replyUsername = strings.TrimSpace(msg.ReplyToMessage.From.UserName)
			replyDisplayName = buildDisplayName(msg.ReplyToMessage.From)
			replyLabel = extractLabel(replyDisplayName)
			replyUserLink = buildUserLink(msg.ReplyToMessage.From)
			if bot != nil {
				replySenderTag = getChatMemberTagRaw(bot.Token, msg.Chat.ID, msg.ReplyToMessage.From.ID)
			}
		}
	}
	replySenderTagDisplay := replySenderTag
	if strings.TrimSpace(replySenderTagDisplay) == "" {
		replySenderTagDisplay = "не указан"
	}

	chatTitle := strings.TrimSpace(msg.Chat.Title)

	return map[string]string{
		"{{message}}":            userText,
		"{{user_text}}":          userText,
		"{{user_id}}":            strconv.FormatInt(msg.From.ID, 10),
		"{{user_first_name}}":    strings.TrimSpace(msg.From.FirstName),
		"{{user_username}}":      userUsername,
		"{{user_display_name}}":  userDisplayName,
		"{{user_label}}":         userLabel,
		"{{sender_tag}}":         senderTagDisplay,
		"{{user_link}}":          buildUserLink(msg.From),
		"{{chat_id}}":            strconv.FormatInt(msg.Chat.ID, 10),
		"{{chat_title}}":         chatTitle,
		"{{reply_text}}":         replyText,
		"{{reply_user_id}}":      replyUserID,
		"{{reply_first_name}}":   replyFirstName,
		"{{reply_username}}":     replyUsername,
		"{{reply_display_name}}": replyDisplayName,
		"{{reply_label}}":        replyLabel,
		"{{reply_user_link}}":    replyUserLink,
		"{{reply_sender_tag}}":   replySenderTagDisplay,
	}
}

func buildUserDisplayName(u *tgbotapi.User) string {
	if u == nil {
		return ""
	}
	full := strings.TrimSpace(strings.TrimSpace(u.FirstName) + " " + strings.TrimSpace(u.LastName))
	if full != "" {
		return full
	}
	if strings.TrimSpace(u.UserName) != "" {
		return "@" + strings.TrimSpace(u.UserName)
	}
	return strconv.FormatInt(u.ID, 10)
}

func buildUserLink(u *tgbotapi.User) string {
	if u == nil {
		return "Участник без имени"
	}
	name := strings.TrimSpace(u.FirstName)
	if name == "" {
		name = strings.TrimSpace(u.UserName)
	}
	if name == "" {
		name = "Участник без имени"
	}
	return fmt.Sprintf("<a href=\"tg://user?id=%d\">%s</a>", u.ID, name)
}

func extractNewMemberDisplayNames(msg *tgbotapi.Message) []string {
	if msg == nil {
		return nil
	}
	seen := make(map[int64]struct{})
	out := make([]string, 0, 4)
	add := func(u *tgbotapi.User) {
		if u == nil {
			return
		}
		if _, ok := seen[u.ID]; ok {
			return
		}
		seen[u.ID] = struct{}{}
		name := strings.TrimSpace(buildUserDisplayName(u))
		if name != "" {
			out = append(out, name)
		}
	}
	if len(msg.NewChatMembers) > 0 {
		for i := range msg.NewChatMembers {
			u := &msg.NewChatMembers[i]
			add(u)
		}
	}
	return out
}

type triggerActionDeps struct {
	Bot               *tgbotapi.BotAPI
	IdleTracker       *trigger.IdleTracker
	GPTDebouncer      *gpt.Debouncer
	SpotifyMusic      *spotifymusic.Client
	SpotifyDownloader spotifymusic.Downloader
	TemplateLookup    func(string) string
	AudioQueue        *audioSendQueue
}

type triggerHandlerDeps struct {
	triggerActionDeps
	Allowed    chatAllowList
	Engine     *engine.TriggerEngine
	Store      *Store
	AdminCache *adminStatusCache
	ChatRecent *chatRecentStore
}

func handleNewMemberUpdate(deps triggerHandlerDeps, upd *rawChatMemberUpdated) {
	if upd == nil || upd.Chat == nil || upd.NewChatMember == nil || upd.NewChatMember.User == nil {
		return
	}
	// Trigger only on join events.
	if upd.NewChatMember.Status != "member" {
		return
	}
	oldStatus := ""
	if upd.OldChatMember != nil {
		oldStatus = upd.OldChatMember.Status
	}
	if oldStatus == "member" || oldStatus == "administrator" || oldStatus == "creator" {
		return
	}
	chatID := upd.Chat.ID
	if !deps.Allowed.Allows(chatID) {
		return
	}
	if upd.NewChatMember.User.IsBot {
		return
	}
	msg := &tgbotapi.Message{
		Chat: &tgbotapi.Chat{ID: chatID, Title: upd.Chat.Title},
		From: &tgbotapi.User{
			ID:        upd.NewChatMember.User.ID,
			UserName:  upd.NewChatMember.User.UserName,
			FirstName: upd.NewChatMember.User.FirstName,
			LastName:  upd.NewChatMember.User.LastName,
			IsBot:     upd.NewChatMember.User.IsBot,
		},
	}
	if deps.AdminCache != nil {
		_, _ = deps.AdminCache.EnsureChatAdminsFresh(deps.Bot, chatID)
	}
	items, err := deps.Store.ListTriggersCached()
	if err != nil {
		log.Printf("list triggers failed: %v", err)
		return
	}
	tr := deps.Engine.SelectNewMember(engine.SelectNewMemberInput{
		Bot:      deps.Bot,
		Msg:      msg,
		Triggers: items,
		IsAdminFn: func() bool {
			if deps.AdminCache == nil {
				return false
			}
			return deps.AdminCache.IsChatAdmin(deps.Bot, chatID, msg.From.ID)
		},
	})
	if tr == nil {
		return
	}
	tr.CapturingText = ""
	handleTriggerActionForMessage(deps.triggerActionDeps, msg, tr, "")
	if deps.ChatRecent != nil {
		deps.ChatRecent.Add(chatID, recentChatMessage{
			MessageID: 0,
			UserName:  buildUserDisplayName(msg.From),
			Text:      "",
			At:        time.Now(),
		})
	}
}

func handleTriggerActionForMessage(deps triggerActionDeps, msg *tgbotapi.Message, tr *Trigger, recentBefore string) {
	if msg == nil || tr == nil {
		return
	}
	rawTemplate := pickResponseVariantText(tr.ResponseText)
	resolvedTemplate := expandTemplateCalls(rawTemplate, deps.TemplateLookup)
	switch tr.ActionType {
	case "delete":
		if msg.MessageID == 0 {
			return
		}
		cfg := tgbotapi.DeleteMessageConfig{
			ChatID:    msg.Chat.ID,
			MessageID: msg.MessageID,
		}
		if _, err := deps.Bot.Request(cfg); err != nil {
			log.Printf("delete message failed: %v", err)
			reportChatFailure(deps.Bot, msg.Chat.ID, "ошибка удаления сообщения", err)
		} else if deps.IdleTracker != nil {
			deps.IdleTracker.MarkActivity(msg.Chat.ID, time.Now())
		}
	case "gpt_prompt":
		ctx := ""
		if isOlenyamTrigger(tr) {
			ctx = recentBefore
		}
		if deps.GPTDebouncer != nil {
			trCopy := *tr
			trCopy.ResponseText = []ResponseTextItem{{Text: resolvedTemplate}}
			deps.GPTDebouncer.Schedule(msg.Chat.ID, gpt.PromptTask{
				Bot:            deps.Bot,
				Trigger:        trCopy,
				Msg:            msg,
				RecentContext:  ctx,
				TemplateLookup: deps.TemplateLookup,
				IdleMarkActivity: func(chatID int64, now time.Time) {
					if deps.IdleTracker != nil {
						deps.IdleTracker.MarkActivity(chatID, now)
					}
				},
				ChatID: msg.Chat.ID,
			})
			if debugTriggerLogEnabled {
				log.Printf("gpt prompt queued (debounce) trigger=%d chat=%d msg=%d", tr.ID, msg.Chat.ID, msg.MessageID)
			}
			return
		}
		trCopy := *tr
		trCopy.ResponseText = []ResponseTextItem{{Text: resolvedTemplate}}
		executeGPTPromptTask(gpt.PromptTask{
			Bot:            deps.Bot,
			Trigger:        trCopy,
			Msg:            msg,
			RecentContext:  ctx,
			TemplateLookup: deps.TemplateLookup,
			IdleMarkActivity: func(chatID int64, now time.Time) {
				if deps.IdleTracker != nil {
					deps.IdleTracker.MarkActivity(chatID, now)
				}
			},
			ChatID: msg.Chat.ID,
		})
	case "gpt_image":
		tmplCtx := newTemplateContext(deps.Bot, msg, tr, deps.TemplateLookup)
		img, err := generateChatGPTImage(tmplCtx, resolvedTemplate)
		if err != nil {
			log.Printf("gpt image failed: %v", err)
			reportChatFailure(deps.Bot, msg.Chat.ID, "ошибка генерации картинки в ChatGPT", err)
			return
		}
		replyTo := 0
		if tr.Reply || tr.TriggerMode == "command_reply" {
			replyTo = msg.MessageID
		}
		sendCtx := sendContext{Bot: deps.Bot, ChatID: msg.Chat.ID, ReplyTo: replyTo}
		if ok := sendPhoto(sendCtx, img, "CW: сгенерено нейросетью", true); ok && deps.IdleTracker != nil {
			deps.IdleTracker.MarkActivity(msg.Chat.ID, time.Now())
			deleteTriggerSourceMessage(deps.Bot, msg, tr)
		}
	case "search_image":
		tmplCtx := newTemplateContext(deps.Bot, msg, tr, deps.TemplateLookup)
		query := buildImageSearchQueryFromMessage(tmplCtx, resolvedTemplate)
		img, err := searchImageInSerpAPI(query)
		if err != nil {
			log.Printf("search image failed: %v", err)
			reportChatFailure(deps.Bot, msg.Chat.ID, "ошибка поиска картинки", err)
			return
		}
		replyTo := 0
		if tr.Reply || tr.TriggerMode == "command_reply" {
			replyTo = msg.MessageID
		}
		sendCtx := sendContext{Bot: deps.Bot, ChatID: msg.Chat.ID, ReplyTo: replyTo}
		if ok := sendPhoto(sendCtx, img, "", false); ok && deps.IdleTracker != nil {
			deps.IdleTracker.MarkActivity(msg.Chat.ID, time.Now())
			deleteTriggerSourceMessage(deps.Bot, msg, tr)
		}
	case "spotify_music_audio", "vk_music_audio":
		if deps.SpotifyMusic == nil || !deps.SpotifyMusic.Enabled() {
			reportChatFailure(deps.Bot, msg.Chat.ID, "ошибка Spotify-музыки", errors.New("SPOTIPY_CLIENT_ID/SPOTIPY_CLIENT_SECRET не настроены"))
		} else {
			reportChatFailure(deps.Bot, msg.Chat.ID, "ошибка Spotify-музыки", errors.New("Spotify музыка не поддерживается для события входа"))
		}
		return
	default:
		replyTo := 0
		if tr.Reply || tr.TriggerMode == "command_reply" {
			replyTo = msg.MessageID
		}
		tmplCtx := newTemplateContext(deps.Bot, msg, tr, deps.TemplateLookup)
		out := buildResponseFromMessage(tmplCtx, resolvedTemplate)
		sendCtx := sendContext{Bot: deps.Bot, ChatID: msg.Chat.ID, ReplyTo: replyTo}
		if ok := sendHTML(sendCtx, out, tr.Preview); ok && deps.IdleTracker != nil {
			deps.IdleTracker.MarkActivity(msg.Chat.ID, time.Now())
			deleteTriggerSourceMessage(deps.Bot, msg, tr)
		}
	}
}

func getUpdatesChanWithEmojiMeta(bot *tgbotapi.BotAPI, config tgbotapi.UpdateConfig) <-chan updateWithEmojiMeta {
	ch := make(chan updateWithEmojiMeta, 100)
	go func() {
		for {
			items, err := getUpdatesWithEmojiMeta(bot, config)
			if err != nil {
				log.Println(err)
				log.Println("Failed to get updates, retrying in 3 seconds...")
				time.Sleep(3 * time.Second)
				continue
			}
			for _, item := range items {
				if item.Update.UpdateID >= config.Offset {
					config.Offset = item.Update.UpdateID + 1
					ch <- item
				}
			}
		}
	}()
	return ch
}

func getUpdatesWithEmojiMeta(bot *tgbotapi.BotAPI, config tgbotapi.UpdateConfig) ([]updateWithEmojiMeta, error) {
	resp, err := bot.Request(config)
	if err != nil {
		return nil, err
	}
	if !resp.Ok {
		return nil, errors.New(resp.Description)
	}
	var rawItems []json.RawMessage
	if err := json.Unmarshal(resp.Result, &rawItems); err != nil {
		return nil, err
	}
	out := make([]updateWithEmojiMeta, 0, len(rawItems))
	for _, rawItem := range rawItems {
		var upd tgbotapi.Update
		if err := json.Unmarshal(rawItem, &upd); err != nil {
			return nil, err
		}
		var rawUpd rawUpdateWithEmoji
		if err := json.Unmarshal(rawItem, &rawUpd); err != nil {
			return nil, err
		}
		out = append(out, updateWithEmojiMeta{
			Update:          upd,
			RawMessage:      rawUpd.Message,
			RawChatMember:   rawUpd.ChatMember,
			RawMyChatMember: rawUpd.MyChatMember,
		})
	}
	return out, nil
}

func extractCustomEmojiFromRaw(rawMsg *rawMessageWithEmoji) ([]customEmojiHit, int) {
	if rawMsg == nil {
		return nil, 0
	}
	out := make([]customEmojiHit, 0, 4)
	seen := make(map[string]int)
	count := 0
	push := func(e rawMessageEntity, srcText string) {
		if e.Type != "custom_emoji" {
			return
		}
		count++
		id := strings.TrimSpace(e.CustomEmojiID)
		if id == "" {
			return
		}
		fallback := strings.TrimSpace(sliceUTF16ByEntity(srcText, e.Offset, e.Length))
		if idx, ok := seen[id]; ok {
			if out[idx].Fallback == "" && fallback != "" {
				out[idx].Fallback = fallback
			}
			return
		}
		seen[id] = len(out)
		out = append(out, customEmojiHit{
			ID:       id,
			Fallback: fallback,
		})
	}
	for _, e := range rawMsg.Entities {
		push(e, rawMsg.Text)
	}
	for _, e := range rawMsg.CaptionEntities {
		push(e, rawMsg.Caption)
	}
	return out, count
}

func sliceUTF16ByEntity(s string, offsetCU, lengthCU int) string {
	if strings.TrimSpace(s) == "" || offsetCU < 0 || lengthCU <= 0 {
		return ""
	}
	endCU := offsetCU + lengthCU
	if endCU <= offsetCU {
		return ""
	}
	var b strings.Builder
	cu := 0
	for _, r := range s {
		n := utf16.RuneLen(r)
		if n < 1 {
			n = 1
		}
		nextCU := cu + n
		if nextCU <= offsetCU {
			cu = nextCU
			continue
		}
		if cu >= endCU {
			break
		}
		b.WriteRune(r)
		cu = nextCU
	}
	return b.String()
}

func buildTGEmojiSnippet(id, fallback string) string {
	id = strings.TrimSpace(id)
	fallback = strings.TrimSpace(fallback)
	if fallback == "" {
		fallback = "🙂"
	}
	return fmt.Sprintf(`<tg-emoji emoji-id="%s">%s</tg-emoji>`, id, fallback)
}

func searchImageInSerpAPI(query string) (generatedImage, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return generatedImage{}, errors.New("empty search query")
	}

	apiKey := strings.TrimSpace(os.Getenv("SERPAPI_KEY"))
	if apiKey == "" {
		return generatedImage{}, errors.New("SERPAPI_KEY is required for search_image")
	}
	engine := strings.TrimSpace(os.Getenv("SERPAPI_ENGINE"))
	if engine == "" {
		engine = "google_images"
	}

	params := url.Values{}
	params.Set("api_key", apiKey)
	params.Set("engine", engine)
	params.Set("q", query)
	params.Set("hl", "ru")
	params.Set("gl", "ru")
	params.Set("num", "10")
	params.Set("safe", "active")

	endpoint := "https://serpapi.com/search.json?" + params.Encode()
	client := &http.Client{Timeout: 20 * time.Second}
	req, err := http.NewRequest("GET", endpoint, nil)
	if err != nil {
		return generatedImage{}, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return generatedImage{}, err
	}
	defer resp.Body.Close()
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return generatedImage{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return generatedImage{}, fmt.Errorf("serpapi status=%d body=%s", resp.StatusCode, clipText(string(bodyBytes), 600))
	}

	var payload struct {
		Error        string `json:"error"`
		ImagesResult []struct {
			Original  string `json:"original"`
			Link      string `json:"link"`
			Thumbnail string `json:"thumbnail"`
		} `json:"images_results"`
	}
	if err := json.Unmarshal(bodyBytes, &payload); err != nil {
		return generatedImage{}, err
	}
	if strings.TrimSpace(payload.Error) != "" {
		return generatedImage{}, errors.New(payload.Error)
	}
	if len(payload.ImagesResult) == 0 {
		return generatedImage{}, errors.New("nothing found")
	}

	candidates := make([]string, 0, len(payload.ImagesResult))
	for _, it := range payload.ImagesResult {
		u := strings.TrimSpace(it.Original)
		if u == "" {
			u = strings.TrimSpace(it.Link)
		}
		if u == "" {
			u = strings.TrimSpace(it.Thumbnail)
		}
		if u == "" {
			continue
		}
		candidates = append(candidates, u)
	}
	if len(candidates) == 0 {
		return generatedImage{}, errors.New("image URL is empty")
	}

	perm := rand.Perm(len(candidates))
	var lastErr error
	for _, idx := range perm {
		u := candidates[idx]
		imgBytes, err := fetchImageBytes(u)
		if err != nil {
			lastErr = err
			continue
		}
		return generatedImage{Bytes: imgBytes}, nil
	}
	if lastErr != nil {
		return generatedImage{}, fmt.Errorf("all image links failed: %w", lastErr)
	}
	return generatedImage{}, errors.New("image URL is empty")
}

func fetchImageBytes(imageURL string) ([]byte, error) {
	client := &http.Client{Timeout: 15 * time.Second}
	req, err := http.NewRequest("GET", imageURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")
	req.Header.Set("Accept", "image/*,*/*;q=0.8")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, 12<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("download status=%d url=%s", resp.StatusCode, clipText(imageURL, 140))
	}
	if len(bodyBytes) == 0 {
		return nil, fmt.Errorf("downloaded empty body url=%s", clipText(imageURL, 140))
	}
	ctype := strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Type")))
	if ctype != "" && !strings.Contains(ctype, "image/") {
		return nil, fmt.Errorf("not an image content-type=%s url=%s", ctype, clipText(imageURL, 140))
	}
	return bodyBytes, nil
}

func generateChatGPTReply(ctx templateContext, promptTemplate string, recentContext string) (string, error) {
	apiKey := strings.TrimSpace(os.Getenv("OPENAI_API_KEY"))
	if apiKey == "" {
		return "", errors.New("OPENAI_API_KEY is empty")
	}
	model := strings.TrimSpace(os.Getenv("OPENAI_MODEL"))
	if model == "" {
		model = "gpt-4.1-mini"
	}

	prompt := buildPromptFromMessage(ctx, promptTemplate)
	if strings.TrimSpace(recentContext) != "" {
		prompt = prompt + "\n\nБлижайший контекст чата (последние сообщения):\n" + recentContext
	}
	if debugGPTLogEnabled {
		log.Printf("gpt request model=%s prompt=%q", model, clipText(prompt, 1800))
	}

	payload := map[string]interface{}{
		"model": model,
		"messages": []map[string]string{
			{"role": "system", "content": "Ты вежливый помощник для чата. Отвечай на русском, кратко и по теме."},
			{"role": "user", "content": prompt},
		},
	}
	body, _ := json.Marshal(payload)

	req, err := http.NewRequest("POST", "https://api.openai.com/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 25 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if debugGPTLogEnabled {
		log.Printf("gpt response status=%d body=%q", resp.StatusCode, clipText(string(bodyBytes), 1800))
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("openai status=%d body=%s", resp.StatusCode, clipText(string(bodyBytes), 600))
	}

	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(bodyBytes, &result); err != nil {
		return "", err
	}
	if len(result.Choices) == 0 {
		return "", errors.New("empty choices")
	}
	out := strings.TrimSpace(result.Choices[0].Message.Content)
	if out == "" {
		return "", errors.New("empty answer")
	}
	return out, nil
}

func generateChatGPTImage(ctx templateContext, promptTemplate string) (generatedImage, error) {
	apiKey := strings.TrimSpace(os.Getenv("OPENAI_API_KEY"))
	if apiKey == "" {
		return generatedImage{}, errors.New("OPENAI_API_KEY is empty")
	}
	model := strings.TrimSpace(os.Getenv("OPENAI_IMAGE_MODEL"))
	if model == "" {
		model = "gpt-image-1"
	}
	size := strings.TrimSpace(os.Getenv("OPENAI_IMAGE_SIZE"))
	if size == "" {
		size = "1024x1024"
	}

	prompt := buildPromptFromMessage(ctx, promptTemplate)
	if debugGPTLogEnabled {
		log.Printf("gpt image request model=%s size=%s prompt=%q", model, size, clipText(prompt, 1400))
	}

	payload := map[string]interface{}{
		"model":  model,
		"prompt": prompt,
		"size":   size,
	}
	body, _ := json.Marshal(payload)

	req, err := http.NewRequest("POST", "https://api.openai.com/v1/images/generations", bytes.NewReader(body))
	if err != nil {
		return generatedImage{}, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return generatedImage{}, err
	}
	defer resp.Body.Close()
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return generatedImage{}, err
	}
	if debugGPTLogEnabled {
		log.Printf("gpt image response status=%d body=%q", resp.StatusCode, clipText(string(bodyBytes), 1800))
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return generatedImage{}, fmt.Errorf("openai images status=%d body=%s", resp.StatusCode, clipText(string(bodyBytes), 600))
	}

	var result struct {
		Data []struct {
			URL     string `json:"url"`
			B64JSON string `json:"b64_json"`
		} `json:"data"`
	}
	if err := json.Unmarshal(bodyBytes, &result); err != nil {
		return generatedImage{}, err
	}
	if len(result.Data) == 0 {
		return generatedImage{}, errors.New("empty images data")
	}

	if u := strings.TrimSpace(result.Data[0].URL); u != "" {
		return generatedImage{URL: u}, nil
	}
	if b64 := strings.TrimSpace(result.Data[0].B64JSON); b64 != "" {
		decoded, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			return generatedImage{}, fmt.Errorf("decode b64 image failed: %w", err)
		}
		if len(decoded) == 0 {
			return generatedImage{}, errors.New("decoded image is empty")
		}
		return generatedImage{Bytes: decoded}, nil
	}

	return generatedImage{}, errors.New("image payload has neither url nor b64_json")
}

func getChatMemberTagRaw(token string, chatID, userID int64) string {
	token = strings.TrimSpace(token)
	if token == "" {
		return ""
	}
	u := fmt.Sprintf(
		"https://api.telegram.org/bot%s/getChatMember?chat_id=%s&user_id=%s",
		token,
		url.QueryEscape(strconv.FormatInt(chatID, 10)),
		url.QueryEscape(strconv.FormatInt(userID, 10)),
	)
	client := &http.Client{Timeout: 6 * time.Second}
	resp, err := client.Get(u)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	var payload struct {
		OK     bool `json:"ok"`
		Result struct {
			Tag         string `json:"tag"`
			CustomTitle string `json:"custom_title"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return ""
	}
	if !payload.OK {
		return ""
	}
	tag := strings.TrimSpace(payload.Result.Tag)
	if tag != "" {
		return tag
	}
	return strings.TrimSpace(payload.Result.CustomTitle)
}

func reportChatFailure(bot *tgbotapi.BotAPI, chatID int64, context string, err error) {
	if !chatErrorLogEnabled || bot == nil || chatID == 0 || err == nil {
		return
	}
	msgText := strings.TrimSpace(err.Error())
	if len(msgText) > 300 {
		msgText = msgText[:300] + "..."
	}
	text := fmt.Sprintf("⚠️ %s: %s", strings.TrimSpace(context), msgText)
	m := tgbotapi.NewMessage(chatID, text)
	_, _ = bot.Send(m)
}

func clipText(s string, max int) string {
	s = strings.TrimSpace(s)
	if max <= 0 || len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
