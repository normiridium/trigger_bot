package pick

import (
	"strings"
	"testing"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

func resetPickState() {
	pickMu.Lock()
	defer pickMu.Unlock()
	pickRequests = make(map[string]PickRequest)
}

func TestBuildPickKeyboard_StoresReplyAndDeleteFlags(t *testing.T) {
	resetPickState()
	msg := &tgbotapi.Message{
		MessageID: 777,
		Chat:      &tgbotapi.Chat{ID: -100123},
		From:      &tgbotapi.User{ID: 42},
	}

	replyTo := 555
	sourceMsgID := 777
	kb := BuildPickKeyboard(msg, replyTo, sourceMsgID, true, []PickTrack{{
		ID:          "track-1",
		Artist:      "Artist",
		Title:       "Song",
		DurationSec: 213,
	}})

	if len(kb.InlineKeyboard) != 2 {
		t.Fatalf("expected 2 rows (track + cancel), got %d", len(kb.InlineKeyboard))
	}
	data := kb.InlineKeyboard[0][0].CallbackData
	if data == nil || !strings.HasPrefix(*data, "vkpick:") {
		t.Fatalf("unexpected callback data: %#v", data)
	}
	token := strings.TrimPrefix(*data, "vkpick:")
	req, ok, msgText := takePick(token, 42)
	if !ok {
		t.Fatalf("expected pick token to be valid, got msg=%q", msgText)
	}
	if req.ReplyTo != replyTo {
		t.Fatalf("replyTo mismatch: got=%d want=%d", req.ReplyTo, replyTo)
	}
	if req.SourceMsgID != sourceMsgID {
		t.Fatalf("sourceMsgID mismatch: got=%d want=%d", req.SourceMsgID, sourceMsgID)
	}
	if !req.DeleteSource {
		t.Fatalf("expected DeleteSource=true")
	}
}

func TestBuildPickKeyboard_CancelInheritsDeleteSource(t *testing.T) {
	resetPickState()
	msg := &tgbotapi.Message{
		MessageID: 777,
		Chat:      &tgbotapi.Chat{ID: -100123},
		From:      &tgbotapi.User{ID: 42},
	}

	kb := BuildPickKeyboard(msg, 0, msg.MessageID, true, []PickTrack{{ID: "track-1", Title: "Song"}})
	if len(kb.InlineKeyboard) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(kb.InlineKeyboard))
	}
	cancelData := kb.InlineKeyboard[1][0].CallbackData
	if cancelData == nil || !strings.HasPrefix(*cancelData, "vkpick_cancel:") {
		t.Fatalf("unexpected cancel callback data: %#v", cancelData)
	}
	cancelToken := strings.TrimPrefix(*cancelData, "vkpick_cancel:")
	req, ok, msgText := takePick(cancelToken, 42)
	if !ok {
		t.Fatalf("expected cancel token to be valid, got msg=%q", msgText)
	}
	if !req.DeleteSource {
		t.Fatalf("expected cancel request to keep DeleteSource=true")
	}
}

func TestTakePick_RejectsOtherUser(t *testing.T) {
	resetPickState()
	token := putPick(PickRequest{UserID: 111})
	_, ok, msgText := takePick(token, 222)
	if ok {
		t.Fatalf("expected token usage to be rejected for different user")
	}
	if msgText == "" {
		t.Fatalf("expected human-readable rejection message")
	}
}
