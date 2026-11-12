package app

import (
	"strings"
	"testing"

	"trigger-admin-bot/internal/mediadl"
)

func TestMediaServiceEmojiReddit(t *testing.T) {
	const redditEmoji = `emoji-id="5465648490375814741"`
	for _, mode := range []mediadl.Mode{mediadl.ModeVideo, mediadl.ModePhoto, mediadl.ModeAuto} {
		got := mediaServiceEmoji(mediadl.ServiceReddit, mode)
		if !strings.Contains(got, redditEmoji) {
			t.Fatalf("режим %s: бейдж reddit не тот: %q", mode, got)
		}
		if !strings.Contains(got, "💬") {
			t.Fatalf("режим %s: потерялся глиф: %q", mode, got)
		}
	}
}

func TestMediaServiceEmojiOtherServicesUnchanged(t *testing.T) {
	if got := mediaServiceEmoji(mediadl.ServiceCoub, mediadl.ModeVideo); !strings.Contains(got, "5197233100463039550") {
		t.Fatalf("бейдж coub изменился: %q", got)
	}
	if got := mediaServiceEmoji(mediadl.ServiceX, mediadl.ModeVideo); !strings.Contains(got, "5465453979896913711") {
		t.Fatalf("бейдж x изменился: %q", got)
	}
}
