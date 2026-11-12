package app

// Проверка: не срабатывает ли таймаут regexp2 на «тяжёлом» тексте,
// похожем на реальный промпт (много чисел, id, времени).

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestPIIMaskHeavyPrompt(t *testing.T) {
	t.Setenv("PII_MASK_ENABLED", "1")
	piiResetSessionsStore()

	var b strings.Builder
	for i := 0; i < 200; i++ {
		fmt.Fprintf(&b, "сообщение %d от 2026-11-%02d 14:%02d: заказ %d20, счёт 40817810099910004%03d, время 12:30\n",
			i, (i%28)+1, i%60, i, i)
	}
	b.WriteString("мой номер +79004562571\n")
	text := b.String()

	started := time.Now()
	masked := maskPIIForModel("heavy:1", text)
	took := time.Since(started)

	hit := strings.Contains(masked, "<PHONE_")
	t.Logf("длина=%d цифровых фрагментов много, найдено=%v, время=%s", len([]rune(text)), hit, took)
	if !hit {
		t.Fatalf("телефон не найден в тяжёлом промпте (время=%s)", took)
	}
}
