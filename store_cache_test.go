package main

import (
	"path/filepath"
	"testing"
)

func TestStoreListTriggersCachedInvalidatesOnSave(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	s, err := OpenStore(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	if err := s.SaveTrigger(Trigger{
		Title:        "one",
		Enabled:      true,
		TriggerMode:  "all",
		AdminMode:    "anybody",
		MatchText:    "one",
		MatchType:    "full",
		ActionType:   "send",
		ResponseText: []ResponseTextItem{{Text: "ok"}},
		Reply:        true,
		Preview:      false,
		Chance:       100,
	}); err != nil {
		t.Fatalf("save trigger #1: %v", err)
	}

	a, err := s.ListTriggersCached()
	if err != nil {
		t.Fatalf("list cached #1: %v", err)
	}
	if len(a) != 1 {
		t.Fatalf("expected 1 trigger, got %d", len(a))
	}

	if err := s.SaveTrigger(Trigger{
		Title:        "two",
		Enabled:      true,
		TriggerMode:  "all",
		AdminMode:    "anybody",
		MatchText:    "two",
		MatchType:    "full",
		ActionType:   "send",
		ResponseText: []ResponseTextItem{{Text: "ok"}},
		Reply:        true,
		Preview:      false,
		Chance:       100,
	}); err != nil {
		t.Fatalf("save trigger #2: %v", err)
	}

	b, err := s.ListTriggersCached()
	if err != nil {
		t.Fatalf("list cached #2: %v", err)
	}
	if len(b) != 2 {
		t.Fatalf("expected 2 triggers after invalidation, got %d", len(b))
	}
}
