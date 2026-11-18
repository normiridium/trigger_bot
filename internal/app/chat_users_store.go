package app

// Кто есть в чате — постоянное хранилище (коллекция chat_users).
//
// Раньше участники жили только в памяти процесса: после рестарта тег
// {{ .random_username }} знал лишь тех, кто успел написать заново. Теперь
// источник истины — Mongo: индекс chatUserIndex остаётся быстрой памятью для
// resolve/банов, а случайный ник берётся из базы ($sample по chat_id), поэтому
// выбор никогда не смешивается между чатами.

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

const (
	// chatUsersPersistInterval — как часто переписываем last_seen_at по одному
	// и тому же участнику: чаще нет смысла, база не должна пухнуть от болтовни.
	chatUsersPersistInterval = 10 * time.Minute
	// chatUsersWarmupLimit — сколько записей поднимаем в память при старте.
	chatUsersWarmupLimit = 50000
)

// chatUserRecord — одна строка «этот человек есть в этом чате».
type chatUserRecord struct {
	ChatID    int64  `bson:"chat_id"`
	UserID    int64  `bson:"user_id"`
	Username  string `bson:"username"`
	Label     string `bson:"label"`
	FirstSeen int64  `bson:"first_seen_at"`
	LastSeen  int64  `bson:"last_seen_at"`
}

func (m *mongoBackend) ensureChatUserIndexes() error {
	ctx, cancel := mongoCtx()
	defer cancel()
	_, err := m.chatUsers.Indexes().CreateMany(ctx, []mongo.IndexModel{
		{
			// Один человек — одна запись в чате, даже если ник сменился.
			Keys:    bson.D{{Key: "chat_id", Value: 1}, {Key: "user_id", Value: 1}},
			Options: options.Index().SetUnique(true),
		},
		{
			Keys: bson.D{{Key: "chat_id", Value: 1}, {Key: "username", Value: 1}},
		},
	})
	return err
}

// upsertChatUser записывает участника; ник может смениться — перезаписываем.
func (m *mongoBackend) upsertChatUser(rec chatUserRecord) error {
	if rec.ChatID == 0 || rec.UserID == 0 {
		return nil
	}
	if rec.LastSeen <= 0 {
		rec.LastSeen = time.Now().Unix()
	}
	ctx, cancel := mongoCtx()
	defer cancel()
	_, err := m.chatUsers.UpdateOne(ctx,
		bson.M{"chat_id": rec.ChatID, "user_id": rec.UserID},
		bson.M{
			"$set": bson.M{
				"username":     rec.Username,
				"label":        rec.Label,
				"last_seen_at": rec.LastSeen,
			},
			"$setOnInsert": bson.M{
				"chat_id":       rec.ChatID,
				"user_id":       rec.UserID,
				"first_seen_at": rec.LastSeen,
			},
		},
		options.Update().SetUpsert(true),
	)
	return err
}

// sampleChatUsername отдаёт случайный @ник строго из указанного чата.
// $sample по фильтру chat_id — выбор равномерный и без выгрузки списка в память.
func (m *mongoBackend) sampleChatUsername(chatID int64) (string, error) {
	if chatID == 0 {
		return "", nil
	}
	ctx, cancel := mongoCtx()
	defer cancel()
	pipeline := mongo.Pipeline{
		{{Key: "$match", Value: bson.M{"chat_id": chatID, "username": bson.M{"$ne": ""}}}},
		{{Key: "$sample", Value: bson.M{"size": 1}}},
		{{Key: "$project", Value: bson.M{"username": 1, "_id": 0}}},
	}
	cur, err := m.chatUsers.Aggregate(ctx, pipeline)
	if err != nil {
		return "", err
	}
	defer func() { _ = cur.Close(context.Background()) }()
	if !cur.Next(ctx) {
		if err := cur.Err(); err != nil {
			return "", err
		}
		return "", nil
	}
	var doc struct {
		Username string `bson:"username"`
	}
	if err := cur.Decode(&doc); err != nil {
		return "", err
	}
	return strings.TrimSpace(doc.Username), nil
}

