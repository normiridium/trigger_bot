package gpt

import (
	"sync"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"trigger-admin-bot/internal/model"
)

type PromptTask struct {
	Bot              *tgbotapi.BotAPI
	Trigger          model.Trigger
	Msg              *tgbotapi.Message
	RecentContext    string
	IdleMarkActivity func(chatID int64, now time.Time)
	ChatID           int64
}

type Debouncer struct {
	mu      sync.Mutex
	delay   time.Duration
	pending map[int64]*debounceEntry
	run     func(PromptTask)
}

type debounceEntry struct {
	timer      *time.Timer
	task       PromptTask
	hasPending bool
	lastSentAt time.Time
}

func NewDebouncer(delay time.Duration, run func(PromptTask)) *Debouncer {
	if delay <= 0 || run == nil {
		return nil
	}
	return &Debouncer{
		delay:   delay,
		pending: make(map[int64]*debounceEntry),
		run:     run,
	}
}

func (d *Debouncer) Schedule(chatID int64, task PromptTask) {
	if d == nil || chatID == 0 {
		if d != nil && d.run != nil {
			d.run(task)
		}
		return
	}

	executeNow := false
	now := time.Now()
	d.mu.Lock()
	ent, ok := d.pending[chatID]
	if !ok {
		ent = &debounceEntry{}
		d.pending[chatID] = ent
	}

	// Leading edge: if quiet window already passed, answer immediately.
	if ent.lastSentAt.IsZero() || now.Sub(ent.lastSentAt) >= d.delay {
		ent.task = task
		ent.hasPending = false
		ent.lastSentAt = now
		if ent.timer != nil {
			ent.timer.Stop()
			ent.timer = nil
		}
		executeNow = true
	} else {
		// Trailing edge inside active window: keep only latest task.
		ent.task = task
		ent.hasPending = true
		remaining := d.delay - now.Sub(ent.lastSentAt)
		if remaining < 10*time.Millisecond {
			remaining = 10 * time.Millisecond
		}
		if ent.timer != nil {
			ent.timer.Stop()
		}
		ent.timer = time.AfterFunc(remaining, func() {
			d.fire(chatID)
		})
	}
	d.mu.Unlock()

	if executeNow {
		d.run(task)
	}
}

func (d *Debouncer) fire(chatID int64) {
	d.mu.Lock()
	ent := d.pending[chatID]
	if ent == nil {
		d.mu.Unlock()
		return
	}
	ent.timer = nil
	if !ent.hasPending {
		d.mu.Unlock()
		return
	}
	task := ent.task
	ent.hasPending = false
	ent.lastSentAt = time.Now()
	d.mu.Unlock()
	d.run(task)
}
