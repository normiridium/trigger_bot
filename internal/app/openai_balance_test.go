package app

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"
)

func TestParseOpenAICostValue(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want float64
	}{
		{name: "number", raw: `0.25`, want: 0.25},
		{name: "string", raw: `"0.25"`, want: 0.25},
		{name: "null", raw: `null`, want: 0},
		{name: "empty string", raw: `""`, want: 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseOpenAICostValue(json.RawMessage(tc.raw))
			if err != nil {
				t.Fatalf("parseOpenAICostValue(%s): %v", tc.raw, err)
			}
			if got != tc.want {
				t.Fatalf("parseOpenAICostValue(%s)=%v, want %v", tc.raw, got, tc.want)
			}
		})
	}
}

func TestParseOpenAICostPayloadStats(t *testing.T) {
	start := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	body := []byte(`{
		"data": [
			{"start_time": 1785542400, "end_time": 1785628800, "results": [{"amount": {"value": "0.10", "currency": "usd"}}]},
			{"start_time": 1785628800, "end_time": 1785715200, "results": [{"amount": {"value": 0.30, "currency": "usd"}}]},
			{"start_time": 1785715200, "end_time": 1785801600, "results": [{"amount": {"value": "0.20", "currency": "usd"}}]}
		]
	}`)
	got, err := parseOpenAICostPayload(body, start, end)
	if err != nil {
		t.Fatalf("parseOpenAICostPayload: %v", err)
	}
	assertFloatNear(t, got.Total, 0.60)
	assertFloatNear(t, got.Today, 0.20)
	assertFloatNear(t, got.Last7Days, 0.60)
	assertFloatNear(t, got.AveragePerDay, 0.24)
	assertFloatNear(t, got.ProjectedMonth, 7.44)
	if got.PeakDay.Format("2006-01-02") != "2026-08-02" || got.PeakDayCost != 0.30 {
		t.Fatalf("unexpected peak day: %s %.4f", got.PeakDay.Format("2006-01-02"), got.PeakDayCost)
	}

	text := formatOpenAIBalanceMessage(got)
	for _, want := range []string{"Сегодня:", "Последние 7 дней:", "Среднее в день:", "Прогноз месяца:", "Пиковый день:"} {
		if !strings.Contains(text, want) {
			t.Fatalf("formatted balance missing %q in:\n%s", want, text)
		}
	}
}

func TestUserGPTTokenLimitSkipsConfiguredBotOwners(t *testing.T) {
	t.Setenv("OWNER_ID", "1519741912")
	t.Setenv("BOT_ADMIN_USER_IDS", "42, 77")

	for _, userID := range []int64{1519741912, 42, 77} {
		if userGPTTokenLimitApplies(userID) {
			t.Fatalf("configured owner/admin %d must be excluded from GPT token limit", userID)
		}
	}
	if !userGPTTokenLimitApplies(12345) {
		t.Fatalf("regular user must stay limited")
	}
}

func assertFloatNear(t *testing.T, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 0.000001 {
		t.Fatalf("got %.8f, want %.8f", got, want)
	}
}
