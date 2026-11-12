package app

// Интеграционный тест изоляции: чужое расписание удалить нельзя.
// Требует TEST_MONGO_URI (иначе skip).

import (
	"context"
	"os"
	"testing"

	"go.mongodb.org/mongo-driver/bson"
)

func TestScheduleDeleteIsolation(t *testing.T) {
	uri := os.Getenv("TEST_MONGO_URI")
	if uri == "" {
		t.Skip("TEST_MONGO_URI is not set")
	}
	store, err := openMongoStore(uri)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	chatID := int64(-1009999999999)
	ownerID := int64(1519741912)
	strangerID := int64(777000111)
	cleanup := func() {
		for _, uid := range []int64{ownerID, strangerID} {
			_, _ = store.mg.schedules.DeleteMany(context.Background(), bson.M{"chat_id": chatID, "user_id": uid})
		}
	}
	cleanup()
	defer cleanup()

	ownerSchedules, err := store.SaveSchedules(chatID, ownerID, "owner", []ScheduleDirective{
		{Cron: "0 10 * * *", Action: "хозяин"},
	})
	if err != nil {
		t.Fatalf("save owner: %v", err)
	}
	if len(ownerSchedules) != 1 {
		t.Fatalf("expected 1 owner schedule, got %d", len(ownerSchedules))
	}
	ownerIDOfSchedule := ownerSchedules[0].ID

	// Чужой участник в том же чате не должен удалить расписание владельца.
	if ok, err := store.DeleteSchedule(chatID, strangerID, ownerIDOfSchedule); err != nil || ok {
		t.Fatalf("чужое расписание удалилось: ok=%v err=%v", ok, err)
	}
	// И «удалить всё» не должно задеть чужое.
	if n, err := store.DeleteSchedulesForUser(chatID, strangerID); err != nil || n != 0 {
		t.Fatalf("чужая очистка что-то удалила: n=%d err=%v", n, err)
	}
	// Владелец своё удаляет.
	if ok, err := store.DeleteSchedule(chatID, ownerID, ownerIDOfSchedule); err != nil || !ok {
		t.Fatalf("владелец не смог удалить своё: ok=%v err=%v", ok, err)
	}
	items, err := store.ListSchedules(chatID, ownerID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("после удаления осталось %d расписаний", len(items))
	}
}
