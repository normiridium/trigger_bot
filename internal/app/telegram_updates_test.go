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
