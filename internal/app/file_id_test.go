package app

import (
	"strings"
	"testing"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

func TestExtractMessageFileIDs(t *testing.T) {
	msg := &tgbotapi.Message{
		Voice:    &tgbotapi.Voice{FileID: "voice-id", Duration: 12},
		Document: &tgbotapi.Document{FileID: "doc-id", FileName: "sample.ogg"},
		Photo: []tgbotapi.PhotoSize{
			{FileID: "small-photo", Width: 90, Height: 90},
			{FileID: "big-photo", Width: 640, Height: 640},
		},
	}
	items := extractMessageFileIDs(msg)
	if len(items) != 3 {
		t.Fatalf("items len = %d, want 3", len(items))
	}
	got := buildFileIDReply(items)
	for _, want := range []string{"voice-id", "doc-id", "big-photo"} {
		if !strings.Contains(got, want) {
			t.Fatalf("reply %q does not contain %q", got, want)
		}
	}
}

func TestExtractPrivateAutoFileIDsKeepsSpecializedPrivateHandlers(t *testing.T) {
	msg := &tgbotapi.Message{
		Voice:     &tgbotapi.Voice{FileID: "voice-id", Duration: 12},
		Sticker:   &tgbotapi.Sticker{FileID: "sticker-id", SetName: "set"},
		Animation: &tgbotapi.Animation{FileID: "gif-id", FileName: "a.gif"},
	}
	items := extractPrivateAutoFileIDs(msg)
	if len(items) != 1 {
		t.Fatalf("items len = %d, want 1", len(items))
	}
	if items[0].Kind != "voice" || items[0].FileID != "voice-id" {
		t.Fatalf("unexpected private auto item: %#v", items[0])
	}
}
