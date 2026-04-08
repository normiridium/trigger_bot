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
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf16"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	vkmusic "github.com/normiridium/vk-music-bot-api/vkmusic"
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
	values map[string]adminCacheEntry
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
	Message *rawMessageWithEmoji `json:"message"`
}

type updateWithEmojiMeta struct {
	Update     tgbotapi.Update
	RawMessage *rawMessageWithEmoji
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

type gptPromptTask struct {
	Bot           *tgbotapi.BotAPI
	Trigger       Trigger
	Msg           *tgbotapi.Message
	RecentContext string
	IdleTracker   *chatIdleTracker
	ChatID        int64
}

type gptPromptDebouncer struct {
	mu      sync.Mutex
	delay   time.Duration
	pending map[int64]*gptPromptDebounceEntry
}

type gptPromptDebounceEntry struct {
	timer      *time.Timer
	task       gptPromptTask
	hasPending bool
	lastSentAt time.Time
}

type chatIdleState struct {
	firstSeen    time.Time
	lastActivity time.Time
}

type chatIdleTracker struct {
	mu    sync.RWMutex
	chats map[int64]chatIdleState
}

func newChatIdleTracker() *chatIdleTracker {
	return &chatIdleTracker{
		chats: make(map[int64]chatIdleState),
	}
}

func (t *chatIdleTracker) Seen(chatID int64, now time.Time) {
	if t == nil || chatID == 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	st := t.chats[chatID]
	if st.firstSeen.IsZero() {
		st.firstSeen = now
	}
	t.chats[chatID] = st
}

func (t *chatIdleTracker) MarkActivity(chatID int64, now time.Time) {
	if t == nil || chatID == 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	st := t.chats[chatID]
	if st.firstSeen.IsZero() {
		st.firstSeen = now
	}
	st.lastActivity = now
	t.chats[chatID] = st
}

func (t *chatIdleTracker) ShouldAutoReply(chatID int64, idleAfter time.Duration, now time.Time) bool {
	if t == nil || chatID == 0 || idleAfter <= 0 {
		return false
	}
	t.mu.RLock()
	st, ok := t.chats[chatID]
	t.mu.RUnlock()
	if !ok {
		return false
	}
	base := st.lastActivity
	if base.IsZero() {
		base = st.firstSeen
	}
	if base.IsZero() {
		return false
	}
	return now.Sub(base) >= idleAfter
}

func newGPTPromptDebouncer(delay time.Duration) *gptPromptDebouncer {
	if delay <= 0 {
		return nil
	}
	return &gptPromptDebouncer{
		delay:   delay,
		pending: make(map[int64]*gptPromptDebounceEntry),
	}
}

func (d *gptPromptDebouncer) Schedule(chatID int64, task gptPromptTask) {
	if d == nil || chatID == 0 {
		runGPTPromptTask(task)
		return
	}

	executeNow := false
	now := time.Now()
	d.mu.Lock()
	ent, ok := d.pending[chatID]
	if !ok {
		ent = &gptPromptDebounceEntry{}
		d.pending[chatID] = ent
	}

	// Leading edge: if quiet window already passed, answer immediately.
	if ent.lastSentAt.IsZero() || now.Sub(ent.lastSentAt) >= d.delay {
		ent.task = task
		ent.hasPending = false
		ent.lastSentAt = now
		if ent.timer != nil {
			ent.timer.Stop()
			ent.timer = nil
		}
		executeNow = true
	} else {
		// Trailing edge inside active window: keep only latest task.
		ent.task = task
		ent.hasPending = true
		remaining := d.delay - now.Sub(ent.lastSentAt)
		if remaining < 10*time.Millisecond {
			remaining = 10 * time.Millisecond
		}
		if ent.timer != nil {
			ent.timer.Stop()
		}
		ent.timer = time.AfterFunc(remaining, func() {
			d.fire(chatID)
		})
	}
	d.mu.Unlock()

	if executeNow {
		runGPTPromptTask(task)
	}
}

func (d *gptPromptDebouncer) fire(chatID int64) {
	d.mu.Lock()
	ent := d.pending[chatID]
	if ent == nil {
		d.mu.Unlock()
		return
	}
	ent.timer = nil
	if !ent.hasPending {
		d.mu.Unlock()
		return
	}
	task := ent.task
	ent.hasPending = false
	ent.lastSentAt = time.Now()
	d.mu.Unlock()
	runGPTPromptTask(task)
}

var runGPTPromptTask = executeGPTPromptTask

func executeGPTPromptTask(task gptPromptTask) {
	if task.Bot == nil || task.Msg == nil {
		return
	}
	out, err := generateChatGPTReply(task.Bot, task.Trigger.ResponseText, task.Msg, task.RecentContext, task.Trigger.CapturingText)
	if err != nil {
		log.Printf("gpt prompt failed: %v", err)
		reportChatFailure(task.Bot, task.Msg.Chat.ID, "ошибка запроса к ChatGPT", err)
		return
	}
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
	if hasHTML {
		htmlOut := markdownToTelegramHTMLLite(out)
		if debugGPTLogEnabled {
			log.Printf("gpt flow trigger=%d html_len=%d html_tgemoji=%d html=%q",
				task.Trigger.ID, len(htmlOut), countTGEmojiTags(htmlOut), clipText(htmlOut, 1400))
		}
		if ok := sendHTML(task.Bot, task.Msg.Chat.ID, replyTo, htmlOut, task.Trigger.Preview); ok {
			sent = true
			sendMode = "html"
		} else {
			fallbackText := replaceTGEmojiTagsWithFallback(out)
			if debugGPTLogEnabled {
				log.Printf("gpt flow trigger=%d html_send_failed fallback_len=%d fallback_tgemoji=%d fallback=%q",
					task.Trigger.ID, len(fallbackText), countTGEmojiTags(fallbackText), clipText(fallbackText, 1400))
			}
			if ok := sendMarkdownV2(task.Bot, task.Msg.Chat.ID, replyTo, fallbackText, task.Trigger.Preview); ok {
				sent = true
				sendMode = "markdown(fallback)"
			}
		}
	} else if ok := sendMarkdownV2(task.Bot, task.Msg.Chat.ID, replyTo, out, task.Trigger.Preview); ok {
		sent = true
	}
	if sent {
		if task.IdleTracker != nil {
			task.IdleTracker.MarkActivity(task.ChatID, time.Now())
		}
	}
	if debugTriggerLogEnabled {
		log.Printf("send gpt/%s attempted trigger=%d replyTo=%d", sendMode, task.Trigger.ID, replyTo)
	}
}

var tgEmojiLooseRe = regexp.MustCompile(`(?is)"?<tg-emoji\s+emoji-id\s*=\s*"?(?P<id>\d+)"?\s*>"?(?P<fallback>.*?)"?</tg-emoji>"?`)
var tgEmojiCanonicalRe = regexp.MustCompile(`(?is)<tg-emoji[^>]*>(.*?)</tg-emoji>`)
var telegramHTMLTagRe = regexp.MustCompile(`(?is)<\s*/?\s*(b|strong|i|em|u|ins|s|strike|del|code|pre|blockquote|a|tg-spoiler|tg-emoji)\b`)

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

func parseIdleMinutes(raw string) (int, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, false
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v <= 0 {
		return 0, false
	}
	return v, true
}

func selectIdleAutoReplyTrigger(
	bot *tgbotapi.BotAPI,
	msg *tgbotapi.Message,
	items []Trigger,
	isAdminFn func() bool,
) (*Trigger, time.Duration) {
	if msg == nil || len(items) == 0 {
		return nil, 0
	}
	adminChecked := false
	isAdmin := false

	for i := range items {
		it := items[i]
		if !it.Enabled || it.ActionType != "gpt_prompt" || normalizeMatchType(it.MatchType) != "idle" {
			continue
		}
		minutes, ok := parseIdleMinutes(it.MatchText)
		if !ok {
			continue
		}
		if !triggerModeMatches(bot, &it, msg) {
			continue
		}
		if it.AdminMode != "anybody" {
			if !adminChecked {
				isAdmin = isAdminFn()
				adminChecked = true
			}
			if it.AdminMode == "admins" && !isAdmin {
				continue
			}
			if it.AdminMode == "not_admins" && isAdmin {
				continue
			}
		}
		if it.Chance < 100 && rand.Intn(100) >= it.Chance {
			continue
		}
		cp := it
		return &cp, time.Duration(minutes) * time.Minute
	}
	return nil, 0
}

func newAdminStatusCache(ttl time.Duration) *adminStatusCache {
	if ttl <= 0 {
		ttl = 2 * time.Minute
	}
	return &adminStatusCache{
		ttl:    ttl,
		values: make(map[string]adminCacheEntry),
	}
}

func (c *adminStatusCache) IsChatAdmin(bot *tgbotapi.BotAPI, chatID, userID int64) bool {
	if c == nil {
		return fetchChatAdminStatus(bot, chatID, userID)
	}
	key := strconv.FormatInt(chatID, 10) + ":" + strconv.FormatInt(userID, 10)
	now := time.Now()

	c.mu.RLock()
	if cached, ok := c.values[key]; ok && now.Before(cached.expiresAt) {
		c.mu.RUnlock()
		return cached.isAdmin
	}
	c.mu.RUnlock()

	isAdmin := fetchChatAdminStatus(bot, chatID, userID)

	c.mu.Lock()
	c.values[key] = adminCacheEntry{
		isAdmin:   isAdmin,
		expiresAt: now.Add(c.ttl),
	}
	c.mu.Unlock()
	return isAdmin
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

func main() {
	rand.Seed(time.Now().UnixNano())

	token := strings.TrimSpace(os.Getenv("TELEGRAM_BOT_TOKEN"))
	if token == "" {
		log.Fatal("TELEGRAM_BOT_TOKEN is required")
	}

	dbPath := envOr("BOT_DB_FILE", "./trigger_bot.db")
	store, err := OpenStore(dbPath)
	if err != nil {
		log.Fatalf("open db failed: %v", err)
	}
	defer store.Close()

	bot, err := tgbotapi.NewBotAPI(token)
	if err != nil {
		log.Fatalf("create bot failed: %v", err)
	}
	var vkMusicClient *vkmusic.Client
	if vkToken := strings.TrimSpace(os.Getenv("VK_TOKEN")); vkToken != "" {
		vkMusicClient, err = vkmusic.NewClient(vkToken, strings.TrimSpace(os.Getenv("VK_USER_AGENT")))
		if err != nil {
			log.Printf("vk music client init failed: %v", err)
		} else {
			log.Printf("vk music client enabled")
		}
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
	adminCache := newAdminStatusCache(adminCacheTTL)
	chatRecent := newChatRecentStore(envInt("CHAT_RECENT_MAX_MESSAGES", 8), time.Duration(envInt("CHAT_RECENT_MAX_AGE_SEC", 1800))*time.Second)
	idleTracker := newChatIdleTracker()
	gptDebounceSec := envInt("GPT_PROMPT_DEBOUNCE_SEC", 0)
	gptDebouncer := newGPTPromptDebouncer(time.Duration(gptDebounceSec) * time.Second)
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
	updates := getUpdatesChanWithEmojiMeta(bot, u)
	engine := NewTriggerEngine()

	for update := range updates {
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

		if msg.IsCommand() {
			cmd := msg.Command()
			switch cmd {
			case "start", "help":
				s := "Триггер-бот активен.\n\n" +
					"Админка: /trigger_bot\n" +
					"Команды: /start /help /emojiid /vksearch\n\n" +
					"Теги для ChatGPT-промпта:\n" +
					"{{message}} / {{user_text}} — текст сообщения\n" +
					"{{user_id}}, {{user_first_name}}, {{user_username}}\n" +
					"{{user_display_name}}, {{user_label}}\n" +
					"{{sender_tag}}\n" +
					"{{chat_id}}, {{chat_title}}\n" +
					"{{reply_text}}\n" +
					"{{capturing_text}}\n" +
					"{{reply_user_id}}, {{reply_first_name}}, {{reply_username}}\n" +
					"{{reply_display_name}}, {{reply_label}}\n" +
					"{{reply_sender_tag}}\n\n" +
					"Кастомный emoji ID:\n" +
					"— команда /emojiid\n" +
					"— или просто отправьте кастомный emoji в личку боту."
				reply(bot, msg.Chat.ID, msg.MessageID, s, false)
				continue
			case "emojiid", "emoji_id":
				hits, entityCount := extractCustomEmojiFromRaw(rawMsg)
				if len(hits) == 0 && rawMsg != nil && rawMsg.ReplyToMessage != nil {
					hits, entityCount = extractCustomEmojiFromRaw(rawMsg.ReplyToMessage)
				}
				if len(hits) == 0 {
					if entityCount > 0 {
						reply(bot, msg.Chat.ID, msg.MessageID, "Нашла кастомный эмодзи, но не смогла извлечь его ID. Попробуйте отправить другой эмодзи ещё раз.", false)
						continue
					}
					reply(bot, msg.Chat.ID, msg.MessageID, "Кастомный emoji не найден. Отправьте сообщение с premium-эмодзи.", false)
					continue
				}
				lines := make([]string, 0, len(hits)+2)
				lines = append(lines, "Готовый код для вставки:")
				for _, hit := range hits {
					snippet := buildTGEmojiSnippet(hit.ID, hit.Fallback)
					lines = append(lines, "<code>"+html.EscapeString(snippet)+"</code>")
				}
				sendHTML(bot, msg.Chat.ID, msg.MessageID, strings.Join(lines, "\n"), false)
				continue
			case "vksearch", "vkfind":
				query := strings.TrimSpace(msg.CommandArguments())
				if query == "" {
					reply(bot, msg.Chat.ID, msg.MessageID, "Использование: /vksearch исполнитель или трек", false)
					continue
				}
				if vkMusicClient == nil {
					reply(bot, msg.Chat.ID, msg.MessageID, "VK-поиск не настроен (добавьте VK_TOKEN в .env).", false)
					continue
				}
				ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
				tracks, err := vkMusicClient.SearchTracks(ctx, query, 5)
				cancel()
				if err != nil {
					reply(bot, msg.Chat.ID, msg.MessageID, "Ошибка VK-поиска: "+clipText(err.Error(), 240), false)
					continue
				}
				if len(tracks) == 0 {
					reply(bot, msg.Chat.ID, msg.MessageID, "Ничего не найдено в VK.", false)
					continue
				}
				var b strings.Builder
				b.WriteString("VK поиск:\n")
				for i, tr := range tracks {
					fmt.Fprintf(&b, "%d. %s — %s (<code>%s</code>)\n", i+1, strings.TrimSpace(tr.Artist), strings.TrimSpace(tr.Title), strings.TrimSpace(tr.ID))
				}
				sendHTML(bot, msg.Chat.ID, msg.MessageID, strings.TrimSpace(b.String()), false)
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
				sendHTML(bot, msg.Chat.ID, msg.MessageID, strings.Join(lines, "\n"), false)
				continue
			}
			if entityCount > 0 {
				reply(bot, msg.Chat.ID, msg.MessageID, "Нашла кастомный эмодзи, но не смогла извлечь его ID. Попробуйте отправить другой эмодзи ещё раз.", false)
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

		recentBefore := chatRecent.RecentText(msg.Chat.ID, envInt("OLENYAM_CONTEXT_MESSAGES", 4))
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
		tr := engine.Select(bot, msg, text, items, func() bool {
			return adminCache.IsChatAdmin(bot, msg.Chat.ID, msg.From.ID)
		})
		if tr == nil {
			if debugTriggerLogEnabled {
				log.Printf("no trigger matched for msg=%d", msg.MessageID)
			}
			if idleTracker != nil {
				autoTr, idleAfter := selectIdleAutoReplyTrigger(bot, msg, items, func() bool {
					return adminCache.IsChatAdmin(bot, msg.Chat.ID, msg.From.ID)
				})
				if autoTr != nil && idleTracker.ShouldAutoReply(msg.Chat.ID, idleAfter, now) {
					ctx := ""
					if isOlenyamTrigger(autoTr) {
						ctx = recentBefore
					}
					task := gptPromptTask{
						Bot:           bot,
						Trigger:       *autoTr,
						Msg:           msg,
						RecentContext: ctx,
						IdleTracker:   idleTracker,
						ChatID:        msg.Chat.ID,
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
				gptDebouncer.Schedule(msg.Chat.ID, gptPromptTask{
					Bot:           bot,
					Trigger:       *tr,
					Msg:           msg,
					RecentContext: ctx,
					IdleTracker:   idleTracker,
					ChatID:        msg.Chat.ID,
				})
				if debugTriggerLogEnabled {
					log.Printf("gpt prompt queued (debounce) trigger=%d chat=%d msg=%d", tr.ID, msg.Chat.ID, msg.MessageID)
				}
				continue
			}
			executeGPTPromptTask(gptPromptTask{
				Bot:           bot,
				Trigger:       *tr,
				Msg:           msg,
				RecentContext: ctx,
				IdleTracker:   idleTracker,
				ChatID:        msg.Chat.ID,
			})
		case "gpt_image":
			img, err := generateChatGPTImage(bot, tr.ResponseText, msg, tr.CapturingText)
			if err != nil {
				log.Printf("gpt image failed: %v", err)
				reportChatFailure(bot, msg.Chat.ID, "ошибка генерации картинки в ChatGPT", err)
				continue
			}
			replyTo := 0
			if tr.Reply || tr.TriggerMode == "command_reply" {
				replyTo = msg.MessageID
			}
			if ok := sendPhoto(bot, msg.Chat.ID, replyTo, img, "CW: сгенерено нейросетью", true); ok {
				idleTracker.MarkActivity(msg.Chat.ID, time.Now())
			}
			if debugTriggerLogEnabled {
				log.Printf("send gpt/image attempted trigger=%d replyTo=%d", tr.ID, replyTo)
			}
		case "search_image":
			query := buildImageSearchQueryFromMessage(bot, tr.ResponseText, msg, tr.CapturingText)
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
			if ok := sendPhoto(bot, msg.Chat.ID, replyTo, img, "", false); ok {
				idleTracker.MarkActivity(msg.Chat.ID, time.Now())
			}
			if debugTriggerLogEnabled {
				log.Printf("send search/image attempted trigger=%d replyTo=%d query=%q", tr.ID, replyTo, clipText(query, 220))
			}
		case "vk_music_audio":
			if vkMusicClient == nil {
				reportChatFailure(bot, msg.Chat.ID, "ошибка VK-музыки", errors.New("VK_TOKEN не настроен"))
				continue
			}
			query := buildVKMusicQueryFromMessage(bot, tr.ResponseText, msg, tr.CapturingText)
			if query == "" {
				query = strings.TrimSpace(msg.Text)
			}
			if query == "" {
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			tracks, err := vkMusicClient.SearchTracks(ctx, query, 5)
			cancel()
			if err != nil {
				log.Printf("vk music search failed: %v", err)
				reportChatFailure(bot, msg.Chat.ID, "ошибка поиска музыки VK", err)
				continue
			}
			if len(tracks) == 0 {
				if debugTriggerLogEnabled {
					log.Printf("vk music search empty trigger=%d query=%q", tr.ID, clipText(query, 220))
				}
				continue
			}
			replyTo := 0
			if tr.Reply || tr.TriggerMode == "command_reply" {
				replyTo = msg.MessageID
			}
			var sendErr error
			sent := false
			maxCandidates := 3
			for i := 0; i < len(tracks) && i < maxCandidates; i++ {
				ctx2, cancel2 := context.WithTimeout(context.Background(), 15*time.Second)
				song, err := vkMusicClient.GetAudioURL(ctx2, tracks[i].ID)
				cancel2()
				if err != nil {
					sendErr = err
					if debugTriggerLogEnabled {
						log.Printf("vk music get audio failed trigger=%d track_id=%q err=%v", tr.ID, tracks[i].ID, err)
					}
					continue
				}
				if song == nil || strings.TrimSpace(song.URL) == "" {
					sendErr = errors.New("empty audio URL")
					if debugTriggerLogEnabled {
						log.Printf("vk music no direct url trigger=%d track_id=%q", tr.ID, tracks[i].ID)
					}
					continue
				}
				if err := sendAudioFromURL(bot, msg.Chat.ID, replyTo, song.URL, song.Artist, song.Title); err != nil {
					sendErr = err
					if debugTriggerLogEnabled {
						log.Printf("vk music send failed trigger=%d track_id=%q err=%v", tr.ID, tracks[i].ID, err)
					}
					continue
				}
				sent = true
				break
			}
			if !sent {
				if sendErr != nil {
					reportChatFailure(bot, msg.Chat.ID, "ошибка отправки аудио VK", sendErr)
				}
				continue
			}
			idleTracker.MarkActivity(msg.Chat.ID, time.Now())
			if debugTriggerLogEnabled {
				log.Printf("send vk/audio attempted trigger=%d replyTo=%d query=%q", tr.ID, replyTo, clipText(query, 160))
			}
		default:
			replyTo := 0
			if tr.Reply || tr.TriggerMode == "command_reply" {
				replyTo = msg.MessageID
			}
			out := applyCapturingTemplate(tr.ResponseText, tr.CapturingText)
			if ok := sendHTML(bot, msg.Chat.ID, replyTo, out, tr.Preview); ok {
				idleTracker.MarkActivity(msg.Chat.ID, time.Now())
			}
			if debugTriggerLogEnabled {
				log.Printf("send static/html attempted trigger=%d replyTo=%d", tr.ID, replyTo)
			}
		}
	}
}

func triggerModeMatches(bot *tgbotapi.BotAPI, tr *Trigger, msg *tgbotapi.Message) bool {
	if tr == nil || msg == nil {
		return false
	}
	mode := tr.TriggerMode
	switch mode {
	case "only_replies":
		return msg.ReplyToMessage != nil
	case "only_replies_to_any_bot":
		return msg.ReplyToMessage != nil && msg.ReplyToMessage.From != nil && msg.ReplyToMessage.From.IsBot
	case "only_replies_to_combot":
		// Legacy storage key kept for compatibility, actual behavior:
		// trigger only on replies to this bot's own messages.
		if msg.ReplyToMessage == nil || msg.ReplyToMessage.From == nil {
			return false
		}
		if bot == nil {
			return false
		}
		return msg.ReplyToMessage.From.IsBot && msg.ReplyToMessage.From.ID == bot.Self.ID
	case "never_on_replies":
		return msg.ReplyToMessage == nil
	case "command_reply":
		return msg.IsCommand()
	default:
		return true
	}
}

func reply(bot *tgbotapi.BotAPI, chatID int64, replyTo int, text string, preview bool) {
	m := tgbotapi.NewMessage(chatID, text)
	m.DisableWebPagePreview = !preview
	if replyTo > 0 {
		m.ReplyToMessageID = replyTo
		m.AllowSendingWithoutReply = true
	}
	sent, err := bot.Send(m)
	if err != nil {
		log.Printf("send failed: %v", err)
		reportChatFailure(bot, chatID, "ошибка отправки сообщения", err)
		return
	}
	if debugTriggerLogEnabled {
		log.Printf("send ok chat=%d msg=%d replyTo=%d text=%q", chatID, sent.MessageID, replyTo, clipText(text, 120))
	}
}

func sendHTML(bot *tgbotapi.BotAPI, chatID int64, replyTo int, html string, preview bool) bool {
	html = normalizeTelegramLineBreaks(html)
	m := tgbotapi.NewMessage(chatID, html)
	m.ParseMode = "HTML"
	m.DisableWebPagePreview = !preview
	if replyTo > 0 {
		m.ReplyToMessageID = replyTo
		m.AllowSendingWithoutReply = true
	}
	if len(m.Text) > 4096 {
		m.Text = m.Text[:4096]
	}
	sent, err := bot.Send(m)
	if err != nil {
		log.Printf("send html failed: %v", err)
		reportChatFailure(bot, chatID, "ошибка отправки HTML-сообщения", err)
		return false
	}
	if debugTriggerLogEnabled {
		log.Printf("send html ok chat=%d msg=%d replyTo=%d text=%q", chatID, sent.MessageID, replyTo, clipText(m.Text, 120))
	}
	return true
}

func sendMarkdownV2(bot *tgbotapi.BotAPI, chatID int64, replyTo int, text string, preview bool) bool {
	text = normalizeTelegramLineBreaks(text)
	text = escapeMarkdownV2PreservingFences(text)
	m := tgbotapi.NewMessage(chatID, text)
	m.ParseMode = "MarkdownV2"
	m.DisableWebPagePreview = !preview
	if replyTo > 0 {
		m.ReplyToMessageID = replyTo
		m.AllowSendingWithoutReply = true
	}
	if len(m.Text) > 4096 {
		m.Text = m.Text[:4096]
	}
	sent, err := bot.Send(m)
	if err != nil {
		log.Printf("send markdown failed: %v", err)
		reportChatFailure(bot, chatID, "ошибка отправки Markdown-сообщения", err)
		return false
	}
	if debugTriggerLogEnabled {
		log.Printf("send markdown ok chat=%d msg=%d replyTo=%d text=%q", chatID, sent.MessageID, replyTo, clipText(m.Text, 120))
	}
	return true
}

type generatedImage struct {
	URL   string
	Bytes []byte
}

func sendPhoto(bot *tgbotapi.BotAPI, chatID int64, replyTo int, img generatedImage, caption string, spoiler bool) bool {
	if spoiler {
		if err := sendPhotoWithSpoilerAPI(bot, chatID, replyTo, img, caption); err != nil {
			log.Printf("send photo (spoiler) failed: %v", err)
			reportChatFailure(bot, chatID, "ошибка отправки картинки", err)
			return false
		}
		if debugTriggerLogEnabled {
			log.Printf("send photo (spoiler) ok chat=%d replyTo=%d", chatID, replyTo)
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
		reportChatFailure(bot, chatID, "ошибка отправки картинки", errors.New("empty image payload"))
		return false
	}

	m := tgbotapi.NewPhoto(chatID, file)
	if replyTo > 0 {
		m.ReplyToMessageID = replyTo
		m.AllowSendingWithoutReply = true
	}
	m.Caption = strings.TrimSpace(caption)
	sent, err := bot.Send(m)
	if err != nil {
		log.Printf("send photo failed: %v", err)
		reportChatFailure(bot, chatID, "ошибка отправки картинки", err)
		return false
	}
	if debugTriggerLogEnabled {
		if strings.TrimSpace(img.URL) != "" {
			log.Printf("send photo ok chat=%d msg=%d replyTo=%d source=url", chatID, sent.MessageID, replyTo)
		} else {
			log.Printf("send photo ok chat=%d msg=%d replyTo=%d source=bytes size=%d", chatID, sent.MessageID, replyTo, len(img.Bytes))
		}
	}
	return true
}

func sendAudioFromURL(bot *tgbotapi.BotAPI, chatID int64, replyTo int, audioURL, performer, title string) error {
	audioURL = strings.TrimSpace(audioURL)
	if bot == nil || chatID == 0 || audioURL == "" {
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

	m := tgbotapi.NewAudio(chatID, tgbotapi.FilePath(tmpPath))
	if replyTo > 0 {
		m.ReplyToMessageID = replyTo
		m.AllowSendingWithoutReply = true
	}
	m.Performer = strings.TrimSpace(performer)
	m.Title = strings.TrimSpace(title)
	sent, err := bot.Send(m)
	if err != nil {
		return err
	}
	if debugTriggerLogEnabled {
		log.Printf("send audio ok chat=%d msg=%d replyTo=%d performer=%q title=%q", chatID, sent.MessageID, replyTo, m.Performer, m.Title)
	}
	return nil
}

func downloadAudioToTempFile(audioURL string) (string, error) {
	tmp, err := os.CreateTemp("", "vk-audio-*.mp3")
	if err != nil {
		return "", err
	}
	tmpPath := tmp.Name()
	_ = tmp.Close()
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
	var runErr error
	for attempt := 1; attempt <= retries; attempt++ {
		_ = os.Remove(tmpPath)
		ctx, cancel := context.WithTimeout(context.Background(), time.Duration(ffmpegTimeout)*time.Second)
		err := runFFmpegAudioDownload(ctx, audioURL, tmpPath, ua)
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
		return "", runErr
	}
	st, err := os.Stat(tmpPath)
	if err != nil {
		_ = os.Remove(tmpPath)
		return "", err
	}
	size := st.Size()
	limit := int64(maxMB) << 20
	if size <= 0 {
		_ = os.Remove(tmpPath)
		return "", errors.New("ffmpeg produced empty audio file")
	}
	if size > limit {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("audio too large: %d bytes (limit %d MB)", size, maxMB)
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

func sendPhotoWithSpoilerAPI(bot *tgbotapi.BotAPI, chatID int64, replyTo int, img generatedImage, caption string) error {
	if bot == nil || strings.TrimSpace(bot.Token) == "" {
		return errors.New("bot token is empty")
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	_ = writer.WriteField("chat_id", strconv.FormatInt(chatID, 10))
	if replyTo > 0 {
		_ = writer.WriteField("reply_to_message_id", strconv.Itoa(replyTo))
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

	endpoint := fmt.Sprintf("https://api.telegram.org/bot%s/sendPhoto", strings.TrimSpace(bot.Token))
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

func buildPromptFromMessage(bot *tgbotapi.BotAPI, promptTemplate string, msg *tgbotapi.Message) string {
	prompt := strings.TrimSpace(promptTemplate)
	if prompt == "" {
		prompt = "Ответь коротко и по делу."
	}

	if msg == nil {
		return prompt
	}

	replacements := buildMessageTemplateReplacements(bot, msg)
	for tag, value := range replacements {
		prompt = strings.ReplaceAll(prompt, tag, value)
	}

	if strings.Contains(promptTemplate, "{{") {
		return prompt
	}

	return prompt + "\n\nСообщение пользователя:\n" + replacements["{{message}}"]
}

func applyCapturingTemplate(s, capture string) string {
	if strings.TrimSpace(s) == "" {
		return s
	}
	return strings.ReplaceAll(s, "{{capturing_text}}", strings.TrimSpace(capture))
}

func isOlenyamTrigger(tr *Trigger) bool {
	if tr == nil {
		return false
	}
	title := strings.ToLower(strings.TrimSpace(tr.Title))
	return strings.Contains(title, "оле-ням") || strings.Contains(title, "оленям") || strings.Contains(title, "оле ням")
}

func buildImageSearchQueryFromMessage(bot *tgbotapi.BotAPI, queryTemplate string, msg *tgbotapi.Message, capturingText string) string {
	query := strings.TrimSpace(applyCapturingTemplate(queryTemplate, capturingText))
	if msg == nil {
		return query
	}
	replacements := buildMessageTemplateReplacements(bot, msg)
	if query == "" {
		return strings.TrimSpace(replacements["{{message}}"])
	}
	for tag, value := range replacements {
		query = strings.ReplaceAll(query, tag, value)
	}
	return strings.TrimSpace(query)
}

func buildVKMusicQueryFromMessage(bot *tgbotapi.BotAPI, queryTemplate string, msg *tgbotapi.Message, capturingText string) string {
	query := strings.TrimSpace(applyCapturingTemplate(queryTemplate, capturingText))
	if msg == nil {
		return query
	}
	replacements := buildMessageTemplateReplacements(bot, msg)
	if query == "" {
		return strings.TrimSpace(replacements["{{message}}"])
	}
	for tag, value := range replacements {
		query = strings.ReplaceAll(query, tag, value)
	}
	return strings.TrimSpace(query)
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

	userText := strings.TrimSpace(msg.Text)
	if userText == "" {
		userText = strings.TrimSpace(msg.Caption)
	}
	userDisplayName := buildDisplayName(msg.From)
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
		"{{user_username}}":      strings.TrimSpace(msg.From.UserName),
		"{{user_display_name}}":  userDisplayName,
		"{{user_label}}":         userLabel,
		"{{sender_tag}}":         senderTagDisplay,
		"{{chat_id}}":            strconv.FormatInt(msg.Chat.ID, 10),
		"{{chat_title}}":         chatTitle,
		"{{reply_text}}":         replyText,
		"{{reply_user_id}}":      replyUserID,
		"{{reply_first_name}}":   replyFirstName,
		"{{reply_username}}":     replyUsername,
		"{{reply_display_name}}": replyDisplayName,
		"{{reply_label}}":        replyLabel,
		"{{reply_sender_tag}}":   replySenderTagDisplay,
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
			Update:     upd,
			RawMessage: rawUpd.Message,
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

func generateChatGPTReply(bot *tgbotapi.BotAPI, promptTemplate string, msg *tgbotapi.Message, recentContext string, capturingText string) (string, error) {
	apiKey := strings.TrimSpace(os.Getenv("OPENAI_API_KEY"))
	if apiKey == "" {
		return "", errors.New("OPENAI_API_KEY is empty")
	}
	model := strings.TrimSpace(os.Getenv("OPENAI_MODEL"))
	if model == "" {
		model = "gpt-4.1-mini"
	}

	prompt := buildPromptFromMessage(bot, applyCapturingTemplate(promptTemplate, capturingText), msg)
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

func generateChatGPTImage(bot *tgbotapi.BotAPI, promptTemplate string, msg *tgbotapi.Message, capturingText string) (generatedImage, error) {
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

	prompt := buildPromptFromMessage(bot, applyCapturingTemplate(promptTemplate, capturingText), msg)
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
