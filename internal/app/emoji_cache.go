package app

import (
	"crypto/sha256"
	"encoding/hex"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"trigger-admin-bot/internal/bottmp"
)

// Emoji/sticker proxy responses are immutable for a given Telegram file ID,
// so they can be cached on disk instead of re-fetching (getFile + download)
// and re-converting (ffmpeg/lottie) on every admin-panel render.
const (
	emojiCacheTTL           = 30 * 24 * time.Hour
	emojiCacheJanitorMaxAge = 60 * 24 * time.Hour
)

// emojiCacheDir is the on-disk cache directory under the bot temp dir.
func emojiCacheDir() string {
	return filepath.Join(bottmp.BaseDir(), "emoji_cache")
}

// emojiCacheKey returns a filesystem-safe name for a prefix + file ID pair.
func emojiCacheKey(prefix, fileID string) string {
	sum := sha256.Sum256([]byte(fileID))
	return prefix + "-" + hex.EncodeToString(sum[:16])
}

// emojiCacheGet returns cached bytes and their stored content type, or ok=false
// when no fresh entry exists. The content type is stored alongside the bytes so
// cache hits reproduce the original Telegram Content-Type.
func emojiCacheGet(prefix, fileID string) (data []byte, ctype string, ok bool) {
	path := filepath.Join(emojiCacheDir(), emojiCacheKey(prefix, fileID))
	st, err := os.Stat(path)
	if err != nil || st.IsDir() {
		return nil, "", false
	}
	if time.Since(st.ModTime()) > emojiCacheTTL {
		_ = os.Remove(path)
		_ = os.Remove(path + ".ctype")
		return nil, "", false
	}
	data, err = os.ReadFile(path)
	if err != nil || len(data) == 0 {
		return nil, "", false
	}
	if raw, err := os.ReadFile(path + ".ctype"); err == nil {
		ctype = strings.TrimSpace(string(raw))
	}
	return data, ctype, true
}

// emojiCachePut writes bytes and their content type to the cache. Failures are
// non-fatal: the caller still has the bytes in memory and serves them, just
// without caching.
func emojiCachePut(prefix, fileID string, data []byte, ctype string) {
	if len(data) == 0 {
		return
	}
	dir := emojiCacheDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	path := filepath.Join(dir, emojiCacheKey(prefix, fileID))
	_ = os.WriteFile(path, data, 0o600)
	if strings.TrimSpace(ctype) != "" {
		_ = os.WriteFile(path+".ctype", []byte(strings.TrimSpace(ctype)), 0o600)
	}
}

// fileURLResolution caches file_id -> file_path from Telegram getFile so repeat
// preview/file requests don't re-hit getFile (a per-image network round-trip).
// The file_path is documented valid for ~1 hour, so entries expire after that.
const fileURLResolutionTTL = time.Hour

var fileURLResolution = struct {
	sync.RWMutex
	entries map[string]resolvedFilePath
}{entries: make(map[string]resolvedFilePath)}

type resolvedFilePath struct {
	filePath  string
	expiresAt time.Time
}

func cachedFilePath(fileID string) (string, bool) {
	fileURLResolution.RLock()
	defer fileURLResolution.RUnlock()
	e, ok := fileURLResolution.entries[fileID]
	if !ok || time.Now().After(e.expiresAt) {
		return "", false
	}
	return e.filePath, true
}

func cacheFilePath(fileID, filePath string) {
	fileURLResolution.Lock()
	defer fileURLResolution.Unlock()
	// Opportunistically evict expired entries to keep the map bounded.
	if len(fileURLResolution.entries) > 4096 {
		now := time.Now()
		for id, e := range fileURLResolution.entries {
			if now.After(e.expiresAt) {
				delete(fileURLResolution.entries, id)
			}
		}
	}
	fileURLResolution.entries[fileID] = resolvedFilePath{
		filePath:  filePath,
		expiresAt: time.Now().Add(fileURLResolutionTTL),
	}
}

// StartEmojiCacheJanitor removes stale cached files that the TTL-based read
// path left behind (e.g. after a bot token rotation invalidated them).
func StartEmojiCacheJanitor(interval, maxAge time.Duration) {
	if interval <= 0 {
		interval = time.Hour
	}
	if maxAge <= 0 {
		maxAge = emojiCacheJanitorMaxAge
	}
	dir := emojiCacheDir()
	go func() {
		for {
			if removed := pruneEmojiCache(dir, maxAge); removed > 0 {
				log.Printf("emoji cache janitor: removed %d stale files", removed)
			}
			time.Sleep(interval)
		}
	}()
}

func pruneEmojiCache(dir string, maxAge time.Duration) int {
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
		info, err := entry.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		if os.Remove(filepath.Join(dir, entry.Name())) == nil {
			removed++
		}
	}
	return removed
}
