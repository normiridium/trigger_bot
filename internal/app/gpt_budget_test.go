package app

import (
	"strings"
	"testing"
	"time"
)

func TestTimeUntilGPTWindowReset(t *testing.T) {
	// окно 4 часа по UTC: 08:00–12:00
	at := time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC)
	if got := timeUntilGPTWindowReset(at); got != 2*time.Hour {
		t.Fatalf("ожидали 2h до сброса, получили %v", got)
	}
	at = time.Date(2026, 11, 5, 11, 59, 30, 0, time.UTC)
	if got := timeUntilGPTWindowReset(at); got != 30*time.Second {
		t.Fatalf("ожидали 30s до сброса, получили %v", got)
	}
	// ровно на границе окна — полные 4 часа
	at = time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	if got := timeUntilGPTWindowReset(at); got != 4*time.Hour {
		t.Fatalf("на границе ожидали 4h, получили %v", got)
	}
}

func TestHumanDuration(t *testing.T) {
	cases := map[time.Duration]string{
		20 * time.Second:  "меньше минуты",
		42 * time.Minute:  "42 мин",
		80 * time.Minute:  "1 ч 20 мин",
		2 * time.Hour:     "2 ч",
		195 * time.Minute: "3 ч 15 мин",
	}
	for in, want := range cases {
		if got := humanDuration(in); got != want {
			t.Fatalf("humanDuration(%v)=%q, ожидали %q", in, got, want)
		}
	}
}

func TestGPTBudgetRefusalMentionsNumbers(t *testing.T) {
	snap := gptBudgetSnapshot{Limited: true, Remaining: 900, Limit: 12000, ResetIn: 95 * time.Minute}
	text := gptBudgetRefusal(snap, gptCostSummary, "сводку чата")
	for _, want := range []string{"сводку чата", "900", "12000", "4000", "1 ч 35 мин"} {
		if !strings.Contains(text, want) {
			t.Fatalf("в отказе нет %q: %s", want, text)
		}
	}
}

func TestGPTBudgetRefusalFallsBackToThis(t *testing.T) {
	snap := gptBudgetSnapshot{Limited: true, Remaining: 10, Limit: 12000, ResetIn: time.Minute}
	text := gptBudgetRefusal(snap, gptMinRequestTokens, "")
	if !strings.Contains(text, "за это") {
		t.Fatalf("ожидали подстановку «это»: %s", text)
	}
}
