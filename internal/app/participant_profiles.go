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
	participantPortraitBatchSize = 10
	participantPortraitQueueSize = 64
)

type participantPortraitTask struct {
	chatID      int64
	userID      int64
	messages    []string
	oldPortrait string
}

type participantPortraitManager struct {
	store *Store
	queue chan participantPortraitTask
	stop  chan struct{}
	wg    sync.WaitGroup

	cacheMu sync.RWMutex
	cache   map[string]string

	bufferMu sync.Mutex
	buffer   map[int64][]string

	seenMu sync.Mutex
	seen   map[string]time.Time
}

func newParticipantPortraitManager(store *Store) *participantPortraitManager {
	if store == nil {
		return nil
	}
	m := &participantPortraitManager{
		store:  store,
		queue:  make(chan participantPortraitTask, participantPortraitQueueSize),
		stop:   make(chan struct{}),
		cache:  make(map[string]string),
		buffer: make(map[int64][]string),
		seen:   make(map[string]time.Time),
	}
	m.wg.Add(1)
	go m.worker()
	return m
}

func (m *participantPortraitManager) Close() {
	if m == nil {
		return
	}
	close(m.stop)
	m.wg.Wait()
}

func participantPortraitKey(userID int64) string {
	return fmt.Sprintf("%d", userID)
}

func (m *participantPortraitManager) Portrait(chatID, userID int64) string {
	if m == nil || userID == 0 {
		return ""
	}
	key := participantPortraitKey(userID)
	m.cacheMu.RLock()
	if v, ok := m.cache[key]; ok {
		m.cacheMu.RUnlock()
		return v
	}
	m.cacheMu.RUnlock()

	portrait, err := m.store.GetParticipantPortrait(chatID, userID)
	if err != nil {
		log.Printf("participant portrait load failed chat=%d user=%d err=%v", chatID, userID, err)
		return ""
	}
	portrait = strings.TrimSpace(portrait)
	m.cacheMu.Lock()
	m.cache[key] = portrait
	m.cacheMu.Unlock()
	return portrait
}

func (m *participantPortraitManager) DeletePortrait(userID int64) error {
	if m == nil || userID == 0 {
		return nil
	}
	if err := m.store.DeleteParticipantPortrait(userID); err != nil {
		return err
	}

	key := participantPortraitKey(userID)
	m.cacheMu.Lock()
	delete(m.cache, key)
	m.cacheMu.Unlock()

	m.bufferMu.Lock()
	delete(m.buffer, userID)
	m.bufferMu.Unlock()
	return nil
}

func (m *participantPortraitManager) ObserveMessage(msg *tgbotapi.Message) {
	if m == nil || msg == nil || msg.Chat == nil || msg.From == nil {
		return
	}
	if msg.Chat.ID == 0 || msg.From.ID == 0 {
		return
	}
	text := strings.TrimSpace(firstNonEmptyUserText(msg))
	if text == "" {
		return
	}
	if m.isDuplicateMessage(msg.Chat.ID, msg.From.ID, msg.MessageID) {
		return
	}

	userID := msg.From.ID
	batch, ok := m.takeBatch(userID, text)
	if !ok || len(batch) == 0 {
		return
	}
	oldPortrait, err := m.store.GetParticipantPortrait(msg.Chat.ID, userID)
	if err != nil {
		log.Printf("participant portrait load failed before batch chat=%d user=%d err=%v", msg.Chat.ID, userID, err)
		m.prependBatch(userID, batch)
		return
	}

	task := participantPortraitTask{
		chatID:      msg.Chat.ID,
		userID:      userID,
		messages:    append([]string(nil), batch...),
		oldPortrait: strings.TrimSpace(oldPortrait),
	}
	select {
	case m.queue <- task:
	default:
		log.Printf("participant portrait queue full; batch requeued chat=%d user=%d", task.chatID, task.userID)
		m.prependBatch(task.userID, task.messages)
	}
}

func (m *participantPortraitManager) takeBatch(userID int64, text string) ([]string, bool) {
	if userID == 0 {
		return nil, false
	}
	val := strings.TrimSpace(text)
	if val == "" {
		return nil, false
	}
	m.bufferMu.Lock()
	defer m.bufferMu.Unlock()
	items := append(m.buffer[userID], clipText(val, 900))
	if len(items) < participantPortraitBatchSize {
		m.buffer[userID] = items
		return nil, false
	}
	batch := append([]string(nil), items[:participantPortraitBatchSize]...)
	remaining := append([]string(nil), items[participantPortraitBatchSize:]...)
	if len(remaining) == 0 {
		delete(m.buffer, userID)
	} else {
		m.buffer[userID] = remaining
	}
	return batch, true
}

func (m *participantPortraitManager) prependBatch(userID int64, messages []string) {
	if userID == 0 || len(messages) == 0 {
		return
	}
	clean := make([]string, 0, len(messages))
	for _, message := range messages {
		val := strings.TrimSpace(message)
		if val == "" {
			continue
		}
		clean = append(clean, clipText(val, 900))
	}
	if len(clean) == 0 {
		return
	}
	m.bufferMu.Lock()
	existing := m.buffer[userID]
	merged := make([]string, 0, len(clean)+len(existing))
	merged = append(merged, clean...)
	merged = append(merged, existing...)
	if len(merged) > 200 {
		merged = merged[:200]
	}
	m.buffer[userID] = merged
	m.bufferMu.Unlock()
}

func (m *participantPortraitManager) isDuplicateMessage(chatID, userID int64, messageID int) bool {
	if messageID <= 0 {
		return false
	}
	key := fmt.Sprintf("%d:%d:%d", chatID, userID, messageID)
	now := time.Now()

	m.seenMu.Lock()
	defer m.seenMu.Unlock()
	if ts, ok := m.seen[key]; ok {
		if now.Sub(ts) < 30*time.Minute {
			return true
		}
	}
	m.seen[key] = now

	if len(m.seen) > 4096 {
		cutoff := now.Add(-30 * time.Minute)
		for k, ts := range m.seen {
			if ts.Before(cutoff) {
				delete(m.seen, k)
			}
		}
	}
	return false
}

func (m *participantPortraitManager) worker() {
	defer m.wg.Done()
	for {
		select {
		case <-m.stop:
			return
		case task := <-m.queue:
			m.processTask(task)
		}
	}
}

func (m *participantPortraitManager) processTask(task participantPortraitTask) {
	portrait, err := generateParticipantPortrait(task.oldPortrait, task.messages)
	if err != nil {
		log.Printf("participant portrait generation failed chat=%d user=%d err=%v", task.chatID, task.userID, err)
		m.prependBatch(task.userID, task.messages)
		return
	}
	if err := m.store.SaveParticipantPortrait(task.chatID, task.userID, portrait); err != nil {
		log.Printf("participant portrait save failed chat=%d user=%d err=%v", task.chatID, task.userID, err)
		m.prependBatch(task.userID, task.messages)
		return
	}
	key := participantPortraitKey(task.userID)
	m.cacheMu.Lock()
	m.cache[key] = strings.TrimSpace(portrait)
	m.cacheMu.Unlock()
	if debugTriggerLogEnabled {
		log.Printf("participant portrait updated chat=%d user=%d batch=%d", task.chatID, task.userID, len(task.messages))
	}
}
