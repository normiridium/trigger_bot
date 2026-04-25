package app

import "testing"

func TestSanitizeMetaValue(t *testing.T) {
	got := sanitizeMetaValue("  hi\x00there  ", 20)
	if got != "hithere" {
		t.Fatalf("unexpected sanitized value: %q", got)
	}
	long := sanitizeMetaValue("абвгдеёжз", 5)
	if long != "абвгд" {
		t.Fatalf("unexpected rune truncation: %q", long)
	}
}

func TestExtractLyricsText(t *testing.T) {
	if got := extractLyricsText(lrclibGetResponse{
		PlainLyrics: "line1\nline2",
	}); got != "line1\nline2" {
		t.Fatalf("unexpected plain lyrics: %q", got)
	}
	if got := extractLyricsText(lrclibGetResponse{
		SyncedLyrics: "[00:01.00]line",
	}); got != "[00:01.00]line" {
		t.Fatalf("unexpected synced lyrics fallback: %q", got)
	}
	if got := extractLyricsText(lrclibGetResponse{
		Instrumental: true,
		PlainLyrics:  "should be dropped",
	}); got != "" {
		t.Fatalf("instrumental must return empty lyrics, got %q", got)
	}
}

func TestNormalizeID3LyricsLang(t *testing.T) {
	if got := normalizeID3LyricsLang("rus"); got != "rus" {
		t.Fatalf("expected rus, got %q", got)
	}
	if got := normalizeID3LyricsLang("ENg"); got != "eng" {
		t.Fatalf("expected eng lower, got %q", got)
	}
	if got := normalizeID3LyricsLang("xx"); got != "eng" {
		t.Fatalf("expected eng fallback for short code, got %q", got)
	}
	if got := normalizeID3LyricsLang("12#"); got != "eng" {
		t.Fatalf("expected eng fallback for invalid code, got %q", got)
	}
}
