package app

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"trigger-admin-bot/internal/match"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	uri := strings.TrimSpace(os.Getenv("MONGO_URI"))
	if uri == "" {
		t.Skip("MONGO_URI not set; mongo-only tests skipped")
	}
	dbName := fmt.Sprintf("trigger_admin_bot_test_%d", time.Now().UnixNano())
	testURI := withMongoTestDB(uri, dbName)
	s, err := OpenStore(testURI)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		if s.mg != nil && s.mg.db != nil {
			_ = s.mg.db.Drop(context.Background())
		}
		_ = s.Close()
	})
	return s
}

func withMongoTestDB(uri string, dbName string) string {
	parts := strings.SplitN(uri, "?", 2)
	base := parts[0]
	query := ""
	if len(parts) == 2 {
		query = "?" + parts[1]
	}
	schemeIdx := strings.Index(base, "://")
	lastSlash := strings.LastIndex(base, "/")
	if lastSlash == -1 || (schemeIdx >= 0 && lastSlash <= schemeIdx+2) {
		return base + "/" + dbName + query
	}
	if lastSlash == len(base)-1 {
		return base + dbName + query
	}
	return base[:lastSlash+1] + dbName + query
}

func TestTriggerMatchesBasic(t *testing.T) {
	tests := []struct {
		name string
		tr   Trigger
		in   string
		want bool
	}{
		{
			name: "full insensitive",
			tr:   Trigger{MatchText: "Привет", MatchType: "full", CaseSensitive: false},
			in:   "привет",
			want: true,
		},
		{
			name: "partial insensitive",
			tr:   Trigger{MatchText: "кац", MatchType: "partial", CaseSensitive: false},
			in:   "максим кац мой герой",
			want: true,
		},
		{
			name: "regex insensitive",
			tr:   Trigger{MatchText: `дмитрий\s*гудков`, MatchType: "regex", CaseSensitive: false},
			in:   "Дмитрий    Гудков",
			want: true,
		},
		{
			name: "starts",
			tr:   Trigger{MatchText: "оле", MatchType: "starts", CaseSensitive: false},
			in:   "Оле-ням привет",
			want: true,
		},
		{
			name: "ends",
			tr:   Trigger{MatchText: "герой", MatchType: "ends", CaseSensitive: false},
			in:   "максим кац мой герой",
			want: true,
		},
		{
			name: "idle is not text matcher",
			tr:   Trigger{MatchText: "120", MatchType: "idle", CaseSensitive: false},
			in:   "какой-то текст",
			want: false,
		},
		{
			name: "empty incoming no match",
			tr:   Trigger{MatchText: "x", MatchType: "partial"},
			in:   "",
			want: false,
		},
		{
			name: "empty trigger matches any non-empty",
			tr:   Trigger{MatchText: "", MatchType: "partial"},
			in:   "что угодно",
			want: true,
		},
	}

	for _, tc := range tests {
		got := match.TriggerMatches(tc.tr, tc.in)
		if got != tc.want {
			t.Fatalf("%s: got=%v want=%v", tc.name, got, tc.want)
		}
	}
}

func TestTriggerMatchesRegexInvalid(t *testing.T) {
	tr := Trigger{MatchText: "([", MatchType: "regex", CaseSensitive: false}
	if match.TriggerMatches(tr, "abc") {
		t.Fatalf("invalid regex must not match")
	}
}

func TestTriggerMatchCaptureRegex(t *testing.T) {
	tr := Trigger{
		MatchType:     "regex",
		CaseSensitive: false,
		MatchText:     `(^|[^\p{L}\p{N}_])((?:навальн(?:ый|ого|ому|ым|ом|ая|ой|ую|ые|ых|ыми)?|шульман(?:а|у|ом|е)?|кац(?:а|у|ем|е)?))(?:$|[^\p{L}\p{N}_])`,
	}
	ok, capture := match.TriggerMatchCapture(tr, "в чате обсуждают навального сегодня")
	if !ok {
		t.Fatalf("expected regex match")
	}
	if capture != "навального" {
		t.Fatalf("unexpected capture: %q", capture)
	}
}

