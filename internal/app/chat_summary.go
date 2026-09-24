package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"trigger-admin-bot/internal/chatclear"
)

const (
	chatSummaryHistoryLimit = 1000
	chatSummaryPeriod       = 24 * time.Hour
	chatSummaryTemplateKey  = "chat_summary"
)

var summaryCommandPattern = regexp.MustCompile(`(?i)^/summary(?:@[a-z0-9_]+)?(?:\s|$)`)

type summaryHistoryStore struct {
	mu       sync.RWMutex
	max      int
	messages map[int64][]chatclear.HistoryMessage
}

type summaryPromptMessage struct {
	MessageID      int    `json:"message_id"`
	Date           string `json:"date"`
	AuthorID       int64  `json:"author_id,omitempty"`
	AuthorName     string `json:"author_name,omitempty"`
	AuthorUsername string `json:"author_username,omitempty"`
	MessageLink    string `json:"message_link,omitempty"`
	ReplyToMessage int    `json:"reply_to_message_id,omitempty"`
	ReplyToLink    string `json:"reply_to_link,omitempty"`
	Text           string `json:"text"`
}

func newSummaryHistoryStore(max int) *summaryHistoryStore {
	if max <= 0 || max > chatSummaryHistoryLimit {
		max = chatSummaryHistoryLimit
	}
	return &summaryHistoryStore{max: max, messages: make(map[int64][]chatclear.HistoryMessage)}
}

func ensureChatSummaryTemplate(store *Store) {
	if store == nil {
		return
	}
	existing, err := store.getTemplateByKey(chatSummaryTemplateKey)
	if err != nil {
		log.Printf("chat summary template lookup failed: %v", err)
		return
	}
	if existing != nil {
		return
	}
	if err := store.SaveTemplate(ResponseTemplate{
		Key: chatSummaryTemplateKey, Title: "Саммаризация чата", Text: defaultChatSummaryPrompt,
	}); err != nil {
		log.Printf("chat summary template create failed: %v", err)
		return
	}
	log.Printf("chat summary template created key=%s", chatSummaryTemplateKey)
}

func (s *summaryHistoryStore) ObserveMessage(msg *tgbotapi.Message, text string) {
	if s == nil || msg == nil || msg.Chat == nil || msg.Chat.ID == 0 {
		return
	}
	text = strings.TrimSpace(text)
	if text == "" || summaryCommandPattern.MatchString(text) {
		return
	}
	item := chatclear.HistoryMessage{ID: msg.MessageID, Date: int64(msg.Date), Text: text}
	if item.Date <= 0 {
		item.Date = time.Now().Unix()
	}
	if msg.From != nil {
		item.FromID = msg.From.ID
		item.FromUsername = strings.TrimSpace(msg.From.UserName)
		item.FromFirstName = strings.TrimSpace(msg.From.FirstName)
		item.FromLastName = strings.TrimSpace(msg.From.LastName)
	}
	if msg.SenderChat != nil {
		item.FromID = msg.SenderChat.ID
		item.FromUsername = strings.TrimSpace(msg.SenderChat.UserName)
		item.FromTitle = strings.TrimSpace(msg.SenderChat.Title)
	}
	if msg.ReplyToMessage != nil {
		item.ReplyToMessage = msg.ReplyToMessage.MessageID
	}
	s.add(msg.Chat.ID, item)
}

func (s *summaryHistoryStore) add(chatID int64, item chatclear.HistoryMessage) {
	if s == nil || chatID == 0 || item.ID <= 0 || strings.TrimSpace(item.Text) == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	items := s.messages[chatID]
	for i := range items {
		if items[i].ID == item.ID {
			items[i] = item
			s.messages[chatID] = items
			return
		}
	}
	items = append(items, item)
	if len(items) > s.max {
		items = items[len(items)-s.max:]
	}
	s.messages[chatID] = items
}

func (s *summaryHistoryStore) Messages(chatID int64, since time.Time, limit int) []chatclear.HistoryMessage {
	if s == nil || chatID == 0 {
		return nil
	}
	if limit <= 0 || limit > s.max {
		limit = s.max
	}
	s.mu.RLock()
	items := append([]chatclear.HistoryMessage(nil), s.messages[chatID]...)
	s.mu.RUnlock()
	result := make([]chatclear.HistoryMessage, 0, len(items))
	for _, item := range items {
		if !since.IsZero() && item.Date < since.Unix() {
			continue
		}
		if summaryCommandPattern.MatchString(strings.TrimSpace(item.Text)) {
			continue
		}
		result = append(result, item)
	}
	if len(result) > limit {
		result = result[len(result)-limit:]
	}
	return result
}

