package app

import (
	"strings"
	"testing"
)

func TestPIIKeyForMessageBuildsChatUserKey(t *testing.T) {
	key := piiSessionKey(-1002329738424, 1519741912)
	if key != "-1002329738424:1519741912" {
		t.Fatalf("ключ сессии неверен: %q", key)
	}
	if got := piiKeyForMessage(nil); got != "" {
		t.Fatalf("без сообщения ключ должен быть пустым: %q", got)
	}
}

func TestPIIMaskAdapterRoundTrip(t *testing.T) {
	t.Setenv("PII_MASK_ENABLED", "1")
	t.Setenv("PII_MASK_ENTITIES", "email,phone")
	piiResetSessionsStore()
	key := "test-mask-adapter"

	original := "пиши на vasya@example.com или 13812345678"
	masked := maskPIIForModel(key, original)
	if masked == original || strings.Contains(masked, "vasya@example.com") {
		t.Fatalf("маскирование не сработало: %q", masked)
	}
	if restored := restorePIIFromModel(key, "ответ: "+masked); !strings.Contains(restored, "vasya@example.com") {
		t.Fatalf("восстановление не сработало: %q", restored)
	}
	total, distribution := piiSessionStats(key)
	if total != 2 || distribution["EMAIL"] != 1 || distribution["PHONE"] != 1 {
		t.Fatalf("счётчики неверны: total=%d distribution=%v", total, distribution)
	}
}

func TestPIIMaskAdapterDisabled(t *testing.T) {
	t.Setenv("PII_MASK_ENABLED", "0")
	piiResetSessionsStore()
	text := "почта vasya@example.com"
	if got := maskPIIForModel("test-mask-disabled", text); got != text {
		t.Fatalf("при выключенном маскировании текст изменён: %q", got)
	}
}

func TestPIIMaskAdapterEmptyKeyIsNoop(t *testing.T) {
	t.Setenv("PII_MASK_ENABLED", "1")
	piiResetSessionsStore()
	text := "почта vasya@example.com"
	if got := maskPIIForModel("", text); got != text {
		t.Fatalf("без ключа сессии маскировать нельзя: %q", got)
	}
}

func TestPIIMaskAdapterUsesUpstreamPatterns(t *testing.T) {
	t.Setenv("PII_MASK_ENABLED", "1")
	t.Setenv("PII_MASK_ENTITIES", "key")
	piiResetSessionsStore()
	got := maskPIIForModel("test-mask-key", "ключ sk-abcdefghijklmnop1234")
	if !strings.Contains(got, "<KEY_1>") {
		t.Fatalf("паттерн ключа из апстрима не сработал: %q", got)
	}
}
