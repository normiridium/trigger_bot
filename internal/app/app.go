package app

import (
	"context"
	"errors"
	"fmt"
	"html"
	"log"
	"math/rand"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"trigger-admin-bot/internal/engine"
	"trigger-admin-bot/internal/gpt"
	"trigger-admin-bot/internal/mediadl"
	"trigger-admin-bot/internal/musicpick"
	"trigger-admin-bot/internal/pick"
	"trigger-admin-bot/internal/spotifymusic"
	"trigger-admin-bot/internal/trigger"
	"trigger-admin-bot/internal/yandexmusic"
)

var chatErrorLogEnabled bool
var debugTriggerLogEnabled bool
var debugGPTLogEnabled bool
var errTelegramUploadTooLarge = errors.New("telegram upload too large")

type chatAllowList struct {
	enabled bool
	ids     map[int64]struct{}
}

type disallowedChatNotifier struct {
	mu   sync.Mutex
	last map[int64]time.Time
	ttl  time.Duration
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

func newDisallowedChatNotifier(ttl time.Duration) *disallowedChatNotifier {
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}
	return &disallowedChatNotifier{
		last: make(map[int64]time.Time),
		ttl:  ttl,
	}
}

func (n *disallowedChatNotifier) shouldNotify(chatID int64, now time.Time) bool {
	if n == nil || chatID == 0 {
		return false
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if prev, ok := n.last[chatID]; ok && now.Sub(prev) < n.ttl {
		return false
	}
	return true
}

func (n *disallowedChatNotifier) markNotified(chatID int64, now time.Time) {
	if n == nil || chatID == 0 {
		return
	}
	n.mu.Lock()
	n.last[chatID] = now
	n.mu.Unlock()
}

func notifyDisallowedChat(bot *tgbotapi.BotAPI, chatID int64) error {
	if bot == nil || chatID == 0 {
		return errors.New("invalid notifyDisallowedChat args")
	}
	text := fmt.Sprintf(
		"⚠️ Этот чат не входит в список разрешённых.\nchat_id: <code>%d</code>\nДобавьте его в админке в поле «Разрешённые чаты (ALLOWED_CHAT_IDS)».",
		chatID,
	)
	m := tgbotapi.NewMessage(chatID, text)
	m.ParseMode = "HTML"
	m.DisableWebPagePreview = true
	if _, err := bot.Send(m); err != nil {
		log.Printf("send disallowed-chat notice failed chat=%d err=%v", chatID, err)
		return err
	}
	log.Printf("disallowed-chat notice sent chat=%d", chatID)
	return nil
}

func isActiveChatMemberStatus(status string) bool {
	switch strings.TrimSpace(status) {
	case "member", "administrator", "creator":
		return true
	default:
		return false
	}
}

func handleDisallowedMyChatMemberNotice(bot *tgbotapi.BotAPI, allowed chatAllowList, notifier *disallowedChatNotifier, upd *rawChatMemberUpdated) {
	if bot == nil || upd == nil || upd.Chat == nil || upd.NewChatMember == nil || upd.NewChatMember.User == nil {
		return
	}
	// Only for updates about this bot account.
	if !upd.NewChatMember.User.IsBot {
		return
	}
	newStatus := strings.TrimSpace(upd.NewChatMember.Status)
	oldStatus := ""
	if upd.OldChatMember != nil {
		oldStatus = strings.TrimSpace(upd.OldChatMember.Status)
	}
	// Notify only when bot becomes active in chat (added/unbanned), not on every status change.
	if !isActiveChatMemberStatus(newStatus) || isActiveChatMemberStatus(oldStatus) {
		return
	}
	chatID := upd.Chat.ID
	if chatID == 0 || allowed.Allows(chatID) {
		return
	}
	now := time.Now()
	if !notifier.shouldNotify(chatID, now) {
		return
	}
	if err := notifyDisallowedChat(bot, chatID); err == nil {
		notifier.markNotified(chatID, now)
	}
}

type adminCacheEntry struct {
	isAdmin   bool
	expiresAt time.Time
}

type adminStatusCache struct {
	mu     sync.RWMutex
	ttl    time.Duration
	store  ChatAdminStorePort
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

var outgoingChatRecentState = struct {
	mu      sync.RWMutex
	store   *chatRecentStore
	botName string
}{}

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

func genderedModerationVerb(tag, male, female, unknown string) string {
	return resolveGenderVariant(tag, genderVariants{
		Male:    male,
		Female:  female,
		Neuter:  unknown,
		Plural:  unknown,
		Unknown: unknown,
	})
}

func moderationActionVerb(action, senderTag string) string {
	switch strings.TrimSpace(action) {
	case "ban":
		return genderedModerationVerb(senderTag, "забанил", "забанила", "забанил(а)")
	case "unban":
		return genderedModerationVerb(senderTag, "разбанил", "разбанила", "разбанил(а)")
	case "mute":
		return genderedModerationVerb(senderTag, "замьютил", "замьютила", "замьютил(а)")
	case "unmute":
		return genderedModerationVerb(senderTag, "размьютил", "размьютила", "размьютил(а)")
	case "kick":
		return genderedModerationVerb(senderTag, "кикнул", "кикнула", "кикнул(а)")
	default:
		return genderedModerationVerb(senderTag, "изменил", "изменила", "изменил(а)")
	}
}

func moderationReadonlyStateVerb(turnOn bool, senderTag string) string {
	if turnOn {
		return genderedModerationVerb(senderTag, "включил режим только чтения", "включила режим только чтения", "включил(а) режим только чтения")
	}
	return genderedModerationVerb(senderTag, "выключил режим только чтения", "выключила режим только чтения", "выключил(а) режим только чтения")
}

func newChatRecentStore(maxPer int, maxAge time.Duration) *chatRecentStore {
	if maxPer <= 0 {
		maxPer = 12
	}
	if maxPer < 12 {
		maxPer = 12
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
		limit = 12
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
		user := strings.TrimSpace(it.UserName)
		if user == "" {
			user = "участник"
		}
		lines = append(lines, fmt.Sprintf("[%s] %s: %s", it.At.Local().Format("02.01.2006 15:04"), user, txt))
	}
	return strings.Join(lines, "\n")
}

func setOutgoingChatRecentStore(store *chatRecentStore, botName string) {
	outgoingChatRecentState.mu.Lock()
	outgoingChatRecentState.store = store
	outgoingChatRecentState.botName = strings.TrimSpace(botName)
	outgoingChatRecentState.mu.Unlock()
}

func addOutgoingChatRecentMessage(chatID int64, text string) {
	text = strings.TrimSpace(text)
	if chatID == 0 || text == "" {
		return
	}
	outgoingChatRecentState.mu.RLock()
	store := outgoingChatRecentState.store
	botName := strings.TrimSpace(outgoingChatRecentState.botName)
	outgoingChatRecentState.mu.RUnlock()
	if store == nil {
		return
	}
	if botName == "" {
		botName = "Оле-ням"
	}
	store.Add(chatID, recentChatMessage{
		UserName: botName,
		Text:     text,
		At:       time.Now(),
	})
}

func sendTypingAction(bot *tgbotapi.BotAPI, chatID int64) {
	if bot == nil || chatID == 0 {
		return
	}
	_, _ = bot.Request(tgbotapi.NewChatAction(chatID, tgbotapi.ChatTyping))
}

func ensureMinTypingWindow(bot *tgbotapi.BotAPI, chatID int64, startedAt time.Time, min time.Duration) {
	if min <= 0 {
		return
	}
	if startedAt.IsZero() {
		startedAt = time.Now()
	}
	wait := min - time.Since(startedAt)
	if wait <= 0 {
		return
	}
	sendTypingAction(bot, chatID)
	time.Sleep(wait)
}

func executeGPTPromptTask(task gpt.PromptTask) {
	if task.Bot == nil || task.Msg == nil {
		return
	}
	sendTypingAction(task.Bot, task.Msg.Chat.ID)
	tmplCtx := newTemplateContext(task.Bot, task.Msg, &task.Trigger, task.TemplateLookup)
	out, err := generateChatGPTReply(tmplCtx, pickResponseVariantText(task.Trigger.ResponseText), task.RecentContext)
	if err != nil {
		log.Printf("gpt prompt failed: %v", err)
		reportChatFailure(task.Bot, task.Msg.Chat.ID, "ошибка запроса к ChatGPT", err)
		return
	}
	out = expandTemplateCalls(out, task.TemplateLookup)
	startedAt := task.TriggeredAt
	if startedAt.IsZero() {
		startedAt = time.Now()
	}
	ensureMinTypingWindow(task.Bot, task.Msg.Chat.ID, startedAt, 1*time.Second)
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

func triggerDisplayName(tr *Trigger) string {
	if tr == nil {
		return "без названия"
	}
	title := strings.TrimSpace(tr.Title)
	if title != "" {
		return title
	}
	title = strings.TrimSpace(tr.MatchText)
	if title != "" {
		return clipText(title, 80)
	}
	return "без названия"
}

func triggerResponseDebugText(bot *tgbotapi.BotAPI, msg *tgbotapi.Message, tr *Trigger, templateLookup func(string) string) string {
	if tr == nil {
		return ""
	}
	raw := strings.TrimSpace(pickResponseVariantText(tr.ResponseText))
	if raw == "" {
		return ""
	}
	resolved := strings.TrimSpace(expandTemplateCalls(raw, templateLookup))
	if msg == nil {
		return clipText(resolved, 220)
	}
	tmplCtx := newTemplateContext(bot, msg, tr, templateLookup)
	out := strings.TrimSpace(buildResponseFromMessage(tmplCtx, resolved))
	if out == "" {
		out = resolved
	}
	return clipText(out, 220)
}

func reportEmptyTriggerMessage(bot *tgbotapi.BotAPI, chatID int64, tr *Trigger) {
	if bot == nil || chatID == 0 || tr == nil {
		return
	}
	title := html.EscapeString(triggerDisplayName(tr))
	text := fmt.Sprintf("⚠️ Триггер #%d «%s»: задано пустое сообщение.", tr.ID, title)
	m := tgbotapi.NewMessage(chatID, text)
	m.ParseMode = "HTML"
	_, err := bot.Send(m)
	if err != nil && debugTriggerLogEnabled {
		log.Printf("send trigger empty-message warning failed trigger=%d chat=%d err=%v", tr.ID, chatID, err)
	}
}

var tgEmojiLooseRe = regexp.MustCompile(`(?is)"?<tg-emoji\s+emoji-id\s*=\s*"?(?P<id>\d+)"?\s*>"?(?P<fallback>.*?)"?</tg-emoji>"?`)
var tgEmojiCanonicalRe = regexp.MustCompile(`(?is)<tg-emoji[^>]*>(.*?)</tg-emoji>`)
var telegramHTMLTagRe = regexp.MustCompile(`(?is)<\s*/?\s*(b|strong|i|em|u|ins|s|strike|del|code|pre|blockquote|a|tg-spoiler|tg-emoji)\b`)
var templateCallPattern = regexp.MustCompile(`\{\{\s*template\s+\"([^\"]+)\"\s*\}\}`)
var supportedMediaURLRe = regexp.MustCompile(`https?://[^\s<>"']+`)
var htmlTagStripRe = regexp.MustCompile(`(?is)<[^>]+>`)
var stickerFileIDTokenRe = regexp.MustCompile(`^[A-Za-z0-9_-]{16,}$`)

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

func extractStickerFileIDFromTemplate(raw string) string {
	plain := strings.TrimSpace(htmlTagStripRe.ReplaceAllString(raw, " "))
	if plain == "" {
		return ""
	}
	fields := strings.Fields(plain)
	for _, f := range fields {
		token := strings.TrimSpace(strings.Trim(f, " \t\r\n`\"'.,;:!?()[]{}<>"))
		if token == "" {
			continue
		}
		if i := strings.Index(token, ":"); i > 0 {
			token = strings.TrimSpace(token[:i])
		}
		if !stickerFileIDTokenRe.MatchString(token) {
			continue
		}
		if !looksLikeTelegramFileID(token) {
			continue
		}
		return token
	}
	return ""
}

func looksLikeTelegramFileID(s string) bool {
	if s == "" {
		return false
	}
	hasLetter := false
	hasDigit := false
	hasSpecial := false
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
			hasLetter = true
		case r >= '0' && r <= '9':
			hasDigit = true
		case r == '_' || r == '-':
			hasSpecial = true
		}
	}
	return hasLetter && hasDigit && (hasSpecial || len(s) >= 24)
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

func buildTemplateLookup(store TriggerStorePort) func(string) string {
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

func newAdminStatusCache(ttl time.Duration, store ChatAdminStorePort) *adminStatusCache {
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

func normalizeModerationCommandToken(raw string) string {
	cmd := strings.ToLower(strings.TrimSpace(raw))
	if cmd == "" {
		return ""
	}
	if at := strings.IndexByte(cmd, '@'); at >= 0 {
		cmd = strings.TrimSpace(cmd[:at])
	}
	return cmd
}

func ruPlural(n int, one, few, many string) string {
	n = absInt(n)
	lastTwo := n % 100
	if lastTwo >= 11 && lastTwo <= 14 {
		return many
	}
	switch n % 10 {
	case 1:
		return one
	case 2, 3, 4:
		return few
	default:
		return many
	}
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func humanModerationDurationRU(d time.Duration, raw string) string {
	if d <= 0 {
		return strings.TrimSpace(raw)
	}
	raw = strings.ToLower(strings.TrimSpace(raw))
	if raw != "" {
		if parsed, ok := parseModerationDurationToken(raw); ok && parsed == d {
			switch raw[len(raw)-1] {
			case 'm':
				n, _ := strconv.Atoi(raw[:len(raw)-1])
				return fmt.Sprintf("%d %s", n, ruPlural(n, "минута", "минуты", "минут"))
			case 'h':
				n, _ := strconv.Atoi(raw[:len(raw)-1])
				return fmt.Sprintf("%d %s", n, ruPlural(n, "час", "часа", "часов"))
			case 'd':
				n, _ := strconv.Atoi(raw[:len(raw)-1])
				return fmt.Sprintf("%d %s", n, ruPlural(n, "день", "дня", "дней"))
			case 'w':
				n, _ := strconv.Atoi(raw[:len(raw)-1])
				return fmt.Sprintf("%d %s", n, ruPlural(n, "неделя", "недели", "недель"))
			}
		}
	}
	if d%(7*24*time.Hour) == 0 {
		n := int(d / (7 * 24 * time.Hour))
		return fmt.Sprintf("%d %s", n, ruPlural(n, "неделя", "недели", "недель"))
	}
	if d%(24*time.Hour) == 0 {
		n := int(d / (24 * time.Hour))
		return fmt.Sprintf("%d %s", n, ruPlural(n, "день", "дня", "дней"))
	}
	if d%time.Hour == 0 {
		n := int(d / time.Hour)
		return fmt.Sprintf("%d %s", n, ruPlural(n, "час", "часа", "часов"))
	}
	if d%time.Minute == 0 {
		n := int(d / time.Minute)
		return fmt.Sprintf("%d %s", n, ruPlural(n, "минута", "минуты", "минут"))
	}
	n := int(d.Round(time.Second) / time.Second)
	return fmt.Sprintf("%d %s", n, ruPlural(n, "секунда", "секунды", "секунд"))
}

func parseModerationCommand(text string) (moderationRequest, bool, error) {
	raw := strings.TrimSpace(text)
	if raw == "" || (!strings.HasPrefix(raw, "!") && !strings.HasPrefix(raw, "/")) {
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
	cmd := normalizeModerationCommandToken(parts[0])
	args := parts[1:]
	out := moderationRequest{Reason: reason}

	switch cmd {
	case "!ban", "/ban":
		out.Action = "ban"
	case "!sban", "/sban":
		out.Action = "ban"
		out.Silent = true
	case "!unban", "/unban":
		out.Action = "unban"
	case "!sunban", "/sunban":
		out.Action = "unban"
		out.Silent = true
	case "!mute", "/mute":
		out.Action = "mute"
	case "!smute", "/smute":
		out.Action = "mute"
		out.Silent = true
	case "!unmute", "/unmute":
		out.Action = "unmute"
	case "!kick", "/kick":
		out.Action = "kick"
	case "!skick", "/skick":
		out.Action = "kick"
		out.Silent = true
	case "!readonly", "!ro", "!channelmode", "/readonly", "/ro", "/channelmode":
		out.Action = "readonly"
	case "!reload_admins", "/reload_admins":
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

	senderTag := ""
	if ctx.Bot != nil && msg.Chat != nil && msg.From != nil {
		senderTag = getChatMemberTagRaw(ctx.Bot.Token, msg.Chat.ID, msg.From.ID)
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
			state := moderationReadonlyStateVerb(turnOn, senderTag)
			var b strings.Builder
			b.WriteString(modLink)
			b.WriteByte(' ')
			b.WriteString(state)
			if req.DurationRaw != "" && turnOn {
				b.WriteString(" на ")
				b.WriteString(html.EscapeString(humanModerationDurationRU(req.Duration, req.DurationRaw)))
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
	verb := moderationActionVerb(req.Action, senderTag)
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
		b.WriteString(html.EscapeString(humanModerationDurationRU(req.Duration, req.DurationRaw)))
	}
	if req.Reason != "" {
		b.WriteString(" — ")
		b.WriteString(html.EscapeString(req.Reason))
	}
	sendHTML(sendCtx, strings.TrimSpace(b.String()), false)
	return true
}

func Run() {
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
	var spotifyMusicClient SpotifyMusicPort
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
		AudioFormat:  strings.TrimSpace(os.Getenv("AUDIO_FORMAT")),
		AudioQuality: strings.TrimSpace(os.Getenv("AUDIO_QUALITY")),
	}
	yandexDownloader := yandexmusic.Downloader{
		Token:         strings.TrimSpace(firstNonEmptyEnv("YA_MUSIC_TOKEN", "YANDEX_MUSIC_TOKEN")),
		Quality:       envInt("YANDEX_MUSIC_QUALITY", 1),
		TimeoutSec:    envInt("YANDEX_MUSIC_TIMEOUT_SEC", 45),
		Tries:         envInt("YANDEX_MUSIC_TRIES", 6),
		RetryDelaySec: envInt("YANDEX_MUSIC_RETRY_DELAY_SEC", 2),
	}
	telegramUploadMaxMB := envInt("TELEGRAM_UPLOAD_MAX_MB", 50)
	if telegramUploadMaxMB <= 0 {
		telegramUploadMaxMB = 50
	}
	mediaMaxMB := envInt("MEDIA_DOWNLOAD_MAX_MB", telegramUploadMaxMB)
	if mediaMaxMB <= 0 {
		mediaMaxMB = telegramUploadMaxMB
	}
	if mediaMaxMB > telegramUploadMaxMB {
		log.Printf("MEDIA_DOWNLOAD_MAX_MB=%d capped to TELEGRAM_UPLOAD_MAX_MB=%d", mediaMaxMB, telegramUploadMaxMB)
		mediaMaxMB = telegramUploadMaxMB
	}
	mediaDownloader := mediadl.Downloader{
		YTDLPBin:      strings.TrimSpace(os.Getenv("YTDLP_BIN")),
		ProxySocks:    strings.TrimSpace(os.Getenv("FIXIE_SOCKS_HOST")),
		AudioFormat:   strings.TrimSpace(os.Getenv("AUDIO_FORMAT")),
		AudioQuality:  strings.TrimSpace(os.Getenv("AUDIO_QUALITY")),
		ExtractorArgs: strings.TrimSpace(os.Getenv("YTDLP_EXTRACTOR_ARGS")),
		MaxSizeMB:     mediaMaxMB,
		MaxHeight:     envInt("MEDIA_DOWNLOAD_MAX_HEIGHT", 720),
	}
	mediaInteractive := envBool("MEDIA_DOWNLOAD_INTERACTIVE", true)
	spotifyQueue := newSpotifyPickQueue(envInt("SPOTIFY_AUDIO_WORKERS", 1), envInt("SPOTIFY_AUDIO_QUEUE", 8))
	yandexMusicQueue := newYandexMusicQueue(envInt("YANDEX_MUSIC_WORKERS", 1), envInt("YANDEX_MUSIC_QUEUE", 4))
	mediaQueue := newMediaDownloadQueue(envInt("MEDIA_DOWNLOAD_WORKERS", 1), envInt("MEDIA_DOWNLOAD_QUEUE", 8))
	chatErrorLogEnabled = envBool("CHAT_ERROR_LOG", true)
	debugTriggerLogEnabled = envBool("DEBUG_TRIGGER_LOG", false)
	debugGPTLogEnabled = envBool("DEBUG_GPT_LOG", false)
	logTextClipMax = envInt("LOG_TEXT_CLIP_CHARS", 200)
	if logTextClipMax < 0 {
		logTextClipMax = 200
	}
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
	setOutgoingChatRecentStore(chatRecent, bot.Self.FirstName)
	setChatContextResolver(func(chatID int64, limit int) string {
		return chatRecent.RecentText(chatID, limit)
	})
	defer func() {
		setOutgoingChatRecentStore(nil, "")
		setChatContextResolver(nil)
	}()
	disallowedNotifier := newDisallowedChatNotifier(time.Duration(envInt("DISALLOWED_CHAT_NOTICE_TTL_SEC", 600)) * time.Second)
	portraitManager := newParticipantPortraitManager(store)
	if portraitManager != nil {
		setParticipantPortraitResolver(func(chatID, userID int64) string {
			return portraitManager.Portrait(chatID, userID)
		})
		setParticipantPortraitRemainingResolver(func(chatID, userID int64) int {
			return portraitManager.RemainingUntilUpdate(userID)
		})
		defer func() {
			setParticipantPortraitResolver(nil)
			setParticipantPortraitRemainingResolver(nil)
			portraitManager.Close()
		}()
	}
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
	u.AllowedUpdates = []string{"message", "callback_query", "chat_member", "my_chat_member"}
	updates := getUpdatesChanWithEmojiMeta(bot, u)
	triggerEngine := engine.NewTriggerEngine()
	templateLookup := buildTemplateLookup(store)
	handlerDeps := triggerHandlerDeps{
		triggerActionDeps: triggerActionDeps{
			Bot:               bot,
			IdleTracker:       idleTracker,
			GPTDebouncer:      gptDebouncer,
			Portraits:         portraitManager,
			SpotifyMusic:      spotifyMusicClient,
			SpotifyDownloader: spotifyDownloader,
			SpotifyQueue:      spotifyQueue,
			YandexDownloader:  yandexDownloader,
			YandexQueue:       yandexMusicQueue,
			MediaDownloader:   mediaDownloader,
			MediaQueue:        mediaQueue,
			MediaInteractive:  mediaInteractive,
			TemplateLookup:    templateLookup,
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
			handleDisallowedMyChatMemberNotice(bot, allowedChats, disallowedNotifier, update.RawMyChatMember)
			handleNewMemberUpdate(handlerDeps, update.RawMyChatMember)
		}
		if update.Update.CallbackQuery != nil {
			if musicpick.HandleChoiceCallback(
				bot,
				update.Update.CallbackQuery,
				func(chatID int64, title string, err error) {
					reportChatFailure(bot, chatID, title, err)
				},
				func(ctx context.Context, req musicpick.ChoiceRequest, provider string) error {
					return processMusicProviderChoice(ctx, musicProviderDeps{
						Bot:               bot,
						SpotifyMusic:      spotifyMusicClient,
						SpotifyDownloader: spotifyDownloader,
						SpotifyQueue:      spotifyQueue,
						YandexDownloader:  yandexDownloader,
						YandexQueue:       yandexMusicQueue,
						IdleTracker:       idleTracker,
					}, req, provider)
				},
			) {
				continue
			}
			if pick.HandlePickCallback(
				bot,
				update.Update.CallbackQuery,
				func(chatID int64, title string, err error) {
					reportChatFailure(bot, chatID, title, err)
				},
				func(ctx context.Context, req pick.PickRequest) error {
					_ = ctx
					if strings.EqualFold(strings.TrimSpace(req.Provider), "yandex") {
						targetURL := strings.TrimSpace(req.SourceURL)
						if targetURL == "" {
							return errors.New("empty yandex track url")
						}
						task := yandexMusicTask{
							SendCtx:  sendContext{Bot: bot, ChatID: req.ChatID, ReplyTo: req.ReplyTo},
							URL:      targetURL,
							DL:       yandexDownloader,
							ReportTo: req.ChatID,
						}
						if yandexMusicQueue == nil || !yandexMusicQueue.enqueue(task) {
							return errors.New("yandex music queue is full")
						}
						return nil
					}
					task := spotifyPickTask{
						SendCtx:  sendContext{Bot: bot, ChatID: req.ChatID, ReplyTo: req.ReplyTo},
						Req:      req,
						DL:       spotifyDownloader,
						ReportTo: req.ChatID,
					}
					if spotifyQueue == nil || !spotifyQueue.enqueue(task) {
						return errors.New("spotify queue is full")
					}
					return nil
				},
			) {
				continue
			}
			if mediadl.HandleChoiceCallback(
				bot,
				update.Update.CallbackQuery,
				func(chatID int64, title string, err error) {
					reportChatFailure(bot, chatID, title, err)
				},
				func(ctx context.Context, req mediadl.ChoiceRequest, mode string) error {
					_ = ctx
					log.Printf("media choice selected chat=%d user=%d mode=%s url=%q", req.ChatID, req.UserID, mode, clipText(req.URL, 220))
					task := mediaDownloadTask{
						SendCtx:  sendContext{Bot: bot, ChatID: req.ChatID, ReplyTo: req.ReplyTo},
						URL:      req.URL,
						Mode:     mode,
						DL:       mediaDownloader,
						ReportTo: req.ChatID,
					}
					if mediaQueue == nil || !mediaQueue.enqueue(task) {
						return errors.New("media queue is full")
					}
					log.Printf("media choice enqueued chat=%d mode=%s replyTo=%d", req.ChatID, mode, req.ReplyTo)
					return nil
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
		senderChatPresent := msg != nil && msg.SenderChat != nil
		if senderChatPresent {
			// Telegram anonymous/channel-posted messages may come with From=nil or GroupAnonymousBot.
			// Normalize into a pseudo-user so regular trigger flow can process them.
			senderID := msg.SenderChat.ID
			senderTitle := strings.TrimSpace(msg.SenderChat.Title)
			senderUsername := strings.TrimPrefix(strings.TrimSpace(msg.SenderChat.UserName), "@")
			if senderTitle == "" {
				senderTitle = senderUsername
			}
			if senderTitle == "" {
				senderTitle = "sender_chat"
			}
			if senderID == 0 && msg.From != nil {
				senderID = msg.From.ID
			}
			if msg.From == nil || msg.From.IsBot {
				msg.From = &tgbotapi.User{
					ID:        senderID,
					FirstName: senderTitle,
					UserName:  senderUsername,
					IsBot:     false,
				}
				if debugTriggerLogEnabled {
					log.Printf("sender_chat normalized chat=%d msg=%d sender_chat_id=%d sender_chat_type=%q sender_chat_title=%q",
						msg.Chat.ID, msg.MessageID, msg.SenderChat.ID, msg.SenderChat.Type, strings.TrimSpace(msg.SenderChat.Title))
				}
			}
		}
		if msg.Chat == nil {
			if debugTriggerLogEnabled {
				log.Printf("skip message: missing chat msg=%d", msg.MessageID)
			}
			continue
		}
		if msg.From == nil {
			if debugTriggerLogEnabled {
				senderChatID := int64(0)
				senderChatType := ""
				senderChatTitle := ""
				if msg.SenderChat != nil {
					senderChatID = msg.SenderChat.ID
					senderChatType = msg.SenderChat.Type
					senderChatTitle = strings.TrimSpace(msg.SenderChat.Title)
				}
				log.Printf("skip message: from=nil (likely sender_chat) chat=%d msg=%d sender_chat_id=%d sender_chat_type=%q sender_chat_title=%q text=%q",
					msg.Chat.ID, msg.MessageID, senderChatID, senderChatType, senderChatTitle, clipLogText(strings.TrimSpace(firstNonEmptyUserText(msg)), 180))
			}
			continue
		}
		if msg.From.IsBot {
			if debugTriggerLogEnabled {
				log.Printf("skip message: from bot chat=%d msg=%d from_id=%d from_username=%q sender_chat_present=%v",
					msg.Chat.ID, msg.MessageID, msg.From.ID, strings.TrimSpace(msg.From.UserName), msg.SenderChat != nil)
			}
			continue
		}
		isPrivateChat := msg.Chat.IsPrivate()
		if !isPrivateChat && !allowedChats.Allows(msg.Chat.ID) {
			now := time.Now()
			if disallowedNotifier.shouldNotify(msg.Chat.ID, now) {
				if err := notifyDisallowedChat(bot, msg.Chat.ID); err == nil {
					disallowedNotifier.markNotified(msg.Chat.ID, now)
				}
			}
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
				s := ""
				if isPrivateChat {
					s = "Триггер-бот активен.\n\n" +
						"Админка: /trigger_bot\n" +
						"Команды: /start /help /emojiid /stickerid /spsearch\n" +
						"Мод-команды: !ban/ban !unban/unban !mute/mute !unmute/unmute !kick/kick !readonly/readonly !reload_admins/reload_admins (+ тихие !sban/sban !smute/smute !skick/skick)\n\n" +
						"Теги для ChatGPT-промпта:\n" +
						"{{message}} / {{user_text}} — текст сообщения\n" +
						"{{user_id}}, {{user_first_name}}, {{user_username}}\n" +
						"{{user_display_name}}, {{user_label}}\n" +
						"{{user_portrait}}\n" +
						"{{user_portrait_remaining}}\n" +
						"{{chat_context 12}}\n" +
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
				} else {
					triggerInfo := "Триггеры: список временно недоступен."
					featureInfo := "Что умею:\n— выполнять триггеры, настроенные админами"
					usageInfo := "Как пользоваться:\n— /emojiid — показать ID кастомного эмодзи"
					if items, err := store.ListTriggers(); err == nil {
						enabled := make([]string, 0, len(items))
						hasSpotify := false
						hasUnifiedMusic := false
						hasYandexMusic := false
						hasYouTube := false
						hasInstagram := false
						hasTikTok := false
						hasSoundCloud := false
						hasX := false
						for _, it := range items {
							if !it.Enabled {
								continue
							}
							switch strings.TrimSpace(it.UID) {
							case "system-media-youtube-link-audio":
								hasYouTube = true
							case "system-media-instagram-link-audio":
								hasInstagram = true
							case "system-media-tiktok-link-audio":
								hasTikTok = true
							case "system-media-soundcloud-link-audio":
								hasSoundCloud = true
							case "system-media-x-link-video":
								hasX = true
							}
							if strings.TrimSpace(string(it.ActionType)) == "spotify_music_audio" {
								hasSpotify = true
							}
							if strings.TrimSpace(string(it.ActionType)) == "music_audio" {
								hasUnifiedMusic = true
							}
							if strings.TrimSpace(string(it.ActionType)) == "yandex_music_audio" {
								hasYandexMusic = true
							}
							if strings.TrimSpace(string(it.ActionType)) == "media_x_download" {
								hasX = true
							}
							title := strings.TrimSpace(it.Title)
							if title == "" {
								title = strings.TrimSpace(it.MatchText)
							}
							if title == "" {
								title = fmt.Sprintf("ID %d", it.ID)
							}
							enabled = append(enabled, "• "+clipText(title, 70))
						}
						if len(enabled) > 0 {
							triggerInfo = fmt.Sprintf("Активные триггеры: %d\n%s", len(enabled), strings.Join(enabled, "\n"))
						} else {
							triggerInfo = "Активные триггеры: пока не настроены."
						}
						featureLines := []string{"Что умею:"}
						if hasUnifiedMusic {
							featureLines = append(featureLines, "— искать музыку с выбором сервиса: Spotify или Яндекс.Музыка")
						} else if hasSpotify {
							featureLines = append(featureLines, "— искать и скачивать музыку Spotify")
						}
						if hasYandexMusic {
							featureLines = append(featureLines, "— скачивать музыку из Яндекс.Музыки по ссылке")
						}
						mediaServices := make([]string, 0, 3)
						if hasYouTube {
							mediaServices = append(mediaServices, "YouTube")
						}
						if hasInstagram {
							mediaServices = append(mediaServices, "Instagram")
						}
						if hasTikTok {
							mediaServices = append(mediaServices, "TikTok")
						}
						if hasSoundCloud {
							mediaServices = append(mediaServices, "SoundCloud")
						}
						if hasX {
							mediaServices = append(mediaServices, "X")
						}
						if len(mediaServices) > 0 {
							featureLines = append(featureLines, "— скачивать аудио/видео по ссылкам: "+strings.Join(mediaServices, ", "))
						}
						featureLines = append(featureLines, "— выполнять триггеры и GPT-ответы, настроенные админами")
						featureInfo = strings.Join(featureLines, "\n")
						usageLines := []string{"Как пользоваться:"}
						if len(mediaServices) > 0 {
							usageLines = append(usageLines, "— отправьте ссылку, и я предложу формат (аудио/видео)")
						}
						if hasUnifiedMusic {
							usageLines = append(usageLines, "— напишите: включи/поставь/найди трек ..., затем выберите сервис")
						} else if hasSpotify {
							usageLines = append(usageLines, "— для Spotify: /spsearch <запрос>")
						}
						if hasYandexMusic {
							usageLines = append(usageLines, "— для Яндекс.Музыки: отправьте ссылку music.yandex.ru")
						}
						usageLines = append(usageLines, "— если нужен ID кастомного эмодзи: /emojiid")
						usageLines = append(usageLines, "— если нужен код стикера: отправьте /stickerid в ответ на стикер")
						usageInfo = strings.Join(usageLines, "\n")
					}
					s = "Привет! Я тут, чтобы помогать с музыкой и автоматизацией чата.\n\n" +
						featureInfo + "\n\n" +
						usageInfo + "\n\n" +
						triggerInfo
				}
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
			case "stickerid", "sticker_id":
				stickerHit, stickerOK := extractStickerCode(msg)
				if !stickerOK && msg != nil && msg.ReplyToMessage != nil {
					stickerHit, stickerOK = extractStickerCode(msg.ReplyToMessage)
				}
				if !stickerOK {
					reply(cmdSendCtx.WithReply(msg.MessageID), "Стикер не найден. Отправьте стикер или ответьте этой командой на стикер.", false)
					continue
				}
				lines := []string{
					"Коды стикера:",
					"<code>" + html.EscapeString(buildStickerPairCode(stickerHit)) + "</code>",
				}
				sendHTML(cmdSendCtx.WithReply(msg.MessageID), strings.Join(lines, "\n"), false)
				continue
			case "spsearch", "spfind":
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
			if stickerHit, ok := extractStickerCode(msg); ok {
				lines := []string{
					"Коды стикера:",
					"<code>" + html.EscapeString(buildStickerPairCode(stickerHit)) + "</code>",
				}
				sendHTML(cmdSendCtx.WithReply(msg.MessageID), strings.Join(lines, "\n"), false)
				continue
			}
		}
		if isPrivateChat {
			if debugTriggerLogEnabled {
				log.Printf("skip non-command message in private chat chat=%d msg=%d", msg.Chat.ID, msg.MessageID)
			}
			continue
		}

		text := strings.TrimSpace(firstNonEmptyUserText(msg))
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
				msg.Chat.ID, msg.MessageID, msg.From.ID, msg.From.UserName, clipLogText(text, 220))
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
		matchedAny := false
		used := make(map[int64]struct{}, 4)

		primary := triggerEngine.Select(engine.SelectInput{
			Bot:      bot,
			Msg:      msg,
			Text:     text,
			Triggers: filterNonPassThroughTriggers(items),
			IsAdminFn: func() bool {
				return adminCache.IsChatAdmin(bot, msg.Chat.ID, msg.From.ID)
			},
		})
		if primary != nil {
			matchedAny = true
			used[primary.ID] = struct{}{}
			if debugTriggerLogEnabled {
				if response := triggerResponseDebugText(bot, msg, primary, templateLookup); response != "" {
					log.Printf("pick id=%d title=%q mode=%s action=%s pass_through=%v response=%q", primary.ID, primary.Title, primary.TriggerMode, primary.ActionType, primary.PassThrough, response)
				} else {
					log.Printf("pick id=%d title=%q mode=%s action=%s pass_through=%v", primary.ID, primary.Title, primary.TriggerMode, primary.ActionType, primary.PassThrough)
				}
			}
			handleTriggerActionForMessage(handlerDeps.triggerActionDeps, msg, primary, recentBefore)
		}

		// Second pass: always execute all matching pass-through triggers, even if primary trigger was non-pass-through.
		for len(used) < len(items) {
			tr := triggerEngine.Select(engine.SelectInput{
				Bot:      bot,
				Msg:      msg,
				Text:     text,
				Triggers: filterPassThroughTriggers(filterUnusedTriggers(items, used)),
				IsAdminFn: func() bool {
					return adminCache.IsChatAdmin(bot, msg.Chat.ID, msg.From.ID)
				},
			})
			if tr == nil {
				break
			}
			matchedAny = true
			used[tr.ID] = struct{}{}
			if debugTriggerLogEnabled {
				if response := triggerResponseDebugText(bot, msg, tr, templateLookup); response != "" {
					log.Printf("pass-through pick id=%d title=%q mode=%s action=%s response=%q", tr.ID, tr.Title, tr.TriggerMode, tr.ActionType, response)
				} else {
					log.Printf("pass-through pick id=%d title=%q mode=%s action=%s", tr.ID, tr.Title, tr.TriggerMode, tr.ActionType)
				}
			}
			handleTriggerActionForMessage(handlerDeps.triggerActionDeps, msg, tr, recentBefore)
		}
		if matchedAny {
			continue
		}
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
					TriggeredAt:    time.Now(),
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
	Portraits         *participantPortraitManager
	SpotifyMusic      SpotifyMusicPort
	SpotifyDownloader SpotifyDownloadPort
	SpotifyQueue      *spotifyPickQueue
	YandexDownloader  YandexMusicDownloadPort
	YandexQueue       *yandexMusicQueue
	MediaDownloader   MediaDownloadPort
	MediaQueue        *mediaDownloadQueue
	MediaInteractive  bool
	TemplateLookup    func(string) string
}

type triggerHandlerDeps struct {
	triggerActionDeps
	Allowed    chatAllowList
	Engine     *engine.TriggerEngine
	Store      TriggerStorePort
	AdminCache *adminStatusCache
	ChatRecent *chatRecentStore
}

type musicProviderDeps struct {
	Bot               *tgbotapi.BotAPI
	SpotifyMusic      SpotifyMusicPort
	SpotifyDownloader SpotifyDownloadPort
	SpotifyQueue      *spotifyPickQueue
	YandexDownloader  YandexMusicDownloadPort
	YandexQueue       *yandexMusicQueue
	IdleTracker       *trigger.IdleTracker
}

func processMusicProviderChoice(ctx context.Context, deps musicProviderDeps, req musicpick.ChoiceRequest, provider string) error {
	query := strings.TrimSpace(req.Query)
	if query == "" {
		return errors.New("empty music query")
	}
	replyTo := req.ReplyTo
	switch provider {
	case musicpick.ProviderSpotify:
		if deps.SpotifyMusic == nil || !deps.SpotifyMusic.Enabled() {
			return errors.New("SPOTIPY_CLIENT_ID/SPOTIPY_CLIENT_SECRET не настроены")
		}
		searchCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		tracks, err := deps.SpotifyMusic.SearchTracks(searchCtx, query, 10)
		cancel()
		if err != nil {
			return err
		}
		if len(tracks) == 0 {
			return errors.New("ничего не найдено в Spotify")
		}
		if envBool("SPOTIFY_AUDIO_INTERACTIVE", true) {
			maxResults := 10
			if len(tracks) > maxResults {
				tracks = tracks[:maxResults]
			}
			pickTracks := make([]pick.PickTrack, 0, len(tracks))
			for _, track := range tracks {
				pickTracks = append(pickTracks, pick.PickTrack{
					ID:          track.ID,
					Artist:      track.Artist,
					Title:       track.Title,
					DurationSec: track.DurationSec,
				})
			}
			msg := &tgbotapi.Message{
				Chat: &tgbotapi.Chat{ID: req.ChatID},
				From: &tgbotapi.User{ID: req.UserID},
			}
			m := tgbotapi.NewMessage(req.ChatID, "🎵 Результаты поиска (Spotify):")
			m.ReplyMarkup = pick.BuildPickKeyboard(msg, replyTo, req.SourceMsgID, req.DeleteSource, pickTracks)
			if replyTo > 0 {
				m.ReplyToMessageID = replyTo
				m.AllowSendingWithoutReply = true
			}
			_, err := deps.Bot.Send(m)
			return err
		}
		task := spotifyPickTask{
			SendCtx: sendContext{Bot: deps.Bot, ChatID: req.ChatID, ReplyTo: replyTo},
			Req: pick.PickRequest{
				TrackID:      tracks[0].ID,
				Artist:       tracks[0].Artist,
				Title:        tracks[0].Title,
				ChatID:       req.ChatID,
				ReplyTo:      replyTo,
				SourceMsgID:  req.SourceMsgID,
				DeleteSource: req.DeleteSource,
				UserID:       req.UserID,
			},
			DL:       deps.SpotifyDownloader,
			ReportTo: req.ChatID,
		}
		if deps.SpotifyQueue == nil || !deps.SpotifyQueue.enqueue(task) {
			return errors.New("spotify queue is full")
		}
		return nil
	case musicpick.ProviderYandex:
		searchCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
		tracks, err := deps.YandexDownloader.SearchTracks(searchCtx, query, 10)
		cancel()
		if err != nil {
			return err
		}
		if len(tracks) == 0 {
			return errors.New("ничего не найдено в Yandex Music")
		}
		pickTracks := make([]pick.PickTrack, 0, len(tracks))
		for _, track := range tracks {
			pickTracks = append(pickTracks, pick.PickTrack{
				ID:          strconv.FormatInt(track.ID, 10),
				Provider:    "yandex",
				SourceURL:   strings.TrimSpace(track.URL),
				Artist:      track.Artist,
				Title:       track.Title,
				DurationSec: track.DurationSec,
			})
		}
		msg := &tgbotapi.Message{
			Chat: &tgbotapi.Chat{ID: req.ChatID},
			From: &tgbotapi.User{ID: req.UserID},
		}
		m := tgbotapi.NewMessage(req.ChatID, "🎵 Результаты поиска (Yandex Music):")
		m.ReplyMarkup = pick.BuildPickKeyboard(msg, replyTo, req.SourceMsgID, req.DeleteSource, pickTracks)
		if replyTo > 0 {
			m.ReplyToMessageID = replyTo
			m.AllowSendingWithoutReply = true
		}
		_, err = deps.Bot.Send(m)
		if err != nil {
			return err
		}
		if deps.IdleTracker != nil {
			deps.IdleTracker.MarkActivity(req.ChatID, time.Now())
		}
		return nil
	default:
		return errors.New("unknown music provider")
	}
}

func filterUnusedTriggers(all []Trigger, used map[int64]struct{}) []Trigger {
	if len(all) == 0 {
		return nil
	}
	out := make([]Trigger, 0, len(all))
	for i := range all {
		if _, ok := used[all[i].ID]; ok {
			continue
		}
		out = append(out, all[i])
	}
	return out
}

func filterPassThroughTriggers(all []Trigger) []Trigger {
	if len(all) == 0 {
		return nil
	}
	out := make([]Trigger, 0, len(all))
	for i := range all {
		if !all[i].PassThrough {
			continue
		}
		out = append(out, all[i])
	}
	return out
}

func filterNonPassThroughTriggers(all []Trigger) []Trigger {
	if len(all) == 0 {
		return nil
	}
	out := make([]Trigger, 0, len(all))
	for i := range all {
		if all[i].PassThrough {
			continue
		}
		out = append(out, all[i])
	}
	return out
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
	case "send_sticker":
		replyTo := 0
		if tr.Reply || tr.TriggerMode == "command_reply" {
			replyTo = msg.MessageID
		}
		tmplCtx := newTemplateContext(deps.Bot, msg, tr, deps.TemplateLookup)
		stickerRaw := buildResponseFromMessage(tmplCtx, resolvedTemplate)
		stickerID := extractStickerFileIDFromTemplate(stickerRaw)
		if stickerID == "" {
			reportChatFailure(deps.Bot, msg.Chat.ID, "ошибка отправки стикера", errors.New("empty or invalid sticker file_id in response_text"))
			return
		}
		sendCtx := sendContext{Bot: deps.Bot, ChatID: msg.Chat.ID, ReplyTo: replyTo}
		if ok := sendSticker(sendCtx, stickerID); ok && deps.IdleTracker != nil {
			deps.IdleTracker.MarkActivity(msg.Chat.ID, time.Now())
			deleteTriggerSourceMessage(deps.Bot, msg, tr)
		}
		return
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
	case "delete_user_portrait":
		if msg.From == nil || msg.From.ID == 0 {
			return
		}
		if deps.Portraits == nil {
			reportChatFailure(deps.Bot, msg.Chat.ID, "ошибка удаления портрета", errors.New("participant portraits are not initialized"))
			return
		}
		if err := deps.Portraits.DeletePortrait(msg.From.ID); err != nil {
			log.Printf("delete participant portrait failed chat=%d user=%d err=%v", msg.Chat.ID, msg.From.ID, err)
			reportChatFailure(deps.Bot, msg.Chat.ID, "ошибка удаления портрета", err)
			return
		}
		replyTo := 0
		if tr.Reply || tr.TriggerMode == "command_reply" {
			replyTo = msg.MessageID
		}
		tmplCtx := newTemplateContext(deps.Bot, msg, tr, deps.TemplateLookup)
		out := buildResponseFromMessage(tmplCtx, resolvedTemplate)
		if strings.TrimSpace(out) == "" {
			out = "Портрет удалён. Начну собирать новый по следующим сообщениям."
		}
		sendCtx := sendContext{Bot: deps.Bot, ChatID: msg.Chat.ID, ReplyTo: replyTo}
		if ok := sendHTML(sendCtx, out, tr.Preview); ok && deps.IdleTracker != nil {
			deps.IdleTracker.MarkActivity(msg.Chat.ID, time.Now())
			deleteTriggerSourceMessage(deps.Bot, msg, tr)
		}
		return
	case "gpt_prompt":
		if deps.Portraits != nil {
			deps.Portraits.ObserveMessage(msg)
		}
		ctx := ""
		if isOlenyamTrigger(tr) {
			ctx = recentBefore
		}
		if deps.GPTDebouncer != nil {
			trCopy := *tr
			trCopy.ResponseText = []ResponseTextItem{{Text: resolvedTemplate}}
			triggeredAt := time.Now()
			deps.GPTDebouncer.Schedule(msg.Chat.ID, gpt.PromptTask{
				Bot:            deps.Bot,
				Trigger:        trCopy,
				Msg:            msg,
				TriggeredAt:    triggeredAt,
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
		triggeredAt := time.Now()
		executeGPTPromptTask(gpt.PromptTask{
			Bot:            deps.Bot,
			Trigger:        trCopy,
			Msg:            msg,
			TriggeredAt:    triggeredAt,
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
	case "spotify_music_audio":
		if deps.SpotifyMusic == nil || !deps.SpotifyMusic.Enabled() {
			reportChatFailure(deps.Bot, msg.Chat.ID, "ошибка Spotify-музыки", errors.New("SPOTIPY_CLIENT_ID/SPOTIPY_CLIENT_SECRET не настроены"))
			return
		}
		query := buildSpotifyMusicQueryFromMessage(newTemplateContext(deps.Bot, msg, tr, deps.TemplateLookup), resolvedTemplate)
		if query == "" {
			query = strings.TrimSpace(msg.Text)
		}
		if query == "" {
			return
		}
		replyTo := 0
		if tr.Reply || tr.TriggerMode == "command_reply" {
			replyTo = msg.MessageID
		}
		if trackID, ok := spotifymusic.ExtractTrackID(query); ok {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			track, err := deps.SpotifyMusic.GetTrack(ctx, trackID)
			cancel()
			if err != nil {
				log.Printf("spotify get track failed id=%s err=%v", trackID, err)
				reportChatFailure(deps.Bot, msg.Chat.ID, "ошибка загрузки трека Spotify", err)
				return
			}
			task := spotifyPickTask{
				SendCtx: sendContext{Bot: deps.Bot, ChatID: msg.Chat.ID, ReplyTo: replyTo},
				Req: pick.PickRequest{
					TrackID:      track.ID,
					Artist:       track.Artist,
					Title:        track.Title,
					ChatID:       msg.Chat.ID,
					ReplyTo:      replyTo,
					SourceMsgID:  msg.MessageID,
					DeleteSource: tr.DeleteSource,
					UserID:       msg.From.ID,
				},
				DL:       deps.SpotifyDownloader,
				Msg:      msg,
				Trigger:  tr,
				Idle:     deps.IdleTracker,
				ReportTo: msg.Chat.ID,
			}
			if deps.SpotifyQueue == nil || !deps.SpotifyQueue.enqueue(task) {
				reportChatFailure(deps.Bot, msg.Chat.ID, "ошибка отправки аудио Spotify", errors.New("spotify queue is full"))
				return
			}
			if debugTriggerLogEnabled {
				log.Printf("send spotify/audio queued by link trigger=%d replyTo=%d track=%q", tr.ID, replyTo, clipText(spotifymusic.BuildSearchQuery(track), 180))
			}
			return
		}

		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		tracks, err := deps.SpotifyMusic.SearchTracks(ctx, query, 10)
		cancel()
		if err != nil {
			log.Printf("spotify music search failed: %v", err)
			reportChatFailure(deps.Bot, msg.Chat.ID, "ошибка поиска музыки Spotify", err)
			return
		}
		if len(tracks) == 0 {
			if debugTriggerLogEnabled {
				log.Printf("spotify music search empty trigger=%d query=%q", tr.ID, clipText(query, 220))
			}
			return
		}
		if envBool("SPOTIFY_AUDIO_INTERACTIVE", true) {
			maxResults := 10
			if len(tracks) > maxResults {
				tracks = tracks[:maxResults]
			}
			pickTracks := make([]pick.PickTrack, 0, len(tracks))
			for _, track := range tracks {
				pickTracks = append(pickTracks, pick.PickTrack{
					ID:          track.ID,
					Artist:      track.Artist,
					Title:       track.Title,
					DurationSec: track.DurationSec,
				})
			}
			m := tgbotapi.NewMessage(msg.Chat.ID, "🎵 Результаты поиска:")
			m.ReplyMarkup = pick.BuildPickKeyboard(msg, replyTo, msg.MessageID, tr.DeleteSource, pickTracks)
			if replyTo > 0 {
				m.ReplyToMessageID = replyTo
				m.AllowSendingWithoutReply = true
			}
			if _, err := deps.Bot.Send(m); err != nil {
				reportChatFailure(deps.Bot, msg.Chat.ID, "ошибка отправки списка Spotify", err)
				return
			}
			if tr.DeleteSource && msg.MessageID > 0 {
				_, _ = deps.Bot.Request(tgbotapi.DeleteMessageConfig{
					ChatID:    msg.Chat.ID,
					MessageID: msg.MessageID,
				})
			}
			if deps.IdleTracker != nil {
				deps.IdleTracker.MarkActivity(msg.Chat.ID, time.Now())
			}
			return
		}
		task := spotifyPickTask{
			SendCtx: sendContext{Bot: deps.Bot, ChatID: msg.Chat.ID, ReplyTo: replyTo},
			Req: pick.PickRequest{
				TrackID:      tracks[0].ID,
				Artist:       tracks[0].Artist,
				Title:        tracks[0].Title,
				ChatID:       msg.Chat.ID,
				ReplyTo:      replyTo,
				SourceMsgID:  msg.MessageID,
				DeleteSource: tr.DeleteSource,
				UserID:       msg.From.ID,
			},
			DL:       deps.SpotifyDownloader,
			Msg:      msg,
			Trigger:  tr,
			Idle:     deps.IdleTracker,
			ReportTo: msg.Chat.ID,
		}
		if deps.SpotifyQueue == nil || !deps.SpotifyQueue.enqueue(task) {
			reportChatFailure(deps.Bot, msg.Chat.ID, "ошибка отправки аудио Spotify", errors.New("spotify queue is full"))
			return
		}
		if debugTriggerLogEnabled {
			log.Printf("send spotify/audio queued trigger=%d replyTo=%d query=%q", tr.ID, replyTo, clipText(query, 160))
		}
		return
	case "music_audio":
		query := buildSpotifyMusicQueryFromMessage(newTemplateContext(deps.Bot, msg, tr, deps.TemplateLookup), resolvedTemplate)
		if query == "" {
			query = strings.TrimSpace(firstNonEmptyUserText(msg))
		}
		query = strings.TrimSpace(query)
		if query == "" {
			return
		}
		replyTo := 0
		if tr.Reply || tr.TriggerMode == "command_reply" {
			replyTo = msg.MessageID
		}
		// Fast paths for direct links.
		if trackID, ok := spotifymusic.ExtractTrackID(query); ok {
			if deps.SpotifyMusic == nil || !deps.SpotifyMusic.Enabled() {
				reportChatFailure(deps.Bot, msg.Chat.ID, "ошибка Spotify-музыки", errors.New("SPOTIPY_CLIENT_ID/SPOTIPY_CLIENT_SECRET не настроены"))
				return
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			track, err := deps.SpotifyMusic.GetTrack(ctx, trackID)
			cancel()
			if err != nil {
				reportChatFailure(deps.Bot, msg.Chat.ID, "ошибка загрузки трека Spotify", err)
				return
			}
			task := spotifyPickTask{
				SendCtx: sendContext{Bot: deps.Bot, ChatID: msg.Chat.ID, ReplyTo: replyTo},
				Req: pick.PickRequest{
					TrackID:      track.ID,
					Artist:       track.Artist,
					Title:        track.Title,
					ChatID:       msg.Chat.ID,
					ReplyTo:      replyTo,
					SourceMsgID:  msg.MessageID,
					DeleteSource: tr.DeleteSource,
					UserID:       msg.From.ID,
				},
				DL:       deps.SpotifyDownloader,
				Msg:      msg,
				Trigger:  tr,
				Idle:     deps.IdleTracker,
				ReportTo: msg.Chat.ID,
			}
			if deps.SpotifyQueue == nil || !deps.SpotifyQueue.enqueue(task) {
				reportChatFailure(deps.Bot, msg.Chat.ID, "ошибка отправки аудио Spotify", errors.New("spotify queue is full"))
				return
			}
			return
		}
		if targetURL := extractYandexMusicURL(query); targetURL != "" {
			task := yandexMusicTask{
				SendCtx:  sendContext{Bot: deps.Bot, ChatID: msg.Chat.ID, ReplyTo: replyTo},
				URL:      targetURL,
				DL:       deps.YandexDownloader,
				Msg:      msg,
				Trigger:  tr,
				Idle:     deps.IdleTracker,
				ReportTo: msg.Chat.ID,
			}
			if deps.YandexQueue == nil || !deps.YandexQueue.enqueue(task) {
				reportChatFailure(deps.Bot, msg.Chat.ID, "ошибка скачивания Yandex Music", errors.New("yandex music queue is full"))
				return
			}
			return
		}
		m := tgbotapi.NewMessage(msg.Chat.ID, "🎵 Где искать трек?")
		m.ReplyMarkup = musicpick.BuildChoiceKeyboard(msg, replyTo, msg.MessageID, tr.DeleteSource, query)
		if replyTo > 0 {
			m.ReplyToMessageID = replyTo
			m.AllowSendingWithoutReply = true
		}
		if _, err := deps.Bot.Send(m); err != nil {
			reportChatFailure(deps.Bot, msg.Chat.ID, "ошибка выбора музыкального сервиса", err)
			return
		}
		if deps.IdleTracker != nil {
			deps.IdleTracker.MarkActivity(msg.Chat.ID, time.Now())
		}
		return
	case "yandex_music_audio":
		query := buildMediaDownloadQueryFromMessage(newTemplateContext(deps.Bot, msg, tr, deps.TemplateLookup), resolvedTemplate)
		targetURL := extractYandexMusicURL(query)
		if targetURL == "" {
			return
		}
		replyTo := 0
		if tr.Reply || tr.TriggerMode == "command_reply" {
			replyTo = msg.MessageID
		}
		task := yandexMusicTask{
			SendCtx:  sendContext{Bot: deps.Bot, ChatID: msg.Chat.ID, ReplyTo: replyTo},
			URL:      targetURL,
			DL:       deps.YandexDownloader,
			Msg:      msg,
			Trigger:  tr,
			Idle:     deps.IdleTracker,
			ReportTo: msg.Chat.ID,
		}
		if deps.YandexQueue == nil || !deps.YandexQueue.enqueue(task) {
			reportChatFailure(deps.Bot, msg.Chat.ID, "ошибка скачивания Yandex Music", errors.New("yandex music queue is full"))
			return
		}
		if debugTriggerLogEnabled {
			log.Printf("send yandex music queued trigger=%d replyTo=%d url=%q", tr.ID, replyTo, clipText(targetURL, 160))
		}
		return
	case "media_link_audio":
		query := buildMediaDownloadQueryFromMessage(newTemplateContext(deps.Bot, msg, tr, deps.TemplateLookup), resolvedTemplate)
		targetURL := extractSupportedMediaURL(query)
		if targetURL == "" {
			return
		}
		_, mediaService, _ := mediadl.NormalizeSupportedURL(targetURL)
		replyTo := 0
		if tr.Reply || tr.TriggerMode == "command_reply" {
			replyTo = msg.MessageID
		}
		mode, useInteractive := mediaModeAndInteractivity(mediaService, deps.MediaInteractive)
		if useInteractive {
			req := mediadl.ChoiceRequest{
				URL:          targetURL,
				ChatID:       msg.Chat.ID,
				ReplyTo:      replyTo,
				SourceMsgID:  msg.MessageID,
				UserID:       msg.From.ID,
				DeleteSource: tr.DeleteSource,
			}
			m := tgbotapi.NewMessage(msg.Chat.ID, "Выбери формат скачивания:")
			kb := mediadl.BuildChoiceKeyboard(msg, req)
			m.ReplyMarkup = &kb
			if replyTo > 0 {
				m.ReplyToMessageID = replyTo
				m.AllowSendingWithoutReply = true
			}
			log.Printf("media pick keyboard built rows=%d chat=%d replyTo=%d", len(kb.InlineKeyboard), msg.Chat.ID, replyTo)
			if _, err := deps.Bot.Send(m); err != nil {
				reportChatFailure(deps.Bot, msg.Chat.ID, "ошибка отправки выбора формата", err)
				return
			}
			log.Printf("send media pick keyboard trigger=%d replyTo=%d url=%q", tr.ID, replyTo, clipText(targetURL, 160))
			if deps.IdleTracker != nil {
				deps.IdleTracker.MarkActivity(msg.Chat.ID, time.Now())
			}
			return
		}
		task := mediaDownloadTask{
			SendCtx:  sendContext{Bot: deps.Bot, ChatID: msg.Chat.ID, ReplyTo: replyTo},
			URL:      targetURL,
			Mode:     mode,
			DL:       deps.MediaDownloader,
			Msg:      msg,
			Trigger:  tr,
			Idle:     deps.IdleTracker,
			ReportTo: msg.Chat.ID,
		}
		if deps.MediaQueue == nil || !deps.MediaQueue.enqueue(task) {
			reportChatFailure(deps.Bot, msg.Chat.ID, "ошибка скачивания аудио", errors.New("media download queue is full"))
			return
		}
		log.Printf("send media queued trigger=%d replyTo=%d mode=%s service=%s url=%q", tr.ID, replyTo, mode, mediaService, clipText(targetURL, 160))
		return
	case "media_tiktok_download":
		query := buildMediaDownloadQueryFromMessage(newTemplateContext(deps.Bot, msg, tr, deps.TemplateLookup), resolvedTemplate)
		targetURL := extractSupportedMediaURLByService(query, "tiktok")
		if targetURL == "" {
			return
		}
		replyTo := 0
		if tr.Reply || tr.TriggerMode == "command_reply" {
			replyTo = msg.MessageID
		}
		task := mediaDownloadTask{
			SendCtx:  sendContext{Bot: deps.Bot, ChatID: msg.Chat.ID, ReplyTo: replyTo},
			URL:      targetURL,
			Mode:     mediadl.ModeAuto,
			DL:       deps.MediaDownloader,
			Msg:      msg,
			Trigger:  tr,
			Idle:     deps.IdleTracker,
			ReportTo: msg.Chat.ID,
		}
		if deps.MediaQueue == nil || !deps.MediaQueue.enqueue(task) {
			reportChatFailure(deps.Bot, msg.Chat.ID, "ошибка скачивания TikTok", errors.New("media download queue is full"))
			return
		}
		log.Printf("send tiktok queued trigger=%d replyTo=%d url=%q", tr.ID, replyTo, clipText(targetURL, 160))
		return
	case "media_x_download":
		query := buildMediaDownloadQueryFromMessage(newTemplateContext(deps.Bot, msg, tr, deps.TemplateLookup), resolvedTemplate)
		targetURL := extractSupportedMediaURLByService(query, "x")
		if targetURL == "" {
			return
		}
		replyTo := 0
		if tr.Reply || tr.TriggerMode == "command_reply" {
			replyTo = msg.MessageID
		}
		task := mediaDownloadTask{
			SendCtx:  sendContext{Bot: deps.Bot, ChatID: msg.Chat.ID, ReplyTo: replyTo},
			URL:      targetURL,
			Mode:     mediadl.ModeVideo,
			DL:       deps.MediaDownloader,
			Msg:      msg,
			Trigger:  tr,
			Idle:     deps.IdleTracker,
			ReportTo: msg.Chat.ID,
		}
		if deps.MediaQueue == nil || !deps.MediaQueue.enqueue(task) {
			reportChatFailure(deps.Bot, msg.Chat.ID, "ошибка скачивания X-видео", errors.New("media download queue is full"))
			return
		}
		log.Printf("send x video queued trigger=%d replyTo=%d url=%q", tr.ID, replyTo, clipText(targetURL, 160))
		return
	default:
		replyTo := 0
		if tr.Reply || tr.TriggerMode == "command_reply" {
			replyTo = msg.MessageID
		}
		tmplCtx := newTemplateContext(deps.Bot, msg, tr, deps.TemplateLookup)
		out := buildResponseFromMessage(tmplCtx, resolvedTemplate)
		if strings.TrimSpace(out) == "" {
			reportEmptyTriggerMessage(deps.Bot, msg.Chat.ID, tr)
			return
		}
		sendCtx := sendContext{Bot: deps.Bot, ChatID: msg.Chat.ID, ReplyTo: replyTo}
		if ok := sendHTML(sendCtx, out, tr.Preview); ok && deps.IdleTracker != nil {
			deps.IdleTracker.MarkActivity(msg.Chat.ID, time.Now())
			deleteTriggerSourceMessage(deps.Bot, msg, tr)
		}
	}
}
