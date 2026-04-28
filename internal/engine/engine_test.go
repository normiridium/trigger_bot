package engine

import (
	"testing"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"trigger-admin-bot/internal/model"
)

func TestEngineSelectSkipsModeMismatchAndPicksNext(t *testing.T) {
	engine := NewTriggerEngine()
	engine.randIntn = func(_ int) int { return 0 }

	msg := &tgbotapi.Message{
		MessageID: 100,
		Chat:      &tgbotapi.Chat{ID: -1001},
		From:      &tgbotapi.User{ID: 1},
		Text:      "дмитрий гудков",
	}
	bot := &tgbotapi.BotAPI{Self: tgbotapi.User{ID: 999, UserName: "olenyam_bot"}}

	triggers := []model.Trigger{
		{ID: 10, Title: "reply only", Enabled: true, TriggerMode: "only_replies_to_combot", MatchText: "", MatchType: "partial", ActionType: "gpt_prompt", Chance: 100},
		{ID: 11, Title: "politics", Enabled: true, TriggerMode: "all", MatchText: `дмитрий\s*гудков`, MatchType: "regex", ActionType: "gpt_prompt", Chance: 100},
	}

	got := engine.Select(SelectInput{
		Bot:      bot,
		Msg:      msg,
		Text:     msg.Text,
		Triggers: triggers,
		IsAdminFn: func() bool {
			return false
		},
	})
	if got == nil || got.ID != 11 {
		t.Fatalf("expected trigger 11, got %#v", got)
	}
}

func TestEngineSelectReplyToBotMode(t *testing.T) {
	engine := NewTriggerEngine()
	engine.randIntn = func(_ int) int { return 0 }

	bot := &tgbotapi.BotAPI{Self: tgbotapi.User{ID: 999, UserName: "olenyam_bot"}}
	replyFromBot := &tgbotapi.Message{
		MessageID: 55,
		From:      &tgbotapi.User{ID: 999, IsBot: true, UserName: "olenyam_bot"},
	}
	msg := &tgbotapi.Message{
		MessageID:      100,
		Chat:           &tgbotapi.Chat{ID: -1001},
		From:           &tgbotapi.User{ID: 1},
		Text:           "тест",
		ReplyToMessage: replyFromBot,
	}
	triggers := []model.Trigger{
		{ID: 10, Title: "reply only", Enabled: true, TriggerMode: "only_replies_to_combot", MatchText: "", MatchType: "partial", ActionType: "gpt_prompt", Chance: 100},
	}
	got := engine.Select(SelectInput{
		Bot:      bot,
		Msg:      msg,
		Text:     msg.Text,
		Triggers: triggers,
		IsAdminFn: func() bool {
			return false
		},
	})
	if got == nil || got.ID != 10 {
		t.Fatalf("expected trigger 10 in reply-to-bot mode, got %#v", got)
	}
}

func TestEngineSelectAdminMode(t *testing.T) {
	engine := NewTriggerEngine()
	engine.randIntn = func(_ int) int { return 0 }

	msg := &tgbotapi.Message{
		MessageID: 100,
		Chat:      &tgbotapi.Chat{ID: -1001},
		From:      &tgbotapi.User{ID: 1},
		Text:      "тест",
	}
	bot := &tgbotapi.BotAPI{Self: tgbotapi.User{ID: 999, UserName: "olenyam_bot"}}
	triggers := []model.Trigger{
		{ID: 1, Title: "admins only", Enabled: true, TriggerMode: "all", MatchText: "", MatchType: "partial", AdminMode: "admins", Chance: 100},
		{ID: 2, Title: "fallback", Enabled: true, TriggerMode: "all", MatchText: "", MatchType: "partial", AdminMode: "anybody", Chance: 100},
	}

	got := engine.Select(SelectInput{
		Bot:      bot,
		Msg:      msg,
		Text:     msg.Text,
		Triggers: triggers,
		IsAdminFn: func() bool {
			return false
		},
	})
	if got == nil || got.ID != 2 {
		t.Fatalf("expected fallback trigger for non-admin, got %#v", got)
	}
}

func TestTriggerModeReplyToSelfNoMedia_TextReplyMatches(t *testing.T) {
	bot := &tgbotapi.BotAPI{Self: tgbotapi.User{ID: 999, UserName: "olenyam_bot"}}
	msg := &tgbotapi.Message{
		MessageID: 200,
		From:      &tgbotapi.User{ID: 100},
		Text:      "текстовый ответ",
		ReplyToMessage: &tgbotapi.Message{
			MessageID: 100,
			From:      &tgbotapi.User{ID: 999, IsBot: true, UserName: "olenyam_bot"},
		},
	}
	tr := &model.Trigger{TriggerMode: model.TriggerModeOnlyRepliesToSelfNoMedia}
	if !TriggerModeMatches(bot, tr, msg) {
		t.Fatalf("expected text reply to self-bot to match")
	}
}

func TestTriggerModeReplyToSelfNoMedia_MediaReplySkipped(t *testing.T) {
	bot := &tgbotapi.BotAPI{Self: tgbotapi.User{ID: 999, UserName: "olenyam_bot"}}
	msg := &tgbotapi.Message{
		MessageID: 201,
		From:      &tgbotapi.User{ID: 100},
		Photo:     []tgbotapi.PhotoSize{{FileID: "abc"}},
		ReplyToMessage: &tgbotapi.Message{
			MessageID: 100,
			From:      &tgbotapi.User{ID: 999, IsBot: true, UserName: "olenyam_bot"},
		},
	}
	tr := &model.Trigger{TriggerMode: model.TriggerModeOnlyRepliesToSelfNoMedia}
	if TriggerModeMatches(bot, tr, msg) {
		t.Fatalf("expected media reply to be skipped")
	}
}

func TestTriggerModeReplyToSelfNoMedia_ReplyToMediaMessageSkipped(t *testing.T) {
	bot := &tgbotapi.BotAPI{Self: tgbotapi.User{ID: 999, UserName: "olenyam_bot"}}
	msg := &tgbotapi.Message{
		MessageID: 202,
		From:      &tgbotapi.User{ID: 100},
		Text:      "клевая песня",
		ReplyToMessage: &tgbotapi.Message{
			MessageID: 100,
			From:      &tgbotapi.User{ID: 999, IsBot: true, UserName: "olenyam_bot"},
			Audio:     &tgbotapi.Audio{FileID: "aud"},
		},
	}
	tr := &model.Trigger{TriggerMode: model.TriggerModeOnlyRepliesToSelfNoMedia}
	if TriggerModeMatches(bot, tr, msg) {
		t.Fatalf("expected reply to media message to be skipped")
	}
}

func TestTriggerModeReplyToSelfNoMedia_DocumentReplySkipped(t *testing.T) {
	bot := &tgbotapi.BotAPI{Self: tgbotapi.User{ID: 999, UserName: "olenyam_bot"}}
	msg := &tgbotapi.Message{
		MessageID: 203,
		From:      &tgbotapi.User{ID: 100},
		Document:  &tgbotapi.Document{FileID: "doc-1", FileName: "memo.pdf", MimeType: "application/pdf"},
		ReplyToMessage: &tgbotapi.Message{
			MessageID: 100,
			From:      &tgbotapi.User{ID: 999, IsBot: true, UserName: "olenyam_bot"},
		},
	}
	tr := &model.Trigger{TriggerMode: model.TriggerModeOnlyRepliesToSelfNoMedia}
	if TriggerModeMatches(bot, tr, msg) {
		t.Fatalf("expected document reply to be skipped")
	}
}
