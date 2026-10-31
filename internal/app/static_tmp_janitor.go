package app

import (
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// StartStaticTmpJanitor removes generated temp artifacts from the publicly
// served static/tmp directory. Manual uploads and voice previews piled up there
// (several MB of user content) and were never cleaned.
func StartStaticTmpJanitor(interval, maxAge time.Duration) {
	if interval <= 0 {
		interval = time.Hour
	}
	if maxAge <= 0 {
		maxAge = 7 * 24 * time.Hour
	}
	dir := filepath.Join(strings.TrimSpace(envOr("WEB_STATIC_DIR", "./static")), "tmp")
	go func() {
		for {
			if removed := pruneStaticTmp(dir, maxAge); removed > 0 {
				log.Printf("static tmp janitor: removed %d stale files from %s", removed, dir)
			}
			time.Sleep(interval)
		}
	}()
}

// pruneStaticTmp deletes regular files older than maxAge and returns the count.
func pruneStaticTmp(dir string, maxAge time.Duration) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	cutoff := time.Now().Add(-maxAge)
	removed := 0
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		info, err := entry.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		if err := os.Remove(filepath.Join(dir, name)); err == nil {
			removed++
		}
	}
	return removed
}
