package main

import (
	"path/filepath"
	"testing"
)

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
		got := TriggerMatches(tc.tr, tc.in)
		if got != tc.want {
			t.Fatalf("%s: got=%v want=%v", tc.name, got, tc.want)
		}
	}
}

func TestTriggerMatchesRegexInvalid(t *testing.T) {
	tr := Trigger{MatchText: "([", MatchType: "regex", CaseSensitive: false}
	if TriggerMatches(tr, "abc") {
		t.Fatalf("invalid regex must not match")
	}
}

func TestTriggerMatchCaptureRegex(t *testing.T) {
	tr := Trigger{
		MatchType:     "regex",
		CaseSensitive: false,
		MatchText:     `(^|[^\p{L}\p{N}_])((?:навальн(?:ый|ого|ому|ым|ом|ая|ой|ую|ые|ых|ыми)?|шульман(?:а|у|ом|е)?|кац(?:а|у|ем|е)?))(?:$|[^\p{L}\p{N}_])`,
	}
	ok, capture := TriggerMatchCapture(tr, "в чате обсуждают навального сегодня")
	if !ok {
		t.Fatalf("expected regex match")
	}
	if capture != "навального" {
		t.Fatalf("unexpected capture: %q", capture)
	}
}

func TestPickBestCapturePrefersLongestNonEmptyGroup(t *testing.T) {
	got := pickBestCapture([]string{"  Кац  ", "", "Ка", "Кац"})
	if got != "Кац" {
		t.Fatalf("unexpected best capture: %q", got)
	}
}

func TestSaveTriggerInvalidRegexDisablesAndMarksError(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "regex_invalid.db")
	s, err := OpenStore(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	err = s.SaveTrigger(Trigger{
		Title:        "bad regex",
		Enabled:      true,
		TriggerMode:  "all",
		AdminMode:    "anybody",
		MatchText:    "([",
		MatchType:    "regex",
		ActionType:   "send",
		ResponseText: "ok",
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
	dbPath := filepath.Join(t.TempDir(), "regex_valid.db")
	s, err := OpenStore(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	err = s.SaveTrigger(Trigger{
		Title:        "regex fixed",
		Enabled:      true,
		TriggerMode:  "all",
		AdminMode:    "anybody",
		MatchText:    "(?i)кац",
		MatchType:    "regex",
		ActionType:   "send",
		ResponseText: "ok",
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
}

func TestSaveTriggerAssignsUID(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "uid_assign.db")
	s, err := OpenStore(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	if err := s.SaveTrigger(Trigger{
		Title:        "uid auto",
		Enabled:      true,
		TriggerMode:  "all",
		AdminMode:    "anybody",
		MatchText:    "тест",
		MatchType:    "partial",
		ActionType:   "send",
		ResponseText: "ok",
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
	dbPath := filepath.Join(t.TempDir(), "response_verbatim.db")
	s, err := OpenStore(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	want := "\n  первая строка\nвторая строка  \n"
	if err := s.SaveTrigger(Trigger{
		Title:        "verbatim response",
		Enabled:      true,
		TriggerMode:  "all",
		AdminMode:    "anybody",
		MatchText:    "тест",
		MatchType:    "partial",
		ActionType:   "send",
		ResponseText: want,
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
	if items[0].ResponseText != want {
		t.Fatalf("response text changed on save, got=%q want=%q", items[0].ResponseText, want)
	}
}

func TestImportJSONUpsertsByUIDAndToleratesMissingFields(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "uid_import.db")
	s, err := OpenStore(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

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
		ResponseText: "старый ответ",
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
	if updated.Title != "new title" || updated.MatchText != "новое" || updated.ResponseText != "новый ответ" {
		t.Fatalf("uid-based update not applied: %#v", *updated)
	}
	if partial == nil {
		t.Fatalf("partial item was not imported")
	}
}

func TestImportJSONLegacyFormatNotSupported(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "legacy_import.db")
	s, err := OpenStore(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	legacy := `[{"t":"legacy","cos":[{"tt":"x","ty":"1"}],"acs":[{"ty":"se","t":"ok","sr":"1"}]}]`
	if _, err := s.ImportJSON([]byte(legacy)); err == nil {
		t.Fatalf("legacy format should not be supported anymore")
	}
}

func TestImportJSONColumnFormatUsesDefaults(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "import_defaults.db")
	s, err := OpenStore(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

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
	dbPath := filepath.Join(t.TempDir(), "import_mixed.db")
	s, err := OpenStore(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

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
	dbPath := filepath.Join(t.TempDir(), "reorder.db")
	s, err := OpenStore(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	save := func(title string) int64 {
		if err := s.SaveTrigger(Trigger{
			Title:        title,
			Enabled:      true,
			TriggerMode:  "all",
			AdminMode:    "anybody",
			MatchText:    "найди",
			MatchType:    "partial",
			ActionType:   "send",
			ResponseText: title,
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
