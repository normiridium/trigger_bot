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
	d := Downloader{AudioFormat: "flac", AudioQuality: "0", ExtractorArgs: "youtube:player_client=web"}
	if got := d.audioFormat(); got != "flac" {
		t.Fatalf("unexpected format: %q", got)
	}
	if got := d.audioQuality(); got != "0" {
		t.Fatalf("unexpected quality: %q", got)
	}
	if got := d.extractorArgs(); got != "youtube:player_client=web" {
		t.Fatalf("unexpected extractor args: %q", got)
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
