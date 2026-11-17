package app

// Живой прогон карусели VK: скачать пост и отправить статьёй в чат.
//
//	VK_CAROUSEL_URL=https://vk.ru/wall-50930433_228697 VK_CAROUSEL_CHAT=<id> \
//	  go test -run TestVKCarouselLive -v ./internal/app
//
// Идёт тем же путём, что и боевой обработчик: sendVKCarousel.

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"trigger-admin-bot/internal/mediadl"
)

func TestVKCarouselLive(t *testing.T) {
	rawURL := strings.TrimSpace(os.Getenv("VK_CAROUSEL_URL"))
	chatID := strings.TrimSpace(os.Getenv("VK_CAROUSEL_CHAT"))
	if rawURL == "" || chatID == "" {
		t.Skip("VK_CAROUSEL_URL / VK_CAROUSEL_CHAT не заданы")
	}
	const envPath = "/home/faline/trigger_admin_bot/.env"
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

	dl := mediadl.Downloader{
		VKToken:             envFileValue(envPath, "VK_TOKEN"),
		YTDLPBin:            envFileValue(envPath, "YTDLP_BIN"),
		ProxySocks:          envFileValue(envPath, "FIXIE_SOCKS_HOST"),
		CookiesFile:         envFileValue(envPath, "YTDLP_COOKIES_FILE"),
		MaxSizeMB:           300,
		MaxHeight:           720,
		MaxVideoDurationSec: 1800,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()

	started := time.Now()
	handled, sendErr := sendVKCarousel(ctx, sendContext{Bot: bot, ChatID: chat}, dl, rawURL, mediadl.ModeAuto, nil)
	if !handled {
		t.Fatalf("%s не ушёл в ветку карусели VK", rawURL)
	}
	if sendErr != nil {
		t.Fatalf("отправка карусели: %v", sendErr)
	}
	t.Logf("карусель отправлена в чат %d за %.1f с", chat, time.Since(started).Seconds())
}