func TestTriggerMatchCapturePrefersLongestNonEmptyGroup(t *testing.T) {
	tr := Trigger{
		MatchType:     "regex",
		CaseSensitive: true,
		MatchText:     `(.+)(.+)`,
	}
	ok, capture := match.TriggerMatchCapture(tr, "Кац")
	if !ok {
		t.Fatalf("expected regex match")
	}
	if capture != "Ка" {
		t.Fatalf("unexpected capture: %q", capture)
	}
}

func TestSaveTriggerInvalidRegexDisablesAndMarksError(t *testing.T) {
	s := openTestStore(t)

	err := s.SaveTrigger(Trigger{
		Title:        "bad regex",
		Enabled:      true,
		TriggerMode:  "all",
		AdminMode:    "anybody",
		MatchText:    "([",
		MatchType:    "regex",
		ActionType:   "send",
		ResponseText: []ResponseTextItem{{Text: "ok"}},
		Reply:        true,
		Preview:      false,
		Chance:       100,
	})
	if err != nil {
		t.Fatalf("save trigger: %v", err)
	}

	items, err := s.ListTriggers()
	if err != nil {
		t.Fatalf("list triggers: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 trigger, got %d", len(items))
	}
	if items[0].Enabled {
		t.Fatalf("invalid regex trigger must be disabled")
	}
	if items[0].RegexError == "" {
		t.Fatalf("invalid regex trigger must have regex error mark")
	}
}

func TestSaveTriggerValidRegexClearsError(t *testing.T) {
	s := openTestStore(t)

	err := s.SaveTrigger(Trigger{
		Title:        "regex fixed",
		Enabled:      true,
		TriggerMode:  "all",
		AdminMode:    "anybody",
		MatchText:    "(?i)кац",
		MatchType:    "regex",
		ActionType:   "send",
		ResponseText: []ResponseTextItem{{Text: "ok"}},
		Reply:        true,
		Preview:      false,
		Chance:       100,
	})
	if err != nil {
		t.Fatalf("save trigger: %v", err)
	}

	items, err := s.ListTriggers()
	if err != nil {
		t.Fatalf("list triggers: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 trigger, got %d", len(items))
	}
	if !items[0].Enabled {
		t.Fatalf("valid regex trigger must remain enabled")
	}
	if items[0].RegexError != "" {
		t.Fatalf("valid regex trigger must not have regex error: %q", items[0].RegexError)
	}
	if items[0].RegexBenchUS <= 0 {
		t.Fatalf("valid regex trigger must record benchmark, got %d", items[0].RegexBenchUS)
	}
	if items[0].MatchText != "кац" {
		t.Fatalf("stored regex should not keep leading (?i), got %q", items[0].MatchText)
	}
}

func TestExportImportBundleRoundTrip(t *testing.T) {
	s1 := openTestStore(t)
	err := s1.SaveTrigger(Trigger{
		Title:        "hello",
		Enabled:      true,
		TriggerMode:  "all",
		AdminMode:    "anybody",
		MatchText:    "hi",
		MatchType:    "partial",
		ActionType:   "send",
		ResponseText: []ResponseTextItem{{Text: "hi there"}},
		Reply:        true,
		Preview:      false,
		Chance:       100,
	})
	if err != nil {
		t.Fatalf("save trigger: %v", err)
	}
	if err := s1.SaveTemplate(ResponseTemplate{
		Key:   "t_base",
		Title: "Base",
		Text:  "hello {{message}}",
	}); err != nil {
		t.Fatalf("save template: %v", err)
	}

	raw, err := s1.ExportJSON()
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	s2 := openTestStore(t)
	added, err := s2.ImportJSON(raw)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if added != 2 {
		t.Fatalf("expected 2 imported items, got %d", added)
	}

	triggers, err := s2.ListTriggers()
	if err != nil {
		t.Fatalf("list triggers: %v", err)
	}
	if len(triggers) != 1 {
		t.Fatalf("expected 1 trigger, got %d", len(triggers))
	}
	if triggers[0].Title != "hello" {
		t.Fatalf("unexpected trigger title: %q", triggers[0].Title)
	}

	templates, err := s2.ListTemplates()
	if err != nil {
		t.Fatalf("list templates: %v", err)
	}
	if len(templates) != 1 {
		t.Fatalf("expected 1 template, got %d", len(templates))
	}
	if templates[0].Key != "t_base" || templates[0].Text != "hello {{message}}" {
		t.Fatalf("unexpected template: %+v", templates[0])
	}
}

