package app

import (
	"strings"
	"testing"
)

func TestSplitTelegramHTMLMessage_PreservesTagsAndLimits(t *testing.T) {
	src := "<b>" + strings.Repeat("Привет мир! ", 1200) + "</b>"
	parts := splitTelegramHTMLMessage(src, 800)
	if len(parts) < 2 {
		t.Fatalf("expected split into multiple parts, got=%d", len(parts))
	}
	for i, part := range parts {
		if l := len([]rune(part)); l > 800 {
			t.Fatalf("part %d is too long: %d", i, l)
		}
		if strings.Count(part, "<b>") != strings.Count(part, "</b>") {
			t.Fatalf("part %d has unbalanced <b> tags: %q", i, part)
		}
	}
}

func TestSplitTelegramHTMLMessage_HandlesNestedTags(t *testing.T) {
	src := `<i>начало <b>` + strings.Repeat("текст ", 900) + `</b> конец</i>`
	parts := splitTelegramHTMLMessage(src, 700)
	if len(parts) < 2 {
		t.Fatalf("expected split into multiple parts, got=%d", len(parts))
	}
	for i, part := range parts {
		if l := len([]rune(part)); l > 700 {
			t.Fatalf("part %d is too long: %d", i, l)
		}
		if strings.Count(part, "<i>") != strings.Count(part, "</i>") {
			t.Fatalf("part %d has unbalanced <i> tags", i)
		}
		if strings.Count(part, "<b>") != strings.Count(part, "</b>") {
			t.Fatalf("part %d has unbalanced <b> tags", i)
		}
	}
}
