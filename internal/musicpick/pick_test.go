package musicpick

import (
	"strings"
	"testing"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

func resetChoiceRequestsForTest() {
	choiceMu.Lock()
	choiceRequests = make(map[string]ChoiceRequest)
	choiceMu.Unlock()
}

func TestBuildChoiceKeyboardStoresThreeRequests(t *testing.T) {
	resetChoiceRequestsForTest()
	msg := &tgbotapi.Message{
		Chat: &tgbotapi.Chat{ID: -100123},
		From: &tgbotapi.User{ID: 42},
	}
	kb := BuildChoiceKeyboard(msg, 11, 22, true, "  my song ")
	if len(kb.InlineKeyboard) != 2 {
		t.Fatalf("unexpected keyboard rows: %d", len(kb.InlineKeyboard))
	}
	if len(kb.InlineKeyboard[0]) != 2 || len(kb.InlineKeyboard[1]) != 1 {
		t.Fatalf("unexpected keyboard layout: %+v", kb.InlineKeyboard)
	}
	if kb.InlineKeyboard[0][0].CallbackData == nil || !strings.HasPrefix(*kb.InlineKeyboard[0][0].CallbackData, "musicpick_s:") {
		t.Fatalf("spotify callback mismatch: %v", kb.InlineKeyboard[0][0].CallbackData)
	}
	if kb.InlineKeyboard[0][1].CallbackData == nil || !strings.HasPrefix(*kb.InlineKeyboard[0][1].CallbackData, "musicpick_y:") {
		t.Fatalf("yandex callback mismatch: %v", kb.InlineKeyboard[0][1].CallbackData)
	}
	if kb.InlineKeyboard[1][0].CallbackData == nil || !strings.HasPrefix(*kb.InlineKeyboard[1][0].CallbackData, "musicpick_c:") {
		t.Fatalf("cancel callback mismatch: %v", kb.InlineKeyboard[1][0].CallbackData)
	}

	choiceMu.Lock()
	count := len(choiceRequests)
	choiceMu.Unlock()
	if count != 3 {
		t.Fatalf("expected 3 stored requests, got %d", count)
	}
}

func TestTakeChoiceAccessAndExpiry(t *testing.T) {
	resetChoiceRequestsForTest()
	token := putChoice(ChoiceRequest{
		Query:     "test",
		UserID:    100,
		ExpiresAt: time.Now().Add(2 * time.Minute),
	})
	if _, ok, msg := takeChoice(token, 200); ok || msg == "" {
		t.Fatalf("expected forbidden for other user, ok=%v msg=%q", ok, msg)
	}

	req, ok, msg := takeChoice(token, 100)
	if !ok || msg != "" {
		t.Fatalf("expected successful take, ok=%v msg=%q", ok, msg)
	}
	if req.Query != "test" {
		t.Fatalf("unexpected request payload: %+v", req)
	}
	if _, ok, _ := takeChoice(token, 100); ok {
		t.Fatal("token must be single-use")
	}
}

func TestPutChoiceCleansExpiredEntries(t *testing.T) {
	resetChoiceRequestsForTest()
	choiceMu.Lock()
	choiceRequests["expired"] = ChoiceRequest{
		Token:     "expired",
		Query:     "old",
		ExpiresAt: time.Now().Add(-time.Minute),
	}
	choiceMu.Unlock()

	_ = putChoice(ChoiceRequest{Query: "new"})

	choiceMu.Lock()
	_, hasExpired := choiceRequests["expired"]
	choiceMu.Unlock()
	if hasExpired {
		t.Fatal("expired request must be removed")
	}
}

func TestHandleChoiceCallbackGuards(t *testing.T) {
	if HandleChoiceCallback(nil, nil, nil, nil) {
		t.Fatal("nil callback/bot must return false")
	}
	cb := &tgbotapi.CallbackQuery{Data: "other:data", From: &tgbotapi.User{ID: 1}}
	if HandleChoiceCallback(&tgbotapi.BotAPI{}, cb, nil, nil) {
		t.Fatal("unknown callback prefix must return false")
	}
}

func TestReportFailure(t *testing.T) {
	called := false
	reportFailure(func(chatID int64, title string, err error) {
		called = true
		if chatID != 10 {
			t.Fatalf("unexpected chatID: %d", chatID)
		}
		if title != "title" {
			t.Fatalf("unexpected title: %q", title)
		}
		if err == nil {
			t.Fatal("expected err")
		}
	}, 10, "title", errTest{})
	if !called {
		t.Fatal("expected report callback")
	}
	// nil reporter must be a no-op
	reportFailure(nil, 10, "title", errTest{})
}

type errTest struct{}

func (errTest) Error() string { return "err" }