func TestImportBundleUpdatesTemplateByKey(t *testing.T) {
	s1 := openTestStore(t)
	if err := s1.SaveTemplate(ResponseTemplate{
		Key:   "t_update",
		Title: "Old",
		Text:  "old",
	}); err != nil {
		t.Fatalf("save template: %v", err)
	}
	raw, err := s1.ExportJSON()
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	s2 := openTestStore(t)
	if err := s2.SaveTemplate(ResponseTemplate{
		Key:   "t_update",
		Title: "Legacy",
		Text:  "legacy",
	}); err != nil {
		t.Fatalf("seed template: %v", err)
	}
	added, err := s2.ImportJSON(raw)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if added != 1 {
		t.Fatalf("expected 1 imported item, got %d", added)
	}
	tpl, err := s2.getTemplateByKey("t_update")
	if err != nil {
		t.Fatalf("get template: %v", err)
	}
	if tpl == nil {
		t.Fatalf("template missing after import")
	}
	if tpl.Title != "Old" || tpl.Text != "old" {
		t.Fatalf("template not updated: %+v", tpl)
	}
}

func TestImportBundleRejectsLegacyArray(t *testing.T) {
	s := openTestStore(t)
	raw := []byte(`[{"title":"legacy","match_text":"x"}]`)
	if _, err := s.ImportJSON(raw); err == nil {
		t.Fatalf("expected legacy array import to fail")
	}
}

func TestSaveTriggerAssignsUID(t *testing.T) {
	s := openTestStore(t)

	if err := s.SaveTrigger(Trigger{
		Title:        "uid auto",
		Enabled:      true,
		TriggerMode:  "all",
		AdminMode:    "anybody",
		MatchText:    "тест",
		MatchType:    "partial",
		ActionType:   "send",
		ResponseText: []ResponseTextItem{{Text: "ok"}},
		Reply:        true,
		Chance:       100,
	}); err != nil {
		t.Fatalf("save trigger: %v", err)
	}

	items, err := s.ListTriggers()
	if err != nil {
		t.Fatalf("list triggers: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 trigger, got %d", len(items))
	}
	if items[0].UID == "" {
		t.Fatalf("uid must be auto-assigned")
	}
}

func TestSaveTriggerPreservesResponseTextVerbatim(t *testing.T) {
	s := openTestStore(t)

	want := "\n  первая строка\nвторая строка  \n"
	if err := s.SaveTrigger(Trigger{
		Title:        "verbatim response",
		Enabled:      true,
		TriggerMode:  "all",
		AdminMode:    "anybody",
		MatchText:    "тест",
		MatchType:    "partial",
		ActionType:   "send",
		ResponseText: []ResponseTextItem{{Text: want}},
		Reply:        true,
		Chance:       100,
	}); err != nil {
		t.Fatalf("save trigger: %v", err)
	}

	items, err := s.ListTriggers()
	if err != nil {
		t.Fatalf("list triggers: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 trigger, got %d", len(items))
	}
	if len(items[0].ResponseText) != 1 || items[0].ResponseText[0].Text != want {
		t.Fatalf("response text changed on save, got=%#v want=%q", items[0].ResponseText, want)
	}
}

