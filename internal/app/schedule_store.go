package app

// Хранилище расписаний участников (коллекция participant_schedules).

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// maxSchedulesPerUser limits how many schedules one participant may keep.
const maxSchedulesPerUser = 10

// maxScheduleActionRunes bounds the stored action text.
const maxScheduleActionRunes = 300

// errScheduleLimitReached is returned when the participant already keeps the
// maximum number of schedules.
var errScheduleLimitReached = errors.New("schedule limit reached")

// clipScheduleAction trims long action text so the DB stays tidy.
func clipScheduleAction(action string) string {
	action = strings.TrimSpace(action)
	runes := []rune(action)
	if len(runes) <= maxScheduleActionRunes {
		return action
	}
	return strings.TrimSpace(string(runes[:maxScheduleActionRunes]))
}

// maxScheduleRunsPerDay is the anti-spam cap the group asked for: a participant
// gets at most three scheduled messages per day, across all their schedules.
const maxScheduleRunsPerDay = 3

// ParticipantSchedule is one cron entry owned by a chat participant.
type ParticipantSchedule struct {
	ID        string
	ChatID    int64
	UserID    int64
	Username  string
	Cron      string
	Action    string
	Enabled   bool
	CreatedAt int64
	LastRunAt int64
	RunDate   string
	RunsToday int
}

type mongoScheduleDoc struct {
	ID        string `bson:"_id"`
	ChatID    int64  `bson:"chat_id"`
	UserID    int64  `bson:"user_id"`
	Username  string `bson:"username"`
	Cron      string `bson:"cron"`
	Action    string `bson:"action"`
	Enabled   bool   `bson:"enabled"`
	CreatedAt int64  `bson:"created_at"`
	LastRunAt int64  `bson:"last_run_at"`
	RunDate   string `bson:"run_date"`
	RunsToday int    `bson:"runs_today"`
}

func newScheduleID() string {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return strings.ReplaceAll(time.Now().UTC().Format("20060102150405.000000000"), ".", "")
	}
	return hex.EncodeToString(buf[:])
}

func (m *mongoBackend) saveSchedules(chatID, userID int64, username string, directives []ScheduleDirective, now time.Time) ([]ParticipantSchedule, error) {
	if m == nil || len(directives) == 0 {
		return nil, nil
	}
	ctxCount, cancelCount := mongoCtx()
	existing, err := m.schedules.CountDocuments(ctxCount, bson.M{"chat_id": chatID, "user_id": userID})
	cancelCount()
	if err != nil {
		return nil, err
	}
	if existing >= maxSchedulesPerUser {
		return nil, errScheduleLimitReached
	}
	slots := maxSchedulesPerUser - int(existing)
	if slots < len(directives) {
		directives = directives[:slots]
	}
	saved := make([]ParticipantSchedule, 0, len(directives))
	for _, d := range directives {
		spec := &ParticipantSchedule{
			ID:        newScheduleID(),
			ChatID:    chatID,
			UserID:    userID,
			Username:  strings.TrimSpace(username),
			Cron:      strings.TrimSpace(d.Cron),
			Action:    clipScheduleAction(d.Action),
			Enabled:   true,
			CreatedAt: now.Unix(),
		}
		ctx, cancel := mongoCtx()
		_, err := m.schedules.InsertOne(ctx, mongoScheduleDoc{
			ID:        spec.ID,
			ChatID:    spec.ChatID,
			UserID:    spec.UserID,
			Username:  spec.Username,
			Cron:      spec.Cron,
			Action:    spec.Action,
			Enabled:   true,
			CreatedAt: spec.CreatedAt,
		})
		cancel()
		if err != nil {
			return saved, err
		}
		saved = append(saved, *spec)
	}
	return saved, nil
}

