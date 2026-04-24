package app

import (
	"strings"
	"testing"
	"time"
)

func TestDescribeTimeOfDay(t *testing.T) {
	cases := []struct {
		hour int
		want string
	}{
		{hour: 6, want: "утро"},
		{hour: 12, want: "день"},
		{hour: 19, want: "вечер"},
		{hour: 2, want: "ночь"},
	}
	for _, tc := range cases {
		if got := describeTimeOfDay(tc.hour); got != tc.want {
			t.Fatalf("describeTimeOfDay(%d)=%q want=%q", tc.hour, got, tc.want)
		}
	}
}

func TestRussianWeekdayName(t *testing.T) {
	cases := []struct {
		day  time.Weekday
		want string
	}{
		{day: time.Monday, want: "понедельник"},
		{day: time.Tuesday, want: "вторник"},
		{day: time.Wednesday, want: "среда"},
		{day: time.Thursday, want: "четверг"},
		{day: time.Friday, want: "пятница"},
		{day: time.Saturday, want: "суббота"},
		{day: time.Sunday, want: "воскресенье"},
	}
	for _, tc := range cases {
		if got := russianWeekdayName(tc.day); got != tc.want {
			t.Fatalf("russianWeekdayName(%v)=%q want=%q", tc.day, got, tc.want)
		}
	}
}

func TestLoadTemplateLocationFallback(t *testing.T) {
	if loadTemplateLocation("") == nil {
		t.Fatal("expected local location for empty timezone")
	}
	if loadTemplateLocation("Definitely/Unknown_Timezone") == nil {
		t.Fatal("expected local location fallback for invalid timezone")
	}
	if got := loadTemplateLocation("Europe/Moscow"); got == nil || !strings.Contains(got.String(), "Europe/Moscow") {
		t.Fatalf("expected Europe/Moscow location, got=%v", got)
	}
}

func TestResolveWeatherNowUsesCache(t *testing.T) {
	templateWeatherCache.mu.Lock()
	old := templateWeatherCache.items
	templateWeatherCache.items = map[string]weatherCacheEntry{
		"рязань": {
			value:     "пасмурно, 5°C (ощущается как 2°C)",
			expiresAt: time.Now().Add(5 * time.Minute),
		},
	}
	templateWeatherCache.mu.Unlock()
	defer func() {
		templateWeatherCache.mu.Lock()
		templateWeatherCache.items = old
		templateWeatherCache.mu.Unlock()
	}()

	got := resolveWeatherNow("Рязань")
	if got != "пасмурно, 5°C (ощущается как 2°C)" {
		t.Fatalf("unexpected cached weather: %q", got)
	}
}

func TestRenderResponseTemplateWeatherTagFromCache(t *testing.T) {
	templateWeatherCache.mu.Lock()
	old := templateWeatherCache.items
	templateWeatherCache.items = map[string]weatherCacheEntry{
		"рязань": {
			value:     "ясно, 12°C (ощущается как 11°C)",
			expiresAt: time.Now().Add(5 * time.Minute),
		},
	}
	templateWeatherCache.mu.Unlock()
	defer func() {
		templateWeatherCache.mu.Lock()
		templateWeatherCache.items = old
		templateWeatherCache.mu.Unlock()
	}()

	got, err := renderResponseTemplate(`{{ weather "Рязань" }}`, map[string]interface{}{}, nil)
	if err != nil {
		t.Fatalf("renderResponseTemplate error: %v", err)
	}
	if got != "ясно, 12°C (ощущается как 11°C)" {
		t.Fatalf("unexpected weather render: %q", got)
	}
}

func TestChanceFunctionBoundaries(t *testing.T) {
	f, ok := responseTemplateFuncs["chance"]
	if !ok {
		t.Fatal("chance function is missing")
	}
	chanceFn, ok := f.(func(int) bool)
	if !ok {
		t.Fatalf("unexpected chance func type: %T", f)
	}
	if chanceFn(0) {
		t.Fatal("chance(0) must be false")
	}
	if !chanceFn(100) {
		t.Fatal("chance(100) must be true")
	}
}

