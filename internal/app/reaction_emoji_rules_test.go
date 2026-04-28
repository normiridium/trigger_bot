package app

import "testing"

func TestConvertToAllowedReactionEmoji_Direct(t *testing.T) {
	got, ok, rule := convertToAllowedReactionEmoji("😎")
	if !ok || got != "😎" || rule != "direct" {
		t.Fatalf("unexpected direct conversion: ok=%v got=%q rule=%q", ok, got, rule)
	}
}

func TestConvertToAllowedReactionEmoji_Variation(t *testing.T) {
	got, ok, rule := convertToAllowedReactionEmoji("❤️")
	if !ok || got != "❤" || rule != "direct" {
		t.Fatalf("unexpected variation conversion: ok=%v got=%q rule=%q", ok, got, rule)
	}
}

func TestConvertToAllowedReactionEmoji_Alias(t *testing.T) {
	got, ok, rule := convertToAllowedReactionEmoji("😏")
	if !ok || got != "😎" || rule != "alias" {
		t.Fatalf("unexpected alias conversion: ok=%v got=%q rule=%q", ok, got, rule)
	}
}

func TestConvertToAllowedReactionEmoji_DeerAlias(t *testing.T) {
	got, ok, rule := convertToAllowedReactionEmoji("🦌")
	if !ok || got != "🦄" || rule != "alias" {
		t.Fatalf("unexpected deer alias conversion: ok=%v got=%q rule=%q", ok, got, rule)
	}
}

func TestConvertToAllowedReactionEmoji_ButterflyAlias(t *testing.T) {
	got, ok, rule := convertToAllowedReactionEmoji("🦋")
	if !ok || got != "🦄" || rule != "alias" {
		t.Fatalf("unexpected butterfly alias conversion: ok=%v got=%q rule=%q", ok, got, rule)
	}
}

func TestExtractFirstReactionEmoji_PrefersUnicodeOutsideCustomTag(t *testing.T) {
	in := `<tg-emoji emoji-id="5474174773552516493">💋</tg-emoji> привет 😎`
	emoji, start, end, ok := extractFirstReactionEmoji(in)
	if !ok {
		t.Fatalf("expected emoji")
	}
	if emoji != "😎" {
		t.Fatalf("unexpected emoji: %q", emoji)
	}
	if start <= 0 || end <= start {
		t.Fatalf("unexpected range: %d..%d", start, end)
	}
}

func TestExtractFirstReactionEmoji_UsesFirstUnicode(t *testing.T) {
	in := "привет 😏 мир 😎"
	emoji, start, end, ok := extractFirstReactionEmoji(in)
	if !ok {
		t.Fatalf("expected emoji")
	}
	if emoji != "😏" {
		t.Fatalf("unexpected first emoji: %q", emoji)
	}
	if start < 0 || end <= start {
		t.Fatalf("unexpected range: %d..%d", start, end)
	}
}
