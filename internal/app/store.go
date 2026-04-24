package app

import (
	crand "crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"regexp"
	"strings"
	"sync"
	"time"

	"trigger-admin-bot/internal/match"
)

type Store struct {
	mg *mongoBackend

	cacheMu    sync.RWMutex
	cached     []Trigger
	cacheUntil time.Time
	cacheTTL   time.Duration
}

type ScheduledUnmute struct {
	ChatID   int64
	UserID   int64
	UnmuteAt int64
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
	if !isMongoURI(path) {
		return nil, fmt.Errorf("mongo uri required (set MONGO_URI)")
	}
	return openMongoStore(path)
}

func (s *Store) Close() error {
	if s == nil {
		return nil
	}
	if s.mg != nil {
		return s.mg.close()
	}
	return nil
}

func (s *Store) GetChatAdminCache(chatID, userID int64) (bool, int64, bool, error) {
	if s == nil || s.mg == nil {
		return false, 0, false, errors.New("mongo backend not initialized")
	}
	return s.mg.getChatAdminCache(chatID, userID)
}

func (s *Store) GetChatAdminSync(chatID int64) (int64, int, bool, error) {
	if s == nil || s.mg == nil {
		return 0, 0, false, errors.New("mongo backend not initialized")
	}
	return s.mg.getChatAdminSync(chatID)
}

func (s *Store) UpsertChatAdminSync(chatID int64, updatedAt int64, adminCount int) error {
	if s == nil || s.mg == nil {
		return errors.New("mongo backend not initialized")
	}
	if updatedAt <= 0 {
		updatedAt = time.Now().Unix()
	}
	return s.mg.upsertChatAdminSync(chatID, updatedAt, adminCount)
}

func (s *Store) UpsertChatAdminCache(chatID, userID int64, isAdmin bool, updatedAt int64) error {
	if s == nil || s.mg == nil {
		return errors.New("mongo backend not initialized")
	}
	if updatedAt <= 0 {
		updatedAt = time.Now().Unix()
	}
	return s.mg.upsertChatAdminCache(chatID, userID, isAdmin, updatedAt)
}

func (s *Store) TryConsumeDailyUserBotMessage(userID int64, now time.Time, limit int) (bool, error) {
	if s == nil || s.mg == nil {
		return false, errors.New("mongo backend not initialized")
	}
	return s.mg.tryConsumeDailyUserBotMessage(userID, now, limit)
}

func (s *Store) UpsertScheduledUnmute(chatID, userID int64, unmuteAt int64) error {
	if s == nil || s.mg == nil {
		return errors.New("mongo backend not initialized")
	}
	return s.mg.upsertScheduledUnmute(chatID, userID, unmuteAt)
}

func (s *Store) DeleteScheduledUnmute(chatID, userID int64) error {
	if s == nil || s.mg == nil {
		return errors.New("mongo backend not initialized")
	}
	return s.mg.deleteScheduledUnmute(chatID, userID)
}

func (s *Store) ListDueScheduledUnmutes(nowUnix int64, limit int) ([]ScheduledUnmute, error) {
	if s == nil || s.mg == nil {
		return nil, errors.New("mongo backend not initialized")
	}
	return s.mg.listDueScheduledUnmutes(nowUnix, limit)
}

func (s *Store) ClearChatAdminCache(chatID int64) error {
	if s == nil || s.mg == nil {
		return errors.New("mongo backend not initialized")
	}
	return s.mg.clearChatAdminCache(chatID)
}

func (s *Store) GetParticipantPortrait(chatID, userID int64) (string, error) {
	if s == nil || s.mg == nil {
		return "", errors.New("mongo backend not initialized")
	}
	if userID == 0 {
		return "", nil
	}
	return s.mg.getParticipantPortrait(chatID, userID)
}

func (s *Store) SaveParticipantPortrait(chatID, userID int64, portrait string) error {
	if s == nil || s.mg == nil {
		return errors.New("mongo backend not initialized")
	}
	if userID == 0 {
		return nil
	}
	return s.mg.saveParticipantPortrait(chatID, userID, portrait, time.Now().Unix())
}

