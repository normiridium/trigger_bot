package main

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.mongodb.org/mongo-driver/mongo/readpref"
)

type mongoBackend struct {
	client    *mongo.Client
	db        *mongo.Database
	triggers  *mongo.Collection
	admins    *mongo.Collection
	adminSync *mongo.Collection
	counters  *mongo.Collection
}

type mongoTriggerDoc struct {
	ID            int64  `bson:"id"`
	UID           string `bson:"uid"`
	Priority      int    `bson:"priority"`
	RegexBenchUS  int64  `bson:"regex_bench_us"`
	Title         string `bson:"title"`
	Enabled       bool   `bson:"enabled"`
	TriggerMode   string `bson:"trigger_mode"`
	AdminMode     string `bson:"admin_mode"`
	MatchText     string `bson:"match_text"`
	MatchType     string `bson:"match_type"`
	CaseSensitive bool   `bson:"case_sensitive"`
	ActionType    string `bson:"action_type"`
	ResponseText  string `bson:"response_text"`
	Reply         bool   `bson:"send_as_reply"`
	Preview       bool   `bson:"preview_first_link"`
	DeleteSource  bool   `bson:"delete_source_message"`
	Chance        int    `bson:"chance"`
	CreatedAt     int64  `bson:"created_at"`
	UpdatedAt     int64  `bson:"updated_at"`
	RegexError    string `bson:"regex_error"`
}

type mongoAdminCacheDoc struct {
	ChatID    int64 `bson:"chat_id"`
	UserID    int64 `bson:"user_id"`
	IsAdmin   bool  `bson:"is_admin"`
	UpdatedAt int64 `bson:"updated_at"`
}

type mongoAdminSyncDoc struct {
	ChatID    int64 `bson:"chat_id"`
	UpdatedAt int64 `bson:"updated_at"`
	AdminCnt  int   `bson:"admin_count"`
}

type mongoCounterDoc struct {
	ID  string `bson:"_id"`
	Seq int64  `bson:"seq"`
}

func isMongoURI(v string) bool {
	v = strings.ToLower(strings.TrimSpace(v))
	return strings.HasPrefix(v, "mongodb://") || strings.HasPrefix(v, "mongodb+srv://")
}

func mongoDBNameFromURI(uri string) string {
	u, err := url.Parse(uri)
	if err != nil {
		return "trigger_admin_bot"
	}
	p := strings.Trim(strings.TrimSpace(u.Path), "/")
	if p == "" {
		return "trigger_admin_bot"
	}
	return p
}

func mongoCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 8*time.Second)
}

func openMongoStore(uri string) (*Store, error) {
	ctx, cancel := mongoCtx()
	defer cancel()

	client, err := mongo.Connect(ctx, options.Client().ApplyURI(uri))
	if err != nil {
		return nil, err
	}
	if err := client.Ping(ctx, readpref.Primary()); err != nil {
		_ = client.Disconnect(ctx)
		return nil, err
	}

	db := client.Database(mongoDBNameFromURI(uri))
	mg := &mongoBackend{
		client:    client,
		db:        db,
		triggers:  db.Collection("triggers"),
		admins:    db.Collection("chat_admin_cache"),
		adminSync: db.Collection("chat_admin_sync"),
		counters:  db.Collection("counters"),
	}
	if err := mg.ensureIndexes(); err != nil {
		_ = client.Disconnect(ctx)
		return nil, err
	}
	return &Store{
		mg:       mg,
		cacheTTL: 2 * time.Second,
	}, nil
}

func (m *mongoBackend) close() error {
	if m == nil || m.client == nil {
		return nil
	}
	ctx, cancel := mongoCtx()
	defer cancel()
	return m.client.Disconnect(ctx)
}

