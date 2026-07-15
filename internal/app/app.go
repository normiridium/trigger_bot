package app

import (
	"bytes"
	"context"
	crand "crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	gmhtml "github.com/yuin/goldmark/renderer/html"

	"trigger-admin-bot/internal/bottmp"
	"trigger-admin-bot/internal/chataccess"
	"trigger-admin-bot/internal/chatclear"
	"trigger-admin-bot/internal/engine"
	"trigger-admin-bot/internal/gpt"
	"trigger-admin-bot/internal/mediadl"
	"trigger-admin-bot/internal/musicpick"
	"trigger-admin-bot/internal/pick"
	"trigger-admin-bot/internal/spotifymusic"
	"trigger-admin-bot/internal/trigger"
	"trigger-admin-bot/internal/vkaudio"
	"trigger-admin-bot/internal/yandexmusic"
)

var chatErrorLogEnabled bool
var debugTriggerLogEnabled bool
var debugGPTLogEnabled bool
var errTelegramUploadTooLarge = errors.New("telegram upload too large")
var appLogFile *os.File

type yandexTop10Request struct {
	Token        string
	ChatID       int64
	UserID       int64
	ReplyTo      int
	SourceMsgID  int
	DeleteSource bool
	Query        string
	Tracks       []yandexmusic.SearchTrack
	ExpiresAt    time.Time
}

var yandexTop10State = struct {
	mu   sync.Mutex
	data map[string]yandexTop10Request
}{
	data: make(map[string]yandexTop10Request),
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
	s.mu.RLock()
	items := s.messages[chatID]
	s.mu.RUnlock()
	return renderRecentContextLines(items, s.maxAge, limit)
}

