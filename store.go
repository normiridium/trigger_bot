package main

import (
	"database/sql"
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
	Title         string
	Enabled       bool
	TriggerMode   string // all
	AdminMode     string // anybody|admins
	MatchText     string
	MatchType     string // full|partial|regex|starts|ends
	CaseSensitive bool
	ActionType    string // send|delete|gpt_prompt|gpt_image|search_image
	ResponseText  string
	Reply         bool
	Preview       bool
	Chance        int
	CreatedAt     int64
	UpdatedAt     int64
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
  updated_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_triggers_enabled ON triggers(enabled);
`); err != nil {
		_ = db.Close()
		return nil, err
	}
	// migration for older schema
	_ = ensureColumn(db, "triggers", "trigger_mode", "TEXT NOT NULL DEFAULT 'all'")
	_ = ensureColumn(db, "triggers", "admin_mode", "TEXT NOT NULL DEFAULT 'anybody'")
	_ = ensureColumn(db, "triggers", "action_type", "TEXT NOT NULL DEFAULT 'send'")
	return &Store{
		db:       db,
		cacheTTL: 2 * time.Second,
	}, nil
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
	default:
		return "send"
	}
}

func (s *Store) ListTriggers() ([]Trigger, error) {
	rows, err := s.db.Query(`SELECT id,title,enabled,trigger_mode,admin_mode,match_text,match_type,case_sensitive,action_type,response_text,send_as_reply,preview_first_link,chance,created_at,updated_at FROM triggers ORDER BY id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Trigger, 0, 64)
	for rows.Next() {
		var t Trigger
		var enabled, cs, reply, preview int
		if err := rows.Scan(&t.ID, &t.Title, &enabled, &t.TriggerMode, &t.AdminMode, &t.MatchText, &t.MatchType, &cs, &t.ActionType, &t.ResponseText, &reply, &preview, &t.Chance, &t.CreatedAt, &t.UpdatedAt); err != nil {
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
	err := s.db.QueryRow(`SELECT id,title,enabled,trigger_mode,admin_mode,match_text,match_type,case_sensitive,action_type,response_text,send_as_reply,preview_first_link,chance,created_at,updated_at FROM triggers WHERE id=?`, id).
		Scan(&t.ID, &t.Title, &enabled, &t.TriggerMode, &t.AdminMode, &t.MatchText, &t.MatchType, &cs, &t.ActionType, &t.ResponseText, &reply, &preview, &t.Chance, &t.CreatedAt, &t.UpdatedAt)
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
	t.Title = strings.TrimSpace(t.Title)
	if t.Title == "" {
		t.Title = strings.TrimSpace(t.MatchText)
	}
	if t.Title == "" {
		t.Title = "Новый триггер"
	}
	t.MatchText = strings.TrimSpace(t.MatchText)
	t.ResponseText = strings.TrimSpace(t.ResponseText)
	t.TriggerMode = normalizeTriggerMode(t.TriggerMode)
	t.AdminMode = normalizeAdminMode(t.AdminMode)
	t.MatchType = normalizeMatchType(t.MatchType)
	t.ActionType = normalizeActionType(t.ActionType)
	t.Chance = sanitizeChance(t.Chance)
	if t.ID <= 0 {
		_, err := s.db.Exec(`INSERT INTO triggers(title,enabled,trigger_mode,admin_mode,match_text,match_type,case_sensitive,action_type,response_text,send_as_reply,preview_first_link,chance,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			t.Title, b2i(t.Enabled), t.TriggerMode, t.AdminMode, t.MatchText, t.MatchType, b2i(t.CaseSensitive), t.ActionType, t.ResponseText, b2i(t.Reply), b2i(t.Preview), t.Chance, now, now)
		if err == nil {
			s.invalidateCache()
		}
		return err
	}
	_, err := s.db.Exec(`UPDATE triggers SET title=?,enabled=?,trigger_mode=?,admin_mode=?,match_text=?,match_type=?,case_sensitive=?,action_type=?,response_text=?,send_as_reply=?,preview_first_link=?,chance=?,updated_at=? WHERE id=?`,
		t.Title, b2i(t.Enabled), t.TriggerMode, t.AdminMode, t.MatchText, t.MatchType, b2i(t.CaseSensitive), t.ActionType, t.ResponseText, b2i(t.Reply), b2i(t.Preview), t.Chance, now, t.ID)
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
	needle := strings.TrimSpace(t.MatchText)
	hay := strings.TrimSpace(incoming)
	if hay == "" {
		return false
	}
	// Empty match_text means "match any non-empty message".
	// This is useful for reply-driven GPT triggers.
	if needle == "" {
		return true
	}
	switch normalizeMatchType(t.MatchType) {
	case "partial":
		if !t.CaseSensitive {
			needle = strings.ToLower(needle)
			hay = strings.ToLower(hay)
		}
		return strings.Contains(hay, needle)
	case "starts":
		if !t.CaseSensitive {
			needle = strings.ToLower(needle)
			hay = strings.ToLower(hay)
		}
		return strings.HasPrefix(hay, needle)
	case "ends":
		if !t.CaseSensitive {
			needle = strings.ToLower(needle)
			hay = strings.ToLower(hay)
		}
		return strings.HasSuffix(hay, needle)
	case "regex":
		if !t.CaseSensitive && !strings.HasPrefix(needle, "(?i)") {
			needle = "(?i)" + needle
		}
		re := sCompileRegex(needle)
		if re == nil {
			return false
		}
		return re.MatchString(hay)
	default:
		if !t.CaseSensitive {
			needle = strings.ToLower(needle)
			hay = strings.ToLower(hay)
		}
		return hay == needle
	}
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

type exportItem struct {
	T   string       `json:"t"`
	Cos []exportCond `json:"cos"`
	Acs []exportAct  `json:"acs"`
}

type exportCond struct {
	Mty string `json:"mty"`
	Tt  string `json:"tt"`
	Ty  string `json:"ty"`
}

type exportAct struct {
	Ty string `json:"ty"`
	T  string `json:"t"`
	Sr string `json:"sr"`
}

func exportTyToMatchType(ty string) string {
	switch strings.TrimSpace(ty) {
	case "2":
		return "regex"
	case "1":
		return "partial"
	default:
		return "full"
	}
}

func matchTypeToExportTy(mt string) string {
	switch normalizeMatchType(mt) {
	case "regex":
		return "2"
	case "partial", "starts", "ends":
		return "1"
	default:
		return "0"
	}
}

func (s *Store) ExportJSON() ([]byte, error) {
	items, err := s.ListTriggers()
	if err != nil {
		return nil, err
	}
	out := make([]exportItem, 0, len(items))
	for _, t := range items {
		out = append(out, exportItem{
			T: t.Title,
			Cos: []exportCond{{
				Mty: "0",
				Tt:  t.MatchText,
				Ty:  matchTypeToExportTy(t.MatchType),
			}},
			Acs: []exportAct{{
				Ty: "se",
				T:  t.ResponseText,
				Sr: map[bool]string{true: "1", false: "0"}[t.Reply],
			}},
		})
	}
	return json.MarshalIndent(out, "", "  ")
}

func (s *Store) ImportJSON(raw []byte) (int, error) {
	raw = []byte(strings.TrimSpace(string(raw)))
	if len(raw) == 0 {
		return 0, nil
	}
	var items []exportItem
	if err := json.Unmarshal(raw, &items); err != nil {
		return 0, err
	}
	added := 0
	for _, it := range items {
		if len(it.Cos) == 0 || len(it.Acs) == 0 {
			continue
		}
		matchText := strings.TrimSpace(it.Cos[0].Tt)
		responseText := strings.TrimSpace(it.Acs[0].T)
		if matchText == "" || responseText == "" {
			continue
		}
		err := s.SaveTrigger(Trigger{
			Title:        strings.TrimSpace(it.T),
			Enabled:      true,
			TriggerMode:  "all",
			AdminMode:    "anybody",
			MatchText:    matchText,
			MatchType:    exportTyToMatchType(it.Cos[0].Ty),
			ActionType:   "send",
			ResponseText: responseText,
			Reply:        strings.TrimSpace(it.Acs[0].Sr) == "1",
			Preview:      false,
			Chance:       100,
		})
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
