package app

// DRM-треки: в чат должно уходить понятное сообщение, а не «подробности в логах».

import (
	"errors"
	"strings"
	"testing"
)

func TestDRMErrorIsExplained(t *testing.T) {
	err := errors.New(`yt-dlp failed: exit status 1: ERROR: [soundcloud] 1421352049: This video is DRM protected`)

	hint := chatFailureHint(err)
	if !strings.Contains(strings.ToLower(hint), "drm") {
		t.Fatalf("в подсказке нет упоминания DRM: %q", hint)
	}
	if !strings.Contains(hint, "Попробуйте другой трек") {
		t.Fatalf("в подсказке нет совета: %q", hint)
	}

	text := userFacingMediaDownloadError(err)
	if !strings.Contains(strings.ToLower(text), "drm") {
		t.Fatalf("текст ошибки не объясняет причину: %q", text)
	}
	if !strings.Contains(text, "другой трек") {
		t.Fatalf("текст ошибки без совета: %q", text)
	}
}

func TestOrdinaryMediaErrorHasNoDRMHints(t *testing.T) {
	err := errors.New("yt-dlp failed: exit status 1: ERROR: Unsupported URL")
	if hint := chatFailureHint(err); hint != "" {
		t.Fatalf("обычная ошибка получила подсказку про DRM: %q", hint)
	}
	if text := userFacingMediaDownloadError(err); strings.Contains(strings.ToLower(text), "drm") {
		t.Fatalf("обычная ошибка описана как DRM: %q", text)
	}
}
