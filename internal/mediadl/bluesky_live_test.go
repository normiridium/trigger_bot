package mediadl

// Живой прогон Bluesky (сеть):
//
//	LIVE_BLUESKY_IMAGE=https://fxbsky.app/profile/riolu.gay/post/3mzse3ojnes2y \
//	LIVE_BLUESKY_VIDEO=https://bsky.app/profile/bsky.app/post/<rkey> \
//	  go test -run TestBlueskyLive -v ./internal/mediadl

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestBlueskyLive(t *testing.T) {
	cases := []struct {
		env  string
		kind string
	}{
		{"LIVE_BLUESKY_IMAGE", "photo"},
		{"LIVE_BLUESKY_VIDEO", "video"},
	}
	var d Downloader
	ran := false
	for _, c := range cases {
		raw := os.Getenv(c.env)
		if raw == "" {
			continue
		}
		ran = true
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
		started := time.Now()
		media, supported, err := d.BlueskyPost(ctx, raw)
		cancel()
		if !supported {
			t.Fatalf("%s: ссылка не распознана как Bluesky: %s", c.env, raw)
		}
		if err != nil {
			t.Fatalf("%s: %v", c.env, err)
		}
		defer os.RemoveAll(media.Dir)
		if media.Kind != c.kind {
			t.Fatalf("%s: ожидали %s, получили %s", c.env, c.kind, media.Kind)
		}
		var total int64
		for _, p := range media.Paths {
			if st, statErr := os.Stat(p); statErr == nil {
				total += st.Size()
			}
		}
		t.Logf("%s: %s, файлов %d, %.2f МБ, автор %q, за %.1f с",
			c.env, media.Kind, len(media.Paths), float64(total)/1e6, media.Author, time.Since(started).Seconds())
		if len(media.Paths) == 0 || total == 0 {
			t.Fatalf("%s: пустой результат", c.env)
		}
	}
	if !ran {
		t.Skip("LIVE_BLUESKY_IMAGE / LIVE_BLUESKY_VIDEO не заданы")
	}
}
