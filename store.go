package main

import (
	crand "crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"regexp"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

type Trigger struct {
	ID            int64
	UID           string `json:"uid,omitempty"`
	Priority      int    `json:"priority"`
	RegexBenchUS  int64  `json:"regex_bench_us"`
	Title         string
	Enabled       bool
	TriggerMode   string // all
	AdminMode     string // anybody|admins
	MatchText     string
	MatchType     string // full|partial|regex|starts|ends|idle|new_member
	CaseSensitive bool
	ActionType    string // send|delete|gpt_prompt|gpt_image|search_image|vk_music_audio
	ResponseText  []ResponseTextItem `json:"response_text"`
	Reply         bool
	Preview       bool
	DeleteSource  bool
	Chance        int
	CreatedAt     int64
	UpdatedAt     int64
	RegexError    string
	CapturingText string `json:"-"`
}

type ResponseTextItem struct {
	Text string `json:"text" bson:"text"`
}

type Store struct {
	db *sql.DB
	mg *mongoBackend

	cacheMu      sync.RWMutex
	cached       []Trigger
	cacheUntil   time.Time
	cacheTTL     time.Duration
	compiledRegs sync.Map // map[string]*regexp.Regexp
}

func decodeResponseText(raw string) []ResponseTextItem {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	if strings.HasPrefix(raw, "[") {
		var items []ResponseTextItem
		if err := json.Unmarshal([]byte(raw), &items); err == nil && len(items) > 0 {
			return items
		}
	}
	return []ResponseTextItem{{Text: raw}}
}

func encodeResponseText(items []ResponseTextItem) string {
	if len(items) == 0 {
		return "[]"
	}
	data, err := json.Marshal(items)
	if err != nil {
		return "[]"
	}
	return string(data)
}

func parseResponseTextRaw(raw json.RawMessage) []ResponseTextItem {
	if len(raw) == 0 {
		return nil
	}
	raw = json.RawMessage(strings.TrimSpace(string(raw)))
	if len(raw) == 0 {
		return nil
	}
	if raw[0] == '[' {
		var items []ResponseTextItem
		if err := json.Unmarshal(raw, &items); err == nil {
			return items
		}
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		s = strings.TrimSpace(s)
		if s == "" {
			return nil
		}
		return []ResponseTextItem{{Text: s}}
	}
	return nil
}

func OpenStore(path string) (*Store, error) {
	if isMongoURI(path) {
		return openMongoStore(path)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(`PRAGMA journal_mode=WAL;`); err != nil {
		_ = db.Close()
		return nil, err
	}
	if _, err := db.Exec(`PRAGMA synchronous=NORMAL;`); err != nil {
		_ = db.Close()
		return nil, err
	}
	if _, err := db.Exec(`PRAGMA busy_timeout=5000;`); err != nil {
		_ = db.Close()
		return nil, err
	}
	if _, err := db.Exec(`
CREATE TABLE IF NOT EXISTS triggers (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  uid TEXT NOT NULL DEFAULT '',
  priority INTEGER NOT NULL DEFAULT 0,
  regex_bench_us INTEGER NOT NULL DEFAULT 0,
  title TEXT NOT NULL,
  enabled INTEGER NOT NULL DEFAULT 1,
  trigger_mode TEXT NOT NULL DEFAULT 'all',
  admin_mode TEXT NOT NULL DEFAULT 'anybody',
  match_text TEXT NOT NULL,
  match_type TEXT NOT NULL DEFAULT 'full',
  case_sensitive INTEGER NOT NULL DEFAULT 0,
  action_type TEXT NOT NULL DEFAULT 'send',
  response_text TEXT NOT NULL,
  send_as_reply INTEGER NOT NULL DEFAULT 1,
  preview_first_link INTEGER NOT NULL DEFAULT 0,
  delete_source_message INTEGER NOT NULL DEFAULT 0,
  chance INTEGER NOT NULL DEFAULT 100,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  regex_error TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_triggers_enabled ON triggers(enabled);

CREATE TABLE IF NOT EXISTS chat_admin_cache (
  chat_id INTEGER NOT NULL,
  user_id INTEGER NOT NULL,
  is_admin INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  PRIMARY KEY (chat_id, user_id)
);
CREATE INDEX IF NOT EXISTS idx_chat_admin_cache_updated_at ON chat_admin_cache(updated_at);

CREATE TABLE IF NOT EXISTS chat_admin_sync (
  chat_id INTEGER PRIMARY KEY,
  updated_at INTEGER NOT NULL,
  admin_count INTEGER NOT NULL DEFAULT 0
);
`); err != nil {
		_ = db.Close()
		return nil, err
	}
	_, _ = db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_triggers_uid ON triggers(uid) WHERE uid <> ''`)
	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_triggers_priority ON triggers(priority DESC, id ASC)`)
	s := &Store{
		db:       db,
		cacheTTL: 2 * time.Second,
	}
	return s, nil
}

func (s *Store) Close() error {
	if s == nil {
		return nil
	}
	if s.mg != nil {
		return s.mg.close()
	}
	if s.db != nil {
		return s.db.Close()
	}
	return nil
}

func (s *Store) GetChatAdminCache(chatID, userID int64) (bool, int64, bool, error) {
	if s != nil && s.mg != nil {
		return s.mg.getChatAdminCache(chatID, userID)
	}
	var (
		isAdmin   int
		updatedAt int64
	)
	err := s.db.QueryRow(`SELECT is_admin, updated_at FROM chat_admin_cache WHERE chat_id=? AND user_id=?`, chatID, userID).Scan(&isAdmin, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return false, 0, false, nil
	}
	if err != nil {
		return false, 0, false, err
	}
	return isAdmin == 1, updatedAt, true, nil
}

func (s *Store) GetChatAdminSync(chatID int64) (int64, int, bool, error) {
	if s != nil && s.mg != nil {
		return s.mg.getChatAdminSync(chatID)
	}
	var (
		updatedAt int64
		adminCnt  int
	)
	err := s.db.QueryRow(`SELECT updated_at, admin_count FROM chat_admin_sync WHERE chat_id=?`, chatID).Scan(&updatedAt, &adminCnt)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, 0, false, nil
	}
	if err != nil {
		return 0, 0, false, err
	}
	return updatedAt, adminCnt, true, nil
}

