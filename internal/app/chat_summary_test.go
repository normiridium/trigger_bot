package app

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"trigger-admin-bot/internal/chatclear"
)

type summaryTestService struct {
	history chatclear.HistoryResult
	err     error
	calls   int
}

func (s *summaryTestService) Clear(context.Context, chatclear.Request) error { return nil }
func (s *summaryTestService) StartAuth(context.Context, chatclear.AuthStartRequest) (chatclear.AuthStartResult, error) {
	return chatclear.AuthStartResult{}, nil
}
func (s *summaryTestService) CompleteAuth(context.Context, chatclear.AuthCompleteRequest) (chatclear.AuthCompleteResult, error) {
	return chatclear.AuthCompleteResult{}, nil
}
func (s *summaryTestService) Available(context.Context) bool { return true }
func (s *summaryTestService) GetHistory(context.Context, chatclear.HistoryRequest) (chatclear.HistoryResult, error) {
	s.calls++
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
}

func TestLoadSummaryMessagesFallsBackToMemoryOnServiceError(t *testing.T) {
	now := time.Now()
	service := &summaryTestService{err: errors.New("service down")}
	memory := newSummaryHistoryStore(1000)
	memory.add(-1001, chatclear.HistoryMessage{ID: 8, Date: now.Unix(), Text: "memory"})

	messages, source, err := loadSummaryMessages(service, memory, -1001, "", now.Add(-time.Hour), now)
	if err != nil {
		t.Fatalf("load summary messages: %v", err)
	}
	if source != "memory" || len(messages) != 1 || messages[0].Text != "memory" {
		t.Fatalf("unexpected source/messages: source=%q messages=%+v", source, messages)
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
