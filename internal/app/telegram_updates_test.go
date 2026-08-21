package app

import (
	"encoding/json"
	"strings"
	"testing"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

func TestHydrateReplyToMessageTextFromRawRichMarkdown(t *testing.T) {
	msg := &tgbotapi.Message{
		MessageID: 102,
		Chat:      &tgbotapi.Chat{ID: -1001},
		From:      &tgbotapi.User{ID: 1, FirstName: "User"},
		Text:      "Саша Семёнова?",
		ReplyToMessage: &tgbotapi.Message{
			MessageID: 101,
			From:      &tgbotapi.User{ID: 2, FirstName: "Оле-ням", IsBot: true},
		},
	}
	raw := &rawMessageWithEmoji{
		Text: "Саша Семёнова?",
		ReplyToMessage: &rawMessageWithEmoji{
			RichMessage: json.RawMessage(`{"markdown":"# История\n\nСаша Семёнова упоминается в разделе про современность."}`),
		},
	}

	hydrateReplyToMessageTextFromRaw(msg, raw)

	got := strings.TrimSpace(msg.ReplyToMessage.Text)
	if !strings.Contains(got, "Саша Семёнова") || !strings.Contains(got, "# История") {
		t.Fatalf("reply text was not hydrated from rich markdown: %q", got)
	}
}

func TestBuildMessageTemplateReplacements_ReplyTextFromRawRichMarkdown(t *testing.T) {
	msg := &tgbotapi.Message{
		MessageID: 102,
		Chat:      &tgbotapi.Chat{ID: -1001, Title: "chat"},
		From:      &tgbotapi.User{ID: 1, FirstName: "User"},
		Text:      "Саша Семёнова?",
		ReplyToMessage: &tgbotapi.Message{
			MessageID: 101,
			From:      &tgbotapi.User{ID: 2, FirstName: "Оле-ням", IsBot: true},
		},
	}
	raw := &rawMessageWithEmoji{
		Text: "Саша Семёнова?",
		ReplyToMessage: &rawMessageWithEmoji{
			RichMessage: json.RawMessage(`{"markdown":"# История\n\nСаша Семёнова упоминается в разделе про современность."}`),
		},
	}

	hydrateReplyToMessageTextFromRaw(msg, raw)
	replacements := buildMessageTemplateReplacements(nil, msg)

	got := strings.TrimSpace(replacements["{{reply_text}}"])
	if !strings.Contains(got, "Саша Семёнова") || !strings.Contains(got, "# История") {
		t.Fatalf("reply_text did not receive raw rich markdown: %q", got)
	}
}

func TestExtractRichMessageTextHTML(t *testing.T) {
	got := extractRichMessageText(json.RawMessage(`{"html":"<h1>История</h1><p>Саша Семёнова</p>"}`))
	if !strings.Contains(got, "История") || !strings.Contains(got, "Саша Семёнова") {
		t.Fatalf("unexpected rich html text: %q", got)
	}
}

func TestExtractRichMessageTextNestedContentValue(t *testing.T) {
	raw := json.RawMessage(`{
		"blocks": [
			{"type": "image", "media": {"type": "photo", "file_id": "ignored"}},
			{"type": "paragraph", "content": [
				{"type": "text", "value": "FEN: rnbqkbnr/pppppppp/8/8/8/8/PPPPPPPP/RNBQKBNR w KQkq - 0 1"},
				{"type": "text", "value": "Ход: старт (@DearestFaline)"},
				{"type": "text", "value": "Белые: снизу"}
			]}
		]
	}`)

	got := extractRichMessageText(raw)
	if !strings.Contains(got, "FEN: rnbqkbnr/pppppppp/8/8/8/8/PPPPPPPP/RNBQKBNR w KQkq - 0 1") {
		t.Fatalf("rich FEN text was not extracted: %q", got)
	}
	if strings.Contains(got, "ignored") {
		t.Fatalf("media payload leaked into rich text: %q", got)
	}
}