func (s *Store) UpsertChatAdminSync(chatID int64, updatedAt int64, adminCount int) error {
	if s != nil && s.mg != nil {
		return s.mg.upsertChatAdminSync(chatID, updatedAt, adminCount)
	}
	if updatedAt <= 0 {
		updatedAt = time.Now().Unix()
	}
	_, err := s.db.Exec(`
INSERT INTO chat_admin_sync(chat_id,updated_at,admin_count)
VALUES(?,?,?)
ON CONFLICT(chat_id) DO UPDATE SET
  updated_at=excluded.updated_at,
  admin_count=excluded.admin_count
`, chatID, updatedAt, adminCount)
	return err
}

func (s *Store) UpsertChatAdminCache(chatID, userID int64, isAdmin bool, updatedAt int64) error {
	if s != nil && s.mg != nil {
		return s.mg.upsertChatAdminCache(chatID, userID, isAdmin, updatedAt)
	}
	if updatedAt <= 0 {
		updatedAt = time.Now().Unix()
	}
	_, err := s.db.Exec(`
INSERT INTO chat_admin_cache(chat_id,user_id,is_admin,updated_at)
VALUES(?,?,?,?)
ON CONFLICT(chat_id,user_id) DO UPDATE SET
  is_admin=excluded.is_admin,
  updated_at=excluded.updated_at
`, chatID, userID, b2i(isAdmin), updatedAt)
	return err
}

