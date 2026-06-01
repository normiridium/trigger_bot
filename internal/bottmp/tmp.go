package bottmp

import (
	"os"
	"path/filepath"
	"strings"
	"time"
)

const envName = "TRIGGER_BOT_TMP_DIR"

var stalePrefixes = []string{
	"media-audio-",
	"media-video-",
	"media-auto-",
	"spotify-audio-",
	"yandex-music-",
	"vk-audio-",
	"emoji-",
	"translate_mix_",
	"translate_subs_",
	"translate_subs_cache_",
	"translate_text_",
	"voice_src_",
	"voice_tr_",
	"voice_mix_",
	"vot_backend_",
	"vot_source_",
	"vot_src_audio_only_",
}

// BaseDir returns the root for bot runtime temp files.
// If TRIGGER_BOT_TMP_DIR is empty, Go's system temp dir is used for backwards compatibility.
func BaseDir() string {
	if v := strings.TrimSpace(os.Getenv(envName)); v != "" {
		return v
	}
	return os.TempDir()
}

func ensureBaseDir() (string, error) {
	dir := BaseDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}

func MkdirTemp(pattern string) (string, error) {
	dir, err := ensureBaseDir()
	if err != nil {
		return "", err
	}
	return os.MkdirTemp(dir, pattern)
}

func CreateTemp(pattern string) (*os.File, error) {
	dir, err := ensureBaseDir()
	if err != nil {
		return nil, err
	}
	return os.CreateTemp(dir, pattern)
}

func ChildDir(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return BaseDir()
	}
	return filepath.Join(BaseDir(), name)
}

func CleanupStale(maxAge time.Duration) (int, error) {
	if maxAge <= 0 {
		maxAge = 24 * time.Hour
	}
	dir := BaseDir()
	if strings.TrimSpace(dir) == "" {
		return 0, nil
	}
	cleanDir, err := filepath.Abs(dir)
	if err != nil {
		return 0, err
	}
	if cleanDir == string(filepath.Separator) {
		return 0, nil
	}
	entries, err := os.ReadDir(cleanDir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	now := time.Now()
	removed := 0
	for _, entry := range entries {
		name := strings.TrimSpace(entry.Name())
		if !hasStalePrefix(name) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if now.Sub(info.ModTime()) < maxAge {
			continue
		}
		if err := os.RemoveAll(filepath.Join(cleanDir, name)); err == nil {
			removed++
		}
	}
	return removed, nil
}

func hasStalePrefix(name string) bool {
	for _, prefix := range stalePrefixes {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}
