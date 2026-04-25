package yandexmusic

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestAPIQuality(t *testing.T) {
	if got := apiQuality(0); got != "lq" {
		t.Fatalf("apiQuality(0)=%q", got)
	}
	if got := apiQuality(1); got != "nq" {
		t.Fatalf("apiQuality(1)=%q", got)
	}
	if got := apiQuality(2); got != "lossless" {
		t.Fatalf("apiQuality(2)=%q", got)
	}
}

func TestQualityFallbacks(t *testing.T) {
	cases := []struct {
		in   int
		want []int
	}{
		{in: 0, want: []int{0, 1, 2}},
		{in: 1, want: []int{1, 0, 2}},
		{in: 2, want: []int{2, 0, 1}},
		{in: 10, want: []int{0, 1, 2}},
	}
	for _, tc := range cases {
		got := qualityFallbacks(tc.in)
		if len(got) != len(tc.want) {
			t.Fatalf("qualityFallbacks(%d) len=%d want=%d", tc.in, len(got), len(tc.want))
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Fatalf("qualityFallbacks(%d)=%v want=%v", tc.in, got, tc.want)
			}
		}
	}
}

func TestExtFromCodec(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{in: "flac", want: "flac"},
		{in: "flac-mp4", want: "flac"},
		{in: "aac", want: "m4a"},
		{in: "he-aac-mp4", want: "m4a"},
		{in: "mp3", want: "mp3"},
		{in: "unknown", want: "mp3"},
	}
	for _, tc := range cases {
		if got := extFromCodec(tc.in); got != tc.want {
			t.Fatalf("extFromCodec(%q)=%q want=%q", tc.in, got, tc.want)
		}
	}
}

func TestExtractTrackID(t *testing.T) {
	cases := []struct {
		raw   string
		want  int64
		isErr bool
	}{
		{raw: "https://music.yandex.ru/album/1/track/116601318", want: 116601318},
		{raw: "https://music.yandex.com/track/42?from=search", want: 42},
		{raw: "https://www.music.yandex.ru/track/77", want: 77},
		{raw: "https://example.org/track/10", isErr: true},
		{raw: "https://music.yandex.ru/album/1", isErr: true},
	}
	for _, tc := range cases {
		got, err := extractTrackID(tc.raw)
		if tc.isErr {
			if err == nil {
				t.Fatalf("expected error for %q", tc.raw)
			}
			continue
		}
		if err != nil {
			t.Fatalf("extractTrackID(%q) unexpected error: %v", tc.raw, err)
		}
		if got != tc.want {
			t.Fatalf("extractTrackID(%q)=%d want=%d", tc.raw, got, tc.want)
		}
	}
}

func TestSafeNameAndBuildTrackName(t *testing.T) {
	got := safeName(` Artist:/\:*?"<>| `)
	if strings.ContainsAny(got, `/\:*?"<>|`) {
		t.Fatalf("unsafe characters remain: %q", got)
	}
	long := strings.Repeat("a", 120)
	if n := len(safeName(long)); n > 64 {
		t.Fatalf("safeName must be capped to 64 chars, got %d", n)
	}

	track := &Track{
		Title:   "Algorithms",
		Artists: []ArtistBrief{{Name: "Vahabular"}},
	}
	if name := buildTrackBaseName(123, track); name != "Vahabular - Algorithms" {
		t.Fatalf("unexpected base name: %q", name)
	}
	if name := buildTrackBaseName(321, &Track{Title: "Only Title"}); name != "Only Title" {
		t.Fatalf("unexpected title-only name: %q", name)
	}
	if name := buildTrackBaseName(555, nil); name != "555" {
		t.Fatalf("unexpected nil-track fallback: %q", name)
	}
}

func TestBuildTrackURL(t *testing.T) {
	if got := buildTrackURL(116601318, 27054782); got != "https://music.yandex.ru/album/27054782/track/116601318" {
		t.Fatalf("unexpected album track url: %q", got)
	}
	if got := buildTrackURL(42, 0); got != "https://music.yandex.ru/track/42" {
		t.Fatalf("unexpected track url: %q", got)
	}
}

func TestExtractTrackMeta(t *testing.T) {
	meta := extractTrackMeta(&Track{
		Title:   "La Mort d'Arthur",
		Artists: []ArtistBrief{{Name: "Sopor Aeternus & The Ensemble Of Shadows"}},
		Albums: []AlbumBrief{{
			ID:    2403776,
			Title: "Les Fleurs Du Mal",
			Year:  2007,
			TrackPosition: &TrackPosition{
				Index: 7,
			},
		}},
	}, "lyrics line 1\nlyrics line 2")
	if meta.Title != "La Mort d'Arthur" {
		t.Fatalf("unexpected title: %q", meta.Title)
	}
	if meta.Artist != "Sopor Aeternus & The Ensemble Of Shadows" {
		t.Fatalf("unexpected artist: %q", meta.Artist)
	}
	if meta.Album != "Les Fleurs Du Mal" || meta.Year != 2007 || meta.TrackNo != 7 {
		t.Fatalf("unexpected album meta: %+v", meta)
	}
	if !strings.Contains(meta.Lyrics, "lyrics line 2") {
		t.Fatalf("lyrics not propagated: %q", meta.Lyrics)
	}
}

func TestSanitizeMetaValue(t *testing.T) {
	got := sanitizeMetaValue("  hi\x00there  ", 20)
	if got != "hithere" {
		t.Fatalf("unexpected sanitized value: %q", got)
	}
	long := sanitizeMetaValue(strings.Repeat("x", 100), 10)
	if len([]rune(long)) != 10 {
		t.Fatalf("expected truncation to 10 runes, got %d", len([]rune(long)))
	}
}

func TestYMInt64UnmarshalJSON(t *testing.T) {
	var v YMInt64
	if err := json.Unmarshal([]byte(`123`), &v); err != nil || int64(v) != 123 {
		t.Fatalf("numeric unmarshal failed: v=%d err=%v", v, err)
	}
	if err := json.Unmarshal([]byte(`"456:0"`), &v); err != nil || int64(v) != 456 {
		t.Fatalf("string unmarshal failed: v=%d err=%v", v, err)
	}
	if err := json.Unmarshal([]byte(`"bad"`), &v); err == nil {
		t.Fatal("expected parse error for invalid YMInt64")
	}
}

func TestDownloaderInputValidationGuards(t *testing.T) {
	d := Downloader{}
	ctx := context.Background()

	if _, err := d.SearchTracks(ctx, "", 10); err == nil {
		t.Fatal("expected empty query error")
	}
	if _, err := d.SearchTracks(ctx, "teya", 10); err == nil {
		t.Fatal("expected token error for SearchTracks")
	}
	if _, err := d.DownloadByURL(ctx, ""); err == nil {
		t.Fatal("expected empty url error")
	}
	if _, err := d.DownloadByURL(ctx, "https://music.yandex.ru/track/123"); err == nil {
		t.Fatal("expected token error for DownloadByURL")
	}

	d.Token = "x"
	if _, err := d.DownloadByURL(ctx, "https://example.org/track/123"); err == nil {
		t.Fatal("expected host validation error")
	}
}
