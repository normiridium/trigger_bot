package app

import (
	"os"
	"testing"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

func TestTokenBanTargetsFromReply(t *testing.T) {
	index := newChatUserIndex(100)
	msg := &tgbotapi.Message{
		Chat: &tgbotapi.Chat{ID: -100123},
		ReplyToMessage: &tgbotapi.Message{
			From: &tgbotapi.User{ID: 555, FirstName: "Аня", UserName: "anny"},
		},
	}
	ids, labels, reason := tokenBanTargets(msg, index, "спам и провокации")
	if len(ids) != 1 || ids[0] != 555 {
		t.Fatalf("ожидали id 555, получили %v", ids)
	}
	if reason != "спам и провокации" {
		t.Fatalf("причина потерялась: %q", reason)
	}
	if labels[555] == "" {
		t.Fatalf("подпись цели не собрана")
	}
}

func TestTokenBanTargetsFromIDAndUsername(t *testing.T) {
	index := newChatUserIndex(100)
	index.remember(-100123, &tgbotapi.User{ID: 777, FirstName: "Валера", UserName: "sapsanka"})
	msg := &tgbotapi.Message{Chat: &tgbotapi.Chat{ID: -100123}}

	ids, _, _ := tokenBanTargets(msg, index, "777")
	if len(ids) != 1 || ids[0] != 777 {
		t.Fatalf("числовой id не разобран: %v", ids)
	}

	ids, _, _ = tokenBanTargets(msg, index, "@sapsanka")
	if len(ids) != 1 || ids[0] != 777 {
		t.Fatalf("username не разобран: %v", ids)
	}

	ids, _, reason := tokenBanTargets(msg, index, "@sapsanka флудит")
	if len(ids) != 1 || reason != "флудит" {
		t.Fatalf("id/причина разобраны неверно: ids=%v reason=%q", ids, reason)
	}

	if ids, _, _ := tokenBanTargets(msg, index, ""); len(ids) != 0 {
		t.Fatalf("пустая команда не должна давать цели: %v", ids)
	}
}

func TestTokenBanStoreIntegration(t *testing.T) {
	uri := os.Getenv("TEST_MONGO_URI")
	if uri == "" {
		t.Skip("TEST_MONGO_URI is not set")
	}
	store, err := openMongoStore(uri)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	setTokenBanStore(store) // без регистрации хранилища кэш не обновляется
	const testID int64 = 999000111
	_, _ = store.TokenUnban(testID)
	defer func() { _, _ = store.TokenUnban(testID) }()

	if err := store.TokenBan(TokenBan{UserID: testID, Username: "@test", Reason: "проверка", BannedBy: 1, ChatID: -100123, CreatedAt: time.Now().Unix()}); err != nil {
		t.Fatalf("ban: %v", err)
	}
	if _, ok := isTokenBanned(testID); !ok {
		t.Fatalf("кэш не увидел бан")
	}
	list, err := store.TokenBanList()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	found := false
	for _, item := range list {
		if item.UserID == testID {
			found = true
			if item.Reason != "проверка" {
				t.Fatalf("причина не сохранилась: %q", item.Reason)
			}
		}
	}
	if !found {
		t.Fatalf("бан не попал в список")
	}
	removed, err := store.TokenUnban(testID)
	if err != nil || !removed {
		t.Fatalf("unban: removed=%v err=%v", removed, err)
	}
	if _, ok := isTokenBanned(testID); ok {
		t.Fatalf("кэш не сбросил бан")
	}
}
