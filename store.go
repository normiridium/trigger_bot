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
	MatchType     string // full|partial|regex|starts|ends|idle
	CaseSensitive bool
	ActionType    string // send|delete|gpt_prompt|gpt_image|search_image|vk_music_audio
	ResponseText  string
	Reply         bool
	Preview       bool
	Chance        int
	CreatedAt     int64
	UpdatedAt     int64
	RegexError    string
	CapturingText string `json:"-"`
}

type Store struct {
	db *sql.DB

	cacheMu      sync.RWMutex
	cached       []Trigger
	cacheUntil   time.Time
	cacheTTL     time.Duration
	compiledRegs sync.Map // map[string]*regexp.Regexp
}

func OpenStore(path string) (*Store, error) {
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
  chance INTEGER NOT NULL DEFAULT 100,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  regex_error TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_triggers_enabled ON triggers(enabled);
CREATE INDEX IF NOT EXISTS idx_triggers_priority ON triggers(priority DESC, id ASC);
`); err != nil {
		_ = db.Close()
		return nil, err
	}
	// migration for older schema
	_ = ensureColumn(db, "triggers", "trigger_mode", "TEXT NOT NULL DEFAULT 'all'")
	_ = ensureColumn(db, "triggers", "admin_mode", "TEXT NOT NULL DEFAULT 'anybody'")
	_ = ensureColumn(db, "triggers", "action_type", "TEXT NOT NULL DEFAULT 'send'")
	_ = ensureColumn(db, "triggers", "uid", "TEXT NOT NULL DEFAULT ''")
	if err := ensureColumn(db, "triggers", "priority", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := ensureColumn(db, "triggers", "regex_bench_us", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		_ = db.Close()
		return nil, err
	}
	_ = ensureColumn(db, "triggers", "regex_error", "TEXT NOT NULL DEFAULT ''")
	_, _ = db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_triggers_uid ON triggers(uid) WHERE uid <> ''`)
	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_triggers_priority ON triggers(priority DESC, id ASC)`)
	s := &Store{
		db:       db,
		cacheTTL: 2 * time.Second,
	}
	if err := s.backfillMissingPriorities(); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := s.backfillMissingUIDs(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func ensureColumn(db *sql.DB, table, col, ddl string) error {
	rows, err := db.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return err
	}
	defer rows.Close()
	var (
		cid       int
		name      string
		ctype     string
		notnull   int
		dfltValue sql.NullString
		pk        int
	)
	for rows.Next() {
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dfltValue, &pk); err != nil {
			return err
		}
		if name == col {
			return nil
		}
	}
	_, err = db.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, col, ddl))
	return err
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
	rows, err := s.db.Query(`SELECT id,uid,priority,regex_bench_us,title,enabled,trigger_mode,admin_mode,match_text,match_type,case_sensitive,action_type,response_text,send_as_reply,preview_first_link,chance,created_at,updated_at,regex_error FROM triggers ORDER BY priority DESC, id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Trigger, 0, 64)
	for rows.Next() {
		var t Trigger
		var enabled, cs, reply, preview int
		if err := rows.Scan(&t.ID, &t.UID, &t.Priority, &t.RegexBenchUS, &t.Title, &enabled, &t.TriggerMode, &t.AdminMode, &t.MatchText, &t.MatchType, &cs, &t.ActionType, &t.ResponseText, &reply, &preview, &t.Chance, &t.CreatedAt, &t.UpdatedAt, &t.RegexError); err != nil {
			return nil, err
		}
		t.Enabled = i2b(enabled)
		t.TriggerMode = normalizeTriggerMode(t.TriggerMode)
		t.AdminMode = normalizeAdminMode(t.AdminMode)
		t.CaseSensitive = i2b(cs)
		t.ActionType = normalizeActionType(t.ActionType)
		t.Reply = i2b(reply)
		t.Preview = i2b(preview)
		t.MatchType = normalizeMatchType(t.MatchType)
		t.Chance = sanitizeChance(t.Chance)
		out = append(out, t)
	}
	return out, nil
}

