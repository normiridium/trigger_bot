package app

// Dry-run маскирования: PII_DRY_TEXT="мой номер +7 999 123-45-67" \
//
//	go test -run TestPIIMaskDryRun -v ./internal/app
//
// Печатает текст в том виде, в каком он уходит в модель, и то, что бот
// возвращает в чат после восстановления.

import (
	"os"
	"strings"
	"testing"
)

func TestPIIMaskDryRun(t *testing.T) {
	text := os.Getenv("PII_DRY_TEXT")
	if strings.TrimSpace(text) == "" {
		t.Skip("PII_DRY_TEXT не задан")
	}
	t.Setenv("PII_MASK_ENABLED", "1")
	piiResetSessionsStore()

	const session = "dry:1"
	masked := maskPIIForModel(session, text)
	t.Logf("в чате:   %s", text)
	t.Logf("в модель: %s", masked)
	t.Logf("обратно:  %s", restorePIIFromModel(session, masked))
	if masked == text {
		t.Fatalf("ничего не замаскировано — проверь PII_MASK_ENTITIES=%q", os.Getenv("PII_MASK_ENTITIES"))
	}
	if strings.Contains(masked, "999") || strings.Contains(masked, "123-45-67") {
		t.Fatalf("цифры телефона остались в тексте для модели: %q", masked)
	}
}
