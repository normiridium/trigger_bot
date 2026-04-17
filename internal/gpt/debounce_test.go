package gpt

import (
	"testing"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

func TestNewDebouncerInvalid(t *testing.T) {
	if got := NewDebouncer(0, func(PromptTask) {}); got != nil {
		t.Fatal("expected nil debouncer when delay is zero")
	}
	if got := NewDebouncer(10*time.Millisecond, nil); got != nil {
		t.Fatal("expected nil debouncer when run func is nil")
	}
}

func TestScheduleChatIDZeroRunsImmediately(t *testing.T) {
	called := 0
	d := NewDebouncer(100*time.Millisecond, func(task PromptTask) { called++ })
	d.Schedule(0, PromptTask{Msg: &tgbotapi.Message{MessageID: 1}})
	if called != 1 {
		t.Fatalf("expected immediate run for chatID=0, got %d", called)
	}
}