func (m *mongoBackend) ensureIndexes() error {
	ctx, cancel := mongoCtx()
	defer cancel()
	_, err := m.triggers.Indexes().CreateMany(ctx, []mongo.IndexModel{
		{
			Keys:    bson.D{{Key: "id", Value: 1}},
			Options: options.Index().SetUnique(true),
		},
		{
			Keys: bson.D{{Key: "uid", Value: 1}},
			Options: options.Index().
				SetUnique(true).
				SetPartialFilterExpression(bson.M{"uid": bson.M{"$gt": ""}}),
		},
		{
			Keys: bson.D{{Key: "priority", Value: -1}, {Key: "id", Value: 1}},
		},
	})
	if err != nil {
		return err
	}
	_, err = m.admins.Indexes().CreateMany(ctx, []mongo.IndexModel{
		{
			Keys:    bson.D{{Key: "chat_id", Value: 1}, {Key: "user_id", Value: 1}},
			Options: options.Index().SetUnique(true),
		},
		{
			Keys: bson.D{{Key: "updated_at", Value: 1}},
		},
	})
	if err != nil {
		return err
	}
	_, err = m.adminSync.Indexes().CreateMany(ctx, []mongo.IndexModel{
		{
			Keys:    bson.D{{Key: "chat_id", Value: 1}},
			Options: options.Index().SetUnique(true),
		},
	})
	return err
}

func triggerToDoc(t Trigger) mongoTriggerDoc {
	return mongoTriggerDoc{
		ID:            t.ID,
		UID:           strings.TrimSpace(t.UID),
		Priority:      t.Priority,
		RegexBenchUS:  t.RegexBenchUS,
		Title:         t.Title,
		Enabled:       t.Enabled,
		TriggerMode:   normalizeTriggerMode(t.TriggerMode),
		AdminMode:     normalizeAdminMode(t.AdminMode),
		MatchText:     t.MatchText,
		MatchType:     normalizeMatchType(t.MatchType),
		CaseSensitive: t.CaseSensitive,
		ActionType:    normalizeActionType(t.ActionType),
		ResponseText:  t.ResponseText,
		Reply:         t.Reply,
		Preview:       t.Preview,
		DeleteSource:  t.DeleteSource,
		Chance:        sanitizeChance(t.Chance),
		CreatedAt:     t.CreatedAt,
		UpdatedAt:     t.UpdatedAt,
		RegexError:    t.RegexError,
	}
}

func docToTrigger(d mongoTriggerDoc) Trigger {
	return Trigger{
		ID:            d.ID,
		UID:           strings.TrimSpace(d.UID),
		Priority:      d.Priority,
		RegexBenchUS:  d.RegexBenchUS,
		Title:         d.Title,
		Enabled:       d.Enabled,
		TriggerMode:   normalizeTriggerMode(d.TriggerMode),
		AdminMode:     normalizeAdminMode(d.AdminMode),
		MatchText:     d.MatchText,
		MatchType:     normalizeMatchType(d.MatchType),
		CaseSensitive: d.CaseSensitive,
		ActionType:    normalizeActionType(d.ActionType),
		ResponseText:  d.ResponseText,
		Reply:         d.Reply,
		Preview:       d.Preview,
		DeleteSource:  d.DeleteSource,
		Chance:        sanitizeChance(d.Chance),
		CreatedAt:     d.CreatedAt,
		UpdatedAt:     d.UpdatedAt,
		RegexError:    d.RegexError,
	}
}

func (m *mongoBackend) listTriggers() ([]Trigger, error) {
	ctx, cancel := mongoCtx()
	defer cancel()
	cur, err := m.triggers.Find(ctx, bson.M{}, options.Find().SetSort(bson.D{{Key: "priority", Value: -1}, {Key: "id", Value: 1}}))
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	out := make([]Trigger, 0, 64)
	for cur.Next(ctx) {
		var d mongoTriggerDoc
		if err := cur.Decode(&d); err != nil {
			return nil, err
		}
		out = append(out, docToTrigger(d))
	}
	return out, cur.Err()
}

