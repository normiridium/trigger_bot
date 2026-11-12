package app

import (
	"strings"
	"testing"
	"time"
)

func TestParseCronSpecMatches(t *testing.T) {
	spec, err := parseCronSpec("53 10 * * *")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	at := time.Date(2026, 11, 2, 10, 53, 0, 0, time.UTC)
	if !spec.Matches(at) {
		t.Fatalf("expected match at %s", at)
	}
	if spec.Matches(at.Add(time.Minute)) {
		t.Fatal("must not match a minute later")
	}
	if spec.Matches(time.Date(2026, 11, 2, 11, 53, 0, 0, time.UTC)) {
		t.Fatal("must not match another hour")
	}
}

func TestParseCronSpecAcceptsSixFieldsAndSteps(t *testing.T) {
	// Leading seconds field is dropped (models sometimes emit 6 fields).
	spec, err := parseCronSpec("0 0 9 * * 1-5")
	if err != nil {
		t.Fatalf("parse six fields: %v", err)
	}
	monday := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC) // Monday
	if !spec.Matches(monday) {
		t.Fatal("expected weekday match")
	}
	sunday := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	if spec.Matches(sunday) {
		t.Fatal("sunday must not match weekdays")
	}

	step, err := parseCronSpec("*/15 * * * *")
	if err != nil {
		t.Fatalf("parse step: %v", err)
	}
	for _, minute := range []int{0, 15, 30, 45} {
		if !step.Matches(time.Date(2026, 11, 2, 5, minute, 0, 0, time.UTC)) {
			t.Fatalf("expected match at minute %d", minute)
		}
	}
	if step.Matches(time.Date(2026, 11, 2, 5, 7, 0, 0, time.UTC)) {
		t.Fatal("minute 7 must not match */15")
	}
}

func TestParseCronSpecRejectsBadInput(t *testing.T) {
	for _, expr := range []string{"", "* * *", "70 * * * *", "* 25 * * *", "abc * * * *"} {
		if _, err := parseCronSpec(expr); err == nil {
			t.Fatalf("expected error for %q", expr)
		}
	}
}

func TestExtractScheduleDirectives(t *testing.T) {
	answer := "Договорились, буду тегать тебя утром.\n\n`53 10 * * *|тегать @Anny_Wlne и желать хорошего настроения`"
	cleaned, dirs := ExtractScheduleDirectives(answer)
	if len(dirs) != 1 {
		t.Fatalf("expected 1 directive, got %d", len(dirs))
	}
	if dirs[0].Cron != "53 10 * * *" {
		t.Fatalf("bad cron: %q", dirs[0].Cron)
	}
	if dirs[0].Action != "тегать @Anny_Wlne и желать хорошего настроения" {
		t.Fatalf("bad action: %q", dirs[0].Action)
	}
	if strings.Contains(cleaned, "53 10") {
		t.Fatalf("directive must be stripped: %q", cleaned)
	}
	if !strings.Contains(cleaned, "Договорились") {
		t.Fatalf("answer text must survive: %q", cleaned)
	}
}

func TestExtractScheduleDirectivesKeepsBrokenBlocks(t *testing.T) {
	answer := "Вот пример кода: `not a cron|просто текст`"
	cleaned, dirs := ExtractScheduleDirectives(answer)
	if len(dirs) != 0 {
		t.Fatalf("broken block must not produce a directive")
	}
	if cleaned != answer {
		t.Fatalf("broken block must stay untouched, got %q", cleaned)
	}
}

func TestExtractScheduleDirectivesMultiple(t *testing.T) {
	answer := "Ок.\n`0 9 * * *|напоминать про воду`\n`30 21 * * *|напоминать про сон`"
	_, dirs := ExtractScheduleDirectives(answer)
	if len(dirs) != 2 {
		t.Fatalf("expected 2 directives, got %d", len(dirs))
	}
}
