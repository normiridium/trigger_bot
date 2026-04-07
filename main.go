package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

var chatErrorLogEnabled bool
var debugTriggerLogEnabled bool
var debugGPTLogEnabled bool

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
	debugTriggerLogEnabled = envBool("DEBUG_TRIGGER_LOG", true)
	debugGPTLogEnabled = envBool("DEBUG_GPT_LOG", true)
	log.Printf("Bot started as @%s", bot.Self.UserName)

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
			return isChatAdmin(bot, msg.Chat.ID, msg.From.ID)
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
			sendHTML(bot, msg.Chat.ID, replyTo, out, tr.Preview)
			if debugTriggerLogEnabled {
				log.Printf("send gpt/html attempted trigger=%d replyTo=%d", tr.ID, replyTo)
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

func normalizeEscapedHTMLBreaks(s string) string {
	s = strings.ReplaceAll(s, "\\r\\n", "<br>")
	s = strings.ReplaceAll(s, "\\n", "<br>")
	s = strings.ReplaceAll(s, "\\r", "<br>")
	return s
}

func isChatAdmin(bot *tgbotapi.BotAPI, chatID int64, userID int64) bool {
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
	if bot != nil && msg.Chat != nil && msg.From != nil {
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
			if bot != nil && msg.Chat != nil {
				replySenderTag = getChatMemberTagRaw(bot.Token, msg.Chat.ID, msg.ReplyToMessage.From.ID)
			}
		}
	}
	replySenderTagDisplay := replySenderTag
	if strings.TrimSpace(replySenderTagDisplay) == "" {
		replySenderTagDisplay = "не указан"
	}

	chatTitle := ""
	if msg.Chat != nil {
		chatTitle = strings.TrimSpace(msg.Chat.Title)
	}

	replacements := map[string]string{
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

	for tag, value := range replacements {
		prompt = strings.ReplaceAll(prompt, tag, value)
	}

	if strings.Contains(promptTemplate, "{{") {
		return prompt
	}

	return prompt + "\n\nСообщение пользователя:\n" + userText
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
