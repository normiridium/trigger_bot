package app

import (
	"strings"
	"testing"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

func TestUserPortraitTemplateTag(t *testing.T) {
	setParticipantPortraitResolver(func(chatID, userID int64) string {
		if chatID == -1001 && userID == 42 {
			return "любит короткие ответы"
		}
		return ""
	})
	defer setParticipantPortraitResolver(nil)

	msg := &tgbotapi.Message{
		Chat: &tgbotapi.Chat{ID: -1001, Title: "чат"},
		From: &tgbotapi.User{ID: 42, FirstName: "Тест"},
		Text: "привет",
	}

	replacements := buildMessageTemplateReplacements(nil, msg)
	if got := strings.TrimSpace(replacements["{{user_portrait}}"]); got != "любит короткие ответы" {
		t.Fatalf("unexpected user portrait replacement: %q", got)
	}

	out := strings.TrimSpace(renderTemplateWithMessage(templateContext{Msg: msg}, "Портрет: {{ .user_portrait }}"))
	if out != "Портрет: любит короткие ответы" {
		t.Fatalf("unexpected rendered output: %q", out)
	}
}
