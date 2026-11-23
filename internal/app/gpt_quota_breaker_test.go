package app

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestOpenAIQuotaBreakerToggles(t *testing.T) {
	resetOpenAIQuotaBreaker()
	if openAIQuotaBlocked(time.Now()) {
		t.Fatal("свежий предохранитель не должен блокировать OpenAI")
	}
	now := time.Now()
	markOpenAIQuotaExhausted(now)
	if !openAIQuotaBlocked(now) {
		t.Fatal("после отметки OpenAI должен быть заблокирован")
	}
	if openAIQuotaBlocked(now.Add(openAIQuotaCooldown() + time.Second)) {
		t.Fatal("после кулдауна блокировка должна истечь")
	}
	resetOpenAIQuotaBreaker()
	if openAIQuotaBlocked(now) {
		t.Fatal("сброс должен снимать блокировку")
	}
}

func TestOpenAIQuotaBreakerCooldownFromEnv(t *testing.T) {
	t.Setenv("OPENAI_QUOTA_COOLDOWN_SEC", "120")
	if got := openAIQuotaCooldown(); got != 2*time.Minute {
		t.Fatalf("ожидалось 2 минуты, получено %s", got)
	}
	t.Setenv("OPENAI_QUOTA_COOLDOWN_SEC", "0")
	if got := openAIQuotaCooldown(); got != 30*time.Minute {
		t.Fatalf("при нуле должен быть дефолт 30 минут, получено %s", got)
	}
}

// Ошибка «OpenAI пропущен по предохранителю» должна оставаться распознаваемой
// как квота — иначе бот вместо «покормите меня» просто промолчит.
func TestSkippedOpenAIErrorStaysQuotaClassified(t *testing.T) {
	body := `{"error":{"type":"insufficient_quota","message":"openai quota cooldown active"}}`
	err := fmt.Errorf("openai status=%d body=%s (deepseek fallback failed: %v)", 429, body, errors.New("ollama 403"))
	if !isOpenAIInsufficientQuotaError(err) {
		t.Fatalf("ошибка не распознана как insufficient_quota: %v", err)
	}
}
