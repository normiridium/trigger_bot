package app

// Живой прогон контекста YouTube (нужен yt-dlp на машине):
//
//	LIVE_YOUTUBE_CONTEXT_URL=https://youtu.be/jNQXAC9IVRw \
//	  go test -run TestYouTubeContextLive -v ./internal/app

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

func TestYouTubeContextLive(t *testing.T) {
	raw := strings.TrimSpace(os.Getenv("LIVE_YOUTUBE_CONTEXT_URL"))
	if raw == "" {
		t.Skip("LIVE_YOUTUBE_CONTEXT_URL не задан")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	started := time.Now()
	title, text, extra, err := youTubeContextParts(ctx, raw, 3000)
	if err != nil {
		t.Fatalf("контекст YouTube: %v", err)
	}
	t.Logf("заголовок: %q", title)
	t.Logf("текст: %d символов, дополнительно: %d символов, время %.1f с",
		len([]rune(text)), len([]rune(extra)), time.Since(started).Seconds())
	preview := []rune(text)
	if len(preview) > 160 {
		preview = preview[:160]
	}
	t.Logf("начало: %q", string(preview))
	if len([]rune(text)) < 40 {
		t.Fatalf("слишком короткий текст контекста: %q", text)
	}
}
