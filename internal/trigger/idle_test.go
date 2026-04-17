package trigger

import (
	"testing"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"trigger-admin-bot/internal/model"
)

func TestParseIdleDuration(t *testing.T) {
	if got := parseIdleDuration("15m"); got != 15*time.Minute {
		t.Fatalf("unexpected duration: %s", got)
	}
	if got := parseIdleDuration("30"); got != 30*time.Minute {
		t.Fatalf("unexpected minute duration: %s", got)
	}
	if got := parseIdleDuration("abc"); got != 0 {
		t.Fatalf("expected zero for invalid value, got %s", got)
	}
}

func TestParseInt(t *testing.T) {
	if got, err := parseInt("42"); err != nil || got != 42 {
		t.Fatalf("unexpected parseInt result got=%d err=%v", got, err)
	}
	if _, err := parseInt("0"); err == nil {
		t.Fatal("expected error for zero")
	}
	if _, err := parseInt("1a"); err == nil {
		t.Fatal("expected error for invalid integer")
	}
}

func TestSelectIdleAutoReplyTrigger(t *testing.T) {
	msg := &tgbotapi.Message{Chat: &tgbotapi.Chat{ID: -100}, From: &tgbotapi.User{ID: 7}, Text: "hi"}
	items := []model.Trigger{
		{ID: 1, Enabled: true, MatchType: model.MatchTypeIdle, MatchText: "bad", ActionType: model.ActionTypeGPTPrompt, TriggerMode: model.TriggerModeAll, AdminMode: model.AdminModeAnybody, Chance: 100},
		{ID: 2, Enabled: true, MatchType: model.MatchTypeIdle, MatchText: "45", ActionType: model.ActionTypeGPTPrompt, TriggerMode: model.TriggerModeAll, AdminMode: model.AdminModeAnybody, Chance: 100},
	}
	got, after := SelectIdleAutoReplyTrigger(nil, msg, items, func() bool { return false })
	if got == nil || got.ID != 2 {
		t.Fatalf("expected trigger id=2, got=%#v", got)
	}
	if after != 45*time.Minute {
		t.Fatalf("unexpected idle duration: %s", after)
	}
}