func (s *Store) ClearChatAdminCache(chatID int64) error {
	if s != nil && s.mg != nil {
		return s.mg.clearChatAdminCache(chatID)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM chat_admin_cache WHERE chat_id=?`, chatID); err != nil {
		_ = tx.Rollback()
		return err
	}
	if _, err := tx.Exec(`DELETE FROM chat_admin_sync WHERE chat_id=?`, chatID); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func normalizeMatchType(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "contains", "partial":
		return "partial"
	case "regex":
		return "regex"
	case "starts":
		return "starts"
	case "ends":
		return "ends"
	case "idle":
		return "idle"
	case "new_member", "new member", "newmember", "новый участник", "новый_участник":
		return "new_member"
	default:
		return "full"
	}
}

func sanitizeChance(v int) int {
	if v < 1 {
		return 1
	}
	if v > 100 {
		return 100
	}
	return v
}

func b2i(v bool) int {
	if v {
		return 1
	}
	return 0
}

func i2b(v int) bool { return v == 1 }

func normalizeTriggerMode(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "only_replies":
		return "only_replies"
	case "only_replies_to_any_bot":
		return "only_replies_to_any_bot"
	case "only_replies_to_combot":
		return "only_replies_to_combot"
	case "never_on_replies":
		return "never_on_replies"
	case "command_reply":
		return "command_reply"
	default:
		return "all"
	}
}

func normalizeAdminMode(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "admins":
		return "admins"
	case "not_admins":
		return "not_admins"
	default:
		return "anybody"
	}
}

func normalizeActionType(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "delete":
		return "delete"
	case "gpt_prompt":
		return "gpt_prompt"
	case "gpt_image":
		return "gpt_image"
	case "search_image":
		return "search_image"
	case "vk_music_audio":
		return "vk_music_audio"
	default:
		return "send"
	}
}

func (s *Store) ListTriggers() ([]Trigger, error) {
	if s != nil && s.mg != nil {
		return s.mg.listTriggers()
	}
	rows, err := s.db.Query(`SELECT id,uid,priority,regex_bench_us,title,enabled,trigger_mode,admin_mode,match_text,match_type,case_sensitive,action_type,response_text,send_as_reply,preview_first_link,delete_source_message,chance,created_at,updated_at,regex_error FROM triggers ORDER BY priority DESC, id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Trigger, 0, 64)
	for rows.Next() {
		var t Trigger
		var enabled, cs, reply, preview, deleteSource int
		var responseRaw string
		if err := rows.Scan(&t.ID, &t.UID, &t.Priority, &t.RegexBenchUS, &t.Title, &enabled, &t.TriggerMode, &t.AdminMode, &t.MatchText, &t.MatchType, &cs, &t.ActionType, &responseRaw, &reply, &preview, &deleteSource, &t.Chance, &t.CreatedAt, &t.UpdatedAt, &t.RegexError); err != nil {
			return nil, err
		}
		t.ResponseText = decodeResponseText(responseRaw)
		if trimmed := strings.TrimSpace(responseRaw); trimmed != "" && !strings.HasPrefix(trimmed, "[") {
			_, _ = s.db.Exec(`UPDATE triggers SET response_text=? WHERE id=?`, encodeResponseText(t.ResponseText), t.ID)
		}
		t.Enabled = i2b(enabled)
		t.TriggerMode = normalizeTriggerMode(t.TriggerMode)
		t.AdminMode = normalizeAdminMode(t.AdminMode)
		t.CaseSensitive = i2b(cs)
		t.ActionType = normalizeActionType(t.ActionType)
		t.Reply = i2b(reply)
		t.Preview = i2b(preview)
		t.DeleteSource = i2b(deleteSource)
		t.MatchType = normalizeMatchType(t.MatchType)
		t.Chance = sanitizeChance(t.Chance)
		out = append(out, t)
	}
	return out, nil
}

