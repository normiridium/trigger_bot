package match

import (
	"testing"

	"trigger-admin-bot/internal/model"
)

func TestNormalizeMatchType(t *testing.T) {
	if got := NormalizeMatchType("regexp"); got != model.MatchTypeRegex {
		t.Fatalf("expected regex, got %q", got)
	}
	if got := NormalizeMatchType("startswith"); got != model.MatchTypeStarts {
		t.Fatalf("expected starts, got %q", got)
	}
}

func TestTriggerMatchCaptureRegex(t *testing.T) {
	tr := model.Trigger{MatchType: model.MatchTypeRegex, MatchText: `привет (.+)`, CaseSensitive: false}
	ok, cap := TriggerMatchCapture(tr, "Привет мир")
	if !ok {
		t.Fatal("expected regex match")
	}
	if cap != "мир" {
		t.Fatalf("unexpected capture: %q", cap)
	}
}

func TestTriggerMatchesPartial(t *testing.T) {
	tr := model.Trigger{MatchType: model.MatchTypePartial, MatchText: "кот", CaseSensitive: false}
	if !TriggerMatches(tr, "большой КОТ") {
		t.Fatal("expected partial match")
	}
}

func TestStripLeadingCaseInsensitiveFlag(t *testing.T) {
	if got := StripLeadingCaseInsensitiveFlag("(?i) (?i) abc"); got != "abc" {
		t.Fatalf("unexpected stripped value: %q", got)
	}
}
