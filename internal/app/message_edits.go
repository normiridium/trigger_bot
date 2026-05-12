package app

import (
	"fmt"
	"strings"
	"sync"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

type messageEditState struct {
	text      string
	caption   string
	updatedAt time.Time
	rev       int64
	changed   chan struct{}
}

var messageEdits = struct {
	mu    sync.RWMutex
	items map[string]*messageEditState
}{
	items: make(map[string]*messageEditState),
}

func messageEditKey(chatID int64, messageID int) string {
	return fmt.Sprintf("%d:%d", chatID, messageID)
}

func trackMessageRevision(msg *tgbotapi.Message) {
	if msg == nil || msg.Chat == nil || msg.Chat.ID == 0 || msg.MessageID == 0 {
		return
	}
	key := messageEditKey(msg.Chat.ID, msg.MessageID)
	text := strings.TrimSpace(msg.Text)
	caption := strings.TrimSpace(msg.Caption)
	now := time.Now()

	messageEdits.mu.Lock()
	defer messageEdits.mu.Unlock()
	st, ok := messageEdits.items[key]
	if !ok {
		messageEdits.items[key] = &messageEditState{
			text:      text,
			caption:   caption,
			updatedAt: now,
			changed:   make(chan struct{}),
		}
		return
	}
	if st.text == text && st.caption == caption {
		return
	}
	st.text = text
	st.caption = caption
	st.updatedAt = now
	st.rev++
	close(st.changed)
	st.changed = make(chan struct{})
}

func messageEditSnapshot(chatID int64, messageID int) (text string, caption string, rev int64, updatedAt time.Time, changed <-chan struct{}, ok bool) {
	key := messageEditKey(chatID, messageID)
	messageEdits.mu.RLock()
	st, ok := messageEdits.items[key]
	if !ok {
		messageEdits.mu.RUnlock()
		return "", "", 0, time.Time{}, nil, false
	}
	text = st.text
	caption = st.caption
	rev = st.rev
	updatedAt = st.updatedAt
	changed = st.changed
	messageEdits.mu.RUnlock()
	return text, caption, rev, updatedAt, changed, true
}

func waitForMessageEditsSettled(msg *tgbotapi.Message, quietWindow time.Duration, maxWait time.Duration) (waited time.Duration, changed bool) {
	if msg == nil || msg.Chat == nil || msg.Chat.ID == 0 || msg.MessageID == 0 {
		return 0, false
	}
	if quietWindow <= 0 || maxWait <= 0 {
		return 0, false
	}
	trackMessageRevision(msg)
	start := time.Now()
	deadline := start.Add(maxWait)
	_, _, baseRev, _, _, ok := messageEditSnapshot(msg.Chat.ID, msg.MessageID)
	if !ok {
		return 0, false
	}
	lastRev := baseRev

	for {
		now := time.Now()
		if !now.Before(deadline) {
			break
		}
		curText, curCaption, curRev, updatedAt, ch, has := messageEditSnapshot(msg.Chat.ID, msg.MessageID)
		if !has {
			break
		}
		if curRev != lastRev {
			lastRev = curRev
			changed = true
		}
		since := now.Sub(updatedAt)
		need := quietWindow - since
		if need <= 0 {
			msg.Text = curText
			msg.Caption = curCaption
			break
		}
		left := time.Until(deadline)
		if left <= 0 {
			break
		}
		wait := need
		if wait > left {
			wait = left
		}
		timer := time.NewTimer(wait)
		select {
		case <-timer.C:
		case <-ch:
			changed = true
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
	}
	waited = time.Since(start)
	return waited, changed
}

