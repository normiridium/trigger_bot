package app

// Живой прогон: ссылка, которую не удалось скачать как медиа, публикуется как
// статья — если со страницы вытащили достаточно текста.
//
//	ARTICLE_FALLBACK_URL=https://... ARTICLE_FALLBACK_CHAT=<id> \
//	  go test -run TestArticleFallbackLive -v ./internal/app

import (
	"os"
	"strings"
	"testing"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

func TestArticleFallbackLive(t *testing.T) {
	rawURL := strings.TrimSpace(os.Getenv("ARTICLE_FALLBACK_URL"))
	chatID := strings.TrimSpace(os.Getenv("ARTICLE_FALLBACK_CHAT"))
	if rawURL == "" || chatID == "" {
		t.Skip("ARTICLE_FALLBACK_URL / ARTICLE_FALLBACK_CHAT не заданы")
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
	chat = -chat

	res := publishArticleFallback(sendContext{Bot: bot, ChatID: chat}, rawURL)
	t.Logf("url=%s chat=%d результат=%v", rawURL, chat, res)
	if res == articleFallbackNotArticle {
		t.Fatalf("статья не распознана: %s", rawURL)
	}
}
