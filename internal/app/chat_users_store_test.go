package app

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// chat_users: участники чата живут в Mongo, выбор ника — строго по chat_id.

func TestChatUserPersisterThrottle(t *testing.T) {
	var mu sync.Mutex
	var records []chatUserRecord
	upsert := func(rec chatUserRecord) error {
		mu.Lock()
		defer mu.Unlock()
		records = append(records, rec)
		return nil
	}
	p := newChatUserPersister(upsert, time.Hour)

	waitCount := func(want int, window time.Duration) []chatUserRecord {
		deadline := time.Now().Add(window)
		for {
			mu.Lock()
			snapshot := append([]chatUserRecord(nil), records...)
			mu.Unlock()
			if len(snapshot) >= want || time.Now().After(deadline) {
				return snapshot
			}
			time.Sleep(20 * time.Millisecond)
		}
	}

	alice := &tgbotapi.User{ID: 1, UserName: "@Alice", FirstName: "Алиса"}
	bob := &tgbotapi.User{ID: 2, UserName: "bob", FirstName: "Боб"}
	bot := &tgbotapi.User{ID: 99, UserName: "olenyam_bot", FirstName: "Оле-ням", IsBot: true}

	// Один и тот же человек в одном чате — одна запись, дальше троттлинг.
	for i := 0; i < 5; i++ {
		p.observe(-1001, alice)
	}
	p.observe(-1001, bob)
	p.observe(-1001, bot)   // бот не должен попадать в выбор
	p.observe(-2002, alice) // другой чат — отдельная запись

	snapshot := waitCount(3, 3*time.Second)
	if len(snapshot) != 3 {
		t.Fatalf("ждали 3 записи (алиса в чате A, боб в A, алиса в B), получили %d: %+v", len(snapshot), snapshot)
	}
	byKey := map[string]chatUserRecord{}
	for _, rec := range snapshot {
		byKey[chatUserPersistKey(rec.ChatID, rec.UserID)] = rec
	}
	got := byKey[chatUserPersistKey(-1001, 1)]
	if got.Username != "Alice" {
		t.Fatalf("ник должен сохраняться как в Telegram (без @), получили %q", got.Username)
	}
	if got.Label != "Алиса" {
		t.Fatalf("label потерян: %q", got.Label)
	}
	if _, ok := byKey[chatUserPersistKey(-2002, 1)]; !ok {
		t.Fatal("запись для второго чата не создана")
	}

	// После истечения окна тот же человек пишется снова (обновляет last_seen).
	p.mu.Lock()
	p.interval = 0
	p.mu.Unlock()
	p.observe(-1001, alice)

	if snapshot := waitCount(4, 2*time.Second); len(snapshot) != 4 {
		t.Fatalf("после истечения окна ждали 4 записи, получили %d", len(snapshot))
	}
}

func TestPersistChatUserMessageNilSafe(t *testing.T) {
	persistChatUserMessage(nil, nil)
	p := newChatUserPersister(nil, time.Hour)
	persistChatUserMessage(p, nil)
	persistChatUserMessage(p, &tgbotapi.Message{}) // без чата
	// Ничего не должно паниковать.
}

func TestWarmChatUserIndexNilSafe(t *testing.T) {
	n, err := warmChatUserIndex(nil, nil, 10)
	if n != 0 || err != nil {
		t.Fatalf("ждали 0/nil, получили %d/%v", n, err)
	}
}

// Интеграционный тест с живой Mongo: выбор ника не смешивается между чатами.
func TestChatUsersStoreIntegration(t *testing.T) {
	uri := strings.TrimSpace(os.Getenv("MONGO_URI"))
	if uri == "" {
		t.Skip("MONGO_URI is not set")
	}
	store, err := OpenStore(uri)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	const chatA, chatB = int64(-999000111), int64(-999000222)
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = store.mg.chatUsers.DeleteMany(ctx, map[string]interface{}{"chat_id": map[string]interface{}{"$in": []int64{chatA, chatB}}})
	}()

	people := []chatUserRecord{
		{ChatID: chatA, UserID: 1, Username: "alice", Label: "Алиса"},
		{ChatID: chatA, UserID: 2, Username: "bob", Label: "Боб"},
		{ChatID: chatB, UserID: 3, Username: "carol", Label: "Кэрол"},
	}
	for _, rec := range people {
		if err := store.RememberChatUser(rec); err != nil {
			t.Fatalf("запись %s: %v", rec.Username, err)
		}
	}

	for i := 0; i < 30; i++ {
		nick, err := store.SampleChatUsername(chatA)
		if err != nil {
			t.Fatalf("sample chat A: %v", err)
		}
		if nick != "alice" && nick != "bob" {
			t.Fatalf("в чате A выбран ник из чужого чата: %q", nick)
		}
	}
	for i := 0; i < 10; i++ {
		nick, err := store.SampleChatUsername(chatB)
		if err != nil {
			t.Fatalf("sample chat B: %v", err)
		}
		if nick != "carol" {
			t.Fatalf("в чате B выбран чужой ник: %q", nick)
		}
	}
	if nick, err := store.SampleChatUsername(-999000333); err != nil || nick != "" {
		t.Fatalf("пустой чат: nick=%q err=%v", nick, err)
	}

	// Повторная запись с новым ником не создаёт дубль.
	if err := store.RememberChatUser(chatUserRecord{ChatID: chatA, UserID: 1, Username: "alice_new", Label: "Алиса"}); err != nil {
		t.Fatalf("повторная запись: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	count, err := store.mg.chatUsers.CountDocuments(ctx, map[string]interface{}{"chat_id": chatA})
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 2 {
		t.Fatalf("ждали 2 записи в чате A после смены ника, получили %d", count)
	}

	// Прогрев индекса поднимает записи в память.
	index := newChatUserIndex(100)
	warm, err := warmChatUserIndex(store, index, 1000)
	if err != nil {
		t.Fatalf("прогрев: %v", err)
	}
	if warm == 0 {
		t.Fatal("прогрев не поднял ни одной записи")
	}
	if got := index.RandomUsername(chatA); got == "" {
		t.Fatal("после прогрева индекс чата A пуст")
	}
}