func (s *Store) GetTrigger(id int64) (*Trigger, error) {
	var t Trigger
	var enabled, cs, reply, preview int
	err := s.db.QueryRow(`SELECT id,uid,priority,regex_bench_us,title,enabled,trigger_mode,admin_mode,match_text,match_type,case_sensitive,action_type,response_text,send_as_reply,preview_first_link,chance,created_at,updated_at,regex_error FROM triggers WHERE id=?`, id).
		Scan(&t.ID, &t.UID, &t.Priority, &t.RegexBenchUS, &t.Title, &enabled, &t.TriggerMode, &t.AdminMode, &t.MatchText, &t.MatchType, &cs, &t.ActionType, &t.ResponseText, &reply, &preview, &t.Chance, &t.CreatedAt, &t.UpdatedAt, &t.RegexError)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	t.Enabled = i2b(enabled)
	t.TriggerMode = normalizeTriggerMode(t.TriggerMode)
	t.AdminMode = normalizeAdminMode(t.AdminMode)
	t.CaseSensitive = i2b(cs)
	t.ActionType = normalizeActionType(t.ActionType)
	t.Reply = i2b(reply)
	t.Preview = i2b(preview)
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
		pattern := t.MatchText
		if !t.CaseSensitive && !strings.HasPrefix(pattern, "(?i)") {
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
		_, err := s.db.Exec(`INSERT INTO triggers(uid,priority,regex_bench_us,title,enabled,trigger_mode,admin_mode,match_text,match_type,case_sensitive,action_type,response_text,send_as_reply,preview_first_link,chance,created_at,updated_at,regex_error) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			t.UID, t.Priority, t.RegexBenchUS, t.Title, b2i(t.Enabled), t.TriggerMode, t.AdminMode, t.MatchText, t.MatchType, b2i(t.CaseSensitive), t.ActionType, t.ResponseText, b2i(t.Reply), b2i(t.Preview), t.Chance, now, now, t.RegexError)
		if err == nil {
			s.invalidateCache()
		}
		return err
	}
	_, err := s.db.Exec(`UPDATE triggers SET uid=?,regex_bench_us=?,title=?,enabled=?,trigger_mode=?,admin_mode=?,match_text=?,match_type=?,case_sensitive=?,action_type=?,response_text=?,send_as_reply=?,preview_first_link=?,chance=?,updated_at=?,regex_error=? WHERE id=?`,
		t.UID, t.RegexBenchUS, t.Title, b2i(t.Enabled), t.TriggerMode, t.AdminMode, t.MatchText, t.MatchType, b2i(t.CaseSensitive), t.ActionType, t.ResponseText, b2i(t.Reply), b2i(t.Preview), t.Chance, now, t.RegexError, t.ID)
	if err == nil {
		s.invalidateCache()
	}
	return err
}

func (s *Store) ToggleTrigger(id int64) error {
	_, err := s.db.Exec(`UPDATE triggers SET enabled=CASE WHEN enabled=1 THEN 0 ELSE 1 END, updated_at=? WHERE id=?`, time.Now().Unix(), id)
	if err == nil {
		s.invalidateCache()
	}
	return err
}

func (s *Store) DeleteTrigger(id int64) error {
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
	const iterations = 100
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
	ResponseText     string `json:"response_text"`
	SendAsReply      bool   `json:"send_as_reply"`
	PreviewFirstLink bool   `json:"preview_first_link"`
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
	ResponseText     string `json:"response_text"`
	SendAsReply      *bool  `json:"send_as_reply"`
	PreviewFirstLink *bool  `json:"preview_first_link"`
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
		responseText := strings.TrimSpace(it.ResponseText)
		reply := true
		if it.SendAsReply != nil {
			reply = *it.SendAsReply
		}
		preview := false
		if it.PreviewFirstLink != nil {
			preview = *it.PreviewFirstLink
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
			ResponseText:  responseText,
			Reply:         reply,
			Preview:       preview,
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
	var minPri sql.NullInt64
	if err := s.db.QueryRow(`SELECT MIN(priority) FROM triggers`).Scan(&minPri); err != nil {
		return 0, err
	}
	if !minPri.Valid {
		return 1, nil
	}
	return int(minPri.Int64) - 1, nil
}

func (s *Store) backfillMissingPriorities() error {
	rows, err := s.db.Query(`SELECT id FROM triggers WHERE priority=0 ORDER BY id ASC`)
	if err != nil {
		return err
	}
	defer rows.Close()
	ids := make([]int64, 0, 64)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return err
		}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	for i, id := range ids {
		pri := len(ids) - i
		if _, err := tx.Exec(`UPDATE triggers SET priority=? WHERE id=?`, pri, id); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	return tx.Commit()
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

func (s *Store) backfillMissingUIDs() error {
	if s == nil || s.db == nil {
		return nil
	}
	rows, err := s.db.Query(`SELECT id FROM triggers WHERE trim(uid)=''`)
	if err != nil {
		return err
	}
	defer rows.Close()
	ids := make([]int64, 0, 16)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return err
		}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return nil
	}
	for _, id := range ids {
		updated := false
		for i := 0; i < 5; i++ {
			uid, err := newUUID4()
			if err != nil {
				return err
			}
			res, err := s.db.Exec(`UPDATE triggers SET uid=?, updated_at=? WHERE id=? AND trim(uid)=''`, uid, time.Now().Unix(), id)
			if err != nil {
				if strings.Contains(strings.ToLower(err.Error()), "unique") {
					continue
				}
				return err
			}
			aff, _ := res.RowsAffected()
			if aff > 0 {
				updated = true
				break
			}
		}
		if !updated {
			return fmt.Errorf("failed to assign uid for trigger id=%d", id)
		}
	}
	return nil
}
