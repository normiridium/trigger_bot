package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"trigger-admin-bot/internal/chatclear"
)

type summaryTestService struct {
	history chatclear.HistoryResult
	err     error
	calls   int
	request chatclear.HistoryRequest
}

func (s *summaryTestService) Clear(context.Context, chatclear.Request) error { return nil }
func (s *summaryTestService) StartAuth(context.Context, chatclear.AuthStartRequest) (chatclear.AuthStartResult, error) {
	return chatclear.AuthStartResult{}, nil
}
func (s *summaryTestService) CompleteAuth(context.Context, chatclear.AuthCompleteRequest) (chatclear.AuthCompleteResult, error) {
	return chatclear.AuthCompleteResult{}, nil
}
func (s *summaryTestService) Available(context.Context) bool { return true }
func (s *summaryTestService) GetHistory(_ context.Context, req chatclear.HistoryRequest) (chatclear.HistoryResult, error) {
	s.calls++
	s.request = req
	return s.history, s.err
}

func TestLoadSummaryMessagesPrefersService(t *testing.T) {
	now := time.Now()
	service := &summaryTestService{history: chatclear.HistoryResult{Messages: []chatclear.HistoryMessage{{ID: 7, Date: now.Unix(), Text: "service"}}}}
	memory := newSummaryHistoryStore(1000)
	memory.add(-1001, chatclear.HistoryMessage{ID: 8, Date: now.Unix(), Text: "memory"})

	messages, source, err := loadSummaryMessages(service, memory, -1001, "", now.Add(-time.Hour), now)
	if err != nil {
		t.Fatalf("load summary messages: %v", err)
	}
	if source != "tg-ops-service" || len(messages) != 1 || messages[0].Text != "service" {
		t.Fatalf("unexpected source/messages: source=%q messages=%+v", source, messages)
	}
	if service.request.Limit != 0 || service.request.SinceUnix != now.Add(-time.Hour).Unix() || service.request.UntilUnix != now.Unix() {
		t.Fatalf("summary request must be date-bounded without message cap: %+v", service.request)
	}
}

func TestParseEthicsTarget(t *testing.T) {
	replyTarget, err := parseEthicsTarget(&tgbotapi.Message{ReplyToMessage: &tgbotapi.Message{From: &tgbotapi.User{
		ID: 42, UserName: "reply_user", FirstName: "Reply",
	}}})
	if err != nil || replyTarget.ID != 42 || replyTarget.Username != "reply_user" {
		t.Fatalf("unexpected reply target: %+v err=%v", replyTarget, err)
	}

	usernameTarget, err := parseEthicsTarget(&tgbotapi.Message{
		Text:     "/ethics @Example",
		Entities: []tgbotapi.MessageEntity{{Type: "bot_command", Offset: 0, Length: 7}},
	})
	if err != nil || usernameTarget.ID != 0 || usernameTarget.Username != "Example" {
		t.Fatalf("unexpected username target: %+v err=%v", usernameTarget, err)
	}
}

func TestLoadSummaryMessagesDoesNotHideServiceErrorWithMemoryFallback(t *testing.T) {
	now := time.Now()
	service := &summaryTestService{err: errors.New("service down")}
	memory := newSummaryHistoryStore(1000)
	memory.add(-1001, chatclear.HistoryMessage{ID: 8, Date: now.Unix(), Text: "memory"})

	messages, source, err := loadSummaryMessages(service, memory, -1001, "", now.Add(-time.Hour), now)
	if err == nil || !strings.Contains(err.Error(), "service down") {
		t.Fatalf("expected service error, got source=%q messages=%+v err=%v", source, messages, err)
	}
}

func TestSummaryHistoryLimitUpsertAndCommandFilter(t *testing.T) {
	store := newSummaryHistoryStore(3)
	now := time.Now().Unix()
	for i := 1; i <= 4; i++ {
		store.add(10, chatclear.HistoryMessage{ID: i, Date: now, Text: fmt.Sprintf("message %d", i)})
	}
	store.add(10, chatclear.HistoryMessage{ID: 4, Date: now, Text: "updated"})
	store.add(10, chatclear.HistoryMessage{ID: 5, Date: now, Text: "/summary@olenyam_bot"})

	messages := store.Messages(10, time.Time{}, 1000)
	if len(messages) != 2 {
		t.Fatalf("expected two non-command messages after max limit, got %+v", messages)
	}
	if messages[0].ID != 3 || messages[1].ID != 4 || messages[1].Text != "updated" {
		t.Fatalf("unexpected messages: %+v", messages)
	}
}

func TestFilterSummaryMessagesDoesNotTruncateDailyHistory(t *testing.T) {
	messages := make([]chatclear.HistoryMessage, 1500)
	for i := range messages {
		messages[i] = chatclear.HistoryMessage{ID: i + 1, Text: fmt.Sprintf("message %d", i+1)}
	}
	if got := filterSummaryMessages(messages); len(got) != len(messages) {
		t.Fatalf("daily history was truncated: got %d, want %d", len(got), len(messages))
	}
}

func TestBuildSummaryPromptMessagesIncludesIdentityAndLinks(t *testing.T) {
	messages := buildSummaryPromptMessages(-1001761967530, "", []chatclear.HistoryMessage{{
		ID: 514706, Date: 1, FromID: 8434505984, FromUsername: "radio", FromFirstName: "Радио",
		ReplyToMessage: 514700, Text: "Вещаю на 10141 кГц",
	}})
	if len(messages) != 1 {
		t.Fatalf("unexpected messages: %+v", messages)
	}
	got := messages[0]
	if got.AuthorID != 8434505984 || got.AuthorUsername != "radio" || got.AuthorName != "Радио" {
		t.Fatalf("identity missing: %+v", got)
	}
	if got.MessageLink != "https://t.me/c/1761967530/514706" || got.ReplyToLink != "https://t.me/c/1761967530/514700" {
		t.Fatalf("unexpected links: %+v", got)
	}
}

func TestSummaryHistoryObserveUsesTelegramMetadata(t *testing.T) {
	store := newSummaryHistoryStore(10)
	store.ObserveMessage(&tgbotapi.Message{
		MessageID: 12, Date: 1234,
		Chat: &tgbotapi.Chat{ID: -1001},
		From: &tgbotapi.User{ID: 42, FirstName: "Тая", LastName: "Валентайн", UserName: "taya"},
	}, "привет")
	got := store.Messages(-1001, time.Time{}, 10)
	if len(got) != 1 || got[0].FromID != 42 || got[0].FromUsername != "taya" || got[0].Text != "привет" {
		t.Fatalf("unexpected stored message: %+v", got)
	}
}

func TestEnsureSummaryHashtagLineEscapesRichMarkdownHeading(t *testing.T) {
	cases := map[string]string{
		"Текст":                   "Текст\n\n" + `\#summary`,
		"Текст\n\n#summary":       "Текст\n\n" + `\#summary`,
		"Текст\n\nsummary":        "Текст\n\n" + `\#summary`,
		"Текст\n\n" + `\#summary`: "Текст\n\n" + `\#summary`,
	}
	for in, want := range cases {
		if got := ensureSummaryHashtagLine(in); got != want {
			t.Fatalf("ensureSummaryHashtagLine(%q) = %q, want %q", in, got, want)
		}
	}
}