func (m *mongoBackend) listSchedules(chatID, userID int64) ([]ParticipantSchedule, error) {
	ctx, cancel := mongoCtx()
	defer cancel()
	filter := bson.M{"chat_id": chatID}
	if userID != 0 {
		filter["user_id"] = userID
	}
	cur, err := m.schedules.Find(ctx, filter, options.Find().SetSort(bson.D{{Key: "created_at", Value: 1}}).SetLimit(200))
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	out := make([]ParticipantSchedule, 0, 8)
	for cur.Next(ctx) {
		var d mongoScheduleDoc
		if err := cur.Decode(&d); err != nil {
			return nil, err
		}
		out = append(out, ParticipantSchedule{
			ID: d.ID, ChatID: d.ChatID, UserID: d.UserID, Username: d.Username,
			Cron: d.Cron, Action: d.Action, Enabled: d.Enabled, CreatedAt: d.CreatedAt,
			LastRunAt: d.LastRunAt, RunDate: d.RunDate, RunsToday: d.RunsToday,
		})
	}
	return out, cur.Err()
}

func (m *mongoBackend) listEnabledSchedules() ([]ParticipantSchedule, error) {
	ctx, cancel := mongoCtx()
	defer cancel()
	cur, err := m.schedules.Find(ctx, bson.M{"enabled": true}, options.Find().SetLimit(500))
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	out := make([]ParticipantSchedule, 0, 16)
	for cur.Next(ctx) {
		var d mongoScheduleDoc
		if err := cur.Decode(&d); err != nil {
			return nil, err
		}
		out = append(out, ParticipantSchedule{
			ID: d.ID, ChatID: d.ChatID, UserID: d.UserID, Username: d.Username,
			Cron: d.Cron, Action: d.Action, Enabled: d.Enabled, CreatedAt: d.CreatedAt,
			LastRunAt: d.LastRunAt, RunDate: d.RunDate, RunsToday: d.RunsToday,
		})
	}
	return out, cur.Err()
}

func (m *mongoBackend) deleteSchedule(chatID, userID int64, id string) (bool, error) {
	ctx, cancel := mongoCtx()
	defer cancel()
	res, err := m.schedules.DeleteOne(ctx, bson.M{"_id": id, "chat_id": chatID, "user_id": userID})
	if err != nil {
		return false, err
	}
	return res.DeletedCount > 0, nil
}

func (m *mongoBackend) deleteSchedulesForUser(chatID, userID int64) (int64, error) {
	ctx, cancel := mongoCtx()
	defer cancel()
	res, err := m.schedules.DeleteMany(ctx, bson.M{"chat_id": chatID, "user_id": userID})
	if err != nil {
		return 0, err
	}
	return res.DeletedCount, nil
}

// runsOnDate sums today's firings across all schedules of the user: the daily
// cap is per participant, not per schedule.
func (m *mongoBackend) runsOnDate(chatID, userID int64, date string) (int, error) {
	ctx, cancel := mongoCtx()
	defer cancel()
	cur, err := m.schedules.Find(ctx, bson.M{"chat_id": chatID, "user_id": userID, "run_date": date})
	if err != nil {
		return 0, err
	}
	defer cur.Close(ctx)
	total := 0
	for cur.Next(ctx) {
		var d mongoScheduleDoc
		if err := cur.Decode(&d); err != nil {
			return 0, err
		}
		total += d.RunsToday
	}
	return total, cur.Err()
}

func (m *mongoBackend) markScheduleRun(id, prevDate, today string, now time.Time) error {
	ctx, cancel := mongoCtx()
	defer cancel()
	if prevDate != today {
		// New day: start the per-schedule counter from one.
		_, err := m.schedules.UpdateOne(ctx, bson.M{"_id": id}, bson.M{
			"$set": bson.M{"last_run_at": now.Unix(), "run_date": today, "runs_today": 1},
		})
		return err
	}
	_, err := m.schedules.UpdateOne(ctx, bson.M{"_id": id}, bson.M{
		"$set": bson.M{"last_run_at": now.Unix(), "run_date": today},
		"$inc": bson.M{"runs_today": 1},
	})
	return err
}

// Ensure the schedule collection exists and is indexed.
func (m *mongoBackend) ensureScheduleIndexes() error {
	if m == nil {
		return errors.New("mongo backend is nil")
	}
	ctx, cancel := mongoCtx()
	defer cancel()
	_, err := m.schedules.Indexes().CreateMany(ctx, []mongo.IndexModel{
		{Keys: bson.D{{Key: "chat_id", Value: 1}, {Key: "user_id", Value: 1}}},
		{Keys: bson.D{{Key: "enabled", Value: 1}}},
	})
	return err
}
