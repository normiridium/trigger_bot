package app

// Интеграционный тест лимита расписаний: требует живой MongoDB через
// TEST_MONGO_URI. Без переменной — skip, чтобы обычный `go test` не падал.

import (
	"context"
	"errors"
	"os"
	"testing"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

func TestScheduleLimitIntegration(t *testing.T) {
	uri := os.Getenv("TEST_MONGO_URI")
	if uri == "" {
		t.Skip("TEST_MONGO_URI is not set")
	}
	store, err := openMongoStore(uri)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	// Dedicated fake participant so real schedules are untouched.
	chatID := int64(-1009999999999)
	userID := int64(1519741912)
	cleanup := func() {
		_, _ = store.mg.schedules.DeleteMany(context.Background(), bson.M{"chat_id": chatID, "user_id": userID})
	}
	cleanup()
	defer cleanup()

	for i := 0; i < maxSchedulesPerUser; i++ {
		saved, err := store.SaveSchedules(chatID, userID, "limittest", []ScheduleDirective{{Cron: "0 10 * * *", Action: "тест"}})
		if err != nil {
			t.Fatalf("save %d: %v", i+1, err)
		}
		if len(saved) != 1 {
			t.Fatalf("save %d: expected 1 schedule, got %d", i+1, len(saved))
		}
		if _, err := primitive.ObjectIDFromHex(saved[0].ID); err == nil {
			t.Fatalf("ids must be opaque, got ObjectID-like %q", saved[0].ID)
		}
	}
	if _, err := store.SaveSchedules(chatID, userID, "limittest", []ScheduleDirective{{Cron: "0 10 * * *", Action: "лишний"}}); !errors.Is(err, errScheduleLimitReached) {
		t.Fatalf("expected errScheduleLimitReached, got %v", err)
	}
	items, err := store.ListSchedules(chatID, userID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(items) != maxSchedulesPerUser {
		t.Fatalf("expected %d schedules, got %d", maxSchedulesPerUser, len(items))
	}
}
