package app

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

func TestMediaGroupImageStoreReturnsAlbumInMessageOrder(t *testing.T) {
	store := newMediaGroupImageStore(time.Nanosecond, time.Nanosecond, time.Minute)
	chat := &tgbotapi.Chat{ID: -100123}
	second := &tgbotapi.Message{
		MessageID:    102,
		Chat:         chat,
		MediaGroupID: "album-1",
		Photo:        []tgbotapi.PhotoSize{{FileID: "second"}},
	}
	first := &tgbotapi.Message{
		MessageID:    101,
		Chat:         chat,
		MediaGroupID: "album-1",
		Photo:        []tgbotapi.PhotoSize{{FileID: "first"}},
	}
	store.Observe(second)
	store.Observe(first)

	got := store.MessagesFor(first)
	if len(got) != 2 {
		t.Fatalf("album length = %d, want 2", len(got))
	}
	if got[0].MessageID != first.MessageID || got[1].MessageID != second.MessageID {
		t.Fatalf("album message order = %d, %d; want %d, %d", got[0].MessageID, got[1].MessageID, first.MessageID, second.MessageID)
	}
}

func TestResolveMessageImageURLsUsesEveryAlbumPhoto(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/getMe"):
			_, _ = w.Write([]byte(`{"ok":true,"result":{"id":999,"is_bot":true,"first_name":"Оле-ням","username":"olenyam_bot"}}`))
		case strings.HasSuffix(r.URL.Path, "/getFile"):
			fileID := r.FormValue("file_id")
			_, _ = w.Write([]byte(fmt.Sprintf(`{"ok":true,"result":{"file_id":%q,"file_unique_id":"u","file_size":123,"file_path":"photos/%s.jpg"}}`, fileID, fileID)))
		default:
			t.Fatalf("unexpected request path: %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	const token = "123456:ABCDEF"
	bot, err := tgbotapi.NewBotAPIWithAPIEndpoint(token, srv.URL+"/bot%s/%s")
	if err != nil {
		t.Fatalf("create bot: %v", err)
	}
	t.Setenv("TELEGRAM_BOT_FILE_ENDPOINT", srv.URL+"/file/bot%s/%s")
	store := newMediaGroupImageStore(time.Nanosecond, time.Nanosecond, time.Minute)
	setGPTMediaGroupImageResolver(store.MessagesFor)
	defer setGPTMediaGroupImageResolver(nil)

	chat := &tgbotapi.Chat{ID: -100123}
	first := &tgbotapi.Message{MessageID: 201, Chat: chat, MediaGroupID: "album-2", Photo: []tgbotapi.PhotoSize{{FileID: "first"}}}
	second := &tgbotapi.Message{MessageID: 202, Chat: chat, MediaGroupID: "album-2", Photo: []tgbotapi.PhotoSize{{FileID: "second"}}}
	store.Observe(second)
	store.Observe(first)

	got, err := resolveMessageImageURLs(bot, first)
	if err != nil {
		t.Fatalf("resolve album URLs: %v", err)
	}
	want := []string{
		srv.URL + "/file/bot" + token + "/photos/first.jpg",
		srv.URL + "/file/bot" + token + "/photos/second.jpg",
	}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("album URLs = %#v, want %#v", got, want)
	}
}
