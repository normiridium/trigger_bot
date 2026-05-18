package chataccess

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type AllowList struct {
	enabled bool
	ids     map[int64]struct{}
}

func ParseAllowedChatIDs(raw string) (AllowList, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return AllowList{enabled: false}, nil
	}
	parts := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || r == ' ' || r == '\n' || r == '\t'
	})
	ids := make(map[int64]struct{}, len(parts))
	for _, part := range parts {
		v := strings.TrimSpace(part)
		if v == "" {
			continue
		}
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return AllowList{}, fmt.Errorf("invalid ALLOWED_CHAT_IDS value %q: %w", v, err)
		}
		ids[id] = struct{}{}
	}
	if len(ids) == 0 {
		return AllowList{enabled: false}, nil
	}
	return AllowList{enabled: true, ids: ids}, nil
}

func (a AllowList) Allows(chatID int64) bool {
	if !a.enabled {
		return true
	}
	_, ok := a.ids[chatID]
	return ok
}

func (a AllowList) Enabled() bool {
	return a.enabled
}

func (a AllowList) IDs() []int64 {
	if len(a.ids) == 0 {
		return nil
	}
	out := make([]int64, 0, len(a.ids))
	for id := range a.ids {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

type DisallowedChatNotifier struct {
	mu   sync.Mutex
	last map[int64]time.Time
	ttl  time.Duration
}

func NewDisallowedChatNotifier(ttl time.Duration) *DisallowedChatNotifier {
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}
	return &DisallowedChatNotifier{
		last: make(map[int64]time.Time),
		ttl:  ttl,
	}
}

func (n *DisallowedChatNotifier) ShouldNotify(chatID int64, now time.Time) bool {
	if n == nil || chatID == 0 {
		return false
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if prev, ok := n.last[chatID]; ok && now.Sub(prev) < n.ttl {
		return false
	}
	return true
}

func (n *DisallowedChatNotifier) MarkNotified(chatID int64, now time.Time) {
	if n == nil || chatID == 0 {
		return
	}
	n.mu.Lock()
	n.last[chatID] = now
	n.mu.Unlock()
}

func IsActiveChatMemberStatus(status string) bool {
	switch strings.TrimSpace(status) {
	case "member", "administrator", "creator":
		return true
	default:
		return false
	}
}