func (m *mongoBackend) getTrigger(id int64) (*Trigger, error) {
	ctx, cancel := mongoCtx()
	defer cancel()
	var d mongoTriggerDoc
	err := m.triggers.FindOne(ctx, bson.M{"id": id}).Decode(&d)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	t := docToTrigger(d)
	return &t, nil
}

func (m *mongoBackend) nextInsertPriority() (int, error) {
	ctx, cancel := mongoCtx()
	defer cancel()
	var d mongoTriggerDoc
	err := m.triggers.FindOne(ctx, bson.M{}, options.FindOne().SetSort(bson.D{{Key: "priority", Value: 1}}).SetProjection(bson.M{"priority": 1})).Decode(&d)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return 1, nil
	}
	if err != nil {
		return 0, err
	}
	return d.Priority - 1, nil
}

func (m *mongoBackend) nextTriggerID() (int64, error) {
	ctx, cancel := mongoCtx()
	defer cancel()
	opts := options.FindOneAndUpdate().SetUpsert(true).SetReturnDocument(options.After)
	var out mongoCounterDoc
	err := m.counters.FindOneAndUpdate(ctx, bson.M{"_id": "triggers"}, bson.M{"$inc": bson.M{"seq": 1}}, opts).Decode(&out)
	if err != nil {
		return 0, err
	}
	return out.Seq, nil
}

func (m *mongoBackend) insertTrigger(t Trigger, now int64) error {
	if t.ID <= 0 {
		id, err := m.nextTriggerID()
		if err != nil {
			return err
		}
		t.ID = id
	}
	t.CreatedAt = now
	t.UpdatedAt = now
	_, err := m.triggers.InsertOne(context.Background(), triggerToDoc(t))
	return err
}

func (m *mongoBackend) updateTrigger(t Trigger, now int64) error {
	ctx, cancel := mongoCtx()
	defer cancel()
	t.UpdatedAt = now
	set := bson.M{
		"uid":                   strings.TrimSpace(t.UID),
		"regex_bench_us":        t.RegexBenchUS,
		"title":                 t.Title,
		"enabled":               t.Enabled,
		"trigger_mode":          normalizeTriggerMode(t.TriggerMode),
		"admin_mode":            normalizeAdminMode(t.AdminMode),
		"match_text":            t.MatchText,
		"match_type":            normalizeMatchType(t.MatchType),
		"case_sensitive":        t.CaseSensitive,
		"action_type":           normalizeActionType(t.ActionType),
		"response_text":         t.ResponseText,
		"send_as_reply":         t.Reply,
		"preview_first_link":    t.Preview,
		"delete_source_message": t.DeleteSource,
		"chance":                sanitizeChance(t.Chance),
		"updated_at":            now,
		"regex_error":           t.RegexError,
	}
	res, err := m.triggers.UpdateOne(ctx, bson.M{"id": t.ID}, bson.M{"$set": set})
	if err != nil {
		return err
	}
	if res.MatchedCount == 0 {
		return fmt.Errorf("trigger id=%d not found", t.ID)
	}
	return nil
}

func (m *mongoBackend) toggleTrigger(id int64) (bool, error) {
	ctx, cancel := mongoCtx()
	defer cancel()
	var d mongoTriggerDoc
	if err := m.triggers.FindOne(ctx, bson.M{"id": id}).Decode(&d); err != nil {
		return false, err
	}
	next := !d.Enabled
	_, err := m.triggers.UpdateOne(ctx, bson.M{"id": id}, bson.M{"$set": bson.M{"enabled": next, "updated_at": time.Now().Unix()}})
	if err != nil {
		return false, err
	}
	return next, nil
}

func (m *mongoBackend) deleteTrigger(id int64) error {
	ctx, cancel := mongoCtx()
	defer cancel()
	_, err := m.triggers.DeleteOne(ctx, bson.M{"id": id})
	return err
}