func TestWeatherCityCandidatesInflectionFallback(t *testing.T) {
	got := weatherCityCandidates("Рязани")
	if len(got) == 0 {
		t.Fatal("expected candidates")
	}
	hasOrig := false
	hasNormalized := false
	for _, v := range got {
		if strings.EqualFold(v, "Рязани") {
			hasOrig = true
		}
		if strings.EqualFold(v, "Рязань") {
			hasNormalized = true
		}
	}
	if !hasOrig {
		t.Fatalf("expected original city in candidates: %v", got)
	}
	if !hasNormalized {
		t.Fatalf("expected normalized city variant in candidates: %v", got)
	}
}

func TestSanitizeWebSearchOutputRemovesLinks(t *testing.T) {
	in := "1) Заголовок — суть (https://example.org/path)\n2) [источник](https://foo.bar/x)\n3) <https://baz.test/q>"
	got := sanitizeWebSearchOutput(in)
	if strings.Contains(got, "http://") || strings.Contains(got, "https://") {
		t.Fatalf("sanitized output must not contain urls: %q", got)
	}
	if strings.Contains(got, "[источник](") {
		t.Fatalf("sanitized output must not contain markdown links: %q", got)
	}
	if !strings.Contains(got, "Заголовок") {
		t.Fatalf("sanitized output lost content: %q", got)
	}
}

func TestParseWebSearchCallDefault(t *testing.T) {
	opts := parseWebSearchCall("найди анекдот про штирлица")
	if opts.Query != "найди анекдот про штирлица" {
		t.Fatalf("unexpected query: %q", opts.Query)
	}
	if opts.Condition != "" {
		t.Fatalf("unexpected condition: %q", opts.Condition)
	}
	if opts.Compact {
		t.Fatal("compact must be false by default")
	}
}

func TestParseWebSearchCallCompactFlag(t *testing.T) {
	opts := parseWebSearchCall("найди анекдот про штирлица", "компактно")
	if opts.Query != "найди анекдот про штирлица" {
		t.Fatalf("unexpected query: %q", opts.Query)
	}
	if !opts.Compact {
		t.Fatal("compact must be true")
	}
}

func TestParseWebSearchCallPipelineCondition(t *testing.T) {
	opts := parseWebSearchCall("Искать только если запрос про мем, юмор или анекдот.", "расскажи анекдот про штирлица")
	if opts.Query != "расскажи анекдот про штирлица" {
		t.Fatalf("unexpected query: %q", opts.Query)
	}
	if opts.Condition != "Искать только если запрос про мем, юмор или анекдот." {
		t.Fatalf("unexpected condition: %q", opts.Condition)
	}
}

func TestParseWebSearchCallPipelineCompact(t *testing.T) {
	opts := parseWebSearchCall("компактно", "найди анекдот про штирлица")
	if opts.Query != "найди анекдот про штирлица" {
		t.Fatalf("unexpected query: %q", opts.Query)
	}
	if opts.Condition != "" {
		t.Fatalf("unexpected condition: %q", opts.Condition)
	}
	if !opts.Compact {
		t.Fatal("compact must be true")
	}
}

func TestBuildWebSearchPromptModes(t *testing.T) {
	full := buildWebSearchPrompt("расскажи анекдот про штирлица", false)
	if strings.Contains(strings.ToLower(full), "короткие пункты") {
		t.Fatalf("full prompt unexpectedly asks for compact output: %q", full)
	}
	if !strings.Contains(strings.ToLower(full), "умеренно подробную") {
		t.Fatalf("full prompt must request non-compact output: %q", full)
	}

	compact := buildWebSearchPrompt("расскажи анекдот про штирлица", true)
	if !strings.Contains(strings.ToLower(compact), "короткие пункты") {
		t.Fatalf("compact prompt must request compact output: %q", compact)
	}
}

func TestRenderResponseTemplateRegexpReplace(t *testing.T) {
	got, err := renderResponseTemplate(
		`{{ trim (regexp_replace "(?i)(?:оле-ням|оленям)[\\s,.:!?-]*" "" .message) }}`,
		map[string]interface{}{"message": "Оле-ням, 67?"},
		nil,
	)
	if err != nil {
		t.Fatalf("renderResponseTemplate error: %v", err)
	}
	if got != "67?" {
		t.Fatalf("unexpected regexp_replace result: %q", got)
	}
}
