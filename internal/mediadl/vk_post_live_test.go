package mediadl

// Живая проверка карусели VK: реально читает пост и скачивает вложения.
//
//	VK_TOKEN=… VK_CAROUSEL_URL=https://vk.ru/wall-50930433_228697 \
//	  go test -run TestVKWallPostCarouselLive -v ./internal/mediadl
//
// Видео в посте качается yt-dlp, поэтому для смешанных постов нужны ещё
// YTDLP_BIN и FIXIE_SOCKS_HOST (прокси VK).

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

func TestVKWallPostCarouselLive(t *testing.T) {
	rawURL := strings.TrimSpace(os.Getenv("VK_CAROUSEL_URL"))
	token := strings.TrimSpace(os.Getenv("VK_TOKEN"))
	if rawURL == "" || token == "" {
		t.Skip("нужны VK_CAROUSEL_URL и VK_TOKEN")
	}
	dl := Downloader{
		VKToken:             token,
		YTDLPBin:            strings.TrimSpace(os.Getenv("YTDLP_BIN")),
		ProxySocks:          strings.TrimSpace(os.Getenv("FIXIE_SOCKS_HOST")),
		MaxSizeMB:           300,
		MaxHeight:           720,
		MaxVideoDurationSec: 1800,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	carousel, supported, err := dl.VKWallPostCarousel(ctx, rawURL)
	if !supported {
		t.Fatalf("%s не распознан как пост VK", rawURL)
	}
	if err != nil {
		t.Fatalf("карусель: %v", err)
	}
	defer os.RemoveAll(carousel.Dir)
	if len(carousel.Items) == 0 {
		t.Fatal("карусель пустая")
	}
	for _, item := range carousel.Items {
		st, statErr := os.Stat(item.Path)
		if statErr != nil {
			t.Fatalf("элемент %d (%s) не скачался: %v", item.Index, item.Kind, statErr)
		}
		t.Logf("элемент %d: %s %.2f МБ %s", item.Index, item.Kind, float64(st.Size())/1_000_000.0, item.Path)
	}
	t.Logf("итого элементов=%d пропущено=%d заголовок=%q источник=%s",
		len(carousel.Items), carousel.Skipped, carousel.Title, carousel.SourceURL)
}
