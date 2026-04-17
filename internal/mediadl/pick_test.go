package mediadl

import (
	"strings"
	"testing"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

func resetChoiceState() {
	choiceMu.Lock()
	defer choiceMu.Unlock()
	choiceRequests = make(map[string]ChoiceRequest)
}

func TestBuildChoiceKeyboard(t *testing.T) {
	resetChoiceState()
	msg := &tgbotapi.Message{Chat: &tgbotapi.Chat{ID: -1001}, From: &tgbotapi.User{ID: 7}}
	req := ChoiceRequest{URL: "https://instagram.com/reel/abc", ChatID: -1001, UserID: 7}
	kb := BuildChoiceKeyboard(msg, req)
	if len(kb.InlineKeyboard) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(kb.InlineKeyboard))
	}
	if len(kb.InlineKeyboard[0]) != 2 {
		t.Fatalf("expected 2 buttons in first row")
	}
	audioData := kb.InlineKeyboard[0][0].CallbackData
	videoData := kb.InlineKeyboard[0][1].CallbackData
	cancelData := kb.InlineKeyboard[1][0].CallbackData
	if audioData == nil || !strings.HasPrefix(*audioData, "mdpick_a:") {
		t.Fatalf("unexpected audio callback: %#v", audioData)
	}
	if videoData == nil || !strings.HasPrefix(*videoData, "mdpick_v:") {
		t.Fatalf("unexpected video callback: %#v", videoData)
	}
	if cancelData == nil || !strings.HasPrefix(*cancelData, "mdpick_c:") {
		t.Fatalf("unexpected cancel callback: %#v", cancelData)
	}
}

func TestTakeChoice_WrongUser(t *testing.T) {
	resetChoiceState()
	token := putChoice(ChoiceRequest{URL: "https://youtu.be/a", UserID: 11})
	_, ok, msg := takeChoice(token, 22)
	if ok {
		t.Fatalf("expected forbidden choice for wrong user")
	}
	if !strings.Contains(msg, "только автору") {
		t.Fatalf("unexpected error message: %q", msg)
	}
}

func TestTakeChoice_Expired(t *testing.T) {
	resetChoiceState()
	token := putChoice(ChoiceRequest{
		URL:       "https://soundcloud.com/a/b",
		UserID:    1,
		ExpiresAt: time.Now().Add(-1 * time.Minute),
	})
	_, ok, msg := takeChoice(token, 1)
	if ok {
		t.Fatalf("expected expired choice")
	}
	if !strings.Contains(msg, "устарел") {
		t.Fatalf("unexpected expired message: %q", msg)
	}
}
