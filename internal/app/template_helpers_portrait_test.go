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
	setParticipantPortraitRemainingResolver(func(chatID, userID int64) int {
		if chatID == -1001 && userID == 42 {
			return 3
		}
		return participantPortraitBatchSize
	})
	defer setParticipantPortraitRemainingResolver(nil)

	msg := &tgbotapi.Message{
		Chat: &tgbotapi.Chat{ID: -1001, Title: "чат"},
		From: &tgbotapi.User{ID: 42, FirstName: "Тест"},
		Text: "привет",
	}

	replacements := buildMessageTemplateReplacements(nil, msg)
	if got := strings.TrimSpace(replacements["{{user_portrait}}"]); got != "любит короткие ответы" {
		t.Fatalf("unexpected user portrait replacement: %q", got)
	}
	if got := strings.TrimSpace(replacements["{{user_portrait_remaining}}"]); got != "3" {
		t.Fatalf("unexpected user portrait remaining replacement: %q", got)
	}

	out := strings.TrimSpace(renderTemplateWithMessage(templateContext{Msg: msg}, "Портрет: {{ .user_portrait }} (осталось {{ .user_portrait_remaining }})"))
	if out != "Портрет: любит короткие ответы (осталось 3)" {
		t.Fatalf("unexpected rendered output: %q", out)
	}
}

func TestGPT4HLimitTemplateTags(t *testing.T) {
	setGPTLimitResolver(func(chatID, userID int64) gptLimitTemplateInfo {
		if chatID == -1001 && userID == 42 {
			return gptLimitTemplateInfo{
				Remaining:    1500,
				Limit:        20000,
				LowThreshold: 2000,
			}
		}
		return gptLimitTemplateInfo{}
	})
	defer setGPTLimitResolver(nil)

	msg := &tgbotapi.Message{
		Chat: &tgbotapi.Chat{ID: -1001, Title: "чат"},
		From: &tgbotapi.User{ID: 42, FirstName: "Тест"},
		Text: "привет",
	}

	replacements := buildMessageTemplateReplacements(nil, msg)
	if got := strings.TrimSpace(replacements["{{gpt_4h_remaining}}"]); got != "1500" {
		t.Fatalf("unexpected gpt_4h_remaining replacement: %q", got)
	}
	if got := strings.TrimSpace(replacements["{{gpt_4h_limit}}"]); got != "20000" {
		t.Fatalf("unexpected gpt_4h_limit replacement: %q", got)
	}
	if got := strings.TrimSpace(replacements["{{gpt_4h_low_threshold}}"]); got != "2000" {
		t.Fatalf("unexpected gpt_4h_low_threshold replacement: %q", got)
	}

	out := strings.TrimSpace(renderTemplateWithMessage(templateContext{Msg: msg}, `{{ if and (gt .gpt_4h_limit 0) (lt .gpt_4h_remaining .gpt_4h_low_threshold) }}устала{{ end }}`))
	if out != "устала" {
		t.Fatalf("unexpected rendered output: %q", out)
	}
}