func renderRecentContextLines(items []recentChatMessage, maxAge time.Duration, limit int) string {
	if limit <= 0 {
		limit = 12
	}
	charBudget := limit * 150
	if charBudget <= 0 {
		charBudget = 12 * 150
	}
	now := time.Now()
	if len(items) == 0 {
		return ""
	}

	candidates := make([]string, 0, len(items))
	for _, it := range items {
		if maxAge > 0 && now.Sub(it.At) > maxAge {
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
		candidates = append(candidates, fmt.Sprintf("[%s] %s: %s", it.At.Local().Format("02.01.2006 15:04"), user, txt))
	}
	if len(candidates) == 0 {
		return ""
	}

	// Fill from newest to oldest by char budget, only whole lines.
	// If the next full line does not fit, stop (do not truncate).
	chosenReversed := make([]string, 0, len(candidates))
	used := 0
	for i := len(candidates) - 1; i >= 0; i-- {
		line := candidates[i]
		lineLen := len([]rune(line))
		extra := lineLen
		if len(chosenReversed) > 0 {
			extra++ // newline between lines in final join
		}
		if used+extra > charBudget {
			break
		}
		chosenReversed = append(chosenReversed, line)
		used += extra
	}
	if len(chosenReversed) == 0 {
		return ""
	}

	lines := make([]string, 0, len(chosenReversed))
	for i := len(chosenReversed) - 1; i >= 0; i-- {
		lines = append(lines, chosenReversed[i])
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

func sendTypingAction(bot *tgbotapi.BotAPI, chatID int64) bool {
	if bot == nil || chatID == 0 {
		return false
	}
	if _, err := bot.Request(tgbotapi.NewChatAction(chatID, tgbotapi.ChatTyping)); err != nil {
		if debugTriggerLogEnabled {
			log.Printf("send typing action failed chat=%d err=%v", chatID, err)
		}
		return false
	}
	return true
}

func startTypingLoop(bot *tgbotapi.BotAPI, chatID int64, interval time.Duration) func() {
	if bot == nil || chatID == 0 {
		return func() {}
	}
	if interval <= 0 {
		interval = 4 * time.Second
	}
	done := make(chan struct{})
	var once sync.Once
	go func() {
		_ = sendTypingAction(bot, chatID)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				_ = sendTypingAction(bot, chatID)
			}
		}
	}()
	return func() {
		once.Do(func() { close(done) })
	}
}

func simulateStickerPickDelay(bot *tgbotapi.BotAPI, chatID int64, delay time.Duration) {
	if bot == nil || chatID == 0 {
		return
	}
	if delay <= 0 {
		delay = 2 * time.Second
	}
	if _, err := bot.Request(tgbotapi.NewChatAction(chatID, "choose_sticker")); err != nil {
		if debugTriggerLogEnabled {
			log.Printf("send choose_sticker action failed chat=%d err=%v", chatID, err)
		}
		// Some chats/clients may reject choose_sticker; fallback to typing.
		_ = sendTypingAction(bot, chatID)
	}
	time.Sleep(delay)
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
	_ = sendTypingAction(bot, chatID)
	time.Sleep(wait)
}

func estimateGPTReplyHumanPause(text string) time.Duration {
	if !envBool("GPT_HUMAN_PAUSE", true) {
		return 0
	}
	minMS := envInt("GPT_HUMAN_PAUSE_MIN_MS", 1800)
	maxMS := envInt("GPT_HUMAN_PAUSE_MAX_MS", 12000)
	if minMS < 0 {
		minMS = 0
	}
	if maxMS < minMS {
		maxMS = minMS
	}
	cleaned := strings.TrimSpace(strings.ToValidUTF8(text, ""))
	cleaned = canonicalizeTGEmojiTags(cleaned)
	cleaned = htmlTagStripRe.ReplaceAllString(cleaned, " ")
	cleaned = strings.Join(strings.Fields(cleaned), " ")
	runes := len([]rune(cleaned))
	words := len(strings.Fields(cleaned))
	// Typing-like pacing with small human reaction jitter.
	estimateMS := runes*26 + 700 + rand.Intn(650)
	if byWords := words * 260; byWords > estimateMS {
		estimateMS = byWords + 450 + rand.Intn(400)
	}
	if estimateMS < minMS {
		estimateMS = minMS
	}
	if estimateMS > maxMS {
		estimateMS = maxMS
	}
	return time.Duration(estimateMS) * time.Millisecond
}

func executeGPTPromptTask(task gpt.PromptTask) {
	if task.Bot == nil || task.Msg == nil {
		return
	}
	stopTyping := startTypingLoop(task.Bot, task.Msg.Chat.ID, 4*time.Second)
	defer stopTyping()
	editQuietSec := envInt("GPT_EDIT_WAIT_SEC", 3)
	editMaxSec := envInt("GPT_EDIT_WAIT_MAX_SEC", 15)
	if editQuietSec < 0 {
		editQuietSec = 0
	}
	if editMaxSec < editQuietSec {
		editMaxSec = editQuietSec
	}
	editWaited, editChanged := waitForMessageEditsSettled(task.Msg, time.Duration(editQuietSec)*time.Second, time.Duration(editMaxSec)*time.Second)
	if (debugTriggerLogEnabled || debugGPTLogEnabled) && editWaited > 0 {
		log.Printf("gpt edit wait chat=%d msg=%d waited_ms=%d changed=%v text=%q",
			task.Msg.Chat.ID, task.Msg.MessageID, editWaited.Milliseconds(), editChanged, clipLogText(firstNonEmptyUserText(task.Msg), 200))
	}
	tmplCtx := newTemplateContext(task.Bot, task.Msg, &task.Trigger, task.TemplateLookup)
	result, err := generateChatGPTReply(tmplCtx, pickResponseVariantText(task.Trigger.ResponseText), task.RecentContext)
	if err != nil {
		log.Printf("gpt prompt failed: %s", sanitizeSecretText(err.Error()))
		if isOpenAIInsufficientQuotaError(err) {
			if allowOpenAIQuotaWarning(task.Msg.Chat.ID, time.Now(), 30*time.Minute) {
				if !sendUserLimitLowWarning(task.Bot, task.Msg, task.QuotaLowTrigger, task.TemplateLookup) {
					reportChatFailure(task.Bot, task.Msg.Chat.ID, "ошибка запроса к ChatGPT", err)
				}
			}
			return
		}
		reportChatFailure(task.Bot, task.Msg.Chat.ID, "ошибка запроса к ChatGPT", err)
		return
	}
	if task.RecordGPTTokens != nil && result.Usage.TotalTokens > 0 && task.Msg.From != nil {
		remaining, crossedLow, recErr := task.RecordGPTTokens(task.Msg.From.ID, result.Usage.TotalTokens, time.Now())
		if recErr != nil {
			log.Printf("gpt user-token-limit record failed user=%d tokens=%d: %v", task.Msg.From.ID, result.Usage.TotalTokens, recErr)
		} else {
			if debugGPTLogEnabled || debugTriggerLogEnabled {
				log.Printf("gpt user-token-limit user=%d spent=%d remaining=%d", task.Msg.From.ID, result.Usage.TotalTokens, remaining)
			}
			if crossedLow {
				sendUserLimitLowWarning(task.Bot, task.Msg, task.UserLimitLowTrigger, task.TemplateLookup)
			}
		}
	}
	out := result.Text
	out = expandTemplateCalls(out, task.TemplateLookup)
	startedAt := task.TriggeredAt
	if startedAt.IsZero() {
		startedAt = time.Now()
	}
	replyTo := 0
	if task.Trigger.Reply || task.Trigger.TriggerMode == TriggerModeCommandReply {
		replyTo = task.Msg.MessageID
	}
	rawOut := out
	if debugGPTLogEnabled {
		log.Printf("gpt flow trigger=%d chat=%d msg=%d raw_len=%d raw_tgemoji=%d raw=%q",
			task.Trigger.ID, task.Msg.Chat.ID, task.Msg.MessageID, len(rawOut), countTGEmojiTags(rawOut), clipText(rawOut, 1400))
	}
	out = canonicalizeTGEmojiTags(out)
	if task.Msg.ReplyToMessage != nil && task.Msg.ReplyToMessage.MessageID > 0 {
		if rand.Intn(100) < gptReplyReactionChancePercent() {
			if next, reacted := tryApplyReplyReactionFromGPTPrefix(task.Bot, task.Msg.Chat.ID, task.Msg.MessageID, out); reacted {
				out = next
			}
		}
	}
	if debugGPTLogEnabled {
		log.Printf("gpt flow trigger=%d canonical_len=%d canonical_tgemoji=%d canonical=%q",
			task.Trigger.ID, len(out), countTGEmojiTags(out), clipText(out, 1400))
	}
	pause := estimateGPTReplyHumanPause(out)
	if editWaited > 0 {
		if pause > editWaited {
			pause -= editWaited
		} else {
			pause = 0
		}
	}
	if debugTriggerLogEnabled || debugGPTLogEnabled || envBool("GPT_HUMAN_PAUSE_LOG", false) {
		log.Printf("gpt human pause trigger=%d chat=%d msg=%d delay_ms=%d out_len=%d out_words=%d",
			task.Trigger.ID,
			task.Msg.Chat.ID,
			task.Msg.MessageID,
			pause.Milliseconds(),
			len([]rune(strings.TrimSpace(out))),
			len(strings.Fields(strings.TrimSpace(htmlTagStripRe.ReplaceAllString(out, " ")))),
		)
	}
	ensureMinTypingWindow(task.Bot, task.Msg.Chat.ID, startedAt, pause)
	sent := false
	sendMode := "markdown"
	hasHTML := containsTelegramHTMLMarkup(out)
	hasMarkdownLite := containsMarkdownLiteMarkup(out)
	if debugGPTLogEnabled {
		log.Printf("gpt flow trigger=%d has_html=%v has_markdown_lite=%v", task.Trigger.ID, hasHTML, hasMarkdownLite)
	}
	sendCtx := sendContext{Bot: task.Bot, ChatID: task.Msg.Chat.ID, ReplyTo: replyTo}
	if hasHTML || hasMarkdownLite {
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
	if tr.ActionType == ActionTypeDelete {
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

func adminModeAllowsTrigger(tr *Trigger, isAdmin bool) bool {
	if tr == nil {
		return false
	}
	switch tr.AdminMode {
	case AdminModeAdmins:
		return isAdmin
	case AdminModeNotAdmin:
		return !isAdmin
	default:
		return true
	}
}

func pickUserLimitLowWarningTrigger(items []Trigger, isAdmin bool) *Trigger {
	for i := range items {
		it := items[i]
		if !it.Enabled || it.ActionType != ActionTypeUserLimitLow {
			continue
		}
		if !adminModeAllowsTrigger(&it, isAdmin) {
			continue
		}
		return &it
	}
	return nil
}

func sendUserLimitLowWarning(bot *tgbotapi.BotAPI, msg *tgbotapi.Message, tr *Trigger, templateLookup func(string) string) bool {
	if bot == nil || msg == nil || tr == nil {
		return false
	}
	rawTemplate := pickResponseVariantText(tr.ResponseText)
	if strings.TrimSpace(rawTemplate) == "" {
		return false
	}
	resolvedTemplate := expandTemplateCalls(rawTemplate, templateLookup)
	tmplCtx := newTemplateContext(bot, msg, tr, templateLookup)
	out := buildResponseFromMessage(tmplCtx, resolvedTemplate)
	if strings.TrimSpace(out) == "" {
		return false
	}
	replyTo := 0
	if tr.Reply || tr.TriggerMode == TriggerModeCommandReply {
		replyTo = msg.MessageID
	}
	return sendHTML(sendContext{Bot: bot, ChatID: msg.Chat.ID, ReplyTo: replyTo}, out, tr.Preview)
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
var tgEmojiAnyWithIDRe = regexp.MustCompile(`(?is)<tg-emoji[^>]*emoji-id\s*=\s*"?(?P<id>\d+)"?[^>]*>(?P<fallback>.*?)</tg-emoji>`)
var tgEmojiTypoTagRe = regexp.MustCompile(`(?is)<\s*(/?)\s*tr-emoji\b`)
var telegramHTMLTagRe = regexp.MustCompile(`(?is)<\s*/?\s*(b|strong|i|em|u|ins|s|strike|del|code|pre|blockquote|a|tg-spoiler|tg-emoji)\b`)
var templateCallPattern = regexp.MustCompile(`\{\{\s*template\s+\"([^\"]+)\"\s*\}\}`)
var supportedMediaURLRe = regexp.MustCompile(`https?://[^\s<>"']+`)
var htmlTagStripRe = regexp.MustCompile(`(?is)<[^>]+>`)
var stickerFileIDTokenRe = regexp.MustCompile(`^[A-Za-z0-9_-]{16,}$`)

func canonicalizeTGEmojiTags(s string) string {
	if strings.TrimSpace(s) == "" {
		return s
	}
	// Common model typo: <tr-emoji ...>...</tg-emoji>
	// Normalize it early so Telegram parser doesn't fail on unexpected closing tags.
	s = tgEmojiTypoTagRe.ReplaceAllString(s, "<${1}tg-emoji")
	s = strings.ReplaceAll(s, `\"`, `"`)
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

type reactionCandidate struct {
	Emoji         string
	CustomEmojiID string
	Consumed      string
}

type telegramReactionType struct {
	Type          string `json:"type"`
	Emoji         string `json:"emoji,omitempty"`
	CustomEmojiID string `json:"custom_emoji_id,omitempty"`
}

type appliedReaction struct {
	Type  string
	Value string
}

func tryApplyReplyReactionFromGPTPrefix(bot *tgbotapi.BotAPI, chatID int64, messageID int, text string) (string, bool) {
	if bot == nil || chatID == 0 || messageID == 0 {
		return text, false
	}

	emoji, start, end, ok := extractFirstReactionEmoji(text)
	if !ok || emoji == "" {
		return text, false
	}
	normalized, ok, rule := convertToAllowedReactionEmoji(emoji)
	if !ok || normalized == "" {
		if debugTriggerLogEnabled || debugGPTLogEnabled {
			log.Printf("set reaction skipped chat=%d msg=%d strategy=first_unicode_emoji reason=%s candidate=%q", chatID, messageID, rule, emoji)
		}
		return text, false
	}
	applied, err := setMessageReaction(bot, chatID, messageID, reactionCandidate{Emoji: normalized})
	if err != nil {
		if debugTriggerLogEnabled || debugGPTLogEnabled {
			log.Printf("set reaction failed chat=%d msg=%d strategy=first_unicode_emoji err=%v candidate=%q mapped=%q rule=%s", chatID, messageID, err, emoji, normalized, rule)
		}
		return text, false
	}
	if debugTriggerLogEnabled || debugGPTLogEnabled {
		log.Printf("set reaction ok chat=%d msg=%d type=%s value=%q strategy=first_unicode_emoji candidate=%q mapped=%q rule=%s", chatID, messageID, applied.Type, applied.Value, emoji, normalized, rule)
	}
	return removeReactionTokenFromText(text, start, end), true
}

func extractFirstReactionEmoji(text string) (emoji string, start int, end int, ok bool) {
	if strings.TrimSpace(text) == "" {
		return "", -1, -1, false
	}

	customAll := tgEmojiAnyWithIDRe.FindAllStringSubmatchIndex(text, -1)
	customRanges := make([][2]int, 0, len(customAll))
	for _, m := range customAll {
		if len(m) >= 2 && m[0] >= 0 && m[1] > m[0] {
			customRanges = append(customRanges, [2]int{m[0], m[1]})
		}
	}

	// New priority: first prefer plain unicode emoji outside custom tg-emoji tags.
	unicodeStart, unicodeEnd, unicodeEmoji := findFirstUnicodeEmojiOutsideRanges(text, customRanges)
	if unicodeStart >= 0 && unicodeEnd > unicodeStart && unicodeEmoji != "" {
		return unicodeEmoji, unicodeStart, unicodeEnd, true
	}

	// Fallback to custom tg-emoji if no plain unicode emoji is found.
	for _, custom := range customAll {
		customStart := custom[0]
		customEnd := custom[1]
		if idxFallback := tgEmojiAnyWithIDRe.SubexpIndex("fallback"); idxFallback >= 0 {
			from, to := custom[idxFallback*2], custom[idxFallback*2+1]
			if from >= 0 && to >= from {
				if _, _, em := findFirstUnicodeEmojiInText(strings.TrimSpace(text[from:to])); em != "" {
					return em, customStart, customEnd, true
				}
			}
		}
	}
	return "", -1, -1, false
}

func setMessageReaction(bot *tgbotapi.BotAPI, chatID int64, messageID int, c reactionCandidate) (appliedReaction, error) {
	if bot == nil || chatID == 0 || messageID == 0 {
		return appliedReaction{}, errors.New("invalid reaction target")
	}
	if strings.TrimSpace(c.CustomEmojiID) == "" && strings.TrimSpace(c.Emoji) == "" {
		return appliedReaction{}, errors.New("empty reaction candidate")
	}
	if id := strings.TrimSpace(c.CustomEmojiID); id != "" {
		params := tgbotapi.Params{}
		params.AddNonZero64("chat_id", chatID)
		params.AddNonZero("message_id", messageID)
		b, err := json.Marshal([]telegramReactionType{{
			Type:          "custom_emoji",
			CustomEmojiID: id,
		}})
		if err != nil {
			return appliedReaction{}, err
		}
		params["reaction"] = string(b)
		if _, err := bot.MakeRequest("setMessageReaction", params); err == nil {
			return appliedReaction{Type: "custom_emoji", Value: id}, nil
		} else {
			return appliedReaction{}, err
		}
	}

	emoji := strings.TrimSpace(c.Emoji)
	if emoji == "" {
		return appliedReaction{}, errors.New("empty emoji reaction")
	}
	params := tgbotapi.Params{}
	params.AddNonZero64("chat_id", chatID)
	params.AddNonZero("message_id", messageID)
	b, err := json.Marshal([]telegramReactionType{{
		Type:  "emoji",
		Emoji: emoji,
	}})
	if err != nil {
		return appliedReaction{}, err
	}
	params["reaction"] = string(b)
	if _, err := bot.MakeRequest("setMessageReaction", params); err != nil {
		return appliedReaction{}, err
	}
	return appliedReaction{Type: "emoji", Value: emoji}, nil
}

func extractLeadingReactionCandidate(text string) (reactionCandidate, string, bool) {
	if strings.TrimSpace(text) == "" {
		return reactionCandidate{}, text, false
	}

	customAll := tgEmojiAnyWithIDRe.FindAllStringSubmatchIndex(text, -1)
	customRanges := make([][2]int, 0, len(customAll))
	for _, m := range customAll {
		if len(m) >= 2 && m[0] >= 0 && m[1] > m[0] {
			customRanges = append(customRanges, [2]int{m[0], m[1]})
		}
	}

	// New priority: plain unicode emoji outside tg-emoji tags.
	uStart, uEnd, uEmoji := findFirstUnicodeEmojiOutsideRanges(text, customRanges)
	if uStart >= 0 && uEnd > uStart && uEmoji != "" {
		next := removeReactionTokenFromText(text, uStart, uEnd)
		return reactionCandidate{
			Emoji:    uEmoji,
			Consumed: text[uStart:uEnd],
		}, next, true
	}

	// Fallback to first custom tg-emoji candidate.
	if len(customAll) > 0 {
		custom := customAll[0]
		customStart := custom[0]
		customEnd := custom[1]
		customID := ""
		customFallback := "🙂"
		if idIdx := tgEmojiAnyWithIDRe.SubexpIndex("id"); idIdx >= 0 {
			from, to := custom[idIdx*2], custom[idIdx*2+1]
			if from >= 0 && to >= from {
				customID = strings.TrimSpace(text[from:to])
			}
		}
		if fIdx := tgEmojiAnyWithIDRe.SubexpIndex("fallback"); fIdx >= 0 {
			from, to := custom[fIdx*2], custom[fIdx*2+1]
			if from >= 0 && to >= from {
				if f := strings.TrimSpace(text[from:to]); f != "" {
					customFallback = f
				}
			}
		}
		next := removeReactionTokenFromText(text, customStart, customEnd)
		return reactionCandidate{
			Emoji:         customFallback,
			CustomEmojiID: customID,
			Consumed:      text[customStart:customEnd],
		}, next, true
	}
	return reactionCandidate{}, text, false
}

func findFirstUnicodeEmojiOutsideRanges(s string, ranges [][2]int) (start int, end int, emoji string) {
	if len(ranges) == 0 {
		return findFirstUnicodeEmojiInText(s)
	}
	inRange := func(i int) bool {
		for _, r := range ranges {
			if i >= r[0] && i < r[1] {
				return true
			}
		}
		return false
	}
	for i := 0; i < len(s); {
		if inRange(i) {
			i++
			continue
		}
		st, en, em := findFirstUnicodeEmojiInText(s[i:])
		if st < 0 || em == "" {
			return -1, -1, ""
		}
		absStart := i + st
		absEnd := i + en
		if !inRange(absStart) {
			return absStart, absEnd, em
		}
		i = absEnd
	}
	return -1, -1, ""
}

func extractLeadingUnicodeEmoji(s string) (string, string) {
	if s == "" {
		return "", ""
	}
	r, size := utf8.DecodeRuneInString(s)
	if r == utf8.RuneError || size <= 0 {
		return "", ""
	}
	if isKeycapStarterRune(r) {
		consumed := size
		if consumed < len(s) {
			r2, s2 := utf8.DecodeRuneInString(s[consumed:])
			if r2 == 0xFE0F {
				consumed += s2
			}
		}
		if consumed < len(s) {
			r3, s3 := utf8.DecodeRuneInString(s[consumed:])
			if r3 == 0x20E3 {
				consumed += s3
				return s[:consumed], s[:consumed]
			}
		}
		return "", ""
	}
	if !isLikelyEmojiRune(r) {
		return "", ""
	}
	end := size
	prevZWJ := false
	for end < len(s) {
		nr, ns := utf8.DecodeRuneInString(s[end:])
		if nr == utf8.RuneError || ns <= 0 {
			break
		}
		if isEmojiContinuationRune(nr) {
			end += ns
			prevZWJ = nr == 0x200D
			continue
		}
		if prevZWJ && isLikelyEmojiRune(nr) {
			end += ns
			prevZWJ = false
			continue
		}
		break
	}
	return s[:end], s[:end]
}

func findFirstUnicodeEmojiInText(s string) (start int, end int, emoji string) {
	for i := 0; i < len(s); {
		r, sz := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError || sz <= 0 {
			i++
			continue
		}
		e, consumed := extractLeadingUnicodeEmoji(s[i:])
		if e != "" && consumed != "" {
			return i, i + len(consumed), e
		}
		i += sz
	}
	return -1, -1, ""
}

func removeReactionTokenFromText(text string, start, end int) string {
	if start < 0 || end < start || end > len(text) {
		return text
	}
	before := text[:start]
	after := text[end:]
	if len(before) > 0 && len(after) > 0 {
		last := before[len(before)-1]
		first := after[0]
		if (last == ' ' || last == '\t') && (first == ' ' || first == '\t') {
			after = strings.TrimLeft(after, " \t")
		}
	}
	next := before + after
	if strings.TrimSpace(before) == "" {
		next = strings.TrimLeft(next, " \t")
	}
	return next
}

func isLikelyEmojiRune(r rune) bool {
	switch {
	case r >= 0x1F000 && r <= 0x1FAFF:
		return true
	case r >= 0x2600 && r <= 0x27BF:
		return true
	default:
		return false
	}
}

func isKeycapStarterRune(r rune) bool {
	return (r >= '0' && r <= '9') || r == '#' || r == '*'
}

func isEmojiContinuationRune(r rune) bool {
	switch {
	case r == 0xFE0F || r == 0xFE0E:
		return true
	case r == 0x200D:
		return true
	case r == 0x20E3:
		return true
	case r >= 0x1F3FB && r <= 0x1F3FF:
		return true
	default:
		return false
	}
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

var mdHintRe = regexp.MustCompile("(?m)(^\\s{0,3}(#{1,6}\\s+|>\\s?|[-*+]\\s+|\\d+\\.\\s+|---\\s*$)|\\*\\*|__|~~|\\|\\||`|\\[[^\\]]+\\]\\(https?://[^)]+\\))")
var mdSpoilerRe = regexp.MustCompile(`\|\|(.+?)\|\|`)
var mdUnderlineRe = regexp.MustCompile(`__([^\n]+?)__`)
var htmlHeadingRe = regexp.MustCompile(`(?is)<h[1-6][^>]*>(.*?)</h[1-6]>`)
var htmlHRRe = regexp.MustCompile(`(?i)<hr\s*/?>`)
var htmlBRRe = regexp.MustCompile(`(?i)<br\s*/?>`)
var htmlParagraphBreakRe = regexp.MustCompile(`(?is)</p>\s*<p(?:\s[^>]*)?>`)
var htmlParagraphOpenRe = regexp.MustCompile(`(?i)<p(?:\s[^>]*)?>`)
var htmlParagraphCloseRe = regexp.MustCompile(`(?i)</p>`)
var htmlListItemParagraphRe = regexp.MustCompile(`(?is)<li>\s*<p[^>]*>(.*?)</p>\s*</li>`)
var htmlListItemOpenRe = regexp.MustCompile(`(?i)<li[^>]*>`)
var htmlListItemCloseRe = regexp.MustCompile(`(?i)</li>`)
var htmlULOLRe = regexp.MustCompile(`(?i)</?(ul|ol)[^>]*>`)
var htmlStrongOpenRe = regexp.MustCompile(`(?i)<strong>`)
var htmlStrongCloseRe = regexp.MustCompile(`(?i)</strong>`)
var htmlEmOpenRe = regexp.MustCompile(`(?i)<em>`)
var htmlEmCloseRe = regexp.MustCompile(`(?i)</em>`)
var htmlDelOpenRe = regexp.MustCompile(`(?i)<del>`)
var htmlDelCloseRe = regexp.MustCompile(`(?i)</del>`)
var markdownEngine = goldmark.New(
	goldmark.WithExtensions(extension.GFM),
	goldmark.WithParserOptions(parser.WithAutoHeadingID()),
	goldmark.WithRendererOptions(gmhtml.WithUnsafe()),
)

const defaultMarkdownDividerTGEmoji = `<tg-emoji emoji-id="5213083123218147891">〰️</tg-emoji>`
const voiceTranscriptionPrefix = `<tg-emoji emoji-id="5260652149469094137">🎙</tg-emoji> `
const anonMessagePrefix = `<tg-emoji emoji-id="5974347006779329639">🎭</tg-emoji> `

var ignoredAutoReplyPrefixes = []string{
	voiceTranscriptionPrefix,
	anonMessagePrefix,
}

func containsMarkdownLiteMarkup(s string) bool {
	if strings.TrimSpace(s) == "" {
		return false
	}
	return mdHintRe.MatchString(s)
}

func markdownToTelegramHTMLLite(s string) string {
	if strings.TrimSpace(s) == "" {
		return s
	}
	// Custom Telegram-only markdown extensions that stock markdown doesn't support.
	s = mdSpoilerRe.ReplaceAllString(s, `<tg-spoiler>$1</tg-spoiler>`)
	s = mdUnderlineRe.ReplaceAllString(s, `<u>$1</u>`)

	var b bytes.Buffer
	if err := markdownEngine.Convert([]byte(s), &b); err != nil {
		return s
	}
	s = strings.TrimSpace(b.String())
	divider := strings.Repeat(markdownDividerTGEmoji(), 11)
	s = htmlHeadingRe.ReplaceAllStringFunc(s, func(m string) string {
		sub := htmlHeadingRe.FindStringSubmatch(m)
		if len(sub) < 2 {
			return m
		}
		title := strings.TrimSpace(html.UnescapeString(sub[1]))
		if title == "" {
			return ""
		}
		return "<b>§ " + html.EscapeString(title) + "</b>"
	})
	s = htmlHRRe.ReplaceAllString(s, divider)
	s = htmlListItemParagraphRe.ReplaceAllString(s, `<li>$1</li>`)
	s = htmlULOLRe.ReplaceAllString(s, "")
	// Telegram HTML doesn't support <ul>/<ol>/<li>. Convert list tags into plain-text bullets.
	// Replace opening/closing tags independently to avoid broken leftovers on nested lists.
	s = htmlListItemOpenRe.ReplaceAllString(s, "• ")
	s = htmlListItemCloseRe.ReplaceAllString(s, "\n")
	s = htmlParagraphBreakRe.ReplaceAllString(s, "\n\n")
	s = htmlParagraphOpenRe.ReplaceAllString(s, "")
	s = htmlParagraphCloseRe.ReplaceAllString(s, "")
	s = htmlBRRe.ReplaceAllString(s, "\n")
	s = htmlStrongOpenRe.ReplaceAllString(s, "<b>")
	s = htmlStrongCloseRe.ReplaceAllString(s, "</b>")
	s = htmlEmOpenRe.ReplaceAllString(s, "<i>")
	s = htmlEmCloseRe.ReplaceAllString(s, "</i>")
	s = htmlDelOpenRe.ReplaceAllString(s, "<s>")
	s = htmlDelCloseRe.ReplaceAllString(s, "</s>")
	s = strings.ReplaceAll(s, "<blockquote>\n", "<blockquote>")
	s = strings.ReplaceAll(s, "\n</blockquote>", "</blockquote>")
	s = strings.TrimSpace(s)
	return s
}

func markdownDividerTGEmoji() string {
	v := strings.TrimSpace(os.Getenv("GPT_MARKDOWN_DIVIDER_EMOJI"))
	if v == "" {
		return defaultMarkdownDividerTGEmoji
	}
	return v
}

func hasIgnoredAutoReplyPrefix(s string) bool {
	v := strings.TrimSpace(s)
	if v == "" {
		return false
	}
	vFallback := strings.TrimSpace(replaceTGEmojiTagsWithFallback(v))
	for _, prefix := range ignoredAutoReplyPrefixes {
		p := strings.TrimSpace(prefix)
		if p == "" {
			continue
		}
		pFallback := strings.TrimSpace(replaceTGEmojiTagsWithFallback(p))
		if strings.HasPrefix(v, p) || (pFallback != "" && strings.HasPrefix(v, pFallback)) {
			return true
		}
		if vFallback != "" && (strings.HasPrefix(vFallback, p) || (pFallback != "" && strings.HasPrefix(vFallback, pFallback))) {
			return true
		}
	}
	return false
}

func hasIgnoredAutoReplyLeadingToken(msg *tgbotapi.Message) bool {
	if msg == nil {
		return false
	}
	text := firstNonEmptyUserText(msg)
	if strings.TrimSpace(text) == "" {
		return false
	}
	if startsWithCustomEmojiEntity(msg) {
		return true
	}
	trimmed := strings.TrimLeftFunc(text, unicode.IsSpace)
	if loc := tgEmojiAnyWithIDRe.FindStringIndex(trimmed); loc != nil && loc[0] == 0 {
		return true
	}
	if emoji, _ := extractLeadingUnicodeEmoji(trimmed); emoji != "" {
		return true
	}
	return hasIgnoredAutoReplyPrefix(text)
}

func startsWithCustomEmojiEntity(msg *tgbotapi.Message) bool {
	if msg == nil {
		return false
	}
	text := msg.Text
	entities := msg.Entities
	if strings.TrimSpace(msg.Text) == "" && strings.TrimSpace(msg.Caption) != "" {
		text = msg.Caption
		entities = msg.CaptionEntities
	}
	if strings.TrimSpace(text) == "" {
		return false
	}
	if len(entities) == 0 {
		return false
	}
	offset := leadingNonSpaceUTF16Offset(text)
	for _, ent := range entities {
		if ent.Type == "custom_emoji" && ent.Offset == offset && ent.Length > 0 {
			return true
		}
	}
	return false
}

func leadingNonSpaceUTF16Offset(s string) int {
	offset := 0
	for _, r := range s {
		if !unicode.IsSpace(r) {
			return offset
		}
		if r > 0xFFFF {
			offset += 2
		} else {
			offset++
		}
	}
	return offset
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
	isAdmin := fetchChatAdminStatus(bot, chatID, userID)
	c.setCached(chatID, userID, isAdmin, now)
	if c.store != nil {
		_ = c.store.UpsertChatAdminCache(chatID, userID, isAdmin, now.Unix())
	}
	return isAdmin
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
	admins, err := bot.GetChatAdministrators(tgbotapi.ChatAdministratorsConfig{
		ChatConfig: tgbotapi.ChatConfig{ChatID: chatID},
	})
	if err != nil {
		return 0, err
	}
	if err := c.ClearChat(chatID); err != nil {
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

func cleanupTriggerBotTmpStartup() {
	if !envBool("TRIGGER_BOT_TMP_CLEAN_ON_START", true) {
		return
	}
	maxAgeSec := envInt("TRIGGER_BOT_TMP_MAX_AGE_SEC", 24*60*60)
	if maxAgeSec < 3600 {
		maxAgeSec = 3600
	}
	maxAge := time.Duration(maxAgeSec) * time.Second
	removed, err := bottmp.CleanupStale(maxAge)
	if err != nil {
		log.Printf("tmp startup cleanup failed dir=%s max_age=%s err=%v", bottmp.BaseDir(), maxAge, err)
		return
	}
	if removed > 0 || debugTriggerLogEnabled {
		log.Printf("tmp startup cleanup ok removed=%d max_age=%s dir=%s", removed, maxAge, bottmp.BaseDir())
	}
}

func setupAppLogFile() {
	logPath := strings.TrimSpace(os.Getenv("TRIGGER_BOT_LOG_FILE"))
	if logPath == "" {
		logPath = filepath.Join("var", "log", "trigger_admin_bot.log")
	}
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		log.Printf("app log mkdir failed path=%q err=%v", logPath, err)
		return
	}
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		log.Printf("app log open failed path=%q err=%v", logPath, err)
		return
	}
	appLogFile = f
	log.SetOutput(io.MultiWriter(os.Stderr, f))
	log.Printf("app log file enabled path=%s", logPath)
}

func Run() {
	setupAppLogFile()
	token := strings.TrimSpace(os.Getenv("TELEGRAM_BOT_TOKEN"))
	if token == "" {
		log.Fatal("TELEGRAM_BOT_TOKEN is required")
	}
	cleanupTriggerBotTmpStartup()

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

	apiEndpoint := strings.TrimSpace(os.Getenv("TELEGRAM_BOT_API_ENDPOINT"))
	bot, err := func() (*tgbotapi.BotAPI, error) {
		if apiEndpoint == "" {
			return tgbotapi.NewBotAPI(token)
		}
		return tgbotapi.NewBotAPIWithAPIEndpoint(token, apiEndpoint)
	}()
	if err != nil {
		log.Fatalf("create bot failed: %v", err)
	}
	if apiEndpoint != "" {
		log.Printf("telegram bot api endpoint: %s", apiEndpoint)
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
	spotifyCookiesFile := strings.TrimSpace(os.Getenv("YTDLP_COOKIES_FILE"))
	if spotifyCookiesFile == "" {
		spotifyCookiesFile = resolveDefaultCookiesFile()
	}
	spotifyDownloader := spotifymusic.Downloader{
		YTDLPBin:           strings.TrimSpace(os.Getenv("YTDLP_BIN")),
		ProxySocks:         "",
		AudioFormat:        strings.TrimSpace(os.Getenv("AUDIO_FORMAT")),
		AudioQuality:       strings.TrimSpace(os.Getenv("AUDIO_QUALITY")),
		ExtractorArgs:      strings.TrimSpace(os.Getenv("YTDLP_EXTRACTOR_ARGS")),
		CookiesFile:        spotifyCookiesFile,
		CookiesFromBrowser: strings.TrimSpace(os.Getenv("YTDLP_COOKIES_FROM_BROWSER")),
	}
	yandexDownloader := yandexmusic.Downloader{
		Token:         strings.TrimSpace(firstNonEmptyEnv("YA_MUSIC_TOKEN", "YANDEX_MUSIC_TOKEN")),
		Quality:       envInt("YANDEX_MUSIC_QUALITY", 1),
		TimeoutSec:    envInt("YANDEX_MUSIC_TIMEOUT_SEC", 45),
		Tries:         envInt("YANDEX_MUSIC_TRIES", 6),
		RetryDelaySec: envInt("YANDEX_MUSIC_RETRY_DELAY_SEC", 2),
		FFmpegBin:     strings.TrimSpace(firstNonEmptyEnv("YANDEX_MUSIC_FFMPEG_BIN", "FFMPEG_BIN")),
		ForceMP3:      envBool("YANDEX_MUSIC_FORCE_MP3", true),
		EmbedLyrics:   envBool("YANDEX_MUSIC_EMBED_LYRICS", true),
	}
	var vkDownloader VKMusicDownloadPort
	vkUserAgent := strings.TrimSpace(os.Getenv("VK_USER_AGENT"))
	vkToken := strings.TrimSpace(os.Getenv("VK_TOKEN"))
	vkCookiesFile := strings.TrimSpace(firstNonEmptyEnv("VK_COOKIES_FILE", "YTDLP_COOKIES_FILE"))
	if vkToken != "" || vkCookiesFile != "" {
		vkDL, err := vkaudio.NewDownloader(vkToken, vkUserAgent)
		if err != nil {
			log.Printf("vk music client init failed: %v", err)
		} else {
			vkDL.CookiesFile = vkCookiesFile
			vkDL.ProxyURL = strings.TrimSpace(firstNonEmptyEnv("VK_PROXY_URL", "FIXIE_SOCKS_HOST"))
			vkDL.WebUserID = envInt("VK_WEB_USER_ID", 0)
			vkDL.FFmpegBin = strings.TrimSpace(firstNonEmptyEnv("VK_AUDIO_FFMPEG_BIN", "FFMPEG_BIN"))
			vkDL.MaxSizeMB = envInt("VK_AUDIO_MAX_MB", 60)
			vkDL.TimeoutSec = envInt("VK_AUDIO_FFMPEG_TIMEOUT_SEC", 120)
			vkDL.RetryCount = envInt("VK_AUDIO_RETRY_COUNT", 3)
			vkDL.RetryDelayMs = envInt("VK_AUDIO_RETRY_DELAY_MS", 500)
			vkDownloader = vkDL
			log.Printf("vk music client enabled api=%t web=%t", vkToken != "", vkCookiesFile != "")
		}
	} else {
		log.Printf("vk music client disabled: set VK_COOKIES_FILE")
	}
	telegramUploadMaxMB := envInt("TELEGRAM_UPLOAD_MAX_MB", 50)
	if telegramUploadMaxMB <= 0 {
		telegramUploadMaxMB = 50
	}
	mediaMaxMB := envInt("MEDIA_DOWNLOAD_MAX_MB", telegramUploadMaxMB)
	if mediaMaxMB <= 0 {
		mediaMaxMB = telegramUploadMaxMB
	}
	mediaCookiesFile := strings.TrimSpace(os.Getenv("YTDLP_COOKIES_FILE"))
	if mediaCookiesFile == "" {
		mediaCookiesFile = resolveDefaultCookiesFile()
	}
	mediaDownloader := mediadl.Downloader{
		YTDLPBin:            strings.TrimSpace(os.Getenv("YTDLP_BIN")),
		ProxySocks:          strings.TrimSpace(os.Getenv("FIXIE_SOCKS_HOST")),
		AudioFormat:         strings.TrimSpace(os.Getenv("AUDIO_FORMAT")),
		AudioQuality:        strings.TrimSpace(os.Getenv("AUDIO_QUALITY")),
		ExtractorArgs:       strings.TrimSpace(os.Getenv("YTDLP_EXTRACTOR_ARGS")),
		CookiesFile:         mediaCookiesFile,
		CookiesFromBrowser:  strings.TrimSpace(os.Getenv("YTDLP_COOKIES_FROM_BROWSER")),
		MaxSizeMB:           mediaMaxMB,
		MaxHeight:           envInt("MEDIA_DOWNLOAD_MAX_HEIGHT", 720),
		MaxVideoDurationSec: envInt("MEDIA_VIDEO_MAX_DURATION_SEC", 1800),
	}
	mediaInteractive := envBool("MEDIA_DOWNLOAD_INTERACTIVE", true)
	spotifyQueue := newSpotifyPickQueue(envInt("SPOTIFY_AUDIO_WORKERS", 1), envInt("SPOTIFY_AUDIO_QUEUE", 8))
	yandexMusicQueue := newYandexMusicQueue(envInt("YANDEX_MUSIC_WORKERS", 1), envInt("YANDEX_MUSIC_QUEUE", 4))
	vkMusicQueue := newVKMusicQueue(envInt("VK_AUDIO_WORKERS", 1), envInt("VK_AUDIO_QUEUE", 4))
	mediaQueue := newMediaDownloadQueue(envInt("MEDIA_DOWNLOAD_WORKERS", 1), envInt("MEDIA_DOWNLOAD_QUEUE", 8))
	voiceTranslateQueue := newVoiceTranslateQueue(envInt("VOICE_TRANSLATE_WORKERS", 1), envInt("VOICE_TRANSLATE_QUEUE", 4))
	chatErrorLogEnabled = envBool("CHAT_ERROR_LOG", true)
	debugTriggerLogEnabled = envBool("DEBUG_TRIGGER_LOG", false)
	debugGPTLogEnabled = envBool("DEBUG_GPT_LOG", false)
	logTextClipMax = envInt("LOG_TEXT_CLIP_CHARS", 200)
	if logTextClipMax < 0 {
		logTextClipMax = 200
	}
	userGPTTokenLimit := userGPTTokenLimitFromEnv()
	userGPTTokenLowThreshold := userGPTTokenLimitLowThreshold(userGPTTokenLimit)
	if userGPTTokenLimit > 0 {
		log.Printf("per-user GPT token limit enabled: %d tokens per 4h window (UTC), low warning threshold=%d", userGPTTokenLimit, userGPTTokenLowThreshold)
	} else {
		log.Printf("per-user GPT token limit disabled")
	}
	log.Printf("Bot started as @%s", bot.Self.UserName)

	allowedChats, err := chataccess.ParseAllowedChatIDs(os.Getenv("ALLOWED_CHAT_IDS"))
	if err != nil {
		log.Fatalf("ALLOWED_CHAT_IDS parse failed: %v", err)
	}
	if allowedChats.Enabled() {
		ids := make([]string, 0, len(allowedChats.IDs()))
		for _, id := range allowedChats.IDs() {
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
	moderationConfirms := newModerationConfirmManager(time.Duration(envInt("MOD_CONFIRM_TTL_SEC", 600)) * time.Second)
	clearChatConfirms := newClearChatConfirmManager(time.Duration(envInt("CLEAR_CHAT_CONFIRM_TTL_SEC", 600)) * time.Second)
	clearChatService := chatclear.NewServiceFromEnv()
	mtprotoSetup := newMTProtoSetupManager(time.Duration(envInt("MTPROTO_SETUP_TTL_SEC", 1200)) * time.Second)
	setMTProtoSetupVisible(clearChatService != nil && clearChatService.Available(context.Background()))
	chatRecent := newChatRecentStore(envInt("CHAT_RECENT_MAX_MESSAGES", 8), time.Duration(envInt("CHAT_RECENT_MAX_AGE_SEC", 1800))*time.Second)
	quoteHistory := newQuoteStickerHistory(envInt("QS_HISTORY_MAX_MESSAGES", 1000))
	qsSessionTTLSec := envInt("QS_SESSION_TTL_SEC", int(quoteStickerSessionTTL/time.Second))
	if qsSessionTTLSec < 300 {
		qsSessionTTLSec = 300
	}
	quoteSessions := newQuoteStickerSessionManager(time.Duration(qsSessionTTLSec) * time.Second)
	setOutgoingChatRecentStore(chatRecent, bot.Self.FirstName)
	setChatContextResolver(func(chatID int64, limit int) string {
		return chatRecent.RecentText(chatID, limit)
	})
	setChatSummaryResolver(nil)
	defer func() {
		setOutgoingChatRecentStore(nil, "")
		setChatContextResolver(nil)
		setChatSummaryResolver(nil)
	}()
	disallowedNotifier := chataccess.NewDisallowedChatNotifier(time.Duration(envInt("DISALLOWED_CHAT_NOTICE_TTL_SEC", 600)) * time.Second)
	portraitManager := newParticipantPortraitManager(store)
	botPortraits := newBotPortraitManager(store, bot.Self.ID)
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
	if botPortraits != nil {
		setBotPortraitResolver(func(chatID int64) string {
			return botPortraits.Portrait(chatID)
		})
		setOutgoingBotPortraitObserver(func(chatID int64, text string) {
			botPortraits.ObserveOutgoing(chatID, text)
		})
		defer func() {
			setBotPortraitResolver(nil)
			setOutgoingBotPortraitObserver(nil)
			botPortraits.Close()
		}()
	}
	idleTracker := trigger.NewIdleTracker()
	actionQueue := newTriggerActionQueue(defaultTriggerActionWorkers(), defaultTriggerActionQueueSize())

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
	} else if strings.TrimSpace(os.Getenv("VOICE_TRANSLATE_PUBLIC_BASE_URL")) != "" {
		tmpRoutes := http.NewServeMux()
		tmpRoutes.HandleFunc("/trigger_bot/tmp/", NewWebAdmin(nil, "").voiceTranslateTempFile)
		go func() {
			log.Printf("Voice translate temp file server listening on %s", adminBind)
			if err := http.ListenAndServe(adminBind, tmpRoutes); err != nil {
				log.Printf("voice translate temp file server stopped: %v", err)
			}
		}()
	}
	go runScheduledUnmutes(bot, store)
	startBotCommandsSyncLoop(bot)

	startedAtUnix := time.Now().Unix()
	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60
	u.AllowedUpdates = []string{"message", "edited_message", "inline_query", "callback_query", "chat_member", "my_chat_member", "message_reaction", "message_reaction_count"}
	if envBool("TELEGRAM_DROP_PENDING_UPDATES_ON_START", true) {
		nextOffset, count, err := discardPendingUpdatesWithEmojiMeta(bot, u)
		if err != nil {
			log.Printf("drop pending updates on start failed: %v", err)
		} else if nextOffset > 0 {
			u.Offset = nextOffset
			log.Printf("drop pending updates on start ok count=%d next_offset=%d", count, nextOffset)
		} else {
			log.Printf("drop pending updates on start ok count=0")
		}
	}
	updates := getUpdatesChanWithEmojiMeta(bot, u)
	triggerEngine := engine.NewTriggerEngine()
	templateLookup := buildTemplateLookup(store)
	handlerDeps := triggerHandlerDeps{
		ActionQueue: actionQueue,
		triggerActionDeps: triggerActionDeps{
			Bot:               bot,
			Store:             store,
			IdleTracker:       idleTracker,
			Portraits:         portraitManager,
			SpotifyMusic:      spotifyMusicClient,
			SpotifyDownloader: spotifyDownloader,
			SpotifyQueue:      spotifyQueue,
			YandexDownloader:  yandexDownloader,
			YandexQueue:       yandexMusicQueue,
			VKDownloader:      vkDownloader,
			VKQueue:           vkMusicQueue,
			SoundCloudSearch:  mediaDownloader,
			MediaDownloader:   mediaDownloader,
			MediaQueue:        mediaQueue,
			MediaInteractive:  mediaInteractive,
			TemplateLookup:    templateLookup,
			RecordGPTTokens: func(userID int64, tokens int, now time.Time) (int, bool, error) {
				if userGPTTokenLimit <= 0 || userID == 0 || tokens <= 0 {
					return 0, false, nil
				}
				before, err := store.UserGPTTokensRemaining(userID, now, userGPTTokenLimit)
				if err != nil {
					return 0, false, err
				}
				remaining, err := store.AddUserGPTTokens(userID, now, tokens, userGPTTokenLimit)
				if err != nil {
					return 0, false, err
				}
				crossedLow := userGPTTokenLowThreshold > 0 && before > userGPTTokenLowThreshold && remaining <= userGPTTokenLowThreshold
				return remaining, crossedLow, nil
			},
		},
		Allowed:    allowedChats,
		Engine:     triggerEngine,
		Store:      store,
		AdminCache: adminCache,
		ChatRecent: chatRecent,
	}

	for update := range updates {
		if isStartupBacklogRawMember(update.RawChatMember, startedAtUnix) {
			if debugTriggerLogEnabled {
				log.Printf("skip startup backlog chat_member update date=%d", update.RawChatMember.Date)
			}
			continue
		}
		if isStartupBacklogRawMember(update.RawMyChatMember, startedAtUnix) {
			if debugTriggerLogEnabled {
				log.Printf("skip startup backlog my_chat_member update date=%d", update.RawMyChatMember.Date)
			}
			continue
		}
		if isStartupBacklogReactionCount(update.RawMessageReactionCount, startedAtUnix) {
			if debugTriggerLogEnabled {
				log.Printf("skip startup backlog message_reaction_count update date=%d", update.RawMessageReactionCount.Date)
			}
			continue
		}
		if isStartupBacklogMessageReaction(update.RawMessageReaction, startedAtUnix) {
			if debugTriggerLogEnabled {
				log.Printf("skip startup backlog message_reaction update date=%d", update.RawMessageReaction.Date)
			}
			continue
		}
		if update.RawChatMember != nil {
			handleNewMemberUpdate(handlerDeps, update.RawChatMember)
		}
		if update.RawMyChatMember != nil {
			handleDisallowedMyChatMemberNotice(bot, allowedChats, disallowedNotifier, update.RawMyChatMember)
			handleNewMemberUpdate(handlerDeps, update.RawMyChatMember)
		}
		if update.RawMessageReactionCount != nil {
			handleReactionCountUpdate(handlerDeps, update.RawMessageReactionCount)
			continue
		}
		if update.RawMessageReaction != nil {
			handleMessageReactionUpdate(handlerDeps, update.RawMessageReaction)
			continue
		}
		if update.Update.InlineQuery != nil {
			handleRoleplayInlineQuery(bot, update.Update.InlineQuery)
			continue
		}
		if update.Update.CallbackQuery != nil {
			if handleSetMTProtoCallback(bot, mtprotoSetup, update.Update.CallbackQuery) {
				continue
			}
			if handleClearChatCallback(
				bot,
				adminCache,
				clearChatConfirms,
				clearChatService,
				update.Update.CallbackQuery,
			) {
				continue
			}
			if handleModerationConfirmCallback(
				bot,
				adminCache,
				userIndex,
				store,
				moderationConfirms,
				update.Update.CallbackQuery,
			) {
				continue
			}
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
						VKDownloader:      vkDownloader,
						VKQueue:           vkMusicQueue,
						SoundCloudSearch:  mediaDownloader,
						MediaDownloader:   mediaDownloader,
						MediaQueue:        mediaQueue,
						IdleTracker:       idleTracker,
					}, req, provider)
				},
			) {
				continue
			}
			if handleYandexTop10Callback(
				bot,
				update.Update.CallbackQuery,
				yandexDownloader,
				idleTracker,
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
					if strings.EqualFold(strings.TrimSpace(req.Provider), "vk") {
						task := vkMusicTask{
							SendCtx:  sendContext{Bot: bot, ChatID: req.ChatID, ReplyTo: req.ReplyTo},
							TrackID:  strings.TrimSpace(req.TrackID),
							DL:       vkDownloader,
							ReportTo: req.ChatID,
						}
						if vkMusicQueue == nil || !vkMusicQueue.enqueue(task) {
							return errors.New("vk music queue is full")
						}
						return nil
					}
					if strings.EqualFold(strings.TrimSpace(req.Provider), "soundcloud") {
						targetURL := strings.TrimSpace(req.SourceURL)
						if targetURL == "" {
							return errors.New("empty soundcloud track url")
						}
						task := mediaDownloadTask{
							SendCtx:  sendContext{Bot: bot, ChatID: req.ChatID, ReplyTo: req.ReplyTo},
							URL:      targetURL,
							Mode:     mediadl.ModeAudio,
							DL:       mediaDownloader,
							ReportTo: req.ChatID,
						}
						if mediaQueue == nil || !mediaQueue.enqueue(task) {
							return errors.New("media download queue is full")
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
				func(ctx context.Context, req mediadl.ChoiceRequest, mode mediadl.Mode) error {
					_ = ctx
					log.Printf("media choice selected chat=%d user=%d mode=%s url=%q", req.ChatID, req.UserID, mode, clipText(req.URL, 220))
					task := mediaDownloadTask{
						SendCtx:      sendContext{Bot: bot, ChatID: req.ChatID, ReplyTo: req.ReplyTo},
						URL:          req.URL,
						Mode:         mode,
						DL:           mediaDownloader,
						SourceMsgID:  req.SourceMsgID,
						DeleteSource: req.DeleteSource,
						ReportTo:     req.ChatID,
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
			if handleVoiceTranslateOptionCallback(bot, update.Update.CallbackQuery, voiceTranslateQueue) {
				continue
			}
			if handleQuoteStickerCallback(bot, quoteSessions, quoteHistory, update.Update.CallbackQuery) {
				continue
			}
			if handleRoleplayCallback(bot, update.Update.CallbackQuery) {
				continue
			}
		}
		if update.Update.EditedMessage != nil {
			if isStartupBacklogMessage(update.Update.EditedMessage, startedAtUnix) {
				if debugTriggerLogEnabled {
					log.Printf("skip startup backlog edited message chat=%d msg=%d date=%d",
						update.Update.EditedMessage.Chat.ID, update.Update.EditedMessage.MessageID, update.Update.EditedMessage.Date)
				}
				continue
			}
			trackMessageRevision(update.Update.EditedMessage)
			if debugTriggerLogEnabled {
				em := update.Update.EditedMessage
				log.Printf("edited message tracked chat=%d msg=%d text=%q", em.Chat.ID, em.MessageID, clipLogText(firstNonEmptyUserText(em), 200))
			}
			continue
		}

		if update.Update.Message == nil {
			continue
		}
		msg := update.Update.Message
		if isStartupBacklogMessage(msg, startedAtUnix) {
			if debugTriggerLogEnabled {
				chatID := int64(0)
				if msg.Chat != nil {
					chatID = msg.Chat.ID
				}
				log.Printf("skip startup backlog message chat=%d msg=%d date=%d", chatID, msg.MessageID, msg.Date)
			}
			continue
		}
		trackMessageRevision(msg)
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
		quoteHistory.Add(msg, rawMsg)
		isPrivateChat := msg.Chat.IsPrivate()
		if !isPrivateChat && !allowedChats.Allows(msg.Chat.ID) {
			now := time.Now()
			if disallowedNotifier.ShouldNotify(msg.Chat.ID, now) {
				if err := notifyDisallowedChat(bot, msg.Chat.ID); err == nil {
					disallowedNotifier.MarkNotified(msg.Chat.ID, now)
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

		if handleRoleplayInlineSentMessage(bot, msg) {
			continue
		}

		if handleModerationCommand(moderationContext{
			Bot:        bot,
			AdminCache: adminCache,
			UserIndex:  userIndex,
			Readonly:   readonly,
			Store:      store,
			Confirms:   moderationConfirms,
		}, msg, strings.TrimSpace(msg.Text)) {
			continue
		}

		cmdSendCtx := sendContext{Bot: bot, ChatID: msg.Chat.ID}
		if msg.IsCommand() {
			cmd := msg.Command()
			switch cmd {
			case cmdStart, cmdHelp:
				s := ""
				if isPrivateChat {
					commands := []string{
						cmdStart, cmdHelp, cmdEmojiID, cmdStickerID, cmdGifID, cmdQuoteSticker, cmdQuoteDelete,
						cmdSpotifySearch, cmdMyPortrait, cmdDeleteMyPortrait, cmdAnon,
						cmdTranslateVoice, cmdRoleplay, cmdBan, cmdUnban, cmdMute, cmdUnmute, cmdKick,
						cmdReadonly, cmdReloadAdmins,
					}
					s = "Триггер-бот активен.\n\n" +
						"Админка: /trigger_bot\n" +
						"Команды: /" + strings.Join(commands, " /") + "\n" +
						"Мод-команды: !ban/ban !unban/unban !mute/mute !unmute/unmute !kick/kick !readonly/readonly !reload_admins/reload_admins (+ тихие !sban/sban !smute/smute !skick/skick)\n\n" +
						"Теги для ChatGPT-промпта:\n" +
						"{{message}} / {{user_text}} — текст сообщения\n" +
						"{{user_id}}, {{user_first_name}}, {{user_username}}\n" +
						"{{user_display_name}}, {{user_label}}\n" +
						"{{user_portrait}}\n" +
						"{{user_portrait_remaining}}\n" +
						"{{bot_portrait}}\n" +
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
						fmt.Sprintf("— команда /%s\n", cmdEmojiID) +
						"— или просто отправьте кастомный emoji в личку боту."
				} else {
					triggerInfo := "Триггеры: список временно недоступен."
					featureInfo := "Что умею:\n— выполнять триггеры, настроенные админами"
					usageInfo := fmt.Sprintf("Как пользоваться:\n— /%s — показать ID кастомного эмодзи", cmdEmojiID)
					if items, err := store.ListTriggers(); err == nil {
						enabled := make([]string, 0, len(items))
						hasSpotify := false
						hasUnifiedMusic := false
						hasYandexMusic := false
						hasVKMusic := false
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
							if it.ActionType == ActionTypeSpotifyMusic {
								hasSpotify = true
							}
							if it.ActionType == ActionTypeMusic {
								hasUnifiedMusic = true
							}
							if it.ActionType == ActionTypeYandexMusic {
								hasYandexMusic = true
							}
							if it.ActionType == ActionTypeVKMusic {
								hasVKMusic = true
							}
							if it.ActionType == ActionTypeMediaX {
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
							featureLines = append(featureLines, "— искать музыку с выбором сервиса: Spotify, Яндекс.Музыка, VK или SoundCloud")
						} else if hasSpotify {
							featureLines = append(featureLines, "— искать и скачивать музыку Spotify")
						}
						if hasYandexMusic {
							featureLines = append(featureLines, "— скачивать музыку из Яндекс.Музыки по ссылке")
						}
						if hasVKMusic {
							featureLines = append(featureLines, "— искать и скачивать музыку VK")
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
							usageLines = append(usageLines, fmt.Sprintf("— для Spotify: /%s <запрос>", cmdSpotifySearch))
						}
						if hasYandexMusic {
							usageLines = append(usageLines, "— для Яндекс.Музыки: отправьте ссылку music.yandex.ru")
						}
						usageLines = append(usageLines, fmt.Sprintf("— /%s — показать ваш портрет", cmdMyPortrait))
						usageLines = append(usageLines, fmt.Sprintf("— /%s — удалить ваш портрет", cmdDeleteMyPortrait))
						usageLines = append(usageLines, fmt.Sprintf("— если нужен ID кастомного эмодзи: /%s", cmdEmojiID))
						usageLines = append(usageLines, fmt.Sprintf("— если нужен код стикера: отправьте /%s в ответ на стикер", cmdStickerID))
						usageLines = append(usageLines, fmt.Sprintf("— если нужен ID гифки: отправьте /%s в ответ на гифку", cmdGifID))
						usageLines = append(usageLines, fmt.Sprintf("— /%s [N] — сделать quote-стикер (по reply или сообщению выше)", cmdQuoteSticker))
						usageLines = append(usageLines, fmt.Sprintf("— /%s — удалить стикер из стикерпака (по reply или сообщению выше)", cmdQuoteDelete))
						usageLines = append(usageLines, fmt.Sprintf("— /%s — roleplay-действие по reply", cmdRoleplay))
						usageInfo = strings.Join(usageLines, "\n")
					}
					s = "Привет! Я тут, чтобы помогать с музыкой и автоматизацией чата.\n\n" +
						featureInfo + "\n\n" +
						usageInfo + "\n\n" +
						triggerInfo
				}
				reply(cmdSendCtx.WithReply(msg.MessageID), s, false)
				continue
			case cmdEmojiID, cmdEmojiIDAlias:
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
			case cmdStickerID, cmdStickerIDAlias:
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
			case cmdGifID, cmdGifIDAlias:
				animationHit, animationOK := extractAnimationCode(msg)
				if !animationOK && msg != nil && msg.ReplyToMessage != nil {
					animationHit, animationOK = extractAnimationCode(msg.ReplyToMessage)
				}
				if !animationOK {
					reply(cmdSendCtx.WithReply(msg.MessageID), "Гифка не найдена. Отправьте гифку или ответьте этой командой на гифку.", false)
					continue
				}
				reply(cmdSendCtx.WithReply(msg.MessageID), buildAnimationReplyText(animationHit), false)
				continue
			case cmdQuoteSticker, cmdQuoteStickerAlias:
				handled, errText, st := handleQuoteStickerCommand(bot, quoteSessions, quoteHistory, msg)
				if handled {
					if errText != "" {
						reply(cmdSendCtx.WithReply(msg.MessageID), errText, false)
						continue
					}
					if st == nil {
						reply(cmdSendCtx.WithReply(msg.MessageID), "Не удалось создать сессию quote-стикера.", false)
						continue
					}
					if err := sendQuoteStickerPickerMessage(bot, msg.Chat.ID, *st); err != nil {
						reply(cmdSendCtx.WithReply(msg.MessageID), "Не удалось показать меню выбора эмодзи.", false)
						continue
					}
					_, _ = bot.Request(tgbotapi.DeleteMessageConfig{
						ChatID:    msg.Chat.ID,
						MessageID: msg.MessageID,
					})
					continue
				}
			case cmdQuoteDelete:
				handled, out := handleQuoteStickerDelete(bot, quoteHistory, msg)
				if handled {
					reply(cmdSendCtx.WithReply(msg.MessageID), out, false)
					continue
				}
			case cmdSpotifySearch, cmdSpotifySearchAlt:
				query := strings.TrimSpace(msg.CommandArguments())
				if query == "" {
					reply(cmdSendCtx.WithReply(msg.MessageID), fmt.Sprintf("Использование: /%s исполнитель или трек", cmdSpotifySearch), false)
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
			case cmdAnon:
				if handleAnonCommand(bot, msg) {
					continue
				}
			case cmdTranslateVoice:
				if msg.ReplyToMessage == nil {
					reply(cmdSendCtx.WithReply(msg.MessageID), "Использование: ответьте /translate_voice на сообщение с аудио/видео/voice.", false)
					continue
				}
				srcMediaMsg, mediaInfo, mediaSize, mediaOK := detectReplyMediaSource(msg)
				if !mediaOK {
					reply(cmdSendCtx.WithReply(msg.MessageID), "Нужен реплай на аудио/видео/voice.", false)
					continue
				}
				maxMB := envInt("VOICE_TRANSLATE_MAX_MB", 300)
				if maxMB > 0 && mediaSize > 0 && mediaSize > int64(maxMB)<<20 {
					replyToID := msg.ReplyToMessage.MessageID
					if srcMediaMsg != nil {
						replyToID = srcMediaMsg.MessageID
					}
					reply(cmdSendCtx.WithReply(replyToID), fmt.Sprintf("Файл слишком большой для перевода. Лимит: до %d МБ.", maxMB), false)
					continue
				}
				replyToID := msg.ReplyToMessage.MessageID
				if srcMediaMsg != nil {
					replyToID = srcMediaMsg.MessageID
				}
				token := putVoiceTranslateOption(voiceTranslateOptionEntry{
					chatID:  msg.Chat.ID,
					userID:  msg.From.ID,
					replyTo: replyToID,
					media:   mediaInfo,
				})
				menu := tgbotapi.NewMessage(msg.Chat.ID, "Действия с переводом:")
				menu.ReplyMarkup = renderVoiceTranslateOptionKeyboard(token, mediaInfo.HasVideo)
				if replyToID > 0 {
					menu.ReplyToMessageID = replyToID
					menu.AllowSendingWithoutReply = true
				}
				_, _ = bot.Send(menu)
				continue
			case cmdRoleplay:
				handleRoleplayCommand(bot, msg, msg.CommandArguments())
				continue
			case cmdSummary, cmdSummaryAlias:
				reply(cmdSendCtx.WithReply(msg.MessageID), "Сводка чата отключена.", false)
				continue
			case cmdClearChat:
				if handleClearChatCommand(bot, adminCache, clearChatConfirms, msg) {
					continue
				}
			case cmdSetMTProto:
				if handleSetMTProtoCommand(bot, clearChatService, mtprotoSetup, msg) {
					continue
				}
			case cmdMyPortrait, cmdMyPortraitAlias:
				if msg.From == nil || msg.From.ID == 0 {
					reply(cmdSendCtx.WithReply(msg.MessageID), "Не удалось определить пользователя.", false)
					continue
				}
				if portraitManager == nil {
					reply(cmdSendCtx.WithReply(msg.MessageID), "Портреты сейчас отключены.", false)
					continue
				}
				portrait := strings.TrimSpace(portraitManager.Portrait(msg.Chat.ID, msg.From.ID))
				remaining := portraitManager.RemainingUntilUpdate(msg.From.ID)
				if portrait == "" {
					reply(cmdSendCtx.WithReply(msg.MessageID),
						fmt.Sprintf("Портрет пока пуст. Для первого обновления нужно ещё сообщений: %d", remaining), false)
					continue
				}
				out := "Твой текущий портрет:\n" + portrait
				if remaining > 0 {
					out += fmt.Sprintf("\n\nДо следующего обновления: %d сообщений", remaining)
				}
				reply(cmdSendCtx.WithReply(msg.MessageID), out, false)
				continue
			case cmdDeleteMyPortrait, cmdDeleteMyPortrait2:
				if msg.From == nil || msg.From.ID == 0 {
					reply(cmdSendCtx.WithReply(msg.MessageID), "Не удалось определить пользователя.", false)
					continue
				}
				if portraitManager == nil {
					reply(cmdSendCtx.WithReply(msg.MessageID), "Портреты сейчас отключены.", false)
					continue
				}
				if err := portraitManager.DeletePortrait(msg.From.ID); err != nil {
					reply(cmdSendCtx.WithReply(msg.MessageID), "Ошибка удаления портрета: "+clipText(err.Error(), 200), false)
					continue
				}
				reply(cmdSendCtx.WithReply(msg.MessageID), "Портрет удалён. Начну собирать новый по следующим сообщениям.", false)
				continue
			}
		}
		if handleSetMTProtoPrivateText(bot, clearChatService, mtprotoSetup, msg) {
			continue
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
			if animationHit, ok := extractAnimationCode(msg); ok {
				reply(cmdSendCtx.WithReply(msg.MessageID), buildAnimationReplyText(animationHit), false)
				continue
			}
			if out := buildFileIDReply(extractPrivateAutoFileIDs(msg)); out != "" {
				sendHTML(cmdSendCtx.WithReply(msg.MessageID), out, false)
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
		if msg.Voice != nil && envBool("VOICE_TRANSCRIPTION_ENABLED", false) {
			durSec := msg.Voice.Duration
			if durSec >= 3 && durSec <= 180 {
				transcribed, err := transcribeTelegramVoiceMessage(bot, msg)
				if err != nil {
					log.Printf("voice transcription failed chat=%d msg=%d err=%v", msg.Chat.ID, msg.MessageID, err)
				} else if strings.TrimSpace(transcribed) != "" {
					transcribedPlain := strings.TrimSpace(transcribed)
					transcribedText := voiceTranscriptionPrefix + transcribedPlain
					sendHTML(cmdSendCtx.WithReply(msg.MessageID), transcribedText, false)
					if text == "" {
						text = transcribedPlain
					} else {
						text = text + "\n" + transcribedPlain
					}
				}
			} else {
				log.Printf("voice transcription skipped by duration chat=%d msg=%d duration=%ds", msg.Chat.ID, msg.MessageID, durSec)
			}
		}
		if text == "" {
			continue
		}
		msg = messageWithEffectiveUserText(msg, text)
		now := time.Now()
		idleTracker.Seen(msg.Chat.ID, now)
		var quotaLowWarningTrigger *Trigger
		checkGPTTokenQuota := func() bool {
			if userGPTTokenLimit <= 0 || msg.From == nil || msg.From.ID == 0 {
				return true
			}
			remaining, err := store.UserGPTTokensRemaining(msg.From.ID, now, userGPTTokenLimit)
			if err != nil {
				log.Printf("gpt user-token-limit check failed user=%d: %v", msg.From.ID, err)
				return true
			}
			if remaining > 0 {
				return true
			}
			if debugTriggerLogEnabled {
				log.Printf("gpt user-token-limit reached user=%d chat=%d limit=%d/4h", msg.From.ID, msg.Chat.ID, userGPTTokenLimit)
			}
			return false
		}

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
		isAdminAuthor := adminCache.IsChatAdmin(bot, msg.Chat.ID, msg.From.ID)
		quotaLowWarningTrigger = pickUserLimitLowWarningTrigger(items, isAdminAuthor)
		runtimeItems := filterRuntimeTriggersForMessage(bot, msg, filterRuntimeTriggers(items))
		matchedAny := false
		used := make(map[int64]struct{}, 4)

		primary := triggerEngine.Select(engine.SelectInput{
			Bot:      bot,
			Msg:      msg,
			Text:     text,
			Triggers: filterNonPassThroughTriggers(runtimeItems),
			IsAdminFn: func() bool {
				return isAdminAuthor
			},
		})
		if primary != nil {
			if primary.ActionType == ActionTypeGPTPrompt && !checkGPTTokenQuota() {
				continue
			}
			matchedAny = true
			used[primary.ID] = struct{}{}
			if debugTriggerLogEnabled {
				if response := triggerResponseDebugText(bot, msg, primary, templateLookup); response != "" {
					log.Printf("pick id=%d title=%q mode=%s action=%s pass_through=%v response=%q", primary.ID, primary.Title, primary.TriggerMode, primary.ActionType, primary.PassThrough, response)
				} else {
					log.Printf("pick id=%d title=%q mode=%s action=%s pass_through=%v", primary.ID, primary.Title, primary.TriggerMode, primary.ActionType, primary.PassThrough)
				}
			}
			enqueueTriggerAction(handlerDeps.triggerActionDeps, handlerDeps.ActionQueue, msg, primary, recentBefore, quotaLowWarningTrigger)
		}

		// Second pass: always execute all matching pass-through triggers, even if primary trigger was non-pass-through.
		for len(used) < len(runtimeItems) {
			tr := triggerEngine.Select(engine.SelectInput{
				Bot:      bot,
				Msg:      msg,
				Text:     text,
				Triggers: filterPassThroughTriggers(filterUnusedTriggers(runtimeItems, used)),
				IsAdminFn: func() bool {
					return isAdminAuthor
				},
			})
			if tr == nil {
				break
			}
			if tr.ActionType == ActionTypeGPTPrompt && !checkGPTTokenQuota() {
				matchedAny = true
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
			enqueueTriggerAction(handlerDeps.triggerActionDeps, handlerDeps.ActionQueue, msg, tr, recentBefore, quotaLowWarningTrigger)
		}
		if matchedAny {
			continue
		}
		if debugTriggerLogEnabled {
			log.Printf("no trigger matched for msg=%d", msg.MessageID)
		}
		if idleTracker != nil {
			autoTr, idleAfter := trigger.SelectIdleAutoReplyTrigger(bot, msg, runtimeItems, func() bool {
				return isAdminAuthor
			})
			if autoTr != nil && idleTracker.ShouldAutoReply(msg.Chat.ID, idleAfter, now) {
				if autoTr.ActionType == ActionTypeGPTPrompt && !checkGPTTokenQuota() {
					continue
				}
				enqueueTriggerAction(handlerDeps.triggerActionDeps, handlerDeps.ActionQueue, msg, autoTr, recentBefore, quotaLowWarningTrigger)
				if debugTriggerLogEnabled {
					log.Printf("idle auto-reply queued trigger=%d chat=%d msg=%d idle_after=%s", autoTr.ID, msg.Chat.ID, msg.MessageID, idleAfter)
				}
				continue
			}
		}
		continue
	}
}

type triggerActionDeps struct {
	Bot               *tgbotapi.BotAPI
	Store             TriggerStorePort
	IdleTracker       *trigger.IdleTracker
	Portraits         *participantPortraitManager
	SpotifyMusic      SpotifyMusicPort
	SpotifyDownloader SpotifyDownloadPort
	SpotifyQueue      *spotifyPickQueue
	YandexDownloader  YandexMusicDownloadPort
	YandexQueue       *yandexMusicQueue
	VKDownloader      VKMusicDownloadPort
	VKQueue           *vkMusicQueue
	SoundCloudSearch  SoundCloudSearchPort
	MediaDownloader   MediaDownloadPort
	MediaQueue        *mediaDownloadQueue
	MediaInteractive  bool
	TemplateLookup    func(string) string
	RecordGPTTokens   func(userID int64, tokens int, now time.Time) (remaining int, crossedLow bool, err error)
}

type triggerHandlerDeps struct {
	triggerActionDeps
	ActionQueue *triggerActionQueue
	Allowed     chataccess.AllowList
	Engine      *engine.TriggerEngine
	Store       TriggerStorePort
	AdminCache  *adminStatusCache
	ChatRecent  *chatRecentStore
}

func startupBacklogCutoff(startedAtUnix int64) int64 {
	graceSec := envInt("TELEGRAM_STARTUP_OLD_UPDATE_GRACE_SEC", 0)
	if graceSec < 0 {
		graceSec = 0
	}
	return startedAtUnix - int64(graceSec)
}

func isStartupBacklogMessage(msg *tgbotapi.Message, startedAtUnix int64) bool {
	if msg == nil || msg.Date <= 0 || startedAtUnix <= 0 {
		return false
	}
	return int64(msg.Date) < startupBacklogCutoff(startedAtUnix)
}

func isStartupBacklogRawMember(upd *rawChatMemberUpdated, startedAtUnix int64) bool {
	if upd == nil || upd.Date <= 0 || startedAtUnix <= 0 {
		return false
	}
	return upd.Date < startupBacklogCutoff(startedAtUnix)
}

type musicProviderDeps struct {
	Bot               *tgbotapi.BotAPI
	SpotifyMusic      SpotifyMusicPort
	SpotifyDownloader SpotifyDownloadPort
	SpotifyQueue      *spotifyPickQueue
	YandexDownloader  YandexMusicDownloadPort
	YandexQueue       *yandexMusicQueue
	VKDownloader      VKMusicDownloadPort
	VKQueue           *vkMusicQueue
	SoundCloudSearch  SoundCloudSearchPort
	MediaDownloader   MediaDownloadPort
	MediaQueue        *mediaDownloadQueue
	IdleTracker       *trigger.IdleTracker
}

func newYandexTop10Token() string {
	var b [6]byte
	_, _ = crand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func putYandexTop10Request(req yandexTop10Request) string {
	yandexTop10State.mu.Lock()
	defer yandexTop10State.mu.Unlock()
	now := time.Now()
	for k, v := range yandexTop10State.data {
		if v.ExpiresAt.Before(now) {
			delete(yandexTop10State.data, k)
		}
	}
	token := newYandexTop10Token()
	req.Token = token
	if req.ExpiresAt.IsZero() {
		req.ExpiresAt = now.Add(2 * time.Hour)
	}
	yandexTop10State.data[token] = req
	return token
}

func takeYandexTop10Request(token string, userID int64) (yandexTop10Request, bool, string) {
	yandexTop10State.mu.Lock()
	defer yandexTop10State.mu.Unlock()
	req, ok := yandexTop10State.data[token]
	if !ok {
		return yandexTop10Request{}, false, "выбор устарел"
	}
	if time.Now().After(req.ExpiresAt) {
		delete(yandexTop10State.data, token)
		return yandexTop10Request{}, false, "выбор устарел"
	}
	if req.UserID != 0 && userID != 0 && req.UserID != userID {
		return yandexTop10Request{}, false, "эта кнопка доступна только автору запроса"
	}
	delete(yandexTop10State.data, token)
	return req, true, ""
}

func handleYandexTop10Callback(
	bot *tgbotapi.BotAPI,
	cb *tgbotapi.CallbackQuery,
	dl YandexMusicDownloadPort,
	idle *trigger.IdleTracker,
) bool {
	if bot == nil || cb == nil || !strings.HasPrefix(cb.Data, "ymtop10:") {
		return false
	}
	token := strings.TrimSpace(strings.TrimPrefix(cb.Data, "ymtop10:"))
	req, ok, msg := takeYandexTop10Request(token, cb.From.ID)
	if !ok {
		_, _ = bot.Request(tgbotapi.NewCallback(cb.ID, msg))
		return true
	}
	_, _ = bot.Request(tgbotapi.NewCallback(cb.ID, "Скачиваю топ-10..."))

	if cb.Message != nil {
		edit := tgbotapi.NewEditMessageText(cb.Message.Chat.ID, cb.Message.MessageID, "🎵 Скачиваю и отправляю топ-10...")
		_, _ = bot.Request(edit)
	}

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()

		type dlItem struct {
			path      string
			cleanup   string
			artist    string
			title     string
			sourceURL string
		}
		items := make([]dlItem, 0, len(req.Tracks))
		for _, tr := range req.Tracks {
			url := strings.TrimSpace(tr.URL)
			if url == "" {
				continue
			}
			p, err := dl.DownloadByURL(ctx, url)
			if err != nil {
				reportChatFailure(bot, req.ChatID, "ошибка скачивания Yandex Music", err)
				return
			}
			items = append(items, dlItem{
				path:      p,
				cleanup:   filepath.Dir(p),
				artist:    strings.TrimSpace(tr.Artist),
				title:     strings.TrimSpace(tr.Title),
				sourceURL: url,
			})
		}
		for i := range items {
			defer os.RemoveAll(items[i].cleanup)
		}
		if len(items) == 0 {
			reportChatFailure(bot, req.ChatID, "ошибка отправки аудио", errors.New("ничего не скачано"))
			return
		}

		media := make([]interface{}, 0, len(items))
		for _, it := range items {
			im := tgbotapi.NewInputMediaAudio(tgbotapi.FilePath(it.path))
			if it.artist != "" {
				im.Performer = it.artist
			}
			if it.title != "" {
				im.Title = it.title
			}
			media = append(media, im)
		}
		cfg := tgbotapi.NewMediaGroup(req.ChatID, media)
		if req.ReplyTo > 0 {
			cfg.ReplyToMessageID = req.ReplyTo
		}
		if _, err := bot.SendMediaGroup(cfg); err != nil {
			reportChatFailure(bot, req.ChatID, "ошибка отправки аудио", err)
			return
		}

		if cb.Message != nil {
			_, _ = bot.Request(tgbotapi.DeleteMessageConfig{
				ChatID:    cb.Message.Chat.ID,
				MessageID: cb.Message.MessageID,
			})
		}
		if req.DeleteSource && req.SourceMsgID > 0 {
			_, _ = bot.Request(tgbotapi.DeleteMessageConfig{
				ChatID:    req.ChatID,
				MessageID: req.SourceMsgID,
			})
		}
		if idle != nil {
			idle.MarkActivity(req.ChatID, time.Now())
		}
	}()

	return true
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
		kb := pick.BuildPickKeyboard(msg, replyTo, req.SourceMsgID, req.DeleteSource, pickTracks)
		topToken := putYandexTop10Request(yandexTop10Request{
			ChatID:       req.ChatID,
			UserID:       req.UserID,
			ReplyTo:      replyTo,
			SourceMsgID:  req.SourceMsgID,
			DeleteSource: req.DeleteSource,
			Query:        query,
			Tracks:       tracks,
		})
		kb.InlineKeyboard = append(kb.InlineKeyboard, tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("Скачать топ-10", "ymtop10:"+topToken),
		))
		m.ReplyMarkup = kb
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
	case musicpick.ProviderVK:
		if deps.VKDownloader == nil {
			return errors.New("VK Music не настроен: нужен VK_COOKIES_FILE")
		}
		if trackID := extractVKAudioTrackID(query); trackID != "" {
			if deps.VKQueue == nil || !deps.VKQueue.enqueue(vkMusicTask{
				SendCtx:  sendContext{Bot: deps.Bot, ChatID: req.ChatID, ReplyTo: replyTo},
				TrackID:  trackID,
				DL:       deps.VKDownloader,
				ReportTo: req.ChatID,
			}) {
				return errors.New("vk music queue is full")
			}
			return nil
		}
		searchCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
		tracks, err := deps.VKDownloader.SearchTracks(searchCtx, query, 10)
		cancel()
		if err != nil {
			return err
		}
		if len(tracks) == 0 {
			return errors.New("ничего не найдено в VK Music")
		}
		pickTracks := make([]pick.PickTrack, 0, len(tracks))
		for _, track := range tracks {
			pickTracks = append(pickTracks, pick.PickTrack{
				ID:          track.ID,
				Provider:    "vk",
				Artist:      track.Artist,
				Title:       track.Title,
				DurationSec: track.DurationSec,
			})
		}
		msg := &tgbotapi.Message{
			Chat: &tgbotapi.Chat{ID: req.ChatID},
			From: &tgbotapi.User{ID: req.UserID},
		}
		m := tgbotapi.NewMessage(req.ChatID, "🎵 Результаты поиска (VK):")
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
	case musicpick.ProviderSC:
		if deps.SoundCloudSearch == nil {
			return errors.New("SoundCloud search is not configured")
		}
		searchCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		tracks, err := deps.SoundCloudSearch.SearchSoundCloudTracks(searchCtx, query, 10)
		cancel()
		if err != nil {
			return err
		}
		if len(tracks) == 0 {
			return errors.New("ничего не найдено в SoundCloud")
		}
		pickTracks := make([]pick.PickTrack, 0, len(tracks))
		for _, track := range tracks {
			pickTracks = append(pickTracks, pick.PickTrack{
				ID:          track.ID,
				Provider:    "soundcloud",
				SourceURL:   strings.TrimSpace(track.SourceURL),
				Artist:      track.Artist,
				Title:       track.Title,
				DurationSec: track.DurationSec,
			})
		}
		msg := &tgbotapi.Message{
			Chat: &tgbotapi.Chat{ID: req.ChatID},
			From: &tgbotapi.User{ID: req.UserID},
		}
		m := tgbotapi.NewMessage(req.ChatID, "🎵 Результаты поиска (SoundCloud):")
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

func filterRuntimeTriggers(all []Trigger) []Trigger {
	if len(all) == 0 {
		return nil
	}
	out := make([]Trigger, 0, len(all))
	for i := range all {
		if all[i].ActionType == ActionTypeUserLimitLow || all[i].ActionType == ActionTypeOpenAIQuotaLow {
			continue
		}
		out = append(out, all[i])
	}
	return out
}

func filterRuntimeTriggersForMessage(bot *tgbotapi.BotAPI, msg *tgbotapi.Message, all []Trigger) []Trigger {
	if len(all) == 0 || bot == nil || msg == nil || msg.ReplyToMessage == nil || msg.ReplyToMessage.From == nil {
		return all
	}
	if !msg.ReplyToMessage.From.IsBot || msg.ReplyToMessage.From.ID != bot.Self.ID {
		return all
	}
	if !hasIgnoredAutoReplyLeadingToken(msg.ReplyToMessage) {
		return all
	}
	out := make([]Trigger, 0, len(all))
	removed := 0
	for i := range all {
		if all[i].TriggerMode == TriggerModeOnlyRepliesToSelfNoMedia {
			removed++
			continue
		}
		out = append(out, all[i])
	}
	if removed > 0 && debugTriggerLogEnabled {
		log.Printf("filtered ignored-prefix reply triggers chat=%d msg=%d reply_msg=%d removed=%d",
			msg.Chat.ID, msg.MessageID, msg.ReplyToMessage.MessageID, removed)
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
	enqueueTriggerAction(deps.triggerActionDeps, deps.ActionQueue, msg, tr, "", nil)
	if deps.ChatRecent != nil {
		deps.ChatRecent.Add(chatID, recentChatMessage{
			MessageID: 0,
			UserName:  buildUserDisplayName(msg.From),
			Text:      "",
			At:        time.Now(),
		})
	}
}

func handleTriggerActionForMessage(deps triggerActionDeps, msg *tgbotapi.Message, tr *Trigger, recentBefore string, userLimitLowTrigger *Trigger) {
	if msg == nil || tr == nil {
		return
	}
	rawTemplate := pickResponseVariantText(tr.ResponseText)
	resolvedTemplate := expandTemplateCalls(rawTemplate, deps.TemplateLookup)
	switch tr.ActionType {
	case ActionTypeUserLimitLow:
		// System-only action: sent from quota flow, never via regular trigger matching.
		return
	case ActionTypeOpenAIQuotaLow:
		// System-only action: sent from OpenAI quota error flow, never via regular trigger matching.
		return
	case ActionTypeSendSticker:
		replyTo := 0
		if tr.Reply || tr.TriggerMode == TriggerModeCommandReply {
			replyTo = msg.MessageID
		}
		tmplCtx := newTemplateContext(deps.Bot, msg, tr, deps.TemplateLookup)
		stickerRaw := buildResponseFromMessage(tmplCtx, resolvedTemplate)
		stickerID := extractStickerFileIDFromTemplate(stickerRaw)
		if stickerID == "" {
			reportChatFailure(deps.Bot, msg.Chat.ID, "ошибка отправки стикера", errors.New("empty or invalid sticker file_id in response_text"))
			return
		}
		simulateStickerPickDelay(deps.Bot, msg.Chat.ID, 4*time.Second)
		sendCtx := sendContext{Bot: deps.Bot, ChatID: msg.Chat.ID, ReplyTo: replyTo}
		if ok := sendSticker(sendCtx, stickerID); ok && deps.IdleTracker != nil {
			deps.IdleTracker.MarkActivity(msg.Chat.ID, time.Now())
			deleteTriggerSourceMessage(deps.Bot, msg, tr)
		}
		return
	case ActionTypeSendFile:
		replyTo := 0
		if tr.Reply || tr.TriggerMode == TriggerModeCommandReply {
			replyTo = msg.MessageID
		}
		tmplCtx := newTemplateContext(deps.Bot, msg, tr, deps.TemplateLookup)
		raw := strings.TrimSpace(buildResponseFromMessage(tmplCtx, resolvedTemplate))
		if raw == "" {
			reportChatFailure(deps.Bot, msg.Chat.ID, "ошибка отправки файла", errors.New("empty file path in response_text"))
			return
		}
		filePath := raw
		caption := ""
		if i := strings.Index(raw, "\n"); i >= 0 {
			filePath = strings.TrimSpace(raw[:i])
			caption = strings.TrimSpace(raw[i+1:])
		}
		sendCtx := sendContext{Bot: deps.Bot, ChatID: msg.Chat.ID, ReplyTo: replyTo}
		if err := sendDocumentFromFile(sendCtx, filePath, caption); err != nil {
			log.Printf("send file failed path=%q: %v", clipText(filePath, 180), err)
			reportChatFailure(deps.Bot, msg.Chat.ID, "ошибка отправки файла", err)
			return
		}
		if deps.IdleTracker != nil {
			deps.IdleTracker.MarkActivity(msg.Chat.ID, time.Now())
			deleteTriggerSourceMessage(deps.Bot, msg, tr)
		}
		return
	case ActionTypeSendVoice:
		replyTo := 0
		if tr.Reply || tr.TriggerMode == TriggerModeCommandReply {
			replyTo = msg.MessageID
		}
		tmplCtx := newTemplateContext(deps.Bot, msg, tr, deps.TemplateLookup)
		raw := strings.TrimSpace(buildResponseFromMessage(tmplCtx, resolvedTemplate))
		if raw == "" {
			reportChatFailure(deps.Bot, msg.Chat.ID, "ошибка отправки голосового сообщения", errors.New("empty voice source in response_text"))
			return
		}
		source := raw
		caption := ""
		if i := strings.Index(raw, "\n"); i >= 0 {
			source = strings.TrimSpace(raw[:i])
			caption = strings.TrimSpace(raw[i+1:])
		}
		sendCtx := sendContext{Bot: deps.Bot, ChatID: msg.Chat.ID, ReplyTo: replyTo}
		if err := sendVoiceMessage(sendCtx, source, caption); err != nil {
			log.Printf("send voice failed source=%q: %v", clipText(source, 180), err)
			reportChatFailure(deps.Bot, msg.Chat.ID, "ошибка отправки голосового сообщения", err)
			return
		}
		if deps.IdleTracker != nil {
			deps.IdleTracker.MarkActivity(msg.Chat.ID, time.Now())
			deleteTriggerSourceMessage(deps.Bot, msg, tr)
		}
		return
	case ActionTypeSendGIF:
		replyTo := 0
		if tr.Reply || tr.TriggerMode == TriggerModeCommandReply {
			replyTo = msg.MessageID
		}
		tmplCtx := newTemplateContext(deps.Bot, msg, tr, deps.TemplateLookup)
		raw := strings.TrimSpace(buildResponseFromMessage(tmplCtx, resolvedTemplate))
		if raw == "" {
			reportChatFailure(deps.Bot, msg.Chat.ID, "ошибка отправки GIF", errors.New("empty gif file_id in response_text"))
			return
		}
		fileID := raw
		caption := ""
		if i := strings.Index(raw, "\n"); i >= 0 {
			fileID = strings.TrimSpace(raw[:i])
			caption = strings.TrimSpace(raw[i+1:])
		}
		sendCtx := sendContext{Bot: deps.Bot, ChatID: msg.Chat.ID, ReplyTo: replyTo}
		if ok := sendGIF(sendCtx, fileID, caption); ok && deps.IdleTracker != nil {
			deps.IdleTracker.MarkActivity(msg.Chat.ID, time.Now())
			deleteTriggerSourceMessage(deps.Bot, msg, tr)
		}
		return
	case ActionTypeDelete:
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
	case ActionTypeDeletePortrait:
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
		if tr.Reply || tr.TriggerMode == TriggerModeCommandReply {
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
	case ActionTypeGPTPrompt:
		if deps.Portraits != nil {
			deps.Portraits.ObserveMessage(msg)
		}
		ctx := ""
		if isOlenyamTrigger(tr) {
			ctx = recentBefore
		}

		trCopy := *tr
		trCopy.ResponseText = []ResponseTextItem{{Text: resolvedTemplate}}
		triggeredAt := time.Now()
		var openAIQuotaLowTrigger *Trigger
		if deps.Store != nil {
			if all, err := deps.Store.ListTriggers(); err == nil {
				openAIQuotaLowTrigger = pickOlenyamHungryTrigger(all, false)
			}
		}
		executeGPTPromptTask(gpt.PromptTask{
			Bot:                 deps.Bot,
			Trigger:             trCopy,
			QuotaLowTrigger:     openAIQuotaLowTrigger,
			UserLimitLowTrigger: userLimitLowTrigger,
			Msg:                 msg,
			TriggeredAt:         triggeredAt,
			RecentContext:       ctx,
			TemplateLookup:      deps.TemplateLookup,
			RecordGPTTokens:     deps.RecordGPTTokens,
			IdleMarkActivity: func(chatID int64, now time.Time) {
				if deps.IdleTracker != nil {
					deps.IdleTracker.MarkActivity(chatID, now)
				}
			},
			ChatID: msg.Chat.ID,
		})
	case ActionTypeGPTImage:
		tmplCtx := newTemplateContext(deps.Bot, msg, tr, deps.TemplateLookup)
		img, err := generateChatGPTImage(tmplCtx, resolvedTemplate)
		if err != nil {
			log.Printf("gpt image failed: %v", err)
			reportChatFailure(deps.Bot, msg.Chat.ID, "ошибка генерации картинки в ChatGPT", err)
			return
		}
		replyTo := 0
		if tr.Reply || tr.TriggerMode == TriggerModeCommandReply {
			replyTo = msg.MessageID
		}
		sendCtx := sendContext{Bot: deps.Bot, ChatID: msg.Chat.ID, ReplyTo: replyTo}
		if ok := sendPhoto(sendCtx, img, "CW: сгенерено нейросетью", true); ok && deps.IdleTracker != nil {
			deps.IdleTracker.MarkActivity(msg.Chat.ID, time.Now())
			deleteTriggerSourceMessage(deps.Bot, msg, tr)
		}
	case ActionTypeSearchImage:
		tmplCtx := newTemplateContext(deps.Bot, msg, tr, deps.TemplateLookup)
		query := buildImageSearchQueryFromMessage(tmplCtx, resolvedTemplate)
		img, err := searchImageInSerpAPI(query)
		if err != nil {
			log.Printf("search image failed query=%q: %v", clipText(query, 160), err)
			reportChatFailure(deps.Bot, msg.Chat.ID, "ошибка поиска картинки", err)
			return
		}
		replyTo := 0
		if tr.Reply || tr.TriggerMode == TriggerModeCommandReply {
			replyTo = msg.MessageID
		}
		sendCtx := sendContext{Bot: deps.Bot, ChatID: msg.Chat.ID, ReplyTo: replyTo}
		if ok := sendPhoto(sendCtx, img, "", false); ok && deps.IdleTracker != nil {
			deps.IdleTracker.MarkActivity(msg.Chat.ID, time.Now())
			deleteTriggerSourceMessage(deps.Bot, msg, tr)
		}
	case ActionTypeSpotifyMusic:
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
		if tr.Reply || tr.TriggerMode == TriggerModeCommandReply {
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
	case ActionTypeVKMusic:
		if deps.VKDownloader == nil {
			reportChatFailure(deps.Bot, msg.Chat.ID, "ошибка VK-музыки", errors.New("VK Music не настроен: нужен VK_COOKIES_FILE"))
			return
		}
		query := buildSpotifyMusicQueryFromMessage(newTemplateContext(deps.Bot, msg, tr, deps.TemplateLookup), resolvedTemplate)
		if query == "" {
			query = strings.TrimSpace(firstNonEmptyUserText(msg))
		}
		query = strings.TrimSpace(query)
		if query == "" {
			return
		}
		replyTo := 0
		if tr.Reply || tr.TriggerMode == TriggerModeCommandReply {
			replyTo = msg.MessageID
		}
		if trackID := extractVKAudioTrackID(query); trackID != "" {
			task := vkMusicTask{
				SendCtx:  sendContext{Bot: deps.Bot, ChatID: msg.Chat.ID, ReplyTo: replyTo},
				TrackID:  trackID,
				DL:       deps.VKDownloader,
				Msg:      msg,
				Trigger:  tr,
				Idle:     deps.IdleTracker,
				ReportTo: msg.Chat.ID,
			}
			if deps.VKQueue == nil || !deps.VKQueue.enqueue(task) {
				reportChatFailure(deps.Bot, msg.Chat.ID, "ошибка отправки аудио VK", errors.New("vk music queue is full"))
				return
			}
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		tracks, err := deps.VKDownloader.SearchTracks(ctx, query, 10)
		cancel()
		if err != nil {
			log.Printf("vk music search failed: %v", err)
			reportChatFailure(deps.Bot, msg.Chat.ID, "ошибка поиска музыки VK", err)
			return
		}
		if len(tracks) == 0 {
			if debugTriggerLogEnabled {
				log.Printf("vk music search empty trigger=%d query=%q", tr.ID, clipText(query, 220))
			}
			return
		}
		if envBool("VK_AUDIO_INTERACTIVE", true) {
			maxResults := 10
			if len(tracks) > maxResults {
				tracks = tracks[:maxResults]
			}
			pickTracks := make([]pick.PickTrack, 0, len(tracks))
			for _, track := range tracks {
				pickTracks = append(pickTracks, pick.PickTrack{
					ID:          track.ID,
					Provider:    "vk",
					Artist:      track.Artist,
					Title:       track.Title,
					DurationSec: track.DurationSec,
				})
			}
			m := tgbotapi.NewMessage(msg.Chat.ID, "🎵 Результаты поиска (VK):")
			m.ReplyMarkup = pick.BuildPickKeyboard(msg, replyTo, msg.MessageID, tr.DeleteSource, pickTracks)
			if replyTo > 0 {
				m.ReplyToMessageID = replyTo
				m.AllowSendingWithoutReply = true
			}
			if _, err := deps.Bot.Send(m); err != nil {
				reportChatFailure(deps.Bot, msg.Chat.ID, "ошибка отправки списка VK", err)
				return
			}
			if deps.IdleTracker != nil {
				deps.IdleTracker.MarkActivity(msg.Chat.ID, time.Now())
			}
			return
		}
		task := vkMusicTask{
			SendCtx:  sendContext{Bot: deps.Bot, ChatID: msg.Chat.ID, ReplyTo: replyTo},
			TrackID:  tracks[0].ID,
			DL:       deps.VKDownloader,
			Msg:      msg,
			Trigger:  tr,
			Idle:     deps.IdleTracker,
			ReportTo: msg.Chat.ID,
		}
		if deps.VKQueue == nil || !deps.VKQueue.enqueue(task) {
			reportChatFailure(deps.Bot, msg.Chat.ID, "ошибка отправки аудио VK", errors.New("vk music queue is full"))
			return
		}
		if debugTriggerLogEnabled {
			log.Printf("send vk/audio queued trigger=%d replyTo=%d query=%q", tr.ID, replyTo, clipText(query, 160))
		}
		return
	case ActionTypeMusic:
		query := buildSpotifyMusicQueryFromMessage(newTemplateContext(deps.Bot, msg, tr, deps.TemplateLookup), resolvedTemplate)
		if query == "" {
			query = strings.TrimSpace(firstNonEmptyUserText(msg))
		}
		query = strings.TrimSpace(query)
		if query == "" {
			return
		}
		replyTo := 0
		if tr.Reply || tr.TriggerMode == TriggerModeCommandReply {
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
		if trackID := extractVKAudioTrackID(query); trackID != "" {
			task := vkMusicTask{
				SendCtx:  sendContext{Bot: deps.Bot, ChatID: msg.Chat.ID, ReplyTo: replyTo},
				TrackID:  trackID,
				DL:       deps.VKDownloader,
				Msg:      msg,
				Trigger:  tr,
				Idle:     deps.IdleTracker,
				ReportTo: msg.Chat.ID,
			}
			if deps.VKQueue == nil || !deps.VKQueue.enqueue(task) {
				reportChatFailure(deps.Bot, msg.Chat.ID, "ошибка скачивания VK Music", errors.New("vk music queue is full"))
				return
			}
			return
		}
		if targetURL := extractSupportedMediaURLByService(query, "soundcloud"); targetURL != "" {
			task := mediaDownloadTask{
				SendCtx:  sendContext{Bot: deps.Bot, ChatID: msg.Chat.ID, ReplyTo: replyTo},
				URL:      targetURL,
				Mode:     mediadl.ModeAudio,
				DL:       deps.MediaDownloader,
				Msg:      msg,
				Trigger:  tr,
				Idle:     deps.IdleTracker,
				ReportTo: msg.Chat.ID,
			}
			if deps.MediaQueue == nil || !deps.MediaQueue.enqueue(task) {
				reportChatFailure(deps.Bot, msg.Chat.ID, "ошибка скачивания SoundCloud", errors.New("media download queue is full"))
				return
			}
			if debugTriggerLogEnabled {
				log.Printf("send soundcloud/audio queued trigger=%d replyTo=%d url=%q", tr.ID, replyTo, clipText(targetURL, 160))
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
	case ActionTypeYandexMusic:
		query := buildMediaDownloadQueryFromMessage(newTemplateContext(deps.Bot, msg, tr, deps.TemplateLookup), resolvedTemplate)
		targetURL := extractYandexMusicURL(query)
		if targetURL == "" {
			return
		}
		replyTo := 0
		if tr.Reply || tr.TriggerMode == TriggerModeCommandReply {
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
	case ActionTypeMediaAudio:
		query := buildMediaDownloadQueryFromMessage(newTemplateContext(deps.Bot, msg, tr, deps.TemplateLookup), resolvedTemplate)
		targetURL := extractSupportedMediaURL(query)
		if targetURL == "" {
			return
		}
		replyTo := 0
		if tr.Reply || tr.TriggerMode == TriggerModeCommandReply {
			replyTo = msg.MessageID
		}
		if trackID := extractVKAudioTrackID(targetURL); trackID != "" {
			if deps.VKDownloader == nil {
				reportChatFailure(deps.Bot, msg.Chat.ID, "ошибка скачивания VK Music", errors.New("VK Music downloader is not configured"))
				return
			}
			task := vkMusicTask{
				SendCtx:  sendContext{Bot: deps.Bot, ChatID: msg.Chat.ID, ReplyTo: replyTo},
				TrackID:  trackID,
				DL:       deps.VKDownloader,
				Msg:      msg,
				Trigger:  tr,
				Idle:     deps.IdleTracker,
				ReportTo: msg.Chat.ID,
			}
			if deps.VKQueue == nil || !deps.VKQueue.enqueue(task) {
				reportChatFailure(deps.Bot, msg.Chat.ID, "ошибка скачивания VK Music", errors.New("vk music queue is full"))
				return
			}
			if debugTriggerLogEnabled {
				log.Printf("send vk/audio queued from media trigger=%d replyTo=%d track=%q", tr.ID, replyTo, trackID)
			}
			return
		}
		_, mediaService, _ := mediadl.NormalizeSupportedURL(targetURL)
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
			sent, err := deps.Bot.Send(m)
			if err != nil {
				reportChatFailure(deps.Bot, msg.Chat.ID, "ошибка отправки выбора формата", err)
				return
			}
			mediadl.ScheduleChoicePromptDeletion(deps.Bot, msg.Chat.ID, sent.MessageID)
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
	case ActionTypeMediaTikTok:
		query := buildMediaDownloadQueryFromMessage(newTemplateContext(deps.Bot, msg, tr, deps.TemplateLookup), resolvedTemplate)
		targetURL := extractSupportedMediaURLByService(query, "tiktok")
		if targetURL == "" {
			return
		}
		replyTo := 0
		if tr.Reply || tr.TriggerMode == TriggerModeCommandReply {
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
	case ActionTypeMediaCoub:
		query := buildMediaDownloadQueryFromMessage(newTemplateContext(deps.Bot, msg, tr, deps.TemplateLookup), resolvedTemplate)
		targetURL := extractSupportedMediaURLByService(query, "coub")
		if targetURL == "" {
			return
		}
		replyTo := 0
		if tr.Reply || tr.TriggerMode == TriggerModeCommandReply {
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
			reportChatFailure(deps.Bot, msg.Chat.ID, "ошибка скачивания Coub", errors.New("media download queue is full"))
			return
		}
		log.Printf("send coub queued trigger=%d replyTo=%d url=%q", tr.ID, replyTo, clipText(targetURL, 160))
		return
	case ActionTypeMediaX:
		query := buildMediaDownloadQueryFromMessage(newTemplateContext(deps.Bot, msg, tr, deps.TemplateLookup), resolvedTemplate)
		targetURL := extractSupportedMediaURLByService(query, "x")
		if targetURL == "" {
			return
		}
		replyTo := 0
		if tr.Reply || tr.TriggerMode == TriggerModeCommandReply {
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
		if tr.Reply || tr.TriggerMode == TriggerModeCommandReply {
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
