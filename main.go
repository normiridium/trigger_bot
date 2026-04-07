package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/rand"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
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
	updates := bot.GetUpdatesChan(u)
	engine := NewTriggerEngine()

	for update := range updates {
		if update.Message == nil {
			continue
		}
		msg := update.Message
		if msg.Chat == nil || msg.From == nil || msg.From.IsBot {
			continue
		}
		if !allowedChats.Allows(msg.Chat.ID) {
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
					"Команды: /start /help\n\n" +
					"Теги для ChatGPT-промпта:\n" +
					"{{message}} / {{user_text}} — текст сообщения\n" +
					"{{user_id}}, {{user_first_name}}, {{user_username}}\n" +
					"{{user_display_name}}, {{user_label}}\n" +
					"{{sender_tag}}\n" +
					"{{chat_id}}, {{chat_title}}\n" +
					"{{reply_text}}\n" +
					"{{reply_user_id}}, {{reply_first_name}}, {{reply_username}}\n" +
					"{{reply_display_name}}, {{reply_label}}\n" +
					"{{reply_sender_tag}}"
				reply(bot, msg.Chat.ID, msg.MessageID, s, false)
				continue
			}
		}

		text := strings.TrimSpace(msg.Text)
		if text == "" {
			continue
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
		tr := engine.Select(bot, msg, text, items, func() bool {
			return adminCache.IsChatAdmin(bot, msg.Chat.ID, msg.From.ID)
		})
		if tr == nil {
			if debugTriggerLogEnabled {
				log.Printf("no trigger matched for msg=%d", msg.MessageID)
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
			} else if debugTriggerLogEnabled {
				log.Printf("delete ok msg=%d by trigger=%d", msg.MessageID, tr.ID)
			}
		case "gpt_prompt":
			out, err := generateChatGPTReply(bot, tr.ResponseText, msg)
			if err != nil {
				log.Printf("gpt prompt failed: %v", err)
				reportChatFailure(bot, msg.Chat.ID, "ошибка запроса к ChatGPT", err)
				continue
			}
			replyTo := 0
			if tr.Reply || tr.TriggerMode == "command_reply" {
				replyTo = msg.MessageID
			}
			sendMarkdownV2(bot, msg.Chat.ID, replyTo, out, tr.Preview)
			if debugTriggerLogEnabled {
				log.Printf("send gpt/markdown attempted trigger=%d replyTo=%d", tr.ID, replyTo)
			}
		case "gpt_image":
			img, err := generateChatGPTImage(bot, tr.ResponseText, msg)
			if err != nil {
				log.Printf("gpt image failed: %v", err)
				reportChatFailure(bot, msg.Chat.ID, "ошибка генерации картинки в ChatGPT", err)
				continue
			}
			replyTo := 0
			if tr.Reply || tr.TriggerMode == "command_reply" {
				replyTo = msg.MessageID
			}
			sendPhoto(bot, msg.Chat.ID, replyTo, img, "CW: сгенерено нейросетью", true)
			if debugTriggerLogEnabled {
				log.Printf("send gpt/image attempted trigger=%d replyTo=%d", tr.ID, replyTo)
			}
		case "search_image":
			query := buildImageSearchQueryFromMessage(bot, tr.ResponseText, msg)
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
			sendPhoto(bot, msg.Chat.ID, replyTo, img, "", false)
			if debugTriggerLogEnabled {
				log.Printf("send search/image attempted trigger=%d replyTo=%d query=%q", tr.ID, replyTo, clipText(query, 220))
			}
		default:
			replyTo := 0
			if tr.Reply || tr.TriggerMode == "command_reply" {
				replyTo = msg.MessageID
			}
			sendHTML(bot, msg.Chat.ID, replyTo, tr.ResponseText, tr.Preview)
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

func sendHTML(bot *tgbotapi.BotAPI, chatID int64, replyTo int, html string, preview bool) {
	html = normalizeEscapedHTMLBreaks(html)
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
		return
	}
	if debugTriggerLogEnabled {
		log.Printf("send html ok chat=%d msg=%d replyTo=%d text=%q", chatID, sent.MessageID, replyTo, clipText(m.Text, 120))
	}
}

func sendMarkdownV2(bot *tgbotapi.BotAPI, chatID int64, replyTo int, text string, preview bool) {
	text = normalizeEscapedHTMLBreaks(text)
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
		return
	}
	if debugTriggerLogEnabled {
		log.Printf("send markdown ok chat=%d msg=%d replyTo=%d text=%q", chatID, sent.MessageID, replyTo, clipText(m.Text, 120))
	}
}

type generatedImage struct {
	URL   string
	Bytes []byte
}

func sendPhoto(bot *tgbotapi.BotAPI, chatID int64, replyTo int, img generatedImage, caption string, spoiler bool) {
	if spoiler {
		if err := sendPhotoWithSpoilerAPI(bot, chatID, replyTo, img, caption); err != nil {
			log.Printf("send photo (spoiler) failed: %v", err)
			reportChatFailure(bot, chatID, "ошибка отправки картинки", err)
			return
		}
		if debugTriggerLogEnabled {
			log.Printf("send photo (spoiler) ok chat=%d replyTo=%d", chatID, replyTo)
		}
		return
	}

	var file tgbotapi.RequestFileData
	switch {
	case strings.TrimSpace(img.URL) != "":
		file = tgbotapi.FileURL(strings.TrimSpace(img.URL))
	case len(img.Bytes) > 0:
		file = tgbotapi.FileBytes{Name: "generated.png", Bytes: img.Bytes}
	default:
		reportChatFailure(bot, chatID, "ошибка отправки картинки", errors.New("empty image payload"))
		return
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
		return
	}
	if debugTriggerLogEnabled {
		if strings.TrimSpace(img.URL) != "" {
			log.Printf("send photo ok chat=%d msg=%d replyTo=%d source=url", chatID, sent.MessageID, replyTo)
		} else {
			log.Printf("send photo ok chat=%d msg=%d replyTo=%d source=bytes size=%d", chatID, sent.MessageID, replyTo, len(img.Bytes))
		}
	}
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

func normalizeEscapedHTMLBreaks(s string) string {
	s = strings.ReplaceAll(s, "\\r\\n", "<br>")
	s = strings.ReplaceAll(s, "\\n", "<br>")
	s = strings.ReplaceAll(s, "\\r", "<br>")
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

func buildImageSearchQueryFromMessage(bot *tgbotapi.BotAPI, queryTemplate string, msg *tgbotapi.Message) string {
	query := strings.TrimSpace(queryTemplate)
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

	var lastErr error
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

func generateChatGPTReply(bot *tgbotapi.BotAPI, promptTemplate string, msg *tgbotapi.Message) (string, error) {
	apiKey := strings.TrimSpace(os.Getenv("OPENAI_API_KEY"))
	if apiKey == "" {
		return "", errors.New("OPENAI_API_KEY is empty")
	}
	model := strings.TrimSpace(os.Getenv("OPENAI_MODEL"))
	if model == "" {
		model = "gpt-4.1-mini"
	}

	prompt := buildPromptFromMessage(bot, promptTemplate, msg)
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

func generateChatGPTImage(bot *tgbotapi.BotAPI, promptTemplate string, msg *tgbotapi.Message) (generatedImage, error) {
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

	prompt := buildPromptFromMessage(bot, promptTemplate, msg)
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