func TestImportJSONUpsertsByUIDAndToleratesMissingFields(t *testing.T) {
	s := openTestStore(t)

	const uid = "11111111-2222-4333-8444-555555555555"
	if err := s.SaveTrigger(Trigger{
		UID:          uid,
		Title:        "old title",
		Enabled:      true,
		TriggerMode:  "all",
		AdminMode:    "anybody",
		MatchText:    "старое",
		MatchType:    "partial",
		ActionType:   "send",
		ResponseText: []ResponseTextItem{{Text: "старый ответ"}},
		Reply:        true,
		Chance:       100,
	}); err != nil {
		t.Fatalf("save base trigger: %v", err)
	}

	raw := `[
	  {"uid":"11111111-2222-4333-8444-555555555555","title":"new title","match_text":"новое","match_type":"partial","action_type":"send","response_text":"новый ответ","send_as_reply":true},
	  {"title":"partial item without columns"}
	]`
	n, err := s.ImportJSON([]byte(raw))
	if err != nil {
		t.Fatalf("import json: %v", err)
	}
	if n != 2 {
		t.Fatalf("expected imported count=2, got %d", n)
	}

	items, err := s.ListTriggers()
	if err != nil {
		t.Fatalf("list triggers: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 triggers total (1 update + 1 new), got %d", len(items))
	}

	var updated *Trigger
	var partial *Trigger
	for i := range items {
		it := &items[i]
		if it.UID == uid {
			updated = it
			continue
		}
		if it.Title == "partial item without columns" {
			partial = it
		}
	}
	if updated == nil {
		t.Fatalf("updated trigger by uid not found")
	}
	if updated.Title != "new title" || updated.MatchText != "новое" || len(updated.ResponseText) != 1 || updated.ResponseText[0].Text != "новый ответ" {
		t.Fatalf("uid-based update not applied: %#v", *updated)
	}
	if partial == nil {
		t.Fatalf("partial item was not imported")
	}
}

func TestImportJSONLegacyFormatNotSupported(t *testing.T) {
	s := openTestStore(t)

	legacy := `[{"t":"legacy","cos":[{"tt":"x","ty":"1"}],"acs":[{"ty":"se","t":"ok","sr":"1"}]}]`
	if _, err := s.ImportJSON([]byte(legacy)); err == nil {
		t.Fatalf("legacy format should not be supported anymore")
	}
}

func TestImportJSONColumnFormatUsesDefaults(t *testing.T) {
	s := openTestStore(t)

	raw := `[{"title":"minimal import row"}]`
	n, err := s.ImportJSON([]byte(raw))
	if err != nil {
		t.Fatalf("import json: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected imported count=1, got %d", n)
	}

	items, err := s.ListTriggers()
	if err != nil {
		t.Fatalf("list triggers: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 trigger, got %d", len(items))
	}
	it := items[0]
	if it.Title != "minimal import row" {
		t.Fatalf("title mismatch: %q", it.Title)
	}
	if !it.Enabled || it.TriggerMode != "all" || it.AdminMode != "anybody" {
		t.Fatalf("default modes mismatch: %#v", it)
	}
	if it.MatchType != "full" || it.ActionType != "send" {
		t.Fatalf("default types mismatch: match_type=%q action_type=%q", it.MatchType, it.ActionType)
	}
	if !it.Reply || it.Preview {
		t.Fatalf("default reply/preview mismatch: reply=%v preview=%v", it.Reply, it.Preview)
	}
	if it.Chance != 100 {
		t.Fatalf("default chance mismatch: %d", it.Chance)
	}
	if it.UID == "" {
		t.Fatalf("uid should be generated for imported row")
	}
}

func TestImportJSONMixedFormatImportsOnlyNewRows(t *testing.T) {
	s := openTestStore(t)

	raw := `[
	  {"title":"new row","match_text":"abc","match_type":"partial","action_type":"send","response_text":"ok"},
	  {"t":"legacy row","cos":[{"tt":"x","ty":"1"}],"acs":[{"ty":"se","t":"old","sr":"1"}]}
	]`
	n, err := s.ImportJSON([]byte(raw))
	if err != nil {
		t.Fatalf("import mixed json: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected imported count=1 for mixed file, got %d", n)
	}
	items, err := s.ListTriggers()
	if err != nil {
		t.Fatalf("list triggers: %v", err)
	}
	if len(items) != 1 || items[0].Title != "new row" {
		t.Fatalf("unexpected imported items: %#v", items)
	}
}

