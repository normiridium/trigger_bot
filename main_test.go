package main

import (
	"testing"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

func TestApplyCapturingTemplate(t *testing.T) {
	got := applyCapturingTemplate("доброе фото {{capturing_text}}", "  навальный  ")
	if got != "доброе фото навальный" {
		t.Fatalf("unexpected template result: %q", got)
	}
	if applyCapturingTemplate("", "x") != "" {
		t.Fatalf("empty template must stay empty")
	}
	if applyCapturingTemplate("без плейсхолдера", "x") != "без плейсхолдера" {
		t.Fatalf("template without placeholder must stay unchanged")
	}
}

func TestBuildImageSearchQueryFromMessage(t *testing.T) {
	msg := &tgbotapi.Message{
		Chat: &tgbotapi.Chat{ID: -1001, Title: "Чат"},
		From: &tgbotapi.User{ID: 7, FirstName: "Аня", UserName: "anya"},
		Text: "покажи кота",
	}

	gotDefault := buildImageSearchQueryFromMessage(nil, "", msg, "")
	if gotDefault != "покажи кота" {
		t.Fatalf("default query mismatch: %q", gotDefault)
	}

	got := buildImageSearchQueryFromMessage(nil, "доброе фото {{capturing_text}} для {{user_first_name}}", msg, "кац")
	if got != "доброе фото кац для Аня" {
		t.Fatalf("query mismatch: %q", got)
	}
}

func TestParseIdleMinutes(t *testing.T) {
	if v, ok := parseIdleMinutes("120"); !ok || v != 120 {
		t.Fatalf("expected parsed 120, got v=%d ok=%v", v, ok)
	}
	if _, ok := parseIdleMinutes("0"); ok {
		t.Fatalf("zero must be invalid")
	}
	if _, ok := parseIdleMinutes("abc"); ok {
		t.Fatalf("non-number must be invalid")
	}
}

func TestChatIdleTrackerFlow(t *testing.T) {
	tr := newChatIdleTracker()
	chatID := int64(-42)
	base := time.Unix(1_700_000_000, 0)

	tr.Seen(chatID, base)
	if tr.ShouldAutoReply(chatID, time.Hour, base.Add(59*time.Minute)) {
		t.Fatalf("must not auto-reply before idle threshold")
	}
	if !tr.ShouldAutoReply(chatID, time.Hour, base.Add(60*time.Minute)) {
		t.Fatalf("must auto-reply at idle threshold")
	}

	tr.MarkActivity(chatID, base.Add(61*time.Minute))
	if tr.ShouldAutoReply(chatID, time.Hour, base.Add(120*time.Minute)) {
		t.Fatalf("must not auto-reply right after activity")
	}
	if !tr.ShouldAutoReply(chatID, time.Hour, base.Add(121*time.Minute)) {
		t.Fatalf("must auto-reply after new idle period")
	}
}

func TestSelectIdleAutoReplyTrigger(t *testing.T) {
	msg := &tgbotapi.Message{
		MessageID: 1,
		Chat:      &tgbotapi.Chat{ID: -1001},
		From:      &tgbotapi.User{ID: 100},
		Text:      "привет",
	}
	items := []Trigger{
		{ID: 1, Enabled: true, MatchType: "idle", MatchText: "90", ActionType: "send", TriggerMode: "all", AdminMode: "anybody", Chance: 100},
		{ID: 2, Enabled: true, MatchType: "idle", MatchText: "abc", ActionType: "gpt_prompt", TriggerMode: "all", AdminMode: "anybody", Chance: 100},
		{ID: 3, Enabled: true, MatchType: "idle", MatchText: "30", ActionType: "gpt_prompt", TriggerMode: "all", AdminMode: "admins", Chance: 100},
		{ID: 4, Enabled: true, MatchType: "idle", MatchText: "45", ActionType: "gpt_prompt", TriggerMode: "all", AdminMode: "anybody", Chance: 100},
	}

	got, after := selectIdleAutoReplyTrigger(nil, msg, items, func() bool { return false })
	if got == nil || got.ID != 4 {
		t.Fatalf("expected trigger id=4, got=%#v", got)
	}
	if after != 45*time.Minute {
		t.Fatalf("expected 45m idle duration, got %s", after)
	}
}

func TestExtractCustomEmojiFromRaw(t *testing.T) {
	raw := &rawMessageWithEmoji{
		Text: "x🙂y",
		Entities: []rawMessageEntity{
			{Type: "custom_emoji", CustomEmojiID: "111", Offset: 1, Length: 2},
			{Type: "custom_emoji", CustomEmojiID: "111"},
			{Type: "bold"},
		},
		Caption: "z🦌w",
		CaptionEntities: []rawMessageEntity{
			{Type: "custom_emoji", CustomEmojiID: "222", Offset: 1, Length: 2},
			{Type: "custom_emoji"},
		},
	}

	hits, count := extractCustomEmojiFromRaw(raw)
	if count != 4 {
		t.Fatalf("expected count=4, got %d", count)
	}
	if len(hits) != 2 {
		t.Fatalf("expected 2 unique IDs, got %d", len(hits))
	}
	if hits[0].ID != "111" || hits[1].ID != "222" {
		t.Fatalf("unexpected ids order/content: %#v", hits)
	}
	if hits[0].Fallback != "🙂" || hits[1].Fallback != "🦌" {
		t.Fatalf("unexpected fallbacks: %#v", hits)
	}
}
