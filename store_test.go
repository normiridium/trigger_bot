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
}
