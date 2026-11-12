package app

// Невидимые символы внутри номера не должны мешать маскированию:
// Telegram и копипаст приносят U+200B, U+2060, U+200E/F.
//
// Внимание: вывод go test проходит через редактор ПДн, поэтому номера в
// сообщениях печатаются звёздочками — сравнивать значения в ассертах,
// а не глазами по логу.

import (
	"strings"
	"testing"
	"unicode"
)

const invisiblePhoneText = "мой номер +7900\u20604562571"

func TestPIIMaskWithInvisibleChars(t *testing.T) {
	t.Setenv("PII_MASK_ENABLED", "1")
	piiResetSessionsStore()

	variants := map[string]string{
		"чистый":        "мой номер +79004562571",
		"LRI/PDI":       "мой номер \u2066+79004562571\u2069",
		"LRM/RLM":       "мой номер \u200e+79004562571\u200f",
		"между цифрами": invisiblePhoneText,
		"ZWSP":          "мой номер +79004\u200b562571",
	}
	for name, text := range variants {
		masked := maskPIIForModel("invis:1", text)
		if !strings.Contains(masked, "<PHONE_") {
			t.Fatalf("%s: телефон не замаскирован (%q)", name, masked)
		}
		t.Logf("%s: ok", name)
	}
}

func TestPIIMaskKeepsRestoreAfterNormalize(t *testing.T) {
	t.Setenv("PII_MASK_ENABLED", "1")
	piiResetSessionsStore()

	masked := maskPIIForModel("invis:2", invisiblePhoneText)
	restored := restorePIIFromModel("invis:2", masked)

	// Ожидаем исходный текст без невидимого символа.
	want := "мой номер +79004562571"
	if restored != want {
		if restored != invisiblePhoneText {
			t.Fatalf("восстановление не совпало: len=%d (ожидали %d или %d)", len([]rune(restored)), len([]rune(want)), len([]rune(invisiblePhoneText)))
		}
	}
	if strings.IndexFunc(restored, func(r rune) bool { return unicode.Is(unicode.Cf, r) }) >= 0 {
		t.Fatalf("в восстановленном тексте остались невидимые символы")
	}
}
