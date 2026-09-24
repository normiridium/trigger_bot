package app

import (
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// mediaGroupImageStore keeps an in-flight Telegram album together until its
// final update arrives, so one GPT request receives the complete visual context.
type mediaGroupImageStore struct {
	mu      sync.Mutex
	groups  map[string]*mediaGroupImageEntry
	quiet   time.Duration
	maxWait time.Duration
	ttl     time.Duration
}

type mediaGroupImageEntry struct {
	messages []*tgbotapi.Message
	lastSeen time.Time
	expires  time.Time
}

func newMediaGroupImageStore(quiet, maxWait, ttl time.Duration) *mediaGroupImageStore {
	if quiet <= 0 {
		quiet = 900 * time.Millisecond
	}
	if maxWait < quiet {
		maxWait = quiet
	}
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	return &mediaGroupImageStore{
		groups:  make(map[string]*mediaGroupImageEntry),
		quiet:   quiet,
		maxWait: maxWait,
		ttl:     ttl,
	}
}

func newDefaultMediaGroupImageStore() *mediaGroupImageStore {
	quietMS := envInt("GPT_MEDIA_GROUP_QUIET_MS", 900)
	if quietMS < 100 {
		quietMS = 100
	}
	if quietMS > 3000 {
		quietMS = 3000
	}
	maxWaitMS := envInt("GPT_MEDIA_GROUP_MAX_WAIT_MS", 2500)
	if maxWaitMS < quietMS {
		maxWaitMS = quietMS
	}
	if maxWaitMS > 5000 {
		maxWaitMS = 5000
	}
	return newMediaGroupImageStore(
		time.Duration(quietMS)*time.Millisecond,
		time.Duration(maxWaitMS)*time.Millisecond,
		5*time.Minute,
	)
}

func mediaGroupImageKey(msg *tgbotapi.Message) string {
	if msg == nil || msg.Chat == nil || strings.TrimSpace(msg.MediaGroupID) == "" {
		return ""
	}
	return strings.TrimSpace(msg.MediaGroupID) + ":" + strconv.FormatInt(msg.Chat.ID, 10)
}

func (s *mediaGroupImageStore) Observe(msg *tgbotapi.Message) {
	if s == nil || !hasMessageBitmapImage(msg) {
		return
	}
	key := mediaGroupImageKey(msg)
	if key == "" {
		return
	}
	now := time.Now()
	copyMsg := *msg
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupLocked(now)
	entry := s.groups[key]
	if entry == nil {
		entry = &mediaGroupImageEntry{}
		s.groups[key] = entry
	}
	for _, existing := range entry.messages {
		if existing != nil && existing.MessageID == copyMsg.MessageID {
			entry.lastSeen = now
			entry.expires = now.Add(s.ttl)
			return
		}
	}
	entry.messages = append(entry.messages, &copyMsg)
	entry.lastSeen = now
	entry.expires = now.Add(s.ttl)
}

func (s *mediaGroupImageStore) MessagesFor(msg *tgbotapi.Message) []*tgbotapi.Message {
	if msg == nil {
		return nil
	}
	source := msg
	if !hasMessageBitmapImage(source) && msg.ReplyToMessage != nil {
		source = msg.ReplyToMessage
	}
	if !hasMessageBitmapImage(source) {
		return nil
	}
	key := mediaGroupImageKey(source)
	if s == nil || key == "" {
		return []*tgbotapi.Message{source}
	}

	deadline := time.Now().Add(s.maxWait)
	for {
		s.mu.Lock()
		s.cleanupLocked(time.Now())
		entry := s.groups[key]
		if entry == nil {
			s.mu.Unlock()
			return []*tgbotapi.Message{source}
		}
		quietFor := time.Since(entry.lastSeen)
		if quietFor >= s.quiet || !time.Now().Before(deadline) {
			messages := append([]*tgbotapi.Message(nil), entry.messages...)
			s.mu.Unlock()
			sort.Slice(messages, func(i, j int) bool { return messages[i].MessageID < messages[j].MessageID })
			return messages
		}
		wait := s.quiet - quietFor
		remaining := time.Until(deadline)
		if wait > remaining {
			wait = remaining
		}
		s.mu.Unlock()
		if wait <= 0 {
			continue
		}
		time.Sleep(wait)
	}
}

func (s *mediaGroupImageStore) cleanupLocked(now time.Time) {
	for key, entry := range s.groups {
		if entry == nil || !entry.expires.After(now) {
			delete(s.groups, key)
		}
	}
}
