package trigger

import (
	"math/rand"
	"strings"
	"sync"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"trigger-admin-bot/internal/engine"
	"trigger-admin-bot/internal/match"
	"trigger-admin-bot/internal/model"
)

type IdleTracker struct {
	mu    sync.RWMutex
	chats map[int64]chatIdleState
}

type chatIdleState struct {
	firstSeen    time.Time
	lastActivity time.Time
}

func NewIdleTracker() *IdleTracker {
	return &IdleTracker{
		chats: make(map[int64]chatIdleState),
	}
}

func (t *IdleTracker) Seen(chatID int64, now time.Time) {
	if t == nil || chatID == 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	st := t.chats[chatID]
	if st.firstSeen.IsZero() {
		st.firstSeen = now
	}
	t.chats[chatID] = st
}

func (t *IdleTracker) MarkActivity(chatID int64, now time.Time) {
	if t == nil || chatID == 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	st := t.chats[chatID]
	if st.firstSeen.IsZero() {
		st.firstSeen = now
	}
	st.lastActivity = now
	t.chats[chatID] = st
}

func (t *IdleTracker) ShouldAutoReply(chatID int64, idleAfter time.Duration, now time.Time) bool {
	if t == nil || chatID == 0 || idleAfter <= 0 {
		return false
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	st, ok := t.chats[chatID]
	if !ok {
		return false
	}
	base := st.lastActivity
	if base.IsZero() {
		base = st.firstSeen
	}
	if base.IsZero() {
		return false
	}
	return now.Sub(base) >= idleAfter
}

func SelectIdleAutoReplyTrigger(bot *tgbotapi.BotAPI, msg *tgbotapi.Message, items []model.Trigger, isAdminFn func() bool) (*model.Trigger, time.Duration) {
	adminChecked := false
	isAdmin := false
	if msg == nil {
		return nil, 0
	}
	for i := range items {
		it := items[i]
		if !it.Enabled || it.ActionType != "gpt_prompt" || match.NormalizeMatchType(it.MatchType) != "idle" {
			continue
		}
		idleAfter := parseIdleDuration(it.MatchText)
		if idleAfter <= 0 {
			continue
		}
		if !engine.TriggerModeMatches(bot, &it, msg) {
			continue
		}
		if it.AdminMode != "anybody" {
			if !adminChecked {
				isAdmin = isAdminFn()
				adminChecked = true
			}
			if it.AdminMode == "admins" && !isAdmin {
				continue
			}
			if it.AdminMode == "not_admins" && isAdmin {
				continue
			}
		}
		if it.Chance < 100 && rand.Intn(100) >= it.Chance {
			continue
		}
		cp := it
		return &cp, idleAfter
	}
	return nil, 0
}

func parseIdleDuration(raw string) time.Duration {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}
	if v, err := time.ParseDuration(raw); err == nil && v > 0 {
		return v
	}
	if minutes, err := parseInt(raw); err == nil && minutes > 0 {
		return time.Duration(minutes) * time.Minute
	}
	return 0
}

func parseInt(raw string) (int64, error) {
	var out int64
	for _, r := range raw {
		if r < '0' || r > '9' {
			return 0, errInvalidInt
		}
		out = out*10 + int64(r-'0')
	}
	if out == 0 {
		return 0, errInvalidInt
	}
	return out, nil
}

var errInvalidInt = &intParseError{}

type intParseError struct{}

func (e *intParseError) Error() string {
	return "invalid integer"
}