func (m *mongoBackend) reorderTriggersByIDs(finalOrder []int64) error {
	ctx, cancel := mongoCtx()
	defer cancel()
	now := time.Now().Unix()
	for i, id := range finalOrder {
		priority := len(finalOrder) - i
		if _, err := m.triggers.UpdateOne(ctx, bson.M{"id": id}, bson.M{"$set": bson.M{"priority": priority, "updated_at": now}}); err != nil {
			return err
		}
	}
	return nil
}

func (m *mongoBackend) getUIDByID(id int64) (string, error) {
	if id <= 0 {
		return "", nil
	}
	ctx, cancel := mongoCtx()
	defer cancel()
	var d struct {
		UID string `bson:"uid"`
	}
	err := m.triggers.FindOne(ctx, bson.M{"id": id}, options.FindOne().SetProjection(bson.M{"uid": 1})).Decode(&d)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return "", nil
	}
	return strings.TrimSpace(d.UID), err
}

func (m *mongoBackend) getIDByUID(uid string) (int64, error) {
	uid = strings.TrimSpace(uid)
	if uid == "" {
		return 0, nil
	}
	ctx, cancel := mongoCtx()
	defer cancel()
	var d struct {
		ID int64 `bson:"id"`
	}
	err := m.triggers.FindOne(ctx, bson.M{"uid": uid}, options.FindOne().SetProjection(bson.M{"id": 1})).Decode(&d)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return 0, nil
	}
	return d.ID, err
}

func (m *mongoBackend) getChatAdminCache(chatID, userID int64) (bool, int64, bool, error) {
	ctx, cancel := mongoCtx()
	defer cancel()
	var d mongoAdminCacheDoc
	err := m.admins.FindOne(ctx, bson.M{"chat_id": chatID, "user_id": userID}).Decode(&d)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return false, 0, false, nil
	}
	if err != nil {
		return false, 0, false, err
	}
	return d.IsAdmin, d.UpdatedAt, true, nil
}

func (m *mongoBackend) upsertChatAdminCache(chatID, userID int64, isAdmin bool, updatedAt int64) error {
	if updatedAt <= 0 {
		updatedAt = time.Now().Unix()
	}
	ctx, cancel := mongoCtx()
	defer cancel()
	_, err := m.admins.UpdateOne(
		ctx,
		bson.M{"chat_id": chatID, "user_id": userID},
		bson.M{"$set": bson.M{"chat_id": chatID, "user_id": userID, "is_admin": isAdmin, "updated_at": updatedAt}},
		options.Update().SetUpsert(true),
	)
	return err
}

func (m *mongoBackend) clearChatAdminCache(chatID int64) error {
	ctx, cancel := mongoCtx()
	defer cancel()
	if _, err := m.admins.DeleteMany(ctx, bson.M{"chat_id": chatID}); err != nil {
		return err
	}
	_, err := m.adminSync.DeleteOne(ctx, bson.M{"chat_id": chatID})
	return err
}

func (m *mongoBackend) getChatAdminSync(chatID int64) (int64, int, bool, error) {
	ctx, cancel := mongoCtx()
	defer cancel()
	var d mongoAdminSyncDoc
	err := m.adminSync.FindOne(ctx, bson.M{"chat_id": chatID}).Decode(&d)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return 0, 0, false, nil
	}
	if err != nil {
		return 0, 0, false, err
	}
	return d.UpdatedAt, d.AdminCnt, true, nil
}

func (m *mongoBackend) upsertChatAdminSync(chatID int64, updatedAt int64, adminCount int) error {
	if updatedAt <= 0 {
		updatedAt = time.Now().Unix()
	}
	ctx, cancel := mongoCtx()
	defer cancel()
	_, err := m.adminSync.UpdateOne(
		ctx,
		bson.M{"chat_id": chatID},
		bson.M{"$set": bson.M{"chat_id": chatID, "updated_at": updatedAt, "admin_count": adminCount}},
		options.Update().SetUpsert(true),
	)
	return err
}
