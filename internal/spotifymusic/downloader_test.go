package spotifymusic

import (
	"context"
	"strings"
	"testing"
)

func TestDownloaderDefaults(t *testing.T) {
	d := Downloader{}
	if got := d.audioFormat(); got != "mp3" {
		t.Fatalf("unexpected default format: %q", got)
	}
	if got := d.audioQuality(); got != "320K" {
		t.Fatalf("unexpected default quality: %q", got)
	}
	if got := d.extractorArgs(); got != "youtube:player_client=android,web" {
		t.Fatalf("unexpected default extractor args: %q", got)
	}
}

func TestDownloaderConfiguredValues(t *testing.T) {
	d := Downloader{
		AudioFormat:        "flac",
		AudioQuality:       "0",
		ExtractorArgs:      "youtube:player_client=web",
		CookiesFile:        "/tmp/cookies.txt",
		CookiesFromBrowser: "firefox",
	}
	if got := d.audioFormat(); got != "flac" {
		t.Fatalf("unexpected format: %q", got)
	}
	if got := d.audioQuality(); got != "0" {
		t.Fatalf("unexpected quality: %q", got)
	}
	if got := d.extractorArgs(); got != "youtube:player_client=web" {
		t.Fatalf("unexpected extractor args: %q", got)
	}
	if got := strings.Join(d.ytDLPAuthArgs(), " "); got != "--cookies-from-browser firefox" {
		t.Fatalf("unexpected auth args: %q", got)
	}
}

func TestDownloaderAuthArgsCookiesFile(t *testing.T) {
	d := Downloader{CookiesFile: "/tmp/cookies.txt"}
	if got := strings.Join(d.ytDLPAuthArgs(), " "); got != "--cookies /tmp/cookies.txt" {
		t.Fatalf("unexpected auth args: %q", got)
	}
}

func TestDownloaderExtractorArgCandidates(t *testing.T) {
	d := Downloader{ExtractorArgs: "youtube:player_client=web"}
	got := strings.Join(d.extractorArgCandidates(), " | ")
	if !strings.Contains(got, "youtube:player_client=web") {
		t.Fatalf("expected configured extractor args, got %q", got)
	}
	if !strings.Contains(got, "youtube:player_client=android") {
		t.Fatalf("expected android fallback, got %q", got)
	}
	if strings.Count(got, "youtube:player_client=web") != 1 {
		t.Fatalf("expected deduped candidates, got %q", got)
	}
}

func TestDownloadByQuery_EmptyQuery(t *testing.T) {
	d := Downloader{}
	_, err := d.DownloadByQuery(context.Background(), "  ")
	if err == nil || !strings.Contains(err.Error(), "empty query") {
		t.Fatalf("expected empty query error, got: %v", err)
	}
}

func TestDownloadByQuery_MissingBinary(t *testing.T) {
	d := Downloader{YTDLPBin: "/definitely/not/a/real/binary"}
	_, err := d.DownloadByQuery(context.Background(), "megapolis")
	if err == nil {
		t.Fatal("expected start error for missing binary")
	}
}