func (s *Store) GetTrigger(id int64) (*Trigger, error) {
	if s != nil && s.mg != nil {
		return s.mg.getTrigger(id)
	}
	var t Trigger
	var enabled, cs, reply, preview, deleteSource int
	var responseRaw string
	err := s.db.QueryRow(`SELECT id,uid,priority,regex_bench_us,title,enabled,trigger_mode,admin_mode,match_text,match_type,case_sensitive,action_type,response_text,send_as_reply,preview_first_link,delete_source_message,chance,created_at,updated_at,regex_error FROM triggers WHERE id=?`, id).
		Scan(&t.ID, &t.UID, &t.Priority, &t.RegexBenchUS, &t.Title, &enabled, &t.TriggerMode, &t.AdminMode, &t.MatchText, &t.MatchType, &cs, &t.ActionType, &responseRaw, &reply, &preview, &deleteSource, &t.Chance, &t.CreatedAt, &t.UpdatedAt, &t.RegexError)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	t.ResponseText = decodeResponseText(responseRaw)
	if trimmed := strings.TrimSpace(responseRaw); trimmed != "" && !strings.HasPrefix(trimmed, "[") {
		_, _ = s.db.Exec(`UPDATE triggers SET response_text=? WHERE id=?`, encodeResponseText(t.ResponseText), t.ID)
	}
	t.Enabled = i2b(enabled)
	t.TriggerMode = normalizeTriggerMode(t.TriggerMode)
	t.AdminMode = normalizeAdminMode(t.AdminMode)
	t.CaseSensitive = i2b(cs)
	t.ActionType = normalizeActionType(t.ActionType)
	t.Reply = i2b(reply)
	t.Preview = i2b(preview)
	t.DeleteSource = i2b(deleteSource)
	t.MatchType = normalizeMatchType(t.MatchType)
	t.Chance = sanitizeChance(t.Chance)
	return &t, nil
}