func (s *Store) DeleteParticipantPortrait(userID int64) error {
	if s == nil || s.mg == nil {
		return errors.New("mongo backend not initialized")
	}
	if userID == 0 {
		return nil
	}
	return s.mg.deleteParticipantPortrait(userID)
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

func normalizeTriggerMode(v string) TriggerMode {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "only_replies":
		return TriggerModeOnlyReplies
	case "only_replies_to_any_bot":
		return TriggerModeOnlyRepliesToBot
	case "only_replies_to_combot":
		return TriggerModeOnlyRepliesToSelf
	case "only_replies_to_combot_no_media":
		return TriggerModeOnlyRepliesToSelfNoMedia
	case "never_on_replies":
		return TriggerModeNeverOnReplies
	case "command_reply":
		return TriggerModeCommandReply
	default:
		return TriggerModeAll
	}
}

func normalizeAdminMode(v string) AdminMode {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "admins":
		return AdminModeAdmins
	case "not_admins":
		return AdminModeNotAdmin
	default:
		return AdminModeAnybody
	}
}

func normalizeActionType(v string) ActionType {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "send_sticker":
		return ActionTypeSendSticker
	case "delete":
		return ActionTypeDelete
	case "delete_user_portrait":
		return ActionTypeDeletePortrait
	case "gpt_prompt":
		return ActionTypeGPTPrompt
	case "gpt_image":
		return ActionTypeGPTImage
	case "search_image":
		return ActionTypeSearchImage
	case "spotify_music_audio":
		return ActionTypeSpotifyMusic
	case "music_audio":
		return ActionTypeMusic
	case "yandex_music_audio":
		return ActionTypeYandexMusic
	case "media_link_audio":
		return ActionTypeMediaAudio
	case "media_tiktok_download":
		return ActionTypeMediaTikTok
	case "media_x_download":
		return ActionTypeMediaX
	default:
		return ActionTypeSend
	}
}

func (s *Store) ListTriggers() ([]Trigger, error) {
	if s == nil || s.mg == nil {
		return nil, errors.New("mongo backend not initialized")
	}
	return s.mg.listTriggers()
}

