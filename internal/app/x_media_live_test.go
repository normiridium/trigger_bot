package app

// Живые прогоны поста X:
//   - XPostMedia собирает пост и отправляет фото каруселью;
//   - пост без вложений даёт строку «нет ни видео, ни картинок»;
//   - пост с видео не перехватывается (его качает yt-dlp).
//
//	X_POST_URL=https://x.com/NASA/status/2110371804959191288 X_POST_CHAT=<id> \
//	  go test -run TestXPostLive -v ./internal/app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"trigger-admin-bot/internal/mediadl"
)

func xLiveChat(t *testing.T) (*tgbotapi.BotAPI, int64) {
	t.Helper()
	const envPath = "/home/faline/trigger_admin_bot/.env"
	chatID := strings.TrimSpace(os.Getenv("X_POST_CHAT"))
	if chatID == "" {
		t.Skip("X_POST_CHAT не задан")
	}
	token := envFileValue(envPath, "TELEGRAM_BOT_TOKEN")
	if token == "" {
		t.Fatal("нет токена бота в .env")
	}
	bot, err := tgbotapi.NewBotAPI(token)
	if err != nil {
		t.Fatalf("бот: %v", err)
	}
	var chat int64
	for _, r := range chatID {
		if r < '0' || r > '9' {
			t.Fatalf("chat id %q не число", chatID)
		}
		chat = chat*10 + int64(r-'0')
	}
	return bot, chat
}

func TestXPostLive(t *testing.T) {
	rawURL := strings.TrimSpace(os.Getenv("X_POST_URL"))
	if rawURL == "" {
		t.Skip("X_POST_URL не задан")
	}
	bot, chat := xLiveChat(t)
	const envPath = "/home/faline/trigger_admin_bot/.env"
	dl := mediadl.Downloader{
		YTDLPBin:            envFileValue(envPath, "YTDLP_BIN"),
		CookiesFile:         envFileValue(envPath, "YTDLP_COOKIES_FILE"),
		ExtractorArgs:       envFileValue(envPath, "YTDLP_EXTRACTOR_ARGS"),
		MaxSizeMB:           300,
		MaxHeight:           720,
		MaxVideoDurationSec: 1800,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()

	started := time.Now()
	handled, sendErr := sendXPostMedia(ctx, sendContext{Bot: bot, ChatID: chat}, dl, rawURL, mediadl.ModeVideo, nil)
	if !handled {
		t.Logf("%s оставлен обычному пути: в посте есть видео, его качает yt-dlp", rawURL)
		return
	}
	if errors.Is(sendErr, errXPostNoMedia) {
		t.Logf("в посте нет вложений — бот ответил строкой в чат %d за %.1f с", chat, time.Since(started).Seconds())
		return
	}
	if sendErr != nil {
		t.Fatalf("отправка: %v", sendErr)
	}
	t.Logf("пост X обработан и отправлен в чат %d за %.1f с", chat, time.Since(started).Seconds())
}

// xMediaStub отдаёт заранее подготовленную карусель: проверяем отправку
// мульти-фото (слайдшоу) без зависимости от того, какой пост сейчас в X.
type xMediaStub struct {
	post mediadl.XPostMedia
}

func (s xMediaStub) XPostMedia(context.Context, string) (mediadl.XPostMedia, bool, error) {
	return s.post, true, nil
}
func (s xMediaStub) DownloadAudioFromURL(context.Context, string) (mediadl.DownloadResult, error) {
	return mediadl.DownloadResult{}, errors.New("stub")
}
func (s xMediaStub) DownloadVideoFromURL(context.Context, string) (mediadl.DownloadResult, error) {
	return mediadl.DownloadResult{}, errors.New("stub")
}
func (s xMediaStub) DownloadMediaAutoFromURL(context.Context, string) (mediadl.DownloadResult, error) {
	return mediadl.DownloadResult{}, errors.New("stub")
}
func (s xMediaStub) InstagramPhotoAlbum(context.Context, string) (mediadl.PhotoAlbum, bool, error) {
	return mediadl.PhotoAlbum{}, false, nil
}
func (s xMediaStub) InstagramCarousel(context.Context, string) (mediadl.Carousel, bool, error) {
	return mediadl.Carousel{}, false, nil
}
func (s xMediaStub) VKWallPostCarousel(context.Context, string) (mediadl.Carousel, bool, error) {
	return mediadl.Carousel{}, false, nil
}
func (s xMediaStub) RedditPost(context.Context, string) (mediadl.RedditMedia, bool, error) {
	return mediadl.RedditMedia{}, false, nil
}
func (s xMediaStub) BlueskyPost(context.Context, string) (mediadl.BlueskyMedia, bool, error) {
	return mediadl.BlueskyMedia{}, false, nil
}
func (s xMediaStub) ConfiguredMaxSizeMB() int { return 300 }
func (s xMediaStub) ConfiguredMaxHeight() int { return 720 }

// TestXCarouselSendLive отправляет слайдшоу из двух картинок тем же кодом,
// что работает в бою.
func TestXCarouselSendLive(t *testing.T) {
	bot, chat := xLiveChat(t)
	dir := t.TempDir()
	items := make([]mediadl.CarouselItem, 0, 2)
	for i := 1; i <= 2; i++ {
		path := filepath.Join(dir, fmt.Sprintf("%03d.jpg", i))
		if err := os.WriteFile(path, xTestJPEG(t, i), 0o600); err != nil {
			t.Fatalf("подготовка файла: %v", err)
		}
		items = append(items, mediadl.CarouselItem{Kind: "photo", Path: path, Index: i})
	}
	stub := xMediaStub{post: mediadl.XPostMedia{
		Dir:       dir,
		Items:     items,
		Title:     "Проверка карусели X",
		SourceURL: "https://x.com/NASA/status/2110371804959191288",
		Service:   mediadl.ServiceX,
		HasMedia:  true,
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	handled, err := sendXPostMedia(ctx, sendContext{Bot: bot, ChatID: chat}, stub, "https://x.com/NASA/status/2110371804959191288", mediadl.ModeVideo, nil)
	if !handled {
		t.Fatal("карусель не ушла в ветку X")
	}
	if err != nil {
		t.Fatalf("отправка карусели: %v", err)
	}
}

// xTestJPEG собирает настоящий JPEG 640x640: Telegram не принимает
// вырожденные картинки 1x1.
func xTestJPEG(t *testing.T, seed int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 640, 640))
	shade := uint8(40 + seed*60)
	for y := 0; y < 640; y++ {
		for x := 0; x < 640; x++ {
			img.Set(x, y, color.RGBA{R: shade, G: uint8(x % 256), B: uint8(y % 256), A: 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 70}); err != nil {
		t.Fatalf("сборка JPEG: %v", err)
	}
	return buf.Bytes()
}
