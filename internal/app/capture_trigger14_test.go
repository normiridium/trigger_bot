package app

// Проверка регулярки триггера 14 «Ролеплей»: две группы, первая не изменилась.

import (
	"strings"
	"testing"

	"trigger-admin-bot/internal/match"
	"trigger-admin-bot/internal/model"
)

func TestCaptureGroupsTrigger14Tail(t *testing.T) {
	tr := model.Trigger{
		MatchType: model.MatchTypeRegex,
		MatchText: `^~\s*([^\n%]+?)\s*%(?:\s*(.+))?$`,
	}
	cases := []struct {
		text  string
		first string
		tail  string
	}{
		{"~летов%", "летов", ""},
		{"~летов% второй кусок", "летов", "второй кусок"},
		{"~ролеплей % хвост сцены", "ролеплей", "хвост сцены"},
	}
	for _, tc := range cases {
		matched, groups := match.TriggerMatchCaptureGroups(tr, tc.text)
		if !matched {
			t.Fatalf("%q: не совпало", tc.text)
		}
		if len(groups) < 3 {
			t.Fatalf("%q: ожидали 2 группы, получили %v", tc.text, groups)
		}
		if groups[1] != tc.first {
			t.Fatalf("%q: первая группа %q, ожидали %q", tc.text, groups[1], tc.first)
		}
		if groups[2] != tc.tail {
			t.Fatalf("%q: вторая группа %q, ожидали %q", tc.text, groups[2], tc.tail)
		}
	}

	// Как это выглядит в шаблоне триггера 14.
	ctx := templateContext{
		CapturingText:  "летов",
		CapturingTexts: []string{"~летов% сцена вторая", "летов", "сцена вторая"},
	}
	out, err := renderResponseTemplate("{{ .capturing_text}} | {{ index .capturing_groups 2 }}", buildTemplateVars(ctx), nil)
	if err != nil {
		t.Fatalf("рендер: %v", err)
	}
	if strings.TrimSpace(out) != "летов | сцена вторая" {
		// главная группа = первая («летов»), вторая = хвост
		t.Fatalf("рендер дал %q", out)
	}
}