// loadChatUsers поднимает записи для прогрева памяти после старта.
func (m *mongoBackend) loadChatUsers(limit int64) ([]chatUserRecord, error) {
	ctx, cancel := mongoCtx()
	defer cancel()
	if limit <= 0 {
		limit = chatUsersWarmupLimit
	}
	cur, err := m.chatUsers.Find(ctx, bson.M{},
		options.Find().
			SetSort(bson.D{{Key: "last_seen_at", Value: -1}}).
			SetLimit(limit),
	)
	if err != nil {
		return nil, err
	}
	defer func() { _ = cur.Close(context.Background()) }()
	var out []chatUserRecord
	for cur.Next(ctx) {
		var rec chatUserRecord
		if err := cur.Decode(&rec); err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, cur.Err()
}

// errChatUsersStore — единая причина отказа, когда бэкенд не поднят.
var errChatUsersStore = errors.New("mongo backend not initialized")

// RememberChatUser — публичная обёртка для записи участника.
func (s *Store) RememberChatUser(rec chatUserRecord) error {
	if s == nil || s.mg == nil {
		return errChatUsersStore
	}
	return s.mg.upsertChatUser(rec)
}

// SampleChatUsername — случайный @ник участника конкретного чата.
func (s *Store) SampleChatUsername(chatID int64) (string, error) {
	if s == nil || s.mg == nil {
		return "", errChatUsersStore
	}
	return s.mg.sampleChatUsername(chatID)
}

// LoadChatUsers — прогрев индекса после старта.
func (s *Store) LoadChatUsers(limit int64) ([]chatUserRecord, error) {
	if s == nil || s.mg == nil {
		return nil, errChatUsersStore
	}
	return s.mg.loadChatUsers(limit)
}

// chatUserPersister пишет участников в базу с троттлингом: сообщения в чате
// летят часто, а last_seen_at достаточно обновлять раз в несколько минут.
type chatUserPersister struct {
	mu       sync.Mutex
	last     map[string]int64
	interval int64
	upsert   func(chatUserRecord) error
}

func newChatUserPersister(upsert func(chatUserRecord) error, interval time.Duration) *chatUserPersister {
	if interval <= 0 {
		interval = chatUsersPersistInterval
	}
	return &chatUserPersister{
		last:     make(map[string]int64),
		interval: int64(interval / time.Second),
		upsert:   upsert,
	}
}

// observe решает, писать ли запись, и делает это в фоне, чтобы не тормозить
// обработку сообщений сетевым вызовом.
func (p *chatUserPersister) observe(chatID int64, u *tgbotapi.User) {
	if p == nil || p.upsert == nil || chatID == 0 || u == nil || u.ID == 0 {
		return
	}
	// Бот не должен предлагать сам себя: свои сообщения он не получает, но
	// попадает в индекс как автор цитируемого сообщения (reply-to).
	if u.IsBot {
		return
	}
	// Ник храним как в Telegram (@MrDragoRozum): упоминание всё равно
	// регистронезависимое, но в тексте лучше выглядеть как живой ник.
	uname := strings.TrimPrefix(strings.TrimSpace(u.UserName), "@")
	label := strings.TrimSpace(u.FirstName)
	if label == "" {
		label = strings.TrimSpace(u.UserName)
	}

	key := chatUserPersistKey(chatID, u.ID)
	now := time.Now().Unix()
	p.mu.Lock()
	if prev, ok := p.last[key]; ok && now-prev < p.interval {
		p.mu.Unlock()
		return
	}
	p.last[key] = now
	p.mu.Unlock()

	rec := chatUserRecord{
		ChatID:   chatID,
		UserID:   u.ID,
		Username: uname,
		Label:    label,
		LastSeen: now,
	}
	go func() {
		defer func() { _ = recover() }()
		_ = p.upsert(rec)
	}()
}

func chatUserPersistKey(chatID, userID int64) string {
	return strconv.FormatInt(chatID, 10) + ":" + strconv.FormatInt(userID, 10)
}

func persistChatUserMessage(p *chatUserPersister, msg *tgbotapi.Message) {
	if p == nil || msg == nil || msg.Chat == nil {
		return
	}
	p.observe(msg.Chat.ID, msg.From)
	if msg.ReplyToMessage != nil && msg.ReplyToMessage.From != nil {
		p.observe(msg.Chat.ID, msg.ReplyToMessage.From)
	}
}

// warmChatUserIndex поднимает записи из Mongo в память: после рестарта тег
// работает сразу, а не «когда кто-нибудь напишет».
func warmChatUserIndex(store *Store, index *chatUserIndex, limit int64) (int, error) {
	if store == nil || index == nil {
		return 0, nil
	}
	records, err := store.LoadChatUsers(limit)
	if err != nil {
		return 0, err
	}
	for _, rec := range records {
		index.remember(rec.ChatID, &tgbotapi.User{
			ID:        rec.UserID,
			UserName:  rec.Username,
			FirstName: rec.Label,
		})
	}
	return len(records), nil
}
