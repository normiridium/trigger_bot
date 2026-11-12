package app

// Бейдж Bluesky в подписях: кастомная бабочка вместо голого глифа.

import (
	"strings"
	"testing"

	"trigger-admin-bot/internal/mediadl"
)

func TestMediaServiceEmojiBluesky(t *testing.T) {
	const id = `emoji-id="5411329510702331275"`
	for _, mode := range []mediadl.Mode{mediadl.ModePhoto, mediadl.ModeVideo, mediadl.ModeAuto} {
		got := mediaServiceEmoji(mediadl.ServiceBluesky, mode)
		if !strings.Contains(got, id) {
			t.Fatalf("режим %q: нет кастомного эмодзи в %q", mode, got)
		}
		if !strings.Contains(got, "🦋") {
			t.Fatalf("режим %q: потерян глиф-фолбэк в %q", mode, got)
		}
	}
}

func TestMediaServiceEmojiOthersNotBluesky(t *testing.T) {
	coub := mediaServiceEmoji(mediadl.ServiceCoub, mediadl.ModeVideo)
	if !strings.Contains(coub, `emoji-id="5197233100463039550"`) {
		t.Fatalf("бейдж coub сломан: %q", coub)
	}
	reddit := mediaServiceEmoji(mediadl.ServiceReddit, mediadl.ModePhoto)
	if !strings.Contains(reddit, `emoji-id="5465648490375814741"`) {
		t.Fatalf("бейдж reddit сломан: %q", reddit)
	}
}