func (s *Store) GetTrigger(id int64) (*Trigger, error) {
	if s == nil || s.mg == nil {
		return nil, errors.New("mongo backend not initialized")
	}
	return s.mg.getTrigger(id)
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
	t.TriggerMode = normalizeTriggerMode(string(t.TriggerMode))
	t.AdminMode = normalizeAdminMode(string(t.AdminMode))
	t.MatchType = match.NormalizeMatchType(string(t.MatchType))
	t.ActionType = normalizeActionType(string(t.ActionType))
	t.Chance = sanitizeChance(t.Chance)
	t.RegexError = ""
	t.RegexBenchUS = 0
	if t.MatchType == "regex" {
		t.MatchText = match.StripLeadingCaseInsensitiveFlag(t.MatchText)
		pattern := t.MatchText
		if !t.CaseSensitive {
			pattern = "(?i)" + pattern
		}
		re, err := regexp.Compile(pattern)
		if err != nil {
			t.Enabled = false
			t.RegexError = "Ошибка regex: " + strings.TrimSpace(err.Error())
		} else {
			t.RegexBenchUS = match.BenchmarkRegex100US(re)
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
		if s == nil || s.mg == nil {
			return errors.New("mongo backend not initialized")
		}
		if err := s.mg.insertTrigger(t, now); err != nil {
			return err
		}
		s.invalidateCache()
		return nil
	}
	if s == nil || s.mg == nil {
		return errors.New("mongo backend not initialized")
	}
	if err := s.mg.updateTrigger(t, now); err != nil {
		return err
	}
	s.invalidateCache()
	return nil
}

func (s *Store) ToggleTrigger(id int64) (bool, error) {
	if s == nil || s.mg == nil {
		return false, fmt.Errorf("mongo backend not initialized")
	}
	next, err := s.mg.toggleTrigger(id)
	if err != nil {
		return false, err
	}
	s.invalidateCache()
	return next, nil
}

func (s *Store) DeleteTrigger(id int64) error {
	if s == nil || s.mg == nil {
		return fmt.Errorf("mongo backend not initialized")
	}
	if err := s.mg.deleteTrigger(id); err != nil {
		return err
	}
	s.invalidateCache()
	return nil
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
		if !match.TriggerMatches(t, text) {
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
	UID              string             `json:"uid,omitempty"`
	Priority         int                `json:"priority"`
	RegexBenchUS     int64              `json:"regex_bench_us,omitempty"`
	Title            string             `json:"title"`
	Enabled          bool               `json:"enabled"`
	TriggerMode      string             `json:"trigger_mode"`
	AdminMode        string             `json:"admin_mode"`
	MatchText        string             `json:"match_text"`
	MatchType        string             `json:"match_type"`
	CaseSensitive    bool               `json:"case_sensitive"`
	ActionType       string             `json:"action_type"`
	ResponseText     []ResponseTextItem `json:"response_text"`
	SendAsReply      bool               `json:"send_as_reply"`
	PreviewFirstLink bool               `json:"preview_first_link"`
	DeleteSourceMsg  bool               `json:"delete_source_message"`
	PassThrough      bool               `json:"pass_through"`
	Chance           int                `json:"chance"`
}

type importTriggerRow struct {
	UID              string          `json:"uid"`
	Priority         *int            `json:"priority"`
	RegexBenchUS     *int64          `json:"regex_bench_us"`
	Title            string          `json:"title"`
	Enabled          *bool           `json:"enabled"`
	TriggerMode      string          `json:"trigger_mode"`
	AdminMode        string          `json:"admin_mode"`
	MatchText        string          `json:"match_text"`
	MatchType        string          `json:"match_type"`
	CaseSensitive    *bool           `json:"case_sensitive"`
	ActionType       string          `json:"action_type"`
	ResponseText     json.RawMessage `json:"response_text"`
	SendAsReply      *bool           `json:"send_as_reply"`
	PreviewFirstLink *bool           `json:"preview_first_link"`
	DeleteSourceMsg  *bool           `json:"delete_source_message"`
	PassThrough      *bool           `json:"pass_through"`
	Chance           *int            `json:"chance"`
}

type exportTemplateRow struct {
	Key   string `json:"key"`
	Title string `json:"title"`
	Text  string `json:"text"`
}

type exportBundle struct {
	Triggers  []exportTriggerRow  `json:"triggers"`
	Templates []exportTemplateRow `json:"templates"`
}

type importBundle struct {
	Triggers  []importTriggerRow  `json:"triggers"`
	Templates []exportTemplateRow `json:"templates"`
}

func (s *Store) ExportJSON() ([]byte, error) {
	items, err := s.ListTriggers()
	if err != nil {
		return nil, err
	}
	triggers := make([]exportTriggerRow, 0, len(items))
	for _, t := range items {
		triggers = append(triggers, exportTriggerRow{
			UID:              t.UID,
			Priority:         t.Priority,
			RegexBenchUS:     t.RegexBenchUS,
			Title:            t.Title,
			Enabled:          t.Enabled,
			TriggerMode:      string(t.TriggerMode),
			AdminMode:        string(t.AdminMode),
			MatchText:        t.MatchText,
			MatchType:        string(t.MatchType),
			CaseSensitive:    t.CaseSensitive,
			ActionType:       string(t.ActionType),
			ResponseText:     t.ResponseText,
			SendAsReply:      t.Reply,
			PreviewFirstLink: t.Preview,
			DeleteSourceMsg:  t.DeleteSource,
			PassThrough:      t.PassThrough,
			Chance:           t.Chance,
		})
	}
	tpls, err := s.ListTemplates()
	if err != nil {
		return nil, err
	}
	templates := make([]exportTemplateRow, 0, len(tpls))
	for _, t := range tpls {
		templates = append(templates, exportTemplateRow{
			Key:   t.Key,
			Title: t.Title,
			Text:  t.Text,
		})
	}
	return json.MarshalIndent(exportBundle{
		Triggers:  triggers,
		Templates: templates,
	}, "", "  ")
}

func (s *Store) ImportJSON(raw []byte) (int, error) {
	raw = []byte(strings.TrimSpace(string(raw)))
	if len(raw) == 0 {
		return 0, nil
	}
	var payload importBundle
	if err := json.Unmarshal(raw, &payload); err != nil {
		return 0, err
	}
	if len(payload.Triggers) == 0 && len(payload.Templates) == 0 {
		return 0, nil
	}
	added := 0
	for _, it := range payload.Triggers {
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
		passThrough := false
		if it.PassThrough != nil {
			passThrough = *it.PassThrough
		}
		chance := 100
		if it.Chance != nil {
			chance = *it.Chance
		}

		tr := Trigger{
			UID:           strings.TrimSpace(it.UID),
			Title:         title,
			Enabled:       enabled,
			TriggerMode:   normalizeTriggerMode(triggerMode),
			AdminMode:     normalizeAdminMode(adminMode),
			MatchText:     matchText,
			MatchType:     match.NormalizeMatchType(matchType),
			CaseSensitive: caseSensitive,
			ActionType:    normalizeActionType(actionType),
			ResponseText:  responseItems,
			Reply:         reply,
			Preview:       preview,
			DeleteSource:  deleteSource,
			PassThrough:   passThrough,
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
	for _, it := range payload.Templates {
		key := strings.TrimSpace(it.Key)
		if key == "" {
			return added, errors.New("template key is required")
		}
		title := strings.TrimSpace(it.Title)
		if title == "" {
			title = key
		}
		text := strings.TrimSpace(it.Text)
		tpl := ResponseTemplate{
			Key:   key,
			Title: title,
			Text:  text,
		}
		existing, err := s.getTemplateByKey(key)
		if err != nil {
			return added, err
		}
		if existing != nil {
			tpl.ID = existing.ID
		}
		if err := s.SaveTemplate(tpl); err != nil {
			return added, err
		}
		added++
	}
	if added > 0 {
		s.invalidateCache()
	}
	return added, nil
}

func (s *Store) nextInsertPriority() (int, error) {
	if s == nil || s.mg == nil {
		return 0, errors.New("mongo backend not initialized")
	}
	return s.mg.nextInsertPriority()
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
	if s == nil || s.mg == nil {
		return errors.New("mongo backend not initialized")
	}
	if err := s.mg.reorderTriggersByIDs(finalOrder); err != nil {
		return err
	}
	s.invalidateCache()
	return nil
}

func (s *Store) getUIDByID(id int64) (string, error) {
	if id <= 0 {
		return "", nil
	}
	if s == nil || s.mg == nil {
		return "", errors.New("mongo backend not initialized")
	}
	return s.mg.getUIDByID(id)
}

func (s *Store) getIDByUID(uid string) (int64, error) {
	uid = strings.TrimSpace(uid)
	if uid == "" {
		return 0, nil
	}
	if s == nil || s.mg == nil {
		return 0, errors.New("mongo backend not initialized")
	}
	return s.mg.getIDByUID(uid)
}

func (s *Store) ListTemplates() ([]ResponseTemplate, error) {
	if s == nil || s.mg == nil {
		return nil, errors.New("mongo backend not initialized")
	}
	return s.mg.listTemplates()
}

func (s *Store) GetTemplate(id int64) (*ResponseTemplate, error) {
	if s == nil || s.mg == nil {
		return nil, errors.New("mongo backend not initialized")
	}
	return s.mg.getTemplate(id)
}

func (s *Store) getTemplateByKey(key string) (*ResponseTemplate, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return nil, nil
	}
	if s == nil || s.mg == nil {
		return nil, errors.New("mongo backend not initialized")
	}
	return s.mg.getTemplateByKey(key)
}

func (s *Store) SaveTemplate(t ResponseTemplate) error {
	if s == nil || s.mg == nil {
		return errors.New("mongo backend not initialized")
	}
	now := time.Now().Unix()
	t.Key = strings.TrimSpace(t.Key)
	t.Title = strings.TrimSpace(t.Title)
	t.Text = strings.TrimSpace(t.Text)
	if t.Key == "" {
		return errors.New("template key is required")
	}
	if t.Title == "" {
		t.Title = t.Key
	}
	if t.ID <= 0 {
		return s.mg.insertTemplate(t, now)
	}
	return s.mg.updateTemplate(t, now)
}

func (s *Store) DeleteTemplate(id int64) error {
	if s == nil || s.mg == nil {
		return errors.New("mongo backend not initialized")
	}
	return s.mg.deleteTemplate(id)
}

func (s *Store) findTemplateUsageByKey(key string) ([]Trigger, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return nil, nil
	}
	items, err := s.ListTriggers()
	if err != nil {
		return nil, err
	}
	re := regexp.MustCompile(`\\{\\{\\s*template\\s+\"([^\"]+)\"\\s*\\}\\}`)
	out := make([]Trigger, 0, 4)
	for _, tr := range items {
		for _, rt := range tr.ResponseText {
			matches := re.FindAllStringSubmatch(rt.Text, -1)
			for _, m := range matches {
				if len(m) > 1 && m[1] == key {
					out = append(out, tr)
					goto nextTrigger
				}
			}
		}
	nextTrigger:
	}
	return out, nil
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