func (s *Store) SaveTrigger(t Trigger) error {
	now := time.Now().Unix()
	t.UID = strings.TrimSpace(t.UID)
	t.Title = strings.TrimSpace(t.Title)
	if t.Title == "" {
		t.Title = strings.TrimSpace(t.MatchText)
	}
	if t.Title == "" {
		t.Title = "Новый триггер"
	}
	t.MatchText = strings.TrimSpace(t.MatchText)
	t.TriggerMode = normalizeTriggerMode(t.TriggerMode)
	t.AdminMode = normalizeAdminMode(t.AdminMode)
	t.MatchType = normalizeMatchType(t.MatchType)
	t.ActionType = normalizeActionType(t.ActionType)
	t.Chance = sanitizeChance(t.Chance)
	t.RegexError = ""
	t.RegexBenchUS = 0
	if t.MatchType == "regex" {
		t.MatchText = stripLeadingCaseInsensitiveFlag(t.MatchText)
		pattern := t.MatchText
		if !t.CaseSensitive {
			pattern = "(?i)" + pattern
		}
		re, err := regexp.Compile(pattern)
		if err != nil {
			t.Enabled = false
			t.RegexError = "Ошибка regex: " + strings.TrimSpace(err.Error())
		} else {
			t.RegexBenchUS = benchmarkRegex100US(re)
		}
	}
	if t.ID > 0 && t.UID == "" {
		prevUID, err := s.getUIDByID(t.ID)
		if err != nil {
			return err
		}
		t.UID = prevUID
	}
	if t.UID == "" {
		uid, err := newUUID4()
		if err != nil {
			return err
		}
		t.UID = uid
	}
	if t.ID <= 0 {
		if t.Priority == 0 {
			p, err := s.nextInsertPriority()
			if err != nil {
				return err
			}
			t.Priority = p
		}
		if s != nil && s.mg != nil {
			if err := s.mg.insertTrigger(t, now); err == nil {
				s.invalidateCache()
			} else {
				return err
			}
			return nil
		}
	_, err := s.db.Exec(`INSERT INTO triggers(uid,priority,regex_bench_us,title,enabled,trigger_mode,admin_mode,match_text,match_type,case_sensitive,action_type,response_text,send_as_reply,preview_first_link,delete_source_message,chance,created_at,updated_at,regex_error) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		t.UID, t.Priority, t.RegexBenchUS, t.Title, b2i(t.Enabled), t.TriggerMode, t.AdminMode, t.MatchText, t.MatchType, b2i(t.CaseSensitive), t.ActionType, encodeResponseText(t.ResponseText), b2i(t.Reply), b2i(t.Preview), b2i(t.DeleteSource), t.Chance, now, now, t.RegexError)
		if err == nil {
			s.invalidateCache()
		}
		return err
	}
	if s != nil && s.mg != nil {
		if err := s.mg.updateTrigger(t, now); err != nil {
			return err
		}
		s.invalidateCache()
		return nil
	}
	_, err := s.db.Exec(`UPDATE triggers SET uid=?,regex_bench_us=?,title=?,enabled=?,trigger_mode=?,admin_mode=?,match_text=?,match_type=?,case_sensitive=?,action_type=?,response_text=?,send_as_reply=?,preview_first_link=?,delete_source_message=?,chance=?,updated_at=?,regex_error=? WHERE id=?`,
		t.UID, t.RegexBenchUS, t.Title, b2i(t.Enabled), t.TriggerMode, t.AdminMode, t.MatchText, t.MatchType, b2i(t.CaseSensitive), t.ActionType, encodeResponseText(t.ResponseText), b2i(t.Reply), b2i(t.Preview), b2i(t.DeleteSource), t.Chance, now, t.RegexError, t.ID)
	if err == nil {
		s.invalidateCache()
	}
	return err
}

func (s *Store) ToggleTrigger(id int64) (bool, error) {
	if s != nil && s.mg != nil {
		next, err := s.mg.toggleTrigger(id)
		if err != nil {
			return false, err
		}
		s.invalidateCache()
		return next, nil
	}
	if s == nil || s.db == nil {
		return false, fmt.Errorf("store not initialized")
	}
	var cur int
	if err := s.db.QueryRow(`SELECT enabled FROM triggers WHERE id=?`, id).Scan(&cur); err != nil {
		return false, err
	}
	next := 1
	if cur == 1 {
		next = 0
	}
	if _, err := s.db.Exec(`UPDATE triggers SET enabled=?, updated_at=? WHERE id=?`, next, time.Now().Unix(), id); err != nil {
		return false, err
	}
	s.invalidateCache()
	return next == 1, nil
}

func (s *Store) DeleteTrigger(id int64) error {
	if s != nil && s.mg != nil {
		if err := s.mg.deleteTrigger(id); err == nil {
			s.invalidateCache()
		} else {
			return err
		}
		return nil
	}
	_, err := s.db.Exec(`DELETE FROM triggers WHERE id=?`, id)
	if err == nil {
		s.invalidateCache()
	}
	return err
}

func (s *Store) invalidateCache() {
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	s.cached = nil
	s.cacheUntil = time.Time{}
}

func (s *Store) ListTriggersCached() ([]Trigger, error) {
	now := time.Now()
	s.cacheMu.RLock()
	if s.cached != nil && now.Before(s.cacheUntil) {
		out := make([]Trigger, len(s.cached))
		copy(out, s.cached)
		s.cacheMu.RUnlock()
		return out, nil
	}
	s.cacheMu.RUnlock()

	items, err := s.ListTriggers()
	if err != nil {
		return nil, err
	}

	s.cacheMu.Lock()
	s.cached = make([]Trigger, len(items))
	copy(s.cached, items)
	s.cacheUntil = now.Add(s.cacheTTL)
	out := make([]Trigger, len(items))
	copy(out, items)
	s.cacheMu.Unlock()
	return out, nil
}

func TriggerMatches(t Trigger, incoming string) bool {
	ok, _ := TriggerMatchCapture(t, incoming)
	return ok
}

func TriggerMatchCapture(t Trigger, incoming string) (bool, string) {
	needle := strings.TrimSpace(t.MatchText)
	hay := strings.TrimSpace(incoming)
	if normalizeMatchType(t.MatchType) == "new_member" {
		return false, ""
	}
	if hay == "" {
		return false, ""
	}
	// Empty match_text means "match any non-empty message".
	// This is useful for reply-driven GPT triggers.
	if needle == "" {
		return true, ""
	}
	switch normalizeMatchType(t.MatchType) {
	case "idle":
		// Idle mode is not a text matcher: it is evaluated separately in runtime loop.
		return false, ""
	case "partial":
		if !t.CaseSensitive {
			needle = strings.ToLower(needle)
			hay = strings.ToLower(hay)
		}
		return strings.Contains(hay, needle), ""
	case "starts":
		if !t.CaseSensitive {
			needle = strings.ToLower(needle)
			hay = strings.ToLower(hay)
		}
		return strings.HasPrefix(hay, needle), ""
	case "ends":
		if !t.CaseSensitive {
			needle = strings.ToLower(needle)
			hay = strings.ToLower(hay)
		}
		return strings.HasSuffix(hay, needle), ""
	case "regex":
		needle = stripLeadingCaseInsensitiveFlag(needle)
		if !t.CaseSensitive && !strings.HasPrefix(needle, "(?i)") {
			needle = "(?i)" + needle
		}
		re := sCompileRegex(needle)
		if re == nil {
			return false, ""
		}
		sub := re.FindStringSubmatch(hay)
		if len(sub) == 0 {
			return false, ""
		}
		capture := pickBestCapture(sub)
		return true, capture
	default:
		if !t.CaseSensitive {
			needle = strings.ToLower(needle)
			hay = strings.ToLower(hay)
		}
		return hay == needle, ""
	}
}

func stripLeadingCaseInsensitiveFlag(pattern string) string {
	p := strings.TrimSpace(pattern)
	for strings.HasPrefix(p, "(?i)") {
		p = strings.TrimPrefix(p, "(?i)")
		p = strings.TrimSpace(p)
	}
	return p
}

func pickBestCapture(sub []string) string {
	if len(sub) <= 1 {
		return strings.TrimSpace(sub[0])
	}
	best := ""
	for i := 1; i < len(sub); i++ {
		v := strings.TrimSpace(sub[i])
		if v == "" {
			continue
		}
		if len(v) > len(best) {
			best = v
		}
	}
	if best != "" {
		return best
	}
	return strings.TrimSpace(sub[0])
}

var regexGlobalCache sync.Map // map[string]*regexp.Regexp

func sCompileRegex(pattern string) *regexp.Regexp {
	if v, ok := regexGlobalCache.Load(pattern); ok {
		if re, ok2 := v.(*regexp.Regexp); ok2 {
			return re
		}
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil
	}
	regexGlobalCache.Store(pattern, re)
	return re
}

func benchmarkRegex100US(re *regexp.Regexp) int64 {
	if re == nil {
		return 0
	}
	const iterations = 1000
	sample := randomBenchmarkText(512)
	start := time.Now()
	for i := 0; i < iterations; i++ {
		_ = re.MatchString(sample)
	}
	return time.Since(start).Microseconds()
}

func randomBenchmarkText(n int) string {
	if n <= 0 {
		n = 64
	}
	const alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_- .,;:!?/\\|[](){}+=*&^%$#@~"
	b := make([]byte, n)
	for i := 0; i < n; i++ {
		b[i] = alphabet[rand.Intn(len(alphabet))]
	}
	return string(b)
}

func (s *Store) Match(text string) (*Trigger, bool, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, false, nil
	}
	items, err := s.ListTriggers()
	if err != nil {
		return nil, false, err
	}
	for _, t := range items {
		if !t.Enabled {
			continue
		}
		if !TriggerMatches(t, text) {
			continue
		}
		if t.Chance < 100 && rand.Intn(100) >= t.Chance {
			continue
		}
		tc := t
		return &tc, true, nil
	}
	return nil, false, nil
}

type exportTriggerRow struct {
	UID              string `json:"uid,omitempty"`
	Priority         int    `json:"priority"`
	RegexBenchUS     int64  `json:"regex_bench_us,omitempty"`
	Title            string `json:"title"`
	Enabled          bool   `json:"enabled"`
	TriggerMode      string `json:"trigger_mode"`
	AdminMode        string `json:"admin_mode"`
	MatchText        string `json:"match_text"`
	MatchType        string `json:"match_type"`
	CaseSensitive    bool   `json:"case_sensitive"`
	ActionType       string `json:"action_type"`
	ResponseText     []ResponseTextItem `json:"response_text"`
	SendAsReply      bool   `json:"send_as_reply"`
	PreviewFirstLink bool   `json:"preview_first_link"`
	DeleteSourceMsg  bool   `json:"delete_source_message"`
	Chance           int    `json:"chance"`
}

type importTriggerRow struct {
	UID              string `json:"uid"`
	Priority         *int   `json:"priority"`
	RegexBenchUS     *int64 `json:"regex_bench_us"`
	Title            string `json:"title"`
	Enabled          *bool  `json:"enabled"`
	TriggerMode      string `json:"trigger_mode"`
	AdminMode        string `json:"admin_mode"`
	MatchText        string `json:"match_text"`
	MatchType        string `json:"match_type"`
	CaseSensitive    *bool  `json:"case_sensitive"`
	ActionType       string `json:"action_type"`
	ResponseText     json.RawMessage `json:"response_text"`
	SendAsReply      *bool  `json:"send_as_reply"`
	PreviewFirstLink *bool  `json:"preview_first_link"`
	DeleteSourceMsg  *bool  `json:"delete_source_message"`
	Chance           *int   `json:"chance"`
}

func (s *Store) ExportJSON() ([]byte, error) {
	items, err := s.ListTriggers()
	if err != nil {
		return nil, err
	}
	out := make([]exportTriggerRow, 0, len(items))
	for _, t := range items {
		out = append(out, exportTriggerRow{
			UID:              t.UID,
			Priority:         t.Priority,
			RegexBenchUS:     t.RegexBenchUS,
			Title:            t.Title,
			Enabled:          t.Enabled,
			TriggerMode:      t.TriggerMode,
			AdminMode:        t.AdminMode,
			MatchText:        t.MatchText,
			MatchType:        t.MatchType,
			CaseSensitive:    t.CaseSensitive,
			ActionType:       t.ActionType,
			ResponseText:     t.ResponseText,
			SendAsReply:      t.Reply,
			PreviewFirstLink: t.Preview,
			DeleteSourceMsg:  t.DeleteSource,
			Chance:           t.Chance,
		})
	}
	return json.MarshalIndent(out, "", "  ")
}

func (s *Store) ImportJSON(raw []byte) (int, error) {
	raw = []byte(strings.TrimSpace(string(raw)))
	if len(raw) == 0 {
		return 0, nil
	}
	var rawItems []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &rawItems); err != nil {
		return 0, err
	}
	if len(rawItems) == 0 {
		return 0, nil
	}
	supportedRows := 0
	for _, row := range rawItems {
		if hasNewImportKeys(row) {
			supportedRows++
		}
	}
	if supportedRows == 0 {
		return 0, errors.New("unsupported import format: expected column-style fields")
	}

	var items []importTriggerRow
	if err := json.Unmarshal(raw, &items); err != nil {
		return 0, err
	}
	added := 0
	for i, it := range items {
		if i >= len(rawItems) || !hasNewImportKeys(rawItems[i]) {
			continue
		}
		title := strings.TrimSpace(it.Title)
		if title == "" {
			title = "Импортированный триггер"
		}

		enabled := true
		if it.Enabled != nil {
			enabled = *it.Enabled
		}
		triggerMode := "all"
		if strings.TrimSpace(it.TriggerMode) != "" {
			triggerMode = strings.TrimSpace(it.TriggerMode)
		}
		adminMode := "anybody"
		if strings.TrimSpace(it.AdminMode) != "" {
			adminMode = strings.TrimSpace(it.AdminMode)
		}
		matchText := strings.TrimSpace(it.MatchText)
		matchType := "full"
		if strings.TrimSpace(it.MatchType) != "" {
			matchType = strings.TrimSpace(it.MatchType)
		}
		caseSensitive := false
		if it.CaseSensitive != nil {
			caseSensitive = *it.CaseSensitive
		}
		actionType := "send"
		if strings.TrimSpace(it.ActionType) != "" {
			actionType = strings.TrimSpace(it.ActionType)
		}
		responseItems := parseResponseTextRaw(it.ResponseText)
		if len(responseItems) == 0 && i < len(rawItems) {
			if raw, ok := rawItems[i]["response_text"]; ok {
				responseItems = parseResponseTextRaw(raw)
			}
		}
		reply := true
		if it.SendAsReply != nil {
			reply = *it.SendAsReply
		}
		preview := false
		if it.PreviewFirstLink != nil {
			preview = *it.PreviewFirstLink
		}
		deleteSource := false
		if it.DeleteSourceMsg != nil {
			deleteSource = *it.DeleteSourceMsg
		}
		chance := 100
		if it.Chance != nil {
			chance = *it.Chance
		}

		tr := Trigger{
			UID:           strings.TrimSpace(it.UID),
			Title:         title,
			Enabled:       enabled,
			TriggerMode:   triggerMode,
			AdminMode:     adminMode,
			MatchText:     matchText,
			MatchType:     matchType,
			CaseSensitive: caseSensitive,
			ActionType:    actionType,
			ResponseText:  responseItems,
			Reply:         reply,
			Preview:       preview,
			DeleteSource:  deleteSource,
			Chance:        chance,
		}
		if it.Priority != nil {
			tr.Priority = *it.Priority
		}
		if it.RegexBenchUS != nil {
			tr.RegexBenchUS = *it.RegexBenchUS
		}
		if tr.UID != "" {
			id, err := s.getIDByUID(tr.UID)
			if err != nil {
				return added, err
			}
			tr.ID = id
		}
		err := s.SaveTrigger(tr)
		if err != nil {
			return added, err
		}
		added++
	}
	if added > 0 {
		s.invalidateCache()
	}
	return added, nil
}

func hasNewImportKeys(m map[string]json.RawMessage) bool {
	if len(m) == 0 {
		return false
	}
	keys := []string{
		"uid",
		"priority",
		"regex_bench_us",
		"title",
		"enabled",
		"trigger_mode",
		"admin_mode",
		"match_text",
		"match_type",
		"case_sensitive",
		"action_type",
		"response_text",
		"send_as_reply",
		"preview_first_link",
		"delete_source_message",
		"chance",
	}
	for _, k := range keys {
		if _, ok := m[k]; ok {
			return true
		}
	}
	return false
}

func (s *Store) nextInsertPriority() (int, error) {
	if s != nil && s.mg != nil {
		return s.mg.nextInsertPriority()
	}
	var minPri sql.NullInt64
	if err := s.db.QueryRow(`SELECT MIN(priority) FROM triggers`).Scan(&minPri); err != nil {
		return 0, err
	}
	if !minPri.Valid {
		return 1, nil
	}
	return int(minPri.Int64) - 1, nil
}

func (s *Store) ReorderTriggersByIDs(orderedTopToBottom []int64) error {
	if len(orderedTopToBottom) == 0 {
		return nil
	}
	existing, err := s.ListTriggers()
	if err != nil {
		return err
	}
	if len(existing) == 0 {
		return nil
	}
	exists := make(map[int64]struct{}, len(existing))
	for _, t := range existing {
		exists[t.ID] = struct{}{}
	}
	finalOrder := make([]int64, 0, len(existing))
	seen := make(map[int64]struct{}, len(existing))
	for _, id := range orderedTopToBottom {
		if _, ok := exists[id]; !ok {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		finalOrder = append(finalOrder, id)
	}
	for _, t := range existing {
		if _, ok := seen[t.ID]; ok {
			continue
		}
		finalOrder = append(finalOrder, t.ID)
	}
	if s != nil && s.mg != nil {
		if err := s.mg.reorderTriggersByIDs(finalOrder); err != nil {
			return err
		}
		s.invalidateCache()
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	for i, id := range finalOrder {
		priority := len(finalOrder) - i
		if _, err := tx.Exec(`UPDATE triggers SET priority=?, updated_at=? WHERE id=?`, priority, time.Now().Unix(), id); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.invalidateCache()
	return nil
}

func (s *Store) getUIDByID(id int64) (string, error) {
	if s != nil && s.mg != nil {
		return s.mg.getUIDByID(id)
	}
	if id <= 0 {
		return "", nil
	}
	var uid string
	err := s.db.QueryRow(`SELECT uid FROM triggers WHERE id=?`, id).Scan(&uid)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return strings.TrimSpace(uid), err
}

func (s *Store) getIDByUID(uid string) (int64, error) {
	if s != nil && s.mg != nil {
		return s.mg.getIDByUID(uid)
	}
	uid = strings.TrimSpace(uid)
	if uid == "" {
		return 0, nil
	}
	var id int64
	err := s.db.QueryRow(`SELECT id FROM triggers WHERE uid=? LIMIT 1`, uid).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return id, err
}

func newUUID4() (string, error) {
	var b [16]byte
	if _, err := crand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	hexed := hex.EncodeToString(b[:])
	return fmt.Sprintf("%s-%s-%s-%s-%s", hexed[0:8], hexed[8:12], hexed[12:16], hexed[16:20], hexed[20:32]), nil
}
