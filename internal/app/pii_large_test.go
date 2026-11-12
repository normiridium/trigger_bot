package app

// Маскирование на длинном промпте: в бою текст был ~11 КБ и детектор
// не нашёл телефон. Проверяем, что дело не в размере текста.

import (
	"strings"
	"testing"
)

func bigPrompt(phoneAt int) string {
	filler := strings.Repeat("Участник обсуждает расписание тренировок и планы на неделю. ", 200)
	runes := []rune(filler)
	if phoneAt <= 0 || phoneAt >= len(runes) {
		return filler + "мой номер +79004562571"
	}
	return string(runes[:phoneAt]) + " мой номер +79004562571 " + string(runes[phoneAt:])
}

func TestPIIMaskOnLongPrompt(t *testing.T) {
	t.Setenv("PII_MASK_ENABLED", "1")
	piiResetSessionsStore()

	cases := []struct {
		name    string
		text    string
		wantHit bool
	}{
		{"в конце", bigPrompt(0), true},
		{"в середине", bigPrompt(5000), true},
		{"короткий", "верни номер +79004562571", true},
	}
	for _, tc := range cases {
		masked := maskPIIForModel("long:1", tc.text)
		hit := strings.Contains(masked, "<PHONE_")
		if hit != tc.wantHit {
			t.Fatalf("%s: длина=%d, телефон найден=%v (ожидали %v)", tc.name, len(tc.text), hit, tc.wantHit)
		}
		if hit {
			if restored := restorePIIFromModel("long:1", masked); restored != tc.text {
				t.Fatalf("%s: восстановление не совпало", tc.name)
			}
		}
		t.Logf("%s: длина=%d найдено=%v", tc.name, len([]rune(tc.text)), hit)
	}
}

func TestPIIMaskManyCandidates(t *testing.T) {
	t.Setenv("PII_MASK_ENABLED", "1")
	piiResetSessionsStore()

	// Много похожих на ПДн фрагментов — проверяем, что детектор не сдаётся.
	var b strings.Builder
	for i := 0; i < 40; i++ {
		b.WriteString("заказ 1234567890123456789, время 14:30, счёт 40817810099910004312 ")
	}
	b.WriteString("а номер мой +79004562571")
	masked := maskPIIForModel("long:2", b.String())
	if !strings.Contains(masked, "<PHONE_") {
		t.Fatalf("телефон в конце длинного текста не замаскирован (длина=%d)", len([]rune(b.String())))
	}
}
