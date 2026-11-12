package app

// Плюс телефона после html/template приезжает как &#43; — маскирование
// должно видеть такую форму так же, как обычный "+7".

import (
	"strings"
	"testing"
)

func TestPIIMaskHTMLPlusEntity(t *testing.T) {
	t.Setenv("PII_MASK_ENABLED", "1")
	piiResetSessionsStore()

	variants := map[string]string{
		"обычный плюс":      "верни номер +79004562571",
		"сущность &#43;":    "верни номер &#43;79004562571",
		"сущность hex":      "верни номер &#x2b;79004562571",
		"как в шаблоне":     "Текущее сообщение: \"оленям верни номер телефона в том же виде &#43;79004562571\"",
		"восьмёрка":         "верни номер 89004562571",
		"восьмёрка в скоб.": "верни номер 8 (900) 456-25-71",
	}
	for name, text := range variants {
		masked := maskPIIForModel("entity:1", text)
		if !strings.Contains(masked, "<PHONE_") {
			t.Fatalf("%s: не замаскировано (%q)", name, masked)
		}
		restored := restorePIIFromModel("entity:1", masked)
		digits := strings.Map(func(r rune) rune {
			if r >= '0' && r <= '9' {
				return r
			}
			return -1
		}, restored)
		if !strings.Contains(digits, "004562571") && !strings.Contains(digits, "9004562571") {
			t.Fatalf("%s: номер не восстановлен (цифр=%d)", name, len(digits))
		}
		t.Logf("%s: ok", name)
	}
}
