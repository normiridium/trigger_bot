package app

// Группы захвата в шаблонах:
//   {{ .capturing_text }}          — главная группа (как раньше),
//   {{ index .capturing_groups 2 }} — вторая группа,
//   {{ .capturing_count }}          — сколько групп.

import (
	"strings"
	"testing"

	"trigger-admin-bot/internal/match"
	"trigger-admin-bot/internal/model"
)

func renderWithCaptures(t *testing.T, src string, groups []string) string {
	t.Helper()
	ctx := templateContext{
		CapturingText:  "летов",
		CapturingTexts: groups,
	}
	vars := buildTemplateVars(ctx)
	out, err := renderResponseTemplate(src, vars, nil)
	if err != nil {
		t.Fatalf("рендер %q: %v", src, err)
	}
	return strings.TrimSpace(out)
}

func TestCapturingTextGroups(t *testing.T) {
	groups := []string{"песня летов целиком", "летов", "целиком"}

	cases := []struct {
		src  string
		want string
	}{
		{"{{ .capturing_text }}", "летов"},
		{"{{capturing_text}}", "летов"},
		{"{{ index .capturing_groups 1 }}", "летов"},
		{"{{ index .capturing_groups 2 }}", "целиком"},
		{"{{ index .capturing_groups 0 }}", "песня летов целиком"},
		{"{{ index .capturing_groups 9 }}", ""},
		{"{{ .capturing_count }}", "2"},
		{"играй {{ index .capturing_groups 2 }} от {{ .capturing_text }}", "играй целиком от летов"},
	}
	for _, tc := range cases {
		if got := renderWithCaptures(t, tc.src, groups); got != tc.want {
			t.Fatalf("%q -> %q, ожидали %q", tc.src, got, tc.want)
		}
	}
}

func TestCapturingTextWithoutGroups(t *testing.T) {
	for _, src := range []string{"{{ .capturing_text }}", "{{capturing_text}}"} {
		if got := renderWithCaptures(t, src, nil); got != "летов" {
			t.Fatalf("%q -> %q, ожидали «летов»", src, got)
		}
	}
	if got := renderWithCaptures(t, "x{{ index .capturing_groups 2 }}y", nil); got != "xy" {
		t.Fatalf("вторая группа без скобок должна быть пустой, получили %q", got)
	}
	if got := renderWithCaptures(t, "{{ .capturing_count }}", nil); got != "0" {
		t.Fatalf("счётчик групп без скобок: %q", got)
	}
}

func TestTriggerMatchCaptureGroups(t *testing.T) {
	tr := model.Trigger{
		MatchType: model.MatchTypeRegex,
		MatchText: `включи\s+(\S+)\s+в\s+(.+)$`,
	}
	matched, groups := match.TriggerMatchCaptureGroups(tr, "включи летов в плейлист вечер")
	if !matched {
		t.Fatal("триггер не совпал")
	}
	if len(groups) < 3 {
		t.Fatalf("ожидали 3 элемента (весь матч + 2 группы), получили %v", groups)
	}
	if groups[1] != "летов" || groups[2] != "плейлист вечер" {
		t.Fatalf("группы: %v", groups)
	}
	// {{ .capturing_text }} — первая непустая группа.
	if best := match.PickBestCapture(groups); best != "летов" {
		t.Fatalf("главная группа: %q", best)
	}
	if match.PickBestCapture(nil) != "" {
		t.Fatal("PickBestCapture(nil) должен отдавать пустую строку")
	}
}