func TestReorderTriggersByIDsChangesPriorityOrder(t *testing.T) {
	s := openTestStore(t)

	save := func(title string) int64 {
		if err := s.SaveTrigger(Trigger{
			Title:        title,
			Enabled:      true,
			TriggerMode:  "all",
			AdminMode:    "anybody",
			MatchText:    "найди",
			MatchType:    "partial",
			ActionType:   "send",
			ResponseText: []ResponseTextItem{{Text: title}},
			Reply:        true,
			Chance:       100,
		}); err != nil {
			t.Fatalf("save trigger %q: %v", title, err)
		}
		items, err := s.ListTriggers()
		if err != nil {
			t.Fatalf("list triggers: %v", err)
		}
		for _, it := range items {
			if it.Title == title {
				return it.ID
			}
		}
		t.Fatalf("saved trigger %q not found", title)
		return 0
	}

	id1 := save("t1")
	id2 := save("t2")
	id3 := save("t3")

	if err := s.ReorderTriggersByIDs([]int64{id3, id1, id2}); err != nil {
		t.Fatalf("reorder: %v", err)
	}

	items, err := s.ListTriggers()
	if err != nil {
		t.Fatalf("list triggers after reorder: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("expected 3 triggers, got %d", len(items))
	}
	if items[0].ID != id3 || items[1].ID != id1 || items[2].ID != id2 {
		t.Fatalf("unexpected order after reorder: got IDs [%d,%d,%d], want [%d,%d,%d]",
			items[0].ID, items[1].ID, items[2].ID, id3, id1, id2)
	}

	got, ok, err := s.Match("найди песню")
	if err != nil {
		t.Fatalf("match failed: %v", err)
	}
	if !ok || got == nil {
		t.Fatalf("expected match after reorder")
	}
	if got.ID != id3 {
		t.Fatalf("expected highest-priority trigger id=%d, got id=%d", id3, got.ID)
	}
}

func TestTriggerMatchCaptureRegexHonorsCaseFlagWithoutInlinePrefix(t *testing.T) {
	tr := Trigger{
		MatchType:     "regex",
		CaseSensitive: false,
		MatchText:     "(?i)кац",
	}
	ok, capture := match.TriggerMatchCapture(tr, "МАКСИМ КАЦ в чате")
	if !ok {
		t.Fatalf("expected regex match with case-insensitive flag off")
	}
	if capture == "" {
		t.Fatalf("expected non-empty capture")
	}
}

func TestSaveTriggerRegexCaseSensitiveStripsInlineFlagAndStaysCaseSensitive(t *testing.T) {
	s := openTestStore(t)

	if err := s.SaveTrigger(Trigger{
		Title:         "case strict",
		Enabled:       true,
		TriggerMode:   "all",
		AdminMode:     "anybody",
		MatchText:     "(?i)кац",
		MatchType:     "regex",
		CaseSensitive: true,
		ActionType:    "send",
		ResponseText:  []ResponseTextItem{{Text: "ok"}},
		Reply:         true,
		Chance:        100,
	}); err != nil {
		t.Fatalf("save trigger: %v", err)
	}

	items, err := s.ListTriggers()
	if err != nil {
		t.Fatalf("list triggers: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 trigger, got %d", len(items))
	}
	if items[0].MatchText != "кац" {
		t.Fatalf("inline case flag should be stripped, got %q", items[0].MatchText)
	}
	if ok, _ := match.TriggerMatchCapture(items[0], "КАЦ"); ok {
		t.Fatalf("case-sensitive regex should not match upper-case text")
	}
}

func TestChatAdminCacheCRUD(t *testing.T) {
	s := openTestStore(t)

	chatID := int64(-100123)
	userID := int64(42)

	if err := s.UpsertChatAdminCache(chatID, userID, true, 12345); err != nil {
		t.Fatalf("upsert admin cache: %v", err)
	}

	isAdmin, updatedAt, ok, err := s.GetChatAdminCache(chatID, userID)
	if err != nil {
		t.Fatalf("get admin cache: %v", err)
	}
	if !ok || !isAdmin || updatedAt != 12345 {
		t.Fatalf("unexpected cached row: ok=%v is_admin=%v updated_at=%d", ok, isAdmin, updatedAt)
	}

	if err := s.UpsertChatAdminCache(chatID, userID, false, 22222); err != nil {
		t.Fatalf("update admin cache: %v", err)
	}
	isAdmin, updatedAt, ok, err = s.GetChatAdminCache(chatID, userID)
	if err != nil {
		t.Fatalf("get admin cache after update: %v", err)
	}
	if !ok || isAdmin || updatedAt != 22222 {
		t.Fatalf("unexpected updated row: ok=%v is_admin=%v updated_at=%d", ok, isAdmin, updatedAt)
	}

	if err := s.ClearChatAdminCache(chatID); err != nil {
		t.Fatalf("clear admin cache: %v", err)
	}
	_, _, ok, err = s.GetChatAdminCache(chatID, userID)
	if err != nil {
		t.Fatalf("get admin cache after clear: %v", err)
	}
	if ok {
		t.Fatalf("expected cache row to be deleted")
	}
}

func TestChatAdminSyncCRUD(t *testing.T) {
	s := openTestStore(t)

	chatID := int64(-100987)
	if err := s.UpsertChatAdminSync(chatID, 11111, 5); err != nil {
		t.Fatalf("upsert sync: %v", err)
	}
	updatedAt, cnt, ok, err := s.GetChatAdminSync(chatID)
	if err != nil {
		t.Fatalf("get sync: %v", err)
	}
	if !ok || updatedAt != 11111 || cnt != 5 {
		t.Fatalf("unexpected sync row: ok=%v updated=%d cnt=%d", ok, updatedAt, cnt)
	}
	if err := s.UpsertChatAdminSync(chatID, 22222, 3); err != nil {
		t.Fatalf("upsert sync update: %v", err)
	}
	updatedAt, cnt, ok, err = s.GetChatAdminSync(chatID)
	if err != nil {
		t.Fatalf("get sync after update: %v", err)
	}
	if !ok || updatedAt != 22222 || cnt != 3 {
		t.Fatalf("unexpected sync row after update: ok=%v updated=%d cnt=%d", ok, updatedAt, cnt)
	}
	if err := s.ClearChatAdminCache(chatID); err != nil {
		t.Fatalf("clear chat admin cache+sync: %v", err)
	}
	_, _, ok, err = s.GetChatAdminSync(chatID)
	if err != nil {
		t.Fatalf("get sync after clear: %v", err)
	}
	if ok {
		t.Fatalf("expected sync row to be deleted")
	}
}

func TestTryConsumeDailyUserBotMessage(t *testing.T) {
	s := openTestStore(t)
	userID := int64(123456)
	winStart := time.Date(2026, time.April, 23, 12, 5, 0, 0, time.UTC) // 12:00..15:59 window
	nextWin := time.Date(2026, time.April, 23, 16, 0, 1, 0, time.UTC)  // next 4h window

	ok, err := s.TryConsumeDailyUserBotMessage(userID, winStart, 2)
	if err != nil {
		t.Fatalf("first consume: %v", err)
	}
	if !ok {
		t.Fatalf("first consume must pass")
	}
	ok, err = s.TryConsumeDailyUserBotMessage(userID, winStart.Add(2*time.Hour), 2)
	if err != nil {
		t.Fatalf("second consume: %v", err)
	}
	if !ok {
		t.Fatalf("second consume must pass")
	}
	ok, err = s.TryConsumeDailyUserBotMessage(userID, winStart.Add(3*time.Hour), 2)
	if err != nil {
		t.Fatalf("third consume: %v", err)
	}
	if ok {
		t.Fatalf("third consume in same 4h window must be blocked")
	}
	ok, err = s.TryConsumeDailyUserBotMessage(userID, nextWin, 2)
	if err != nil {
		t.Fatalf("next window consume: %v", err)
	}
	if !ok {
		t.Fatalf("next 4h window consume must pass")
	}
}
