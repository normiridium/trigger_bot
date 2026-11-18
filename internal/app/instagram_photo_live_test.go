package app

// Живой прогон фото-поста Instagram: одиночное фото уходит обычным фото,
// карусель — статьёй или альбомом.
//
//	IG_PHOTO_URL=https://www.instagram.com/p/DXl4Pl1EmdS/ IG_PHOTO_CHAT=<id> \
//	  go test -run TestInstagramPhotoPostLive -v ./internal/app

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"trigger-admin-bot/internal/mediadl"
)

func TestInstagramPhotoPostLive(t *testing.T) {
	rawURL := strings.TrimSpace(os.Getenv("IG_PHOTO_URL"))
	chatID := strings.TrimSpace(os.Getenv("IG_PHOTO_CHAT"))
	if rawURL == "" || chatID == "" {
		t.Skip("IG_PHOTO_URL / IG_PHOTO_CHAT не заданы")
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
		YTDLPBin:      envFileValue(envPath, "YTDLP_BIN"),
		CookiesFile:   envFileValue(envPath, "YTDLP_COOKIES_FILE"),
		ExtractorArgs: envFileValue(envPath, "YTDLP_EXTRACTOR_ARGS"),
		Impersonate:   envFileValue(envPath, "YTDLP_IMPERSONATE"),
		MaxSizeMB:     300,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()

	started := time.Now()
	handled, sendErr := sendInstagramPhotoAlbum(ctx, sendContext{Bot: bot, ChatID: chat}, dl, rawURL, nil)
	if !handled {
		t.Fatalf("%s не ушёл в ветку фото-альбома", rawURL)
	}
	if sendErr != nil {
		t.Fatalf("отправка: %v", sendErr)
	}
	t.Logf("фото-пост обработан и отправлен в чат %d за %.1f с", chat, time.Since(started).Seconds())
}
