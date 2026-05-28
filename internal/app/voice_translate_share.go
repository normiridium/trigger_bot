package app

import (
	crand "crypto/rand"
	"encoding/hex"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type voiceShareEntry struct {
	path      string
	publicRel string
	expiresAt time.Time
}

var (
	voiceShareMu   sync.Mutex
	voiceShareData = map[string]voiceShareEntry{}
)

func newVoiceShareToken() string {
	b := make([]byte, 16)
	if _, err := crand.Read(b); err != nil {
		return ""
	}
	return hex.EncodeToString(b)
}

func cleanupVoiceShareLocked(now time.Time) {
	for k, v := range voiceShareData {
		if !v.expiresAt.After(now) {
			_ = os.Remove(v.path)
			delete(voiceShareData, k)
		}
	}
}

func registerVoiceShareFile(localPath string, ttl time.Duration) string {
	if strings.TrimSpace(localPath) == "" {
		return ""
	}
	if ttl <= 0 {
		ttl = 20 * time.Minute
	}
	token := newVoiceShareToken()
	if token == "" {
		return ""
	}
	voiceShareMu.Lock()
	defer voiceShareMu.Unlock()
	cleanupVoiceShareLocked(time.Now())

	staticDir := strings.TrimSpace(envOr("WEB_STATIC_DIR", "./static"))
	tmpDir := filepath.Join(staticDir, "tmp")
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		return ""
	}
	ext := strings.ToLower(strings.TrimSpace(filepath.Ext(localPath)))
	if ext == "" {
		ext = ".bin"
	}
	pubName := token + ext
	dstPath := filepath.Join(tmpDir, pubName)
	src, err := os.Open(localPath)
	if err != nil {
		return ""
	}
	defer src.Close()
	dst, err := os.Create(dstPath)
	if err != nil {
		return ""
	}
	if _, err := io.Copy(dst, src); err != nil {
		_ = dst.Close()
		_ = os.Remove(dstPath)
		return ""
	}
	_ = dst.Close()
	voiceShareData[pubName] = voiceShareEntry{
		path:      dstPath,
		publicRel: "/trigger_bot/tmp/" + pubName,
		expiresAt: time.Now().Add(ttl),
	}
	return pubName
}

func resolveVoiceShareFile(token string) (string, bool) {
	voiceShareMu.Lock()
	defer voiceShareMu.Unlock()
	cleanupVoiceShareLocked(time.Now())
	v, ok := voiceShareData[token]
	if !ok {
		return "", false
	}
	if strings.TrimSpace(v.path) == "" {
		delete(voiceShareData, token)
		return "", false
	}
	if _, err := os.Stat(v.path); err != nil {
		delete(voiceShareData, token)
		return "", false
	}
	return v.path, true
}

func releaseVoiceShareFile(token string) {
	token = strings.TrimSpace(token)
	if token == "" {
		return
	}
	voiceShareMu.Lock()
	defer voiceShareMu.Unlock()
	v, ok := voiceShareData[token]
	if !ok {
		return
	}
	if strings.TrimSpace(v.path) != "" {
		_ = os.Remove(v.path)
	}
	delete(voiceShareData, token)
}

func buildVoiceSharePublicURL(token string) string {
	base := strings.TrimSpace(os.Getenv("VOICE_TRANSLATE_PUBLIC_BASE_URL"))
	if base == "" {
		return ""
	}
	base = strings.TrimRight(base, "/")
	voiceShareMu.Lock()
	defer voiceShareMu.Unlock()
	v, ok := voiceShareData[token]
	if !ok || strings.TrimSpace(v.publicRel) == "" {
		return ""
	}
	return base + v.publicRel
}

func (w *WebAdmin) voiceTranslateTempFile(rw http.ResponseWriter, r *http.Request) {
	if r == nil || rw == nil {
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(rw, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	token := strings.TrimSpace(strings.TrimPrefix(r.URL.Path, "/trigger_bot/tmp/"))
	if token == "" || strings.Contains(token, "/") {
		http.NotFound(rw, r)
		return
	}
	path, ok := resolveVoiceShareFile(token)
	if !ok {
		staticDir := strings.TrimSpace(envOr("WEB_STATIC_DIR", "./static"))
		candidate := filepath.Join(staticDir, "tmp", filepath.Base(token))
		if st, err := os.Stat(candidate); err == nil && !st.IsDir() {
			path = candidate
		} else {
			http.NotFound(rw, r)
			return
		}
	}
	rw.Header().Set("Cache-Control", "no-store")
	rw.Header().Set("X-Robots-Tag", "noindex, nofollow, noarchive")
	if ext := strings.ToLower(filepath.Ext(path)); ext == ".mp4" {
		rw.Header().Set("Content-Type", "video/mp4")
	} else if ext == ".mp3" {
		rw.Header().Set("Content-Type", "audio/mpeg")
	} else {
		rw.Header().Set("Content-Type", "application/octet-stream")
	}
	http.ServeFile(rw, r, path)
}
