package vkaudio

// Живая проверка скачивания VK-трека (нужны VK_COOKIES_FILE и VK_WEB_USER_ID):
//
//	VK_COOKIES_FILE=... VK_WEB_USER_ID=... VK_LIVE_TRACK=157087742_456239040 \
//	  go test -run TestVKDownloadLive -v ./internal/vkaudio

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestVKDownloadLive(t *testing.T) {
	track := os.Getenv("VK_LIVE_TRACK")
	if track == "" {
		t.Skip("VK_LIVE_TRACK не задан")
	}
	d := Downloader{
		UserAgent:   "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0 Safari/537.36",
		CookiesFile: os.Getenv("VK_COOKIES_FILE"),
		WebBaseURL:  "https://vk.com",
		TimeoutSec:  180,
		RetryCount:  1,
	}
	d.ProxyURL = os.Getenv("VK_LIVE_PROXY")
	if uid := os.Getenv("VK_WEB_USER_ID"); uid != "" {
		var id int
		for _, r := range uid {
			if r < '0' || r > '9' {
				id = 0
				break
			}
			id = id*10 + int(r-'0')
		}
		d.WebUserID = id
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	started := time.Now()
	result, err := d.DownloadTrack(ctx, track)
	if err != nil {
		t.Fatalf("скачивание %s: %v", track, err)
	}
	defer os.RemoveAll(result.FilePath)
	st, statErr := os.Stat(result.FilePath)
	if statErr != nil {
		t.Fatalf("файл: %v", statErr)
	}
	t.Logf("трек: %q — %q, %d байт, за %.1f с", result.Artist, result.Title, st.Size(), time.Since(started).Seconds())
}
