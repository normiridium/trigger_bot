package engine

import (
	"testing"
	"time"

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

func TestEngineSelectCooldownChance101_OncePer24h(t *testing.T) {
	engine := NewTriggerEngine()
	base := time.Unix(1_700_000_000, 0)
	cooldownNow = func() time.Time { return base }
	defer func() { cooldownNow = time.Now }()

	triggerCooldownState.mu.Lock()
	triggerCooldownState.last = make(map[string]time.Time)
	triggerCooldownState.mu.Unlock()

	msg := &tgbotapi.Message{
		MessageID: 500,
		Chat:      &tgbotapi.Chat{ID: -2001},
		From:      &tgbotapi.User{ID: 1},
		Text:      "тревога",
	}
	triggers := []model.Trigger{
		{ID: 101, Enabled: true, TriggerMode: "all", MatchText: "тревога", MatchType: "partial", Chance: 101},
	}

	got1 := engine.Select(SelectInput{Msg: msg, Text: msg.Text, Triggers: triggers, IsAdminFn: func() bool { return false }})
	if got1 == nil {
		t.Fatalf("expected first cooldown hit")
	}
	got2 := engine.Select(SelectInput{Msg: msg, Text: msg.Text, Triggers: triggers, IsAdminFn: func() bool { return false }})
	if got2 != nil {
		t.Fatalf("expected second hit within window to be blocked")
	}
	base = base.Add(24*time.Hour + time.Second)
	got3 := engine.Select(SelectInput{Msg: msg, Text: msg.Text, Triggers: triggers, IsAdminFn: func() bool { return false }})
	if got3 == nil {
		t.Fatalf("expected hit after 24h window")
	}
}

func TestEngineSelectCooldownChance102_OncePer12h(t *testing.T) {
	engine := NewTriggerEngine()
	base := time.Unix(1_700_100_000, 0)
	cooldownNow = func() time.Time { return base }
	defer func() { cooldownNow = time.Now }()

	triggerCooldownState.mu.Lock()
	triggerCooldownState.last = make(map[string]time.Time)
	triggerCooldownState.mu.Unlock()

	msg := &tgbotapi.Message{
		MessageID: 501,
		Chat:      &tgbotapi.Chat{ID: -2002},
		From:      &tgbotapi.User{ID: 1},
		Text:      "рпп",
	}
	triggers := []model.Trigger{
		{ID: 102, Enabled: true, TriggerMode: "all", MatchText: "рпп", MatchType: "partial", Chance: 102},
	}

	got1 := engine.Select(SelectInput{Msg: msg, Text: msg.Text, Triggers: triggers, IsAdminFn: func() bool { return false }})
	if got1 == nil {
		t.Fatalf("expected first cooldown hit")
	}
	base = base.Add(11*time.Hour + 59*time.Minute)
	got2 := engine.Select(SelectInput{Msg: msg, Text: msg.Text, Triggers: triggers, IsAdminFn: func() bool { return false }})
	if got2 != nil {
		t.Fatalf("expected blocked hit before 12h window")
	}
	base = base.Add(2 * time.Minute)
	got3 := engine.Select(SelectInput{Msg: msg, Text: msg.Text, Triggers: triggers, IsAdminFn: func() bool { return false }})
	if got3 == nil {
		t.Fatalf("expected hit after 12h window")
	}
}
