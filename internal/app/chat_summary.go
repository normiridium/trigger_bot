package app

import (
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

const (
	chatSummaryQueueSize = 32
)

type chatSummaryTask struct {
	chatID       int64
	messages     []string
	lastMessage  int
	lastUnixTime int64
}

type chatSummaryTracker struct {
	store *Store
	every int
	max   int

	queue chan chatSummaryTask
	stop  chan struct{}
	wg    sync.WaitGroup

	bufferMu sync.Mutex
	buffer   map[int64][]string

	historyMu sync.RWMutex
	history   map[int64][]recentChatMessage

	seenMu sync.Mutex
	seen   map[string]time.Time
}

func newChatSummaryTracker(store *Store, every int, max int) *chatSummaryTracker {
	if store == nil {
		return nil
	}
	if every <= 0 {
		every = 200
	}
	if max <= 0 {
		max = 1000
	}
	t := &chatSummaryTracker{
		store:  store,
		every:  every,
		max:    max,
		queue:  make(chan chatSummaryTask, chatSummaryQueueSize),
		stop:   make(chan struct{}),
		buffer: make(map[int64][]string),
		history: make(map[int64][]recentChatMessage),
		seen:   make(map[string]time.Time),
	}
	t.wg.Add(1)
	go t.worker()
	return t
}

func (t *chatSummaryTracker) Close() {
	if t == nil {
		return
	}
	close(t.stop)
	t.wg.Wait()
}

func (t *chatSummaryTracker) ObserveMessage(msg *tgbotapi.Message, text string) {
	if t == nil || t.store == nil || msg == nil || msg.Chat == nil {
		return
	}
	chatID := msg.Chat.ID
	if chatID == 0 {
		return
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	if t.isDuplicate(chatID, msg.MessageID) {
		return
	}
	name := "участник"
	if msg.From != nil {
		name = strings.TrimSpace(msg.From.FirstName)
		if name == "" {
			name = strings.TrimSpace(msg.From.UserName)
		}
	}
	line := fmt.Sprintf("%s: %s", name, clipText(text, 900))
	t.addHistory(chatID, recentChatMessage{
		MessageID: msg.MessageID,
		UserName:  name,
		Text:      strings.TrimSpace(text),
		At:        time.Now(),
	})

	batch, ok := t.takeBatch(chatID, line)
	if !ok || len(batch) == 0 {
		return
	}
	task := chatSummaryTask{
		chatID:       chatID,
		messages:     append([]string(nil), batch...),
		lastMessage:  msg.MessageID,
		lastUnixTime: time.Now().Unix(),
	}
	select {
	case t.queue <- task:
	default:
		log.Printf("chat summary queue full; batch requeued chat=%d", chatID)
		t.prependBatch(chatID, batch)
	}
}

func (t *chatSummaryTracker) addHistory(chatID int64, msg recentChatMessage) {
	if t == nil || chatID == 0 || strings.TrimSpace(msg.Text) == "" {
		return
	}
	t.historyMu.Lock()
	items := append(t.history[chatID], msg)
	if len(items) > t.max {
		items = items[len(items)-t.max:]
	}
	t.history[chatID] = items
	t.historyMu.Unlock()
}

func (t *chatSummaryTracker) RecentText(chatID int64, limit int) string {
	if t == nil || chatID == 0 {
		return ""
	}
	t.historyMu.RLock()
	items := append([]recentChatMessage(nil), t.history[chatID]...)
	t.historyMu.RUnlock()
	return renderRecentContextLines(items, 0, limit)
}

func (t *chatSummaryTracker) isDuplicate(chatID int64, messageID int) bool {
	if chatID == 0 || messageID <= 0 {
		return false
	}
	key := fmt.Sprintf("%d:%d", chatID, messageID)
	now := time.Now()
	t.seenMu.Lock()
	defer t.seenMu.Unlock()
	if ts, ok := t.seen[key]; ok && now.Sub(ts) < 30*time.Minute {
		return true
	}
	t.seen[key] = now
	if len(t.seen) > 4096 {
		cutoff := now.Add(-30 * time.Minute)
		for k, ts := range t.seen {
			if ts.Before(cutoff) {
				delete(t.seen, k)
			}
		}
	}
	return false
}

func (t *chatSummaryTracker) takeBatch(chatID int64, line string) ([]string, bool) {
	if chatID == 0 {
		return nil, false
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return nil, false
	}
	t.bufferMu.Lock()
	defer t.bufferMu.Unlock()
	items := append(t.buffer[chatID], line)
	if len(items) < t.every {
		t.buffer[chatID] = items
		return nil, false
	}
	batch := append([]string(nil), items[:t.every]...)
	rest := append([]string(nil), items[t.every:]...)
	if len(rest) == 0 {
		delete(t.buffer, chatID)
	} else {
		t.buffer[chatID] = rest
	}
	return batch, true
}

func (t *chatSummaryTracker) prependBatch(chatID int64, messages []string) {
	if chatID == 0 || len(messages) == 0 {
		return
	}
	clean := make([]string, 0, len(messages))
	for _, m := range messages {
		v := strings.TrimSpace(m)
		if v != "" {
			clean = append(clean, clipText(v, 900))
		}
	}
	if len(clean) == 0 {
		return
	}
	t.bufferMu.Lock()
	existing := t.buffer[chatID]
	merged := make([]string, 0, len(clean)+len(existing))
	merged = append(merged, clean...)
	merged = append(merged, existing...)
	limit := t.every * 2
	if limit < 400 {
		limit = 400
	}
	if len(merged) > limit {
		merged = merged[:limit]
	}
	t.buffer[chatID] = merged
	t.bufferMu.Unlock()
}

func (t *chatSummaryTracker) worker() {
	defer t.wg.Done()
	for {
		select {
		case <-t.stop:
			return
		case task := <-t.queue:
			t.processTask(task)
		}
	}
}

func (t *chatSummaryTracker) processTask(task chatSummaryTask) {
	summary, err := generateChatSummary(task.messages)
	if err != nil {
		log.Printf("chat summary generation failed chat=%d err=%v", task.chatID, err)
		t.prependBatch(task.chatID, task.messages)
		return
	}
	if err := t.store.SaveChatSummary(ChatSummary{
		ChatID:             task.chatID,
		Summary:            summary,
		MessagesSince:      0,
		LastMessageID:      task.lastMessage,
		LastMessageUnix:    task.lastUnixTime,
		SummarizedMessages: len(task.messages),
		UpdatedAt:          time.Now().Unix(),
	}); err != nil {
		log.Printf("chat summary save failed chat=%d err=%v", task.chatID, err)
		t.prependBatch(task.chatID, task.messages)
		return
	}
	if debugTriggerLogEnabled {
		log.Printf("chat summary updated chat=%d batch=%d", task.chatID, len(task.messages))
	}
}
