package app

import (
	"strings"
	"testing"
)

func TestClipScheduleAction(t *testing.T) {
	short := "  напоминать про воду  "
	if got := clipScheduleAction(short); got != "напоминать про воду" {
		t.Fatalf("trim failed: %q", got)
	}
	long := strings.Repeat("я", maxScheduleActionRunes+50)
	got := clipScheduleAction(long)
	if len([]rune(got)) != maxScheduleActionRunes {
		t.Fatalf("expected %d runes, got %d", maxScheduleActionRunes, len([]rune(got)))
	}
}
