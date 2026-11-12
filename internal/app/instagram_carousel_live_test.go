package app

// Живой прогон Instagram-карусели: скачать пост и отправить статьёй в чат.
//
//	IG_CAROUSEL_URL=https://www.instagram.com/p/XXXX/ IG_CAROUSEL_CHAT=1519741912 \
//	  go test -run TestInstagramCarouselLive -v ./internal/app

import (
	"bufio"
	"context"
	"os"
	"strings"
	"testing"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"trigger-admin-bot/internal/mediadl"
)

func envFileValue(path, key string) string {
	fh, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer fh.Close()
	scanner := bufio.NewScanner(fh)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, key+"=") {
			value := strings.TrimSpace(strings.TrimPrefix(line, key+"="))
			return strings.Trim(value, `"'`)
		}
	}
	return ""
}

func TestInstagramCarouselLive(t *testing.T) {
	rawURL := strings.TrimSpace(os.Getenv("IG_CAROUSEL_URL"))
	chatID := strings.TrimSpace(os.Getenv("IG_CAROUSEL_CHAT"))
	if rawURL == "" || chatID == "" {
		t.Skip("IG_CAROUSEL_URL / IG_CAROUSEL_CHAT не заданы")
	}
	const envPath = "/home/faline/trigger_admin_bot/.env"
	token := envFileValue(envPath, "TELEGRAM_BOT_TOKEN")
	if token == "" {
		t.Fatal("нет токена бота в .env")
	}

	dl := mediadl.Downloader{
		YTDLPBin:      envFileValue(envPath, "YTDLP_BIN"),
		CookiesFile:   envFileValue(envPath, "YTDLP_COOKIES_FILE"),
		ExtractorArgs: envFileValue(envPath, "YTDLP_EXTRACTOR_ARGS"),
		Impersonate:   envFileValue(envPath, "YTDLP_IMPERSONATE"),
		MaxSizeMB:     50,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()

	started := time.Now()
	carousel, supported, err := dl.InstagramCarousel(ctx, rawURL)
	if err != nil {
		t.Fatalf("карусель: %v", err)
	}
	if !supported {
		t.Fatalf("пост не распознан как карусель: %s", rawURL)
	}
	defer os.RemoveAll(carousel.Dir)

	var videos, photos int
	for _, item := range carousel.Items {
		if item.Kind == "video" {
			videos++
		} else {
			photos++
		}
	}
	t.Logf("элементов: %d (фото %d, видео %d), пропущено: %d, за %.1f с",
		len(carousel.Items), photos, videos, carousel.Skipped, time.Since(started).Seconds())
	for _, item := range carousel.Items {
		st, statErr := os.Stat(item.Path)
		size := int64(0)
		if statErr == nil {
			size = st.Size()
		}
		t.Logf("  [%d] %s %.2f МБ", item.Index, item.Kind, float64(size)/1e6)
	}

	var chat int64
	for _, r := range chatID {
		chat = chat*10 + int64(r-'0')
	}
	bot, err := tgbotapi.NewBotAPI(token)
	if err != nil {
		t.Fatalf("бот: %v", err)
	}

	caption := buildMediaAlbumCaption(len(carousel.Items), carousel.Title, carousel.SourceURL, mediadl.ServiceInstagram)
	html, specs := buildCarouselSlideshow(caption, carousel.Items)
	t.Logf("html: %.200s", html)
	sent, err := sendRichArticleSlideshow(sendContext{Bot: bot, ChatID: chat}, html, carouselRichMedia(specs))
	if err != nil {
		t.Fatalf("отправка статьи: %v", err)
	}
	t.Logf("отправлено сообщение id=%d в чат %d", sent.MessageID, chat)
}
