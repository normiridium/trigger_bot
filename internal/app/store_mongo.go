package app

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"trigger-admin-bot/internal/match"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.mongodb.org/mongo-driver/mongo/readpref"
)

type mongoBackend struct {
	client    *mongo.Client
	db        *mongo.Database
	triggers  *mongo.Collection
	templates *mongo.Collection
	admins    *mongo.Collection
	adminSync *mongo.Collection
	profiles  *mongo.Collection
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
	Reply         bool   `bson:"send_as_reply"`
	Preview       bool   `bson:"preview_first_link"`
	DeleteSource  bool   `bson:"delete_source_message"`
	PassThrough   bool   `bson:"pass_through"`
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

type mongoTemplateDoc struct {
	ID        int64  `bson:"id"`
	Key       string `bson:"key"`
	Title     string `bson:"title"`
	Text      string `bson:"text"`
	CreatedAt int64  `bson:"created_at"`
	UpdatedAt int64  `bson:"updated_at"`
}

type mongoParticipantProfileDoc struct {
	ChatID          int64    `bson:"chat_id"`
	UserID          int64    `bson:"user_id"`
	PortraitText    string   `bson:"portrait_text"`
	PendingMessages []string `bson:"pending_messages"`
	CreatedAt       int64    `bson:"created_at"`
	UpdatedAt       int64    `bson:"updated_at"`
}

func responseItemsFromRaw(v interface{}) ([]ResponseTextItem, bool) {
	if v == nil {
		return nil, false
	}
	needsMigration := false
	items := make([]ResponseTextItem, 0, 4)
	switch vv := v.(type) {
	case string:
		val := strings.TrimSpace(vv)
		if val != "" {
			items = append(items, ResponseTextItem{Text: val})
		}
		needsMigration = true
	case []interface{}:
		for _, item := range vv {
			switch it := item.(type) {
			case string:
				val := strings.TrimSpace(it)
				if val != "" {
					items = append(items, ResponseTextItem{Text: val})
				}
				needsMigration = true
			case bson.M:
				if text, ok := it["text"].(string); ok && strings.TrimSpace(text) != "" {
					items = append(items, ResponseTextItem{Text: strings.TrimSpace(text)})
				}
			case bson.D:
				for _, pair := range it {
					if pair.Key == "text" {
						if text, ok := pair.Value.(string); ok && strings.TrimSpace(text) != "" {
							items = append(items, ResponseTextItem{Text: strings.TrimSpace(text)})
						}
						break
					}
				}
			case map[string]interface{}:
				if text, ok := it["text"].(string); ok && strings.TrimSpace(text) != "" {
					items = append(items, ResponseTextItem{Text: strings.TrimSpace(text)})
				}
			}
		}
	case bson.A:
		for _, item := range vv {
			switch it := item.(type) {
			case string:
				val := strings.TrimSpace(it)
				if val != "" {
					items = append(items, ResponseTextItem{Text: val})
				}
				needsMigration = true
			case bson.M:
				if text, ok := it["text"].(string); ok && strings.TrimSpace(text) != "" {
					items = append(items, ResponseTextItem{Text: strings.TrimSpace(text)})
				}
			case bson.D:
				for _, pair := range it {
					if pair.Key == "text" {
						if text, ok := pair.Value.(string); ok && strings.TrimSpace(text) != "" {
							items = append(items, ResponseTextItem{Text: strings.TrimSpace(text)})
						}
						break
					}
				}
			case map[string]interface{}:
				if text, ok := it["text"].(string); ok && strings.TrimSpace(text) != "" {
					items = append(items, ResponseTextItem{Text: strings.TrimSpace(text)})
				}
			}
		}
	}
	return items, needsMigration
}

func normalizeResponseItems(items []ResponseTextItem) []ResponseTextItem {
	if len(items) == 0 {
		return nil
	}
	out := make([]ResponseTextItem, 0, len(items))
	for _, it := range items {
		val := strings.TrimSpace(it.Text)
		if val != "" {
			out = append(out, ResponseTextItem{Text: val})
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
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
		templates: db.Collection("response_templates"),
		admins:    db.Collection("chat_admin_cache"),
		adminSync: db.Collection("chat_admin_sync"),
		profiles:  db.Collection("participant_profiles"),
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
	_, err = m.templates.Indexes().CreateMany(ctx, []mongo.IndexModel{
		{
			Keys:    bson.D{{Key: "id", Value: 1}},
			Options: options.Index().SetUnique(true),
		},
		{
			Keys:    bson.D{{Key: "key", Value: 1}},
			Options: options.Index().SetUnique(true),
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
	if err != nil {
		return err
	}
	_, err = m.profiles.Indexes().CreateMany(ctx, []mongo.IndexModel{
		{
			Keys:    bson.D{{Key: "chat_id", Value: 1}, {Key: "user_id", Value: 1}},
			Options: options.Index().SetUnique(true),
		},
		{
			Keys: bson.D{{Key: "updated_at", Value: 1}},
		},
	})
	return err
}

func triggerToDocMap(t Trigger) bson.M {
	return bson.M{
		"id":                    t.ID,
		"uid":                   strings.TrimSpace(t.UID),
		"priority":              t.Priority,
		"regex_bench_us":        t.RegexBenchUS,
		"title":                 t.Title,
		"enabled":               t.Enabled,
		"trigger_mode":          normalizeTriggerMode(string(t.TriggerMode)),
		"admin_mode":            normalizeAdminMode(string(t.AdminMode)),
		"match_text":            t.MatchText,
		"match_type":            match.NormalizeMatchType(string(t.MatchType)),
		"case_sensitive":        t.CaseSensitive,
		"action_type":           normalizeActionType(string(t.ActionType)),
		"response_text":         normalizeResponseItems(t.ResponseText),
		"send_as_reply":         t.Reply,
		"preview_first_link":    t.Preview,
		"delete_source_message": t.DeleteSource,
		"pass_through":          t.PassThrough,
		"chance":                sanitizeChance(t.Chance),
		"created_at":            t.CreatedAt,
		"updated_at":            t.UpdatedAt,
		"regex_error":           t.RegexError,
	}
}

func docToTriggerFromRaw(raw bson.M) (Trigger, bool, error) {
	var base mongoTriggerDoc
	b, err := bson.Marshal(raw)
	if err != nil {
		return Trigger{}, false, err
	}
	if err := bson.Unmarshal(b, &base); err != nil {
		return Trigger{}, false, err
	}
	items, needsMigration := responseItemsFromRaw(raw["response_text"])
	t := Trigger{
		ID:            base.ID,
		UID:           strings.TrimSpace(base.UID),
		Priority:      base.Priority,
		RegexBenchUS:  base.RegexBenchUS,
		Title:         base.Title,
		Enabled:       base.Enabled,
		TriggerMode:   normalizeTriggerMode(base.TriggerMode),
		AdminMode:     normalizeAdminMode(base.AdminMode),
		MatchText:     base.MatchText,
		MatchType:     match.NormalizeMatchType(base.MatchType),
		CaseSensitive: base.CaseSensitive,
		ActionType:    normalizeActionType(base.ActionType),
		ResponseText:  items,
		Reply:         base.Reply,
		Preview:       base.Preview,
		DeleteSource:  base.DeleteSource,
		PassThrough:   base.PassThrough,
		Chance:        sanitizeChance(base.Chance),
		CreatedAt:     base.CreatedAt,
		UpdatedAt:     base.UpdatedAt,
		RegexError:    base.RegexError,
	}
	return t, needsMigration, nil
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
		var raw bson.M
		if err := cur.Decode(&raw); err != nil {
			return nil, err
		}
		t, needsMigration, err := docToTriggerFromRaw(raw)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
		if needsMigration {
			_, _ = m.triggers.UpdateOne(ctx, bson.M{"id": t.ID}, bson.M{"$set": bson.M{"response_text": normalizeResponseItems(t.ResponseText)}})
		}
	}
	return out, cur.Err()
}

func (m *mongoBackend) getTrigger(id int64) (*Trigger, error) {
	ctx, cancel := mongoCtx()
	defer cancel()
	var raw bson.M
	err := m.triggers.FindOne(ctx, bson.M{"id": id}).Decode(&raw)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	t, needsMigration, err := docToTriggerFromRaw(raw)
	if err != nil {
		return nil, err
	}
	if needsMigration {
		_, _ = m.triggers.UpdateOne(ctx, bson.M{"id": t.ID}, bson.M{"$set": bson.M{"response_text": normalizeResponseItems(t.ResponseText)}})
	}
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

func (m *mongoBackend) nextTemplateID() (int64, error) {
	ctx, cancel := mongoCtx()
	defer cancel()
	opts := options.FindOneAndUpdate().SetUpsert(true).SetReturnDocument(options.After)
	var out mongoCounterDoc
	err := m.counters.FindOneAndUpdate(ctx, bson.M{"_id": "response_templates"}, bson.M{"$inc": bson.M{"seq": 1}}, opts).Decode(&out)
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
	_, err := m.triggers.InsertOne(context.Background(), triggerToDocMap(t))
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
		"trigger_mode":          normalizeTriggerMode(string(t.TriggerMode)),
		"admin_mode":            normalizeAdminMode(string(t.AdminMode)),
		"match_text":            t.MatchText,
		"match_type":            match.NormalizeMatchType(string(t.MatchType)),
		"case_sensitive":        t.CaseSensitive,
		"action_type":           normalizeActionType(string(t.ActionType)),
		"response_text":         normalizeResponseItems(t.ResponseText),
		"send_as_reply":         t.Reply,
		"preview_first_link":    t.Preview,
		"delete_source_message": t.DeleteSource,
		"pass_through":          t.PassThrough,
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

func (m *mongoBackend) listTemplates() ([]ResponseTemplate, error) {
	ctx, cancel := mongoCtx()
	defer cancel()
	cur, err := m.templates.Find(ctx, bson.M{}, options.Find().SetSort(bson.D{{Key: "id", Value: 1}}))
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	out := make([]ResponseTemplate, 0, 32)
	for cur.Next(ctx) {
		var d mongoTemplateDoc
		if err := cur.Decode(&d); err != nil {
			return nil, err
		}
		out = append(out, ResponseTemplate{
			ID:        d.ID,
			Key:       strings.TrimSpace(d.Key),
			Title:     d.Title,
			Text:      d.Text,
			CreatedAt: d.CreatedAt,
			UpdatedAt: d.UpdatedAt,
		})
	}
	return out, nil
}

func (m *mongoBackend) getTemplate(id int64) (*ResponseTemplate, error) {
	ctx, cancel := mongoCtx()
	defer cancel()
	var d mongoTemplateDoc
	err := m.templates.FindOne(ctx, bson.M{"id": id}).Decode(&d)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	t := ResponseTemplate{
		ID:        d.ID,
		Key:       strings.TrimSpace(d.Key),
		Title:     d.Title,
		Text:      d.Text,
		CreatedAt: d.CreatedAt,
		UpdatedAt: d.UpdatedAt,
	}
	return &t, nil
}

func (m *mongoBackend) getTemplateByKey(key string) (*ResponseTemplate, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return nil, nil
	}
	ctx, cancel := mongoCtx()
	defer cancel()
	var d mongoTemplateDoc
	err := m.templates.FindOne(ctx, bson.M{"key": key}).Decode(&d)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	t := ResponseTemplate{
		ID:        d.ID,
		Key:       strings.TrimSpace(d.Key),
		Title:     d.Title,
		Text:      d.Text,
		CreatedAt: d.CreatedAt,
		UpdatedAt: d.UpdatedAt,
	}
	return &t, nil
}

func (m *mongoBackend) insertTemplate(t ResponseTemplate, now int64) error {
	if t.ID <= 0 {
		id, err := m.nextTemplateID()
		if err != nil {
			return err
		}
		t.ID = id
	}
	t.CreatedAt = now
	t.UpdatedAt = now
	_, err := m.templates.InsertOne(context.Background(), bson.M{
		"id":         t.ID,
		"key":        strings.TrimSpace(t.Key),
		"title":      t.Title,
		"text":       t.Text,
		"created_at": t.CreatedAt,
		"updated_at": t.UpdatedAt,
	})
	return err
}

func (m *mongoBackend) updateTemplate(t ResponseTemplate, now int64) error {
	ctx, cancel := mongoCtx()
	defer cancel()
	t.UpdatedAt = now
	set := bson.M{
		"key":        strings.TrimSpace(t.Key),
		"title":      t.Title,
		"text":       t.Text,
		"updated_at": now,
	}
	res, err := m.templates.UpdateOne(ctx, bson.M{"id": t.ID}, bson.M{"$set": set})
	if err != nil {
		return err
	}
	if res.MatchedCount == 0 {
		return fmt.Errorf("template id=%d not found", t.ID)
	}
	return nil
}

func (m *mongoBackend) deleteTemplate(id int64) error {
	ctx, cancel := mongoCtx()
	defer cancel()
	_, err := m.templates.DeleteOne(ctx, bson.M{"id": id})
	return err
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

func sanitizePendingMessages(items []string) []string {
	if len(items) == 0 {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		val := strings.TrimSpace(item)
		if val == "" {
			continue
		}
		out = append(out, clipText(val, 900))
	}
	if len(out) == 0 {
		return nil
	}
	if len(out) > 200 {
		out = out[len(out)-200:]
	}
	return out
}

func (m *mongoBackend) getParticipantPortrait(chatID, userID int64) (string, error) {
	ctx, cancel := mongoCtx()
	defer cancel()
	var d mongoParticipantProfileDoc
	err := m.profiles.FindOne(ctx, bson.M{"chat_id": chatID, "user_id": userID}).Decode(&d)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(d.PortraitText), nil
}

func (m *mongoBackend) appendParticipantMessage(chatID, userID int64, message string, batchSize int, now int64) (bool, []string, string, error) {
	message = strings.TrimSpace(message)
	if message == "" {
		return false, nil, "", nil
	}
	if batchSize <= 0 {
		batchSize = 10
	}
	if now <= 0 {
		now = time.Now().Unix()
	}
	ctx, cancel := mongoCtx()
	defer cancel()

	var d mongoParticipantProfileDoc
	err := m.profiles.FindOne(ctx, bson.M{"chat_id": chatID, "user_id": userID}).Decode(&d)
	if err != nil && !errors.Is(err, mongo.ErrNoDocuments) {
		return false, nil, "", err
	}
	oldPortrait := strings.TrimSpace(d.PortraitText)
	pending := sanitizePendingMessages(append(d.PendingMessages, message))
	ready := len(pending) >= batchSize
	if ready {
		batch := append([]string(nil), pending[:batchSize]...)
		remaining := sanitizePendingMessages(pending[batchSize:])
		_, err := m.profiles.UpdateOne(
			ctx,
			bson.M{"chat_id": chatID, "user_id": userID},
			bson.M{
				"$set": bson.M{
					"chat_id":          chatID,
					"user_id":          userID,
					"pending_messages": remaining,
					"updated_at":       now,
				},
				"$setOnInsert": bson.M{
					"created_at":    now,
					"portrait_text": "",
				},
			},
			options.Update().SetUpsert(true),
		)
		if err != nil {
			return false, nil, "", err
		}
		return true, batch, oldPortrait, nil
	}
	_, err = m.profiles.UpdateOne(
		ctx,
		bson.M{"chat_id": chatID, "user_id": userID},
		bson.M{
			"$set": bson.M{
				"chat_id":          chatID,
				"user_id":          userID,
				"pending_messages": pending,
				"updated_at":       now,
			},
			"$setOnInsert": bson.M{
				"created_at":    now,
				"portrait_text": "",
			},
		},
		options.Update().SetUpsert(true),
	)
	if err != nil {
		return false, nil, "", err
	}
	return false, nil, oldPortrait, nil
}

func (m *mongoBackend) saveParticipantPortrait(chatID, userID int64, portrait string, now int64) error {
	portrait = strings.TrimSpace(portrait)
	if now <= 0 {
		now = time.Now().Unix()
	}
	ctx, cancel := mongoCtx()
	defer cancel()
	_, err := m.profiles.UpdateOne(
		ctx,
		bson.M{"chat_id": chatID, "user_id": userID},
		bson.M{
			"$set": bson.M{
				"chat_id":       chatID,
				"user_id":       userID,
				"portrait_text": portrait,
				"updated_at":    now,
			},
			"$setOnInsert": bson.M{
				"created_at": now,
			},
		},
		options.Update().SetUpsert(true),
	)
	return err
}

func (m *mongoBackend) prependParticipantMessages(chatID, userID int64, messages []string, now int64) error {
	messages = sanitizePendingMessages(messages)
	if len(messages) == 0 {
		return nil
	}
	if now <= 0 {
		now = time.Now().Unix()
	}
	ctx, cancel := mongoCtx()
	defer cancel()

	var d mongoParticipantProfileDoc
	err := m.profiles.FindOne(ctx, bson.M{"chat_id": chatID, "user_id": userID}).Decode(&d)
	if err != nil && !errors.Is(err, mongo.ErrNoDocuments) {
		return err
	}
	pending := sanitizePendingMessages(append(messages, d.PendingMessages...))
	_, err = m.profiles.UpdateOne(
		ctx,
		bson.M{"chat_id": chatID, "user_id": userID},
		bson.M{
			"$set": bson.M{
				"chat_id":          chatID,
				"user_id":          userID,
				"pending_messages": pending,
				"updated_at":       now,
			},
			"$setOnInsert": bson.M{
				"created_at":    now,
				"portrait_text": strings.TrimSpace(d.PortraitText),
			},
		},
		options.Update().SetUpsert(true),
	)
	return err
}
