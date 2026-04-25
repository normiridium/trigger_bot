package app

import (
	"strings"
	"testing"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

func TestBuildMessageTemplateReplacements_ReplyTextFromAudioMeta(t *testing.T) {
	msg := &tgbotapi.Message{
		Chat: &tgbotapi.Chat{ID: -1001, Title: "chat"},
		From: &tgbotapi.User{ID: 1, FirstName: "User"},
		Text: "about what song?",
		ReplyToMessage: &tgbotapi.Message{
			From: &tgbotapi.User{ID: 2, FirstName: "DJ"},
			Audio: &tgbotapi.Audio{
				Title:     "L'amour Toujours",
				Performer: "Gigi D'Agostino",
			},
		},
	}

	replacements := buildMessageTemplateReplacements(nil, msg)
	if got := replacements["{{reply_text}}"]; got != "Gigi D'Agostino - L'amour Toujours" {
		t.Fatalf("expected audio metadata fallback, got %q", got)
	}
}

func TestFirstNonEmptyMessageContent_Fallbacks(t *testing.T) {
	if got := firstNonEmptyMessageContent(&tgbotapi.Message{Caption: "from caption"}); got != "from caption" {
		t.Fatalf("expected caption fallback, got %q", got)
	}

	if got := firstNonEmptyMessageContent(&tgbotapi.Message{
		Document: &tgbotapi.Document{FileName: "track.flac"},
	}); got != "track.flac" {
		t.Fatalf("expected document filename fallback, got %q", got)
	}

	if got := firstNonEmptyMessageContent(&tgbotapi.Message{
		Audio: &tgbotapi.Audio{FileName: "Großes Rundfunkorchester Live.mp3"},
	}); got != "Großes Rundfunkorchester Live.mp3" {
		t.Fatalf("expected audio filename fallback, got %q", got)
	}

	if got := firstNonEmptyMessageContent(&tgbotapi.Message{
		Caption: "02:58 | 4.68MB | 211Kbps",
		Audio: &tgbotapi.Audio{
			Title:     "La Mort d'Arthur",
			Performer: "Sopor Aeternus & The Ensemble Of Shadows",
		},
	}); got != "Sopor Aeternus & The Ensemble Of Shadows - La Mort d'Arthur" {
		t.Fatalf("expected audio meta to win over caption, got %q", got)
	}

	if got := firstNonEmptyMessageContent(&tgbotapi.Message{
		Photo: []tgbotapi.PhotoSize{{FileID: "x"}},
	}); got != "photo" {
		t.Fatalf("expected photo fallback, got %q", got)
	}
}

func TestBuildReplyAudioText(t *testing.T) {
	got := buildReplyAudioText(replyAudioDetails{
		Title:  "La Mort d'Arthur",
		Artist: "Sopor Aeternus",
		Album:  "Les Fleurs Du Mal",
		Year:   "2007",
		Track:  "7",
		Text:   "line1\nline2",
	})
	if got == "" {
		t.Fatal("expected non-empty formatted reply audio text")
	}
	if want := "Sopor Aeternus - La Mort d'Arthur"; !strings.Contains(got, want) {
		t.Fatalf("missing head %q in %q", want, got)
	}
	if !strings.Contains(got, "album: Les Fleurs Du Mal") {
		t.Fatalf("missing album in %q", got)
	}
	if !strings.Contains(got, "line1") {
		t.Fatalf("missing lyrics text in %q", got)
	}
}

func TestPickReplyAudioTextFromTags(t *testing.T) {
	if got := pickReplyAudioTextFromTags(map[string]string{
		"lyrics-eng": "line1\nline2",
	}); got != "line1\nline2" {
		t.Fatalf("expected lyrics-eng fallback, got %q", got)
	}
	if got := pickReplyAudioTextFromTags(map[string]string{
		"TEXT":       "from text",
		"lyrics-eng": "from lyrics-eng",
	}); got != "from text" {
		t.Fatalf("expected TEXT priority, got %q", got)
	}
}