func handleSummaryCommand(bot *tgbotapi.BotAPI, service chatclear.Service, memory *summaryHistoryStore, templateLookup func(string) string, msg *tgbotapi.Message) {
	if bot == nil || msg == nil || msg.Chat == nil {
		return
	}
	chatID := msg.Chat.ID
	replyTo := msg.MessageID
	chatUsername := strings.TrimPrefix(strings.TrimSpace(msg.Chat.UserName), "@")
	go func() {
		_, _ = bot.Request(tgbotapi.NewChatAction(chatID, tgbotapi.ChatTyping))
		now := time.Now()
		since := now.Add(-chatSummaryPeriod)
		messages, source, err := loadSummaryMessages(service, memory, chatID, chatUsername, since, now)
		if err != nil {
			reportChatFailure(bot, chatID, "ошибка получения истории для сводки", err)
			return
		}
		if len(messages) == 0 {
			reply(sendContext{Bot: bot, ChatID: chatID, ReplyTo: replyTo}, "За последние сутки нет текстовых сообщений для сводки.", false)
			return
		}
		promptMessages := buildSummaryPromptMessages(chatID, chatUsername, messages)
		promptTemplate := defaultChatSummaryPrompt
		if templateLookup != nil {
			if configured := strings.TrimSpace(templateLookup(chatSummaryTemplateKey)); configured != "" {
				promptTemplate = configured
			}
		}
		summary, err := generateChatSummary(promptTemplate, promptMessages)
		if err != nil {
			reportChatFailure(bot, chatID, "ошибка создания сводки", err)
			return
		}
		summary = ensureSummaryHashtagLine(summary)
		log.Printf("chat summary generated chat=%d source=%s messages=%d", chatID, source, len(messages))
		if !sendRichMarkdownArticle(sendContext{Bot: bot, ChatID: chatID, ReplyTo: replyTo}, summary, false) {
			log.Printf("chat summary delivery failed chat=%d source=%s messages=%d", chatID, source, len(messages))
		}
	}()
}

func ensureSummaryHashtagLine(summary string) string {
	summary = strings.TrimSpace(summary)
	if summary == "" {
		return `\#summary`
	}
	lines := strings.Split(summary, "\n")
	for len(lines) > 0 {
		last := strings.TrimSpace(lines[len(lines)-1])
		if last == "" {
			lines = lines[:len(lines)-1]
			continue
		}
		if strings.EqualFold(last, "summary") || strings.EqualFold(last, "#summary") || strings.EqualFold(last, `\#summary`) {
			lines[len(lines)-1] = `\#summary`
			return strings.TrimSpace(strings.Join(lines, "\n"))
		}
		break
	}
	return summary + "\n\n" + `\#summary`
}

func loadSummaryMessages(service chatclear.Service, memory *summaryHistoryStore, chatID int64, username string, since, until time.Time) ([]chatclear.HistoryMessage, string, error) {
	_ = memory
	if service == nil {
		return nil, "", errors.New("tg-ops-service is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	result, err := service.GetHistory(ctx, chatclear.HistoryRequest{
		ChatID: chatID, Username: username,
		SinceUnix: since.Unix(), UntilUnix: until.Unix(),
	})
	cancel()
	if err != nil {
		return nil, "", fmt.Errorf("load complete daily history from tg-ops-service: %w", err)
	}
	return filterSummaryMessages(result.Messages), "tg-ops-service", nil
}

func filterSummaryMessages(messages []chatclear.HistoryMessage) []chatclear.HistoryMessage {
	result := make([]chatclear.HistoryMessage, 0, len(messages))
	for _, item := range messages {
		item.Text = strings.TrimSpace(item.Text)
		if item.Text == "" || summaryCommandPattern.MatchString(item.Text) {
			continue
		}
		result = append(result, item)
	}
	sort.SliceStable(result, func(i, j int) bool {
		if result[i].Date == result[j].Date {
			return result[i].ID < result[j].ID
		}
		return result[i].Date < result[j].Date
	})
	return result
}

func buildSummaryPromptMessages(chatID int64, username string, messages []chatclear.HistoryMessage) []summaryPromptMessage {
	result := make([]summaryPromptMessage, 0, len(messages))
	for _, item := range messages {
		name := strings.TrimSpace(strings.Join([]string{item.FromFirstName, item.FromLastName}, " "))
		if name == "" {
			name = strings.TrimSpace(item.FromTitle)
		}
		promptItem := summaryPromptMessage{
			MessageID: item.ID, Date: time.Unix(item.Date, 0).Format(time.RFC3339),
			AuthorID: item.FromID, AuthorName: name, AuthorUsername: strings.TrimPrefix(strings.TrimSpace(item.FromUsername), "@"),
			MessageLink: telegramMessageLink(chatID, username, item.ID), ReplyToMessage: item.ReplyToMessage,
			Text: item.Text,
		}
		if item.ReplyToMessage > 0 {
			promptItem.ReplyToLink = telegramMessageLink(chatID, username, item.ReplyToMessage)
		}
		result = append(result, promptItem)
	}
	return result
}

func telegramMessageLink(chatID int64, username string, messageID int) string {
	if messageID <= 0 {
		return ""
	}
	if username = strings.TrimPrefix(strings.TrimSpace(username), "@"); username != "" {
		return fmt.Sprintf("https://t.me/%s/%d", username, messageID)
	}
	const supergroupOffset int64 = 1000000000000
	if chatID <= -supergroupOffset {
		return fmt.Sprintf("https://t.me/c/%d/%d", -chatID-supergroupOffset, messageID)
	}
	return ""
}

func marshalSummaryHistory(messages []summaryPromptMessage) (string, error) {
	data, err := json.Marshal(messages)
	if err != nil {
		return "", err
	}
	return string(data), nil
}
