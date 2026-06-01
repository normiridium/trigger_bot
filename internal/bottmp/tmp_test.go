package bottmp

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCleanupStaleRemovesOnlyOldKnownPrefixes(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(envName, dir)

	oldKnown := filepath.Join(dir, "media-audio-old")
	freshKnown := filepath.Join(dir, "media-video-fresh")
	oldUnknown := filepath.Join(dir, "someone-else-old")

	for _, path := range []string{oldKnown, freshKnown, oldUnknown} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", path, err)
		}
	}

	oldTime := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(oldKnown, oldTime, oldTime); err != nil {
		t.Fatalf("chtimes old known: %v", err)
	}
	if err := os.Chtimes(oldUnknown, oldTime, oldTime); err != nil {
		t.Fatalf("chtimes old unknown: %v", err)
	}

	removed, err := CleanupStale(24 * time.Hour)
	if err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if removed != 1 {
		t.Fatalf("expected 1 removed item, got %d", removed)
	}
	if _, err := os.Stat(oldKnown); !os.IsNotExist(err) {
		t.Fatalf("expected old known tmp to be removed, stat err=%v", err)
	}
	if _, err := os.Stat(freshKnown); err != nil {
		t.Fatalf("expected fresh known tmp to stay: %v", err)
	}
	if _, err := os.Stat(oldUnknown); err != nil {
		t.Fatalf("expected old unknown tmp to stay: %v", err)
	}
}
