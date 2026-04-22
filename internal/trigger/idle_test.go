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

func TestIdleTrackerLifecycle(t *testing.T) {
	tr := NewIdleTracker()
	if tr == nil {
		t.Fatal("expected tracker")
	}
	chatID := int64(-1001)
	base := time.Now()

	tr.Seen(chatID, base)
	if tr.ShouldAutoReply(chatID, 5*time.Minute, base.Add(4*time.Minute)) {
		t.Fatal("must not auto reply before idle threshold")
	}
	if !tr.ShouldAutoReply(chatID, 5*time.Minute, base.Add(5*time.Minute)) {
		t.Fatal("expected auto reply at threshold")
	}

	tr.MarkActivity(chatID, base.Add(6*time.Minute))
	if tr.ShouldAutoReply(chatID, 5*time.Minute, base.Add(10*time.Minute)) {
		t.Fatal("must not auto reply right after activity")
	}
	if !tr.ShouldAutoReply(chatID, 5*time.Minute, base.Add(11*time.Minute)) {
		t.Fatal("expected auto reply after renewed idle interval")
	}
}

func TestIdleTrackerGuards(t *testing.T) {
	var nilTracker *IdleTracker
	nilTracker.Seen(-100, time.Now())
	nilTracker.MarkActivity(-100, time.Now())
	if nilTracker.ShouldAutoReply(-100, time.Minute, time.Now()) {
		t.Fatal("nil tracker must never auto reply")
	}

	tr := NewIdleTracker()
	if tr.ShouldAutoReply(0, time.Minute, time.Now()) {
		t.Fatal("zero chat id must never auto reply")
	}
	if tr.ShouldAutoReply(-100, 0, time.Now()) {
		t.Fatal("non-positive idle duration must never auto reply")
	}
}

func TestIntParseErrorString(t *testing.T) {
	if got := errInvalidInt.Error(); got != "invalid integer" {
		t.Fatalf("unexpected error text: %q", got)
	}
}
