package app

import (
	"bytes"
	crand "crypto/rand"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

const (
	votBackendURLPrimary  = "https://vot.toil.cc/v1/video-translation/translate"
	votBackendURLFallback = "https://translate-backend.transly.eu.cc/v2/video-translation/translate"
	votBackendURLWorker   = "https://vot.toil-dump.workers.dev/video-translation/translate"
)

type voiceTranslateTask struct {
	Bot     *tgbotapi.BotAPI
	ChatID  int64
	ReplyTo int
	Msg     *tgbotapi.Message
	Action  string
	Media   replyMediaInfo
	SrcLang string
	ResLang string
}

type replyMediaInfo struct {
	FileID   string
	HasVideo bool
	Ext      string
	Name     string
}

type voiceTranslateQueue struct {
	ch chan voiceTranslateTask
}

type voiceTranslateOptionEntry struct {
	token     string
	chatID    int64
	userID    int64
	replyTo   int
	media     replyMediaInfo
	expiresAt time.Time
}

type votTranslateRequest struct {
	Provider string `json:"provider"`
	Service  string `json:"service"`
	VideoID  string `json:"video_id"`
	FromLang string `json:"from_lang"`
	ToLang   string `json:"to_lang"`
	RawVideo string `json:"raw_video"`
}

type votTranslateResponse struct {
	Status        string `json:"status"`
	TranslatedURL string `json:"translated_url"`
	RemainingTime int    `json:"remaining_time"`
	Message       string `json:"message"`
	ID            any    `json:"id"`
}

type votProviderResult struct {
	translatedURL string
	providerUsed  string
}

type voiceTranslateCacheEntry struct {
	mp3Path   string
	expiresAt time.Time
	createdAt time.Time
	provider  string
	baseName  string
}

type voiceTranslateCacheDiskEntry struct {
	Key       string    `json:"key"`
	MP3Path   string    `json:"mp3_path"`
	ExpiresAt time.Time `json:"expires_at"`
	CreatedAt time.Time `json:"created_at"`
	Provider  string    `json:"provider,omitempty"`
	BaseName  string    `json:"base_name,omitempty"`
}

var (
	voiceTranslateCacheMu sync.Mutex
	voiceTranslateCache   = map[string]voiceTranslateCacheEntry{}
	voiceCacheLoaded      bool

	voiceTranslateOptionMu    sync.Mutex
	voiceTranslateOptionData  = map[string]voiceTranslateOptionEntry{}
	voiceTranslateStartupOnce sync.Once
)

const voiceTranslateCacheIndexPath = "/home/appuser/trigger_admin_bot/static/tmp/voice_cache_index.json"

func voiceTranslateCacheTTL() time.Duration {
	sec := envInt("VOICE_TRANSLATE_CACHE_TTL_SEC", 3600)
	if sec < 60 {
		sec = 60
	}
	return time.Duration(sec) * time.Second
}

func buildVoiceTranslateCacheKey(fileID string) string {
	return buildVoiceTranslateCacheKeyWithLang(fileID, votLangFromEnv("VOICE_TRANSLATE_SRCLANG", "en"), votLangFromEnv("VOICE_TRANSLATE_RESLANG", "ru"))
}

func buildVoiceTranslateCacheKeyWithLang(fileID, srcLang, resLang string) string {
	if v := normalizeVOTLang(srcLang); v != "" {
		srcLang = v
	} else {
		srcLang = "en"
	}
	if v := normalizeVOTLang(resLang); v != "" {
		resLang = v
	} else {
		resLang = "ru"
	}
	provider := strings.ToLower(strings.TrimSpace(os.Getenv("VOICE_TRANSLATE_PROVIDER")))
	if provider == "" {
		provider = "yandex"
	}
	return strings.TrimSpace(fileID) + "|" + srcLang + "|" + resLang + "|" + provider
}

func loadVoiceTranslateCacheLocked() {
	if voiceCacheLoaded {
		return
	}
	voiceCacheLoaded = true
	raw, err := os.ReadFile(voiceTranslateCacheIndexPath)
	if err != nil || len(raw) == 0 {
		return
	}
	var rows []voiceTranslateCacheDiskEntry
	if err := json.Unmarshal(raw, &rows); err != nil {
		return
	}
	now := time.Now()
	for _, r := range rows {
		key := strings.TrimSpace(r.Key)
		path := strings.TrimSpace(r.MP3Path)
		if key == "" || !r.ExpiresAt.After(now) {
			continue
		}
		if path != "" {
			if _, statErr := os.Stat(path); statErr != nil {
				path = ""
			}
		}
		voiceTranslateCache[key] = voiceTranslateCacheEntry{
			mp3Path:   path,
			expiresAt: r.ExpiresAt,
			createdAt: r.CreatedAt,
			provider:  strings.TrimSpace(r.Provider),
			baseName:  strings.TrimSpace(r.BaseName),
		}
	}
}

func saveVoiceTranslateCacheLocked() {
	rows := make([]voiceTranslateCacheDiskEntry, 0, len(voiceTranslateCache))
	for k, v := range voiceTranslateCache {
		if strings.TrimSpace(k) == "" {
			continue
		}
		rows = append(rows, voiceTranslateCacheDiskEntry{
			Key:       k,
			MP3Path:   v.mp3Path,
			ExpiresAt: v.expiresAt,
			CreatedAt: v.createdAt,
			Provider:  strings.TrimSpace(v.provider),
			BaseName:  strings.TrimSpace(v.baseName),
		})
	}
	_ = os.MkdirAll(filepath.Dir(voiceTranslateCacheIndexPath), 0o755)
	tmp := voiceTranslateCacheIndexPath + ".tmp"
	raw, err := json.Marshal(rows)
	if err != nil {
		return
	}
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return
	}
	_ = os.Rename(tmp, voiceTranslateCacheIndexPath)
}

func getVoiceTranslateCache(key string) (string, bool) {
	now := time.Now()
	voiceTranslateCacheMu.Lock()
	defer voiceTranslateCacheMu.Unlock()
	loadVoiceTranslateCacheLocked()
	dirty := false
	for k, v := range voiceTranslateCache {
		if !v.expiresAt.After(now) {
			delete(voiceTranslateCache, k)
			dirty = true
			if strings.TrimSpace(v.mp3Path) != "" {
				_ = os.Remove(v.mp3Path)
			}
		}
	}
	if dirty {
		saveVoiceTranslateCacheLocked()
	}
	v, ok := voiceTranslateCache[key]
	if !ok {
		return "", false
	}
	if strings.TrimSpace(v.mp3Path) == "" {
		return "", false
	}
	if _, err := os.Stat(v.mp3Path); err != nil {
		delete(voiceTranslateCache, key)
		saveVoiceTranslateCacheLocked()
		return "", false
	}
	return v.mp3Path, true
}

func setVoiceTranslateCache(key, mp3Path, provider string) {
	if strings.TrimSpace(key) == "" || strings.TrimSpace(mp3Path) == "" {
		return
	}
	voiceTranslateCacheMu.Lock()
	defer voiceTranslateCacheMu.Unlock()
	loadVoiceTranslateCacheLocked()
	voiceTranslateCache[key] = voiceTranslateCacheEntry{
		mp3Path:   mp3Path,
		createdAt: time.Now(),
		expiresAt: time.Now().Add(voiceTranslateCacheTTL()),
		provider:  strings.TrimSpace(provider),
		baseName:  strings.TrimSpace(voiceTranslateCache[key].baseName),
	}
	saveVoiceTranslateCacheLocked()
}

func getVoiceCacheBaseName(key string) string {
	k := strings.TrimSpace(key)
	if k == "" {
		return ""
	}
	voiceTranslateCacheMu.Lock()
	defer voiceTranslateCacheMu.Unlock()
	loadVoiceTranslateCacheLocked()
	return strings.TrimSpace(voiceTranslateCache[k].baseName)
}

func setVoiceCacheBaseName(key, baseName string) {
	k := strings.TrimSpace(key)
	if k == "" {
		return
	}
	base := sanitizeVoiceBaseName(baseName)
	voiceTranslateCacheMu.Lock()
	defer voiceTranslateCacheMu.Unlock()
	loadVoiceTranslateCacheLocked()
	v := voiceTranslateCache[k]
	v.baseName = base
	if v.createdAt.IsZero() {
		v.createdAt = time.Now()
	}
	if v.expiresAt.Before(time.Now()) {
		v.expiresAt = time.Now().Add(voiceTranslateCacheTTL())
	}
	voiceTranslateCache[k] = v
	saveVoiceTranslateCacheLocked()
}

func voiceSourceCachePath(fileID string) string {
	id := strings.TrimSpace(fileID)
	if id == "" {
		return ""
	}
	sum := sha1.Sum([]byte(id))
	return filepath.Join(filepath.Dir(voiceTranslateCacheIndexPath), "voice_vot_src_cache_"+fmt.Sprintf("%x", sum)+".mp4")
}

func getVoiceSourceCache(fileID string) (string, bool) {
	path := voiceSourceCachePath(fileID)
	if strings.TrimSpace(path) == "" {
		return "", false
	}
	st, err := os.Stat(path)
	if err != nil || st == nil || st.Size() <= 0 {
		return "", false
	}
	if time.Since(st.ModTime()) > voiceTranslateCacheTTL() {
		_ = os.Remove(path)
		return "", false
	}
	return path, true
}

func setVoiceSourceCache(fileID, srcPath string) (string, error) {
	dst := voiceSourceCachePath(fileID)
	if strings.TrimSpace(dst) == "" || strings.TrimSpace(srcPath) == "" {
		return "", fmt.Errorf("invalid source cache args")
	}
	in, err := os.Open(srcPath)
	if err != nil {
		return "", err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", err
	}
	tmp := dst + ".tmp"
	out, err := os.Create(tmp)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		_ = os.Remove(tmp)
		return "", err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	return dst, nil
}

func voiceSubtitlesCachePath(cacheKey string) string {
	k := strings.TrimSpace(cacheKey)
	if k == "" {
		return ""
	}
	sum := sha1.Sum([]byte(k))
	return filepath.Join(filepath.Dir(voiceTranslateCacheIndexPath), "voice_subs_cache_"+fmt.Sprintf("%x", sum)+".srt")
}

func voiceTextCachePath(cacheKey string) string {
	k := strings.TrimSpace(cacheKey)
	if k == "" {
		return ""
	}
	sum := sha1.Sum([]byte(k))
	return filepath.Join(filepath.Dir(voiceTranslateCacheIndexPath), "voice_text_cache_"+fmt.Sprintf("%x", sum)+".txt")
}

func getFreshFile(path string) (string, bool) {
	p := strings.TrimSpace(path)
	if p == "" {
		return "", false
	}
	st, err := os.Stat(p)
	if err != nil || st == nil || st.Size() <= 0 {
		return "", false
	}
	if time.Since(st.ModTime()) > voiceTranslateCacheTTL() {
		_ = os.Remove(p)
		return "", false
	}
	return p, true
}

func saveCacheFile(dstPath, srcPath string) {
	if strings.TrimSpace(dstPath) == "" || strings.TrimSpace(srcPath) == "" {
		return
	}
	in, err := os.Open(srcPath)
	if err != nil {
		return
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dstPath), 0o755); err != nil {
		return
	}
	tmp := dstPath + ".tmp"
	out, err := os.Create(tmp)
	if err != nil {
		return
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		_ = os.Remove(tmp)
		return
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmp)
		return
	}
	_ = os.Rename(tmp, dstPath)
}

func isHexTokenName(name string) bool {
	if len(name) != 32 {
		return false
	}
	for _, r := range name {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

func cleanupVoiceTranslateStartup() {
	voiceTranslateStartupOnce.Do(func() {
		now := time.Now()
		maxAgeSec := envInt("VOICE_TRANSLATE_TMP_MAX_AGE_SEC", int(voiceTranslateCacheTTL().Seconds()))
		if maxAgeSec < 300 {
			maxAgeSec = 300
		}
		maxAge := time.Duration(maxAgeSec) * time.Second
		tmpDir := filepath.Dir(voiceTranslateCacheIndexPath)

		keepCacheFiles := map[string]struct{}{}
		voiceTranslateCacheMu.Lock()
		loadVoiceTranslateCacheLocked()
		dirty := false
		for k, v := range voiceTranslateCache {
			path := strings.TrimSpace(v.mp3Path)
			if !v.expiresAt.After(now) {
				delete(voiceTranslateCache, k)
				dirty = true
				if path != "" {
					_ = os.Remove(path)
				}
				continue
			}
			if path == "" {
				delete(voiceTranslateCache, k)
				dirty = true
				continue
			}
			if _, err := os.Stat(path); err != nil {
				delete(voiceTranslateCache, k)
				dirty = true
				continue
			}
			keepCacheFiles[path] = struct{}{}
		}
		if dirty {
			saveVoiceTranslateCacheLocked()
		}
		voiceTranslateCacheMu.Unlock()

		entries, err := os.ReadDir(tmpDir)
		if err != nil {
			return
		}
		removed := 0
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			name := strings.TrimSpace(e.Name())
			if name == "" || name == "voice_cache_index.json" || name == "bot.log" {
				continue
			}
			path := filepath.Join(tmpDir, name)
			info, infoErr := e.Info()
			if infoErr != nil {
				continue
			}
			if now.Sub(info.ModTime()) < maxAge {
				continue
			}
			shouldRemove := false
			switch {
			case strings.HasPrefix(name, "voice_src_"),
				strings.HasPrefix(name, "voice_tr_"),
				strings.HasPrefix(name, "voice_mix_"),
				strings.HasPrefix(name, "voice_vot_src_cache_"),
				strings.HasPrefix(name, "vot_test_"),
				strings.HasPrefix(name, "vot_probe_"),
				strings.HasPrefix(name, "vot_cli_"),
				strings.HasPrefix(name, "vot_source_"):
				shouldRemove = true
			case strings.HasPrefix(name, "voice_cache_") && strings.HasSuffix(strings.ToLower(name), ".mp3"):
				if _, ok := keepCacheFiles[path]; !ok {
					shouldRemove = true
				}
			default:
				ext := strings.ToLower(filepath.Ext(name))
				base := strings.TrimSuffix(name, ext)
				if (ext == ".mp3" || ext == ".mp4" || ext == ".bin") && isHexTokenName(base) {
					shouldRemove = true
				}
			}
			if shouldRemove {
				if err := os.Remove(path); err == nil {
					removed++
				}
			}
		}
		if debugTriggerLogEnabled && removed > 0 {
			log.Printf("voice translate startup cleanup removed=%d max_age=%s dir=%s", removed, maxAge, tmpDir)
		}
	})
}

func runVOTCLITranslateLocal(sourcePath, outputDir, outputFile, srcLang, resLang string) (string, error) {
	bin := strings.TrimSpace(os.Getenv("VOT_CLI_BIN"))
	if bin == "" {
		bin = "/home/appuser/.nvm/versions/node/v20.20.2/bin/vot-cli"
	}
	nodeBin := strings.TrimSpace(os.Getenv("VOICE_TRANSLATE_NODE_BIN"))
	if nodeBin == "" {
		nodeBin = "/home/appuser/.nvm/versions/node/v20.20.2/bin/node"
	}
	useNodeWrapper := false
	if _, err := os.Stat(nodeBin); err == nil {
		if _, err2 := os.Stat(bin); err2 == nil {
			useNodeWrapper = true
		}
	}
	if !useNodeWrapper {
		if _, err := os.Stat(bin); err != nil {
			bin = "vot-cli"
		}
		if _, err := exec.LookPath(bin); err != nil {
			return "", err
		}
	}
	args := []string{
		"--output=" + outputDir,
		"--output-file=" + outputFile,
	}
	if v := normalizeVOTLang(resLang); v != "" {
		args = append(args, "--reslang="+v)
	}
	if v := normalizeVOTLang(srcLang); v != "" && v != "auto" {
		args = append(args, "--lang="+v)
	}
	args = append(args, sourcePath)
	var cmd *exec.Cmd
	if useNodeWrapper {
		nodeArgs := append([]string{bin}, args...)
		cmd = exec.Command(nodeBin, nodeArgs...)
	} else {
		cmd = exec.Command(bin, args...)
	}
	cmd.Env = append(os.Environ(),
		"PATH=/home/appuser/.nvm/versions/node/v20.20.2/bin:"+os.Getenv("PATH"),
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("vot-cli failed: %v (%s)", err, clipText(strings.TrimSpace(string(out)), 500))
	}

	// vot-cli output extension may vary by version/config; prefer explicit name, then discover.
	candidates := []string{
		filepath.Join(outputDir, outputFile+".mp3"),
		filepath.Join(outputDir, outputFile+".m4a"),
		filepath.Join(outputDir, outputFile+".wav"),
		filepath.Join(outputDir, outputFile+".ogg"),
	}
	for _, p := range candidates {
		if st, statErr := os.Stat(p); statErr == nil && st.Size() > 0 {
			return p, nil
		}
	}
	entries, readErr := os.ReadDir(outputDir)
	if readErr == nil {
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			name := strings.ToLower(strings.TrimSpace(e.Name()))
			if strings.HasSuffix(name, ".mp3") || strings.HasSuffix(name, ".m4a") || strings.HasSuffix(name, ".wav") || strings.HasSuffix(name, ".ogg") {
				p := filepath.Join(outputDir, e.Name())
				if st, statErr := os.Stat(p); statErr == nil && st.Size() > 0 {
					return p, nil
				}
			}
		}
	}
	_ = filepath.WalkDir(outputDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d == nil || d.IsDir() {
			return nil
		}
		name := strings.ToLower(strings.TrimSpace(d.Name()))
		if strings.HasSuffix(name, ".mp3") || strings.HasSuffix(name, ".m4a") || strings.HasSuffix(name, ".wav") || strings.HasSuffix(name, ".ogg") {
			if st, statErr := os.Stat(path); statErr == nil && st.Size() > 0 {
				candidates = append(candidates, path)
			}
		}
		return nil
	})
	for _, p := range candidates {
		if st, statErr := os.Stat(p); statErr == nil && st.Size() > 0 {
			return p, nil
		}
	}
	return "", fmt.Errorf("vot-cli output missing after success (%s)", clipText(strings.TrimSpace(string(out)), 500))
}

func runVOTCLISubtitlesLocal(sourcePath, outputDir, outputFile, srcLang, resLang, outFormat string) (string, error) {
	bin := strings.TrimSpace(os.Getenv("VOT_CLI_BIN"))
	if bin == "" {
		bin = "/home/appuser/.nvm/versions/node/v20.20.2/bin/vot-cli"
	}
	nodeBin := strings.TrimSpace(os.Getenv("VOICE_TRANSLATE_NODE_BIN"))
	if nodeBin == "" {
		nodeBin = "/home/appuser/.nvm/versions/node/v20.20.2/bin/node"
	}
	useNodeWrapper := false
	if _, err := os.Stat(nodeBin); err == nil {
		if _, err2 := os.Stat(bin); err2 == nil {
			useNodeWrapper = true
		}
	}
	if !useNodeWrapper {
		if _, err := os.Stat(bin); err != nil {
			bin = "vot-cli"
		}
		if _, err := exec.LookPath(bin); err != nil {
			return "", err
		}
	}
	subsArg := "--subs"
	ext := ".json"
	if strings.EqualFold(strings.TrimSpace(outFormat), "srt") {
		subsArg = "--subs-srt"
		ext = ".srt"
	}
	args := []string{
		subsArg,
		"--output=" + outputDir,
		"--output-file=" + outputFile,
	}
	if v := normalizeVOTLang(resLang); v != "" {
		args = append(args, "--reslang="+v)
	}
	if v := normalizeVOTLang(srcLang); v != "" && v != "auto" {
		args = append(args, "--lang="+v)
	}
	args = append(args, sourcePath)
	var cmd *exec.Cmd
	if useNodeWrapper {
		nodeArgs := append([]string{bin}, args...)
		cmd = exec.Command(nodeBin, nodeArgs...)
	} else {
		cmd = exec.Command(bin, args...)
	}
	cmd.Env = append(os.Environ(),
		"PATH=/home/appuser/.nvm/versions/node/v20.20.2/bin:"+os.Getenv("PATH"),
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("vot-cli subs failed: %v (%s)", err, clipText(strings.TrimSpace(string(out)), 500))
	}

	candidates := []string{
		filepath.Join(outputDir, outputFile+ext),
		filepath.Join(outputDir, outputFile+".json"),
		filepath.Join(outputDir, outputFile+".srt"),
		filepath.Join(outputDir, outputFile+".vtt"),
	}
	for _, p := range candidates {
		if st, statErr := os.Stat(p); statErr == nil && st.Size() > 0 {
			return p, nil
		}
	}
	entries, readErr := os.ReadDir(outputDir)
	if readErr == nil {
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			name := strings.ToLower(strings.TrimSpace(e.Name()))
			if strings.HasSuffix(name, ".srt") || strings.HasSuffix(name, ".vtt") || strings.HasSuffix(name, ".json") {
				p := filepath.Join(outputDir, e.Name())
				if st, statErr := os.Stat(p); statErr == nil && st.Size() > 0 {
					return p, nil
				}
			}
		}
	}
	return "", fmt.Errorf("vot-cli subtitles output missing after success (%s)", clipText(strings.TrimSpace(string(out)), 500))
}

func subtitlesToPlainText(path string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	if strings.HasSuffix(strings.ToLower(strings.TrimSpace(path)), ".json") {
		type subtitleItem struct {
			Text string `json:"text"`
		}
		var wrap struct {
			Subtitles []subtitleItem `json:"subtitles"`
		}
		if err := json.Unmarshal(raw, &wrap); err == nil && len(wrap.Subtitles) > 0 {
			lines := make([]string, 0, len(wrap.Subtitles))
			for _, it := range wrap.Subtitles {
				t := strings.TrimSpace(it.Text)
				if t != "" {
					lines = append(lines, t)
				}
			}
			return strings.TrimSpace(strings.Join(lines, "\n"))
		}
		var arr []subtitleItem
		if err := json.Unmarshal(raw, &arr); err == nil && len(arr) > 0 {
			lines := make([]string, 0, len(arr))
			for _, it := range arr {
				t := strings.TrimSpace(it.Text)
				if t != "" {
					lines = append(lines, t)
				}
			}
			return strings.TrimSpace(strings.Join(lines, "\n"))
		}
	}
	s := strings.ReplaceAll(string(raw), "\r\n", "\n")
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		t := strings.TrimSpace(line)
		if t == "" {
			continue
		}
		if strings.Contains(t, "-->") {
			continue
		}
		isNum := true
		for _, r := range t {
			if r < '0' || r > '9' {
				isNum = false
				break
			}
		}
		if isNum {
			continue
		}
		if strings.HasPrefix(t, "WEBVTT") {
			continue
		}
		out = append(out, t)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

func subtitlesJSONToSRT(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	type subtitleItem struct {
		Text       string `json:"text"`
		StartMS    int64  `json:"startMs"`
		DurationMS int64  `json:"durationMs"`
	}
	items := make([]subtitleItem, 0)
	var wrap struct {
		Subtitles []subtitleItem `json:"subtitles"`
	}
	if err := json.Unmarshal(raw, &wrap); err == nil && len(wrap.Subtitles) > 0 {
		items = wrap.Subtitles
	} else {
		var arr []subtitleItem
		if err2 := json.Unmarshal(raw, &arr); err2 != nil || len(arr) == 0 {
			return "", fmt.Errorf("invalid subtitles json")
		}
		items = arr
	}
	var b strings.Builder
	for i, it := range items {
		start := float64(it.StartMS) / 1000.0
		end := float64(it.StartMS+it.DurationMS) / 1000.0
		if end < start {
			end = start
		}
		b.WriteString(strconv.Itoa(i + 1))
		b.WriteString("\n")
		b.WriteString(fmtSRTTime(start))
		b.WriteString(" --> ")
		b.WriteString(fmtSRTTime(end))
		b.WriteString("\n")
		b.WriteString(strings.TrimSpace(it.Text))
		b.WriteString("\n\n")
	}
	out := strings.TrimSpace(b.String())
	if out == "" {
		return "", fmt.Errorf("empty subtitles")
	}
	return out + "\n", nil
}

func cleanupVoiceTranslateOptionsLocked(now time.Time) {
	for k, v := range voiceTranslateOptionData {
		if v.expiresAt.After(now) {
			continue
		}
		delete(voiceTranslateOptionData, k)
	}
}

func newVoiceTranslateOptionToken() string {
	var b [6]byte
	_, _ = crand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func putVoiceTranslateOption(e voiceTranslateOptionEntry) string {
	voiceTranslateOptionMu.Lock()
	defer voiceTranslateOptionMu.Unlock()
	cleanupVoiceTranslateOptionsLocked(time.Now())
	token := newVoiceTranslateOptionToken()
	e.token = token
	if e.expiresAt.IsZero() {
		e.expiresAt = time.Now().Add(2 * time.Hour)
	}
	voiceTranslateOptionData[token] = e
	return token
}

func takeVoiceTranslateOption(token string, userID int64) (voiceTranslateOptionEntry, bool, string) {
	voiceTranslateOptionMu.Lock()
	defer voiceTranslateOptionMu.Unlock()
	cleanupVoiceTranslateOptionsLocked(time.Now())
	v, ok := voiceTranslateOptionData[token]
	if !ok {
		return voiceTranslateOptionEntry{}, false, "меню устарело"
	}
	if v.userID != 0 && userID != 0 && v.userID != userID {
		return voiceTranslateOptionEntry{}, false, "эта кнопка доступна только автору"
	}
	return v, true, ""
}

func renderVoiceTranslateOptionKeyboard(token string) tgbotapi.InlineKeyboardMarkup {
	return tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("Скачать аудио", "vtr|audio|"+token),
			tgbotapi.NewInlineKeyboardButtonData("Скачать микс", "vtr|mix|"+token),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("Перевод текст", "vtr|text|"+token),
			tgbotapi.NewInlineKeyboardButtonData("Перевод субтитры", "vtr|subs|"+token),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("Отмена", "vtr|cancel|"+token),
		),
	)
}

type voiceSourceLang struct {
	Code  string
	Label string
}

var voiceTranslateSourceLangs = []voiceSourceLang{
	{Code: "ru", Label: "🇷🇺 Русский"},
	{Code: "en", Label: "🇺🇸 English"},
	{Code: "es", Label: "🇪🇸 Español"},
	{Code: "de", Label: "🇩🇪 Deutsch"},
	{Code: "fr", Label: "🇫🇷 Français"},
	{Code: "it", Label: "🇮🇹 Italiano"},
	{Code: "ja", Label: "🇯🇵 日本語"},
	{Code: "ko", Label: "🇰🇷 한국어"},
	{Code: "zh", Label: "🇨🇳 中文"},
	{Code: "ar", Label: "🇸🇦 العربية"},
	{Code: "kk", Label: "🇰🇿 Қазақша"},
	{Code: "lt", Label: "🇱🇹 Lietuvių"},
	{Code: "lv", Label: "🇱🇻 Latviešu"},
}

func renderVoiceTranslateLangKeyboard(token, action string) tgbotapi.InlineKeyboardMarkup {
	target := normalizeVOTLang(votLangFromEnv("VOICE_TRANSLATE_RESLANG", "ru"))
	if target == "" || target == "auto" {
		target = "ru"
	}
	rows := make([][]tgbotapi.InlineKeyboardButton, 0, 5)
	row := []tgbotapi.InlineKeyboardButton{
		tgbotapi.NewInlineKeyboardButtonData("🌐 Авто", "vtr|lang|"+action+"|"+token+"|auto"),
	}
	rows = append(rows, row)
	row = make([]tgbotapi.InlineKeyboardButton, 0, 4)
	for i, l := range voiceTranslateSourceLangs {
		row = append(row, tgbotapi.NewInlineKeyboardButtonData(l.Label, "vtr|lang|"+action+"|"+token+"|"+l.Code))
		if len(row) == 4 || i == len(voiceTranslateSourceLangs)-1 {
			rows = append(rows, row)
			row = make([]tgbotapi.InlineKeyboardButton, 0, 4)
		}
	}
	rows = append(rows, tgbotapi.NewInlineKeyboardRow(
		tgbotapi.NewInlineKeyboardButtonData("🎯 Целевой: "+strings.ToUpper(target), "vtr|noop|"+token),
	))
	rows = append(rows, tgbotapi.NewInlineKeyboardRow(
		tgbotapi.NewInlineKeyboardButtonData("⬅️ Назад", "vtr|back|"+token),
		tgbotapi.NewInlineKeyboardButtonData("Отмена", "vtr|cancel|"+token),
	))
	return tgbotapi.NewInlineKeyboardMarkup(rows...)
}

func audioDurationSec(path string) float64 {
	cmd := exec.Command("ffprobe", "-v", "error", "-show_entries", "format=duration", "-of", "default=nokey=1:noprint_wrappers=1", path)
	out, err := cmd.Output()
	if err != nil {
		return 0
	}
	val := strings.TrimSpace(string(out))
	if val == "" {
		return 0
	}
	f, _ := strconv.ParseFloat(val, 64)
	return f
}

func fmtSRTTime(sec float64) string {
	if sec < 0 {
		sec = 0
	}
	ms := int64(sec * 1000)
	h := ms / 3600000
	m := (ms % 3600000) / 60000
	s := (ms % 60000) / 1000
	z := ms % 1000
	return fmt.Sprintf("%02d:%02d:%02d,%03d", h, m, s, z)
}

func handleVoiceTranslateOptionCallback(bot *tgbotapi.BotAPI, cb *tgbotapi.CallbackQuery, q *voiceTranslateQueue) bool {
	if bot == nil || cb == nil || q == nil || !strings.HasPrefix(cb.Data, "vtr|") {
		return false
	}
	parts := strings.Split(cb.Data, "|")
	if len(parts) < 2 {
		_, _ = bot.Request(tgbotapi.NewCallback(cb.ID, "неверная кнопка"))
		return true
	}
	action := strings.TrimSpace(parts[1])
	token := ""
	switch action {
	case "lang":
		if len(parts) >= 4 {
			token = strings.TrimSpace(parts[3])
		}
	default:
		if len(parts) >= 3 {
			token = strings.TrimSpace(parts[2])
		}
	}
	if token == "" {
		_, _ = bot.Request(tgbotapi.NewCallback(cb.ID, "неверная кнопка"))
		return true
	}
	entry, ok, msg := takeVoiceTranslateOption(token, cb.From.ID)
	if !ok {
		_, _ = bot.Request(tgbotapi.NewCallback(cb.ID, msg))
		return true
	}

	switch action {
	case "cancel":
		_, _ = bot.Request(tgbotapi.NewCallback(cb.ID, "Отменено"))
		if cb.Message != nil {
			_, _ = bot.Request(tgbotapi.DeleteMessageConfig{
				ChatID:    cb.Message.Chat.ID,
				MessageID: cb.Message.MessageID,
			})
		}
		return true
	case "audio", "mix", "text", "subs":
		if cb.Message != nil {
			edit := tgbotapi.NewEditMessageTextAndMarkup(
				cb.Message.Chat.ID,
				cb.Message.MessageID,
				"Выберите язык перевода:",
				renderVoiceTranslateLangKeyboard(token, action),
			)
			if _, err := bot.Send(edit); err != nil {
				_, _ = bot.Request(tgbotapi.NewCallback(cb.ID, "не удалось показать языки"))
				return true
			}
		}
		_, _ = bot.Request(tgbotapi.NewCallback(cb.ID, "Выберите язык"))
		return true
	case "back":
		if cb.Message != nil {
			edit := tgbotapi.NewEditMessageTextAndMarkup(
				cb.Message.Chat.ID,
				cb.Message.MessageID,
				"Действия с переводом:",
				renderVoiceTranslateOptionKeyboard(token),
			)
			_, _ = bot.Send(edit)
		}
		_, _ = bot.Request(tgbotapi.NewCallback(cb.ID, "Назад"))
		return true
	case "noop":
		_, _ = bot.Request(tgbotapi.NewCallback(cb.ID, ""))
		return true
	case "lang":
		if len(parts) != 5 && len(parts) != 6 {
			_, _ = bot.Request(tgbotapi.NewCallback(cb.ID, "неверный язык"))
			return true
		}
		runAction := strings.TrimSpace(parts[2])
		srcLang := normalizeVOTLang(strings.TrimSpace(parts[4]))
		if srcLang == "" {
			srcLang = "auto"
		}
		resLang := normalizeVOTLang(votLangFromEnv("VOICE_TRANSLATE_RESLANG", "ru"))
		if resLang == "" || resLang == "auto" {
			resLang = "ru"
		}
		_, _ = bot.Request(tgbotapi.NewCallback(cb.ID, "Запускаю..."))
		task := voiceTranslateTask{
			Bot:     bot,
			ChatID:  entry.chatID,
			ReplyTo: entry.replyTo,
			Action:  runAction,
			Media:   entry.media,
			SrcLang: srcLang,
			ResLang: resLang,
		}
		if !q.enqueue(task) {
			reply(sendContext{Bot: bot, ChatID: entry.chatID, ReplyTo: entry.replyTo}, "Очередь голосового перевода переполнена, попробуйте чуть позже.", false)
			return true
		}
		if cb.Message != nil {
			_, _ = bot.Request(tgbotapi.DeleteMessageConfig{
				ChatID:    cb.Message.Chat.ID,
				MessageID: cb.Message.MessageID,
			})
		}
		return true
	default:
		_, _ = bot.Request(tgbotapi.NewCallback(cb.ID, "неизвестное действие"))
		return true
	}
}

func normalizeVOTLang(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	if v == "" {
		return ""
	}
	if v == "auto" {
		return "auto"
	}
	if len(v) > 2 {
		parts := strings.FieldsFunc(v, func(r rune) bool {
			return r == '-' || r == '_' || r == ';' || r == ','
		})
		if len(parts) > 0 {
			v = parts[0]
		}
	}
	if len(v) >= 2 {
		return v[:2]
	}
	return ""
}

func newVoiceTranslateQueue(workers, size int) *voiceTranslateQueue {
	cleanupVoiceTranslateStartup()
	if workers < 1 {
		workers = 1
	}
	if size < 1 {
		size = workers * 2
	}
	q := &voiceTranslateQueue{ch: make(chan voiceTranslateTask, size)}
	for i := 0; i < workers; i++ {
		go func() {
			for task := range q.ch {
				processVoiceTranslateTask(task)
			}
		}()
	}
	return q
}

func (q *voiceTranslateQueue) enqueue(task voiceTranslateTask) bool {
	if q == nil {
		return false
	}
	select {
	case q.ch <- task:
		return true
	default:
		return false
	}
}

func detectMediaInMessage(src *tgbotapi.Message) (info replyMediaInfo, sizeBytes int64, ok bool) {
	if src == nil {
		return replyMediaInfo{}, 0, false
	}
	if src.Voice != nil && strings.TrimSpace(src.Voice.FileID) != "" {
		return replyMediaInfo{FileID: strings.TrimSpace(src.Voice.FileID), HasVideo: false, Ext: ".ogg", Name: "voice"}, int64(src.Voice.FileSize), true
	}
	if src.Audio != nil && strings.TrimSpace(src.Audio.FileID) != "" {
		ext := ".m4a"
		mime := strings.ToLower(strings.TrimSpace(src.Audio.MimeType))
		if strings.Contains(mime, "mpeg") || strings.Contains(mime, "mp3") {
			ext = ".mp3"
		} else if strings.Contains(mime, "ogg") || strings.Contains(mime, "opus") {
			ext = ".ogg"
		}
		name := strings.TrimSpace(src.Audio.FileName)
		if name == "" {
			name = strings.TrimSpace(src.Audio.Title)
		}
		return replyMediaInfo{FileID: strings.TrimSpace(src.Audio.FileID), HasVideo: false, Ext: ext, Name: name}, int64(src.Audio.FileSize), true
	}
	if src.Video != nil && strings.TrimSpace(src.Video.FileID) != "" {
		return replyMediaInfo{FileID: strings.TrimSpace(src.Video.FileID), HasVideo: true, Ext: ".mp4", Name: strings.TrimSpace(src.Video.FileName)}, int64(src.Video.FileSize), true
	}
	if src.VideoNote != nil && strings.TrimSpace(src.VideoNote.FileID) != "" {
		return replyMediaInfo{FileID: strings.TrimSpace(src.VideoNote.FileID), HasVideo: true, Ext: ".mp4", Name: "video_note"}, int64(src.VideoNote.FileSize), true
	}
	if src.Document != nil && strings.TrimSpace(src.Document.FileID) != "" {
		mime := strings.ToLower(strings.TrimSpace(src.Document.MimeType))
		if strings.HasPrefix(mime, "video/") || strings.HasPrefix(mime, "audio/") {
			ext := ".bin"
			if strings.Contains(mime, "mp4") {
				ext = ".mp4"
			} else if strings.Contains(mime, "mpeg") || strings.Contains(mime, "mp3") {
				ext = ".mp3"
			} else if strings.Contains(mime, "ogg") || strings.Contains(mime, "opus") {
				ext = ".ogg"
			} else if strings.Contains(mime, "wav") {
				ext = ".wav"
			} else if strings.Contains(mime, "webm") {
				ext = ".webm"
			}
			return replyMediaInfo{
				FileID:   strings.TrimSpace(src.Document.FileID),
				HasVideo: strings.HasPrefix(mime, "video/"),
				Ext:      ext,
				Name:     strings.TrimSpace(src.Document.FileName),
			}, int64(src.Document.FileSize), true
		}
	}
	return replyMediaInfo{}, 0, false
}

func detectReplyMedia(msg *tgbotapi.Message) (info replyMediaInfo, sizeBytes int64, ok bool) {
	_, info, sizeBytes, ok = detectReplyMediaSource(msg)
	return info, sizeBytes, ok
}

func detectReplyMediaSource(msg *tgbotapi.Message) (source *tgbotapi.Message, info replyMediaInfo, sizeBytes int64, ok bool) {
	if msg == nil || msg.ReplyToMessage == nil {
		return nil, replyMediaInfo{}, 0, false
	}
	src := msg.ReplyToMessage
	for i := 0; i < 4 && src != nil; i++ {
		if mi, sz, yes := detectMediaInMessage(src); yes {
			return src, mi, sz, true
		}
		src = src.ReplyToMessage
	}
	return nil, replyMediaInfo{}, 0, false
}

func votLangFromEnv(key, fallback string) string {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	if v == "" {
		return fallback
	}
	if len(v) > 2 {
		parts := strings.FieldsFunc(v, func(r rune) bool {
			return r == '-' || r == '_' || r == ';' || r == ','
		})
		if len(parts) > 0 && len(parts[0]) >= 2 {
			v = parts[0][:2]
		}
	}
	if len(v) >= 2 {
		return v[:2]
	}
	return fallback
}

func votServiceIDForSource(sourceURL string) string {
	h := sha1.Sum([]byte(strings.TrimSpace(sourceURL)))
	return hex.EncodeToString(h[:])[:24]
}

func sanitizeVoiceBaseName(name string) string {
	n := strings.TrimSpace(name)
	if n == "" {
		return ""
	}
	n = filepath.Base(n)
	n = strings.TrimSpace(strings.Trim(n, `"'`))
	if n == "" || n == "." || n == ".." {
		return ""
	}
	ext := filepath.Ext(n)
	if ext != "" {
		n = strings.TrimSuffix(n, ext)
	}
	n = strings.TrimSpace(n)
	n = strings.ReplaceAll(n, "/", "_")
	n = strings.ReplaceAll(n, "\\", "_")
	if n == "" {
		return ""
	}
	return n
}

func resolveVoiceBaseName(cacheKey string, media replyMediaInfo) string {
	if v := sanitizeVoiceBaseName(media.Name); v != "" {
		return v
	}
	if v := sanitizeVoiceBaseName(getVoiceCacheBaseName(cacheKey)); v != "" {
		return v
	}
	if media.HasVideo {
		return "video"
	}
	return "audio"
}

func voiceOutName(cacheKey string, media replyMediaInfo, ext string) string {
	base := resolveVoiceBaseName(cacheKey, media)
	e := strings.TrimSpace(ext)
	if e == "" {
		e = ".bin"
	}
	if !strings.HasPrefix(e, ".") {
		e = "." + e
	}
	return base + e
}

func sendDocumentFromFileNamed(ctx sendContext, filePath, fileName, caption string) error {
	filePath = strings.TrimSpace(filePath)
	fileName = strings.TrimSpace(fileName)
	if ctx.Bot == nil || ctx.ChatID == 0 || filePath == "" {
		return fmt.Errorf("invalid document send params")
	}
	if err := ensureTelegramUploadLimit(filePath); err != nil {
		return err
	}
	fd, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer fd.Close()
	if fileName == "" {
		fileName = filepath.Base(filePath)
	}
	m := tgbotapi.NewDocument(ctx.ChatID, tgbotapi.FileReader{Name: fileName, Reader: fd})
	if ctx.ReplyTo > 0 {
		m.ReplyToMessageID = ctx.ReplyTo
		m.AllowSendingWithoutReply = true
	}
	caption = strings.TrimSpace(caption)
	if caption != "" {
		m.Caption = clipText(caption, 1024)
		m.ParseMode = "HTML"
	}
	_, err = ctx.Bot.Send(m)
	return err
}

func sendAudioFromFileNamed(ctx sendContext, filePath, fileName, performer, title string) error {
	filePath = strings.TrimSpace(filePath)
	fileName = strings.TrimSpace(fileName)
	if ctx.Bot == nil || ctx.ChatID == 0 || filePath == "" {
		return fmt.Errorf("invalid audio send params")
	}
	if err := ensureTelegramUploadLimit(filePath); err != nil {
		return err
	}
	fd, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer fd.Close()
	if fileName == "" {
		fileName = filepath.Base(filePath)
	}
	m := tgbotapi.NewAudio(ctx.ChatID, tgbotapi.FileReader{Name: fileName, Reader: fd})
	if ctx.ReplyTo > 0 {
		m.ReplyToMessageID = ctx.ReplyTo
		m.AllowSendingWithoutReply = true
	}
	if strings.TrimSpace(performer) != "" {
		m.Performer = clipText(strings.TrimSpace(performer), 64)
	}
	if strings.TrimSpace(title) != "" {
		m.Title = clipText(strings.TrimSpace(title), 64)
	}
	_, err = ctx.Bot.Send(m)
	return err
}

func sendVideoFromFileNamed(ctx sendContext, filePath, fileName, caption string) error {
	filePath = strings.TrimSpace(filePath)
	fileName = strings.TrimSpace(fileName)
	if ctx.Bot == nil || ctx.ChatID == 0 || filePath == "" {
		return fmt.Errorf("invalid video send params")
	}
	if err := ensureTelegramUploadLimit(filePath); err != nil {
		return err
	}
	fd, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer fd.Close()
	if fileName == "" {
		fileName = filepath.Base(filePath)
	}
	m := tgbotapi.NewVideo(ctx.ChatID, tgbotapi.FileReader{Name: fileName, Reader: fd})
	if ctx.ReplyTo > 0 {
		m.ReplyToMessageID = ctx.ReplyTo
		m.AllowSendingWithoutReply = true
	}
	m.SupportsStreaming = true
	caption = strings.TrimSpace(caption)
	if caption != "" {
		m.Caption = clipText(caption, 1024)
		m.ParseMode = "HTML"
	}
	_, err = ctx.Bot.Send(m)
	return err
}

func runVOTBackendTranslate(sourceURL, srcLang, resLang string) (votProviderResult, error) {
	from := normalizeVOTLang(srcLang)
	if from == "" {
		from = votLangFromEnv("VOICE_TRANSLATE_SRCLANG", "en")
	}
	to := normalizeVOTLang(resLang)
	if to == "" || to == "auto" {
		to = votLangFromEnv("VOICE_TRANSLATE_RESLANG", "ru")
	}
	timeoutSec := envInt("VOICE_TRANSLATE_TIMEOUT_SEC", 500)
	if timeoutSec < 60 {
		timeoutSec = 60
	}

	baseProvider := strings.ToLower(strings.TrimSpace(os.Getenv("VOICE_TRANSLATE_PROVIDER")))
	if baseProvider == "" {
		baseProvider = "yandex"
	}
	providers := []string{baseProvider}
	if baseProvider == "yandex_lively" {
		providers = []string{"yandex_lively", "yandex"}
	}

	client := &http.Client{Timeout: 90 * time.Second}
	var lastErr error

	for _, provider := range providers {
		reqBody := votTranslateRequest{
			Provider: provider,
			Service:  "telegram",
			VideoID:  votServiceIDForSource(sourceURL),
			FromLang: from,
			ToLang:   to,
			RawVideo: strings.TrimSpace(sourceURL),
		}
		deadline := time.Now().Add(time.Duration(timeoutSec) * time.Second)

		for {
			if time.Now().After(deadline) {
				lastErr = fmt.Errorf("vot backend timeout after %ds", timeoutSec)
				break
			}

			payload, _ := json.Marshal(reqBody)
			endpoints := []string{votBackendURLPrimary, votBackendURLFallback, votBackendURLWorker}
			var resp *http.Response
			var err error
			for _, endpoint := range endpoints {
				req, reqErr := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(payload))
				if reqErr != nil {
					err = reqErr
					continue
				}
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("User-Agent", "trigger-admin-bot/voice-translate")
				resp, err = client.Do(req)
				if err == nil {
					break
				}
			}
			if err != nil {
				lastErr = err
				break
			}
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
			resp.Body.Close()

			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				lastErr = fmt.Errorf("vot backend status=%d body=%s", resp.StatusCode, clipText(string(body), 300))
				break
			}

			var parsed votTranslateResponse
			if err := json.Unmarshal(body, &parsed); err != nil {
				lastErr = fmt.Errorf("vot backend decode failed: %v body=%s", err, clipText(string(body), 300))
				break
			}

			switch strings.ToLower(strings.TrimSpace(parsed.Status)) {
			case "success":
				if strings.TrimSpace(parsed.TranslatedURL) == "" {
					lastErr = fmt.Errorf("vot backend returned success without translated_url")
					break
				}
				return votProviderResult{
					translatedURL: strings.TrimSpace(parsed.TranslatedURL),
					providerUsed:  provider,
				}, nil
			case "waiting":
				sleepSec := parsed.RemainingTime
				if sleepSec <= 0 {
					sleepSec = 6
				}
				if sleepSec > 20 {
					sleepSec = 20
				}
				time.Sleep(time.Duration(sleepSec) * time.Second)
				continue
			case "failed":
				if strings.TrimSpace(parsed.Message) != "" {
					lastErr = fmt.Errorf("vot backend failed: %s", parsed.Message)
				} else {
					lastErr = fmt.Errorf("vot backend failed")
				}
			default:
				lastErr = fmt.Errorf("vot backend unknown status=%q body=%s", parsed.Status, clipText(string(body), 300))
			}
			break
		}
	}

	if lastErr == nil {
		lastErr = fmt.Errorf("vot backend failed")
	}
	return votProviderResult{}, lastErr
}

func downloadFileToPath(sourceURL, outPath string) error {
	req, err := http.NewRequest(http.MethodGet, strings.TrimSpace(sourceURL), nil)
	if err != nil {
		return err
	}
	resp, err := (&http.Client{Timeout: 180 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("download source status=%d body=%s", resp.StatusCode, clipText(string(body), 300))
	}
	f, err := os.Create(outPath)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := io.Copy(f, resp.Body); err != nil {
		return err
	}
	return nil
}

func mixTranslatedAudioWithSource(sourcePath, translatedMP3Path string, hasVideo bool) (string, error) {
	ext := ".mp3"
	if hasVideo {
		ext = ".mp4"
	}
	outFile, err := os.CreateTemp("", "translate_mix_*"+ext)
	if err != nil {
		return "", err
	}
	outPath := outFile.Name()
	_ = outFile.Close()

	origWav, err := os.CreateTemp("", "voice_src_*.wav")
	if err != nil {
		return "", err
	}
	origWavPath := origWav.Name()
	_ = origWav.Close()
	defer os.Remove(origWavPath)

	trWav, err := os.CreateTemp("", "voice_tr_*.wav")
	if err != nil {
		return "", err
	}
	trWavPath := trWav.Name()
	_ = trWav.Close()
	defer os.Remove(trWavPath)

	// Normalize tracks to predictable PCM when possible, but do not fail hard:
	// some inputs from Telegram may break this step while still being mixable directly.
	origInput := origWavPath
	trInput := trWavPath
	normOrig := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-y", "-i", sourcePath, "-map", "0:a:0?", "-vn", "-ac", "2", "-ar", "48000", origWavPath)
	if out, err := normOrig.CombinedOutput(); err != nil {
		origInput = sourcePath
		if debugTriggerLogEnabled {
			log.Printf("voice translate normalize source skipped err=%v out=%s", err, clipText(strings.TrimSpace(string(out)), 500))
		}
	}
	normTr := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-y", "-i", translatedMP3Path, "-map", "0:a:0?", "-vn", "-ac", "2", "-ar", "48000", trWavPath)
	if out, err := normTr.CombinedOutput(); err != nil {
		trInput = translatedMP3Path
		if debugTriggerLogEnabled {
			log.Printf("voice translate normalize translated skipped err=%v out=%s", err, clipText(strings.TrimSpace(string(out)), 500))
		}
	}

	// Improve speech intelligibility over music:
	// - translated track is boosted noticeably
	// - original is dynamically ducked while translated speech is active
	// - keep limiter to avoid clipping peaks
	dynamicFilter := "[1:a]asplit=2[a1mix0][a1ctrl];[a1mix0]volume=2.1[a1mix];[a1ctrl]highpass=f=140,lowpass=f=4200,agate=threshold=0.015:ratio=14:attack=6:release=260[ctrl];[0:a][ctrl]sidechaincompress=threshold=0.018:ratio=22:attack=4:release=260[a0duck];[a0duck][a1mix]amix=inputs=2:normalize=0:duration=first:dropout_transition=2,alimiter=limit=0.92[mix]"
	staticFilter := "[0:a]volume=0.28[a0];[1:a]volume=1.7[a1];[a0][a1]amix=inputs=2:normalize=0:duration=first:dropout_transition=2,alimiter=limit=0.94[mix]"
	filter := dynamicFilter
	if hasVideo {
		mixWav, err := os.CreateTemp("", "voice_mix_*.wav")
		if err != nil {
			return "", err
		}
		mixWavPath := mixWav.Name()
		_ = mixWav.Close()
		defer os.Remove(mixWavPath)

		mixCmd := exec.Command("ffmpeg",
			"-hide_banner",
			"-loglevel", "error",
			"-y",
			"-i", origInput,
			"-i", trInput,
			"-filter_complex", filter,
			"-map", "[mix]",
			"-c:a", "pcm_s16le",
			mixWavPath,
		)
		if out, err := mixCmd.CombinedOutput(); err != nil {
			// Fallback to static mix if sidechain filter is unsupported on this input.
			mixCmd = exec.Command("ffmpeg",
				"-hide_banner",
				"-loglevel", "error",
				"-y",
				"-i", origInput,
				"-i", trInput,
				"-filter_complex", staticFilter,
				"-map", "[mix]",
				"-c:a", "pcm_s16le",
				mixWavPath,
			)
			if out2, err2 := mixCmd.CombinedOutput(); err2 != nil {
				return "", fmt.Errorf("ffmpeg mix stage failed: %v (%s) | static fallback failed: %v (%s)", err, clipText(strings.TrimSpace(string(out)), 400), err2, clipText(strings.TrimSpace(string(out2)), 400))
			}
		}

		muxCmd := exec.Command("ffmpeg",
			"-hide_banner",
			"-loglevel", "error",
			"-y",
			"-i", sourcePath,
			"-i", mixWavPath,
			"-map", "0:v",
			"-map", "1:a",
			"-c:v", "copy",
			"-c:a", "aac",
			"-b:a", "160k",
			"-shortest",
			outPath,
		)
		if out, err := muxCmd.CombinedOutput(); err != nil {
			return "", fmt.Errorf("ffmpeg mux stage failed: %v (%s)", err, clipText(strings.TrimSpace(string(out)), 600))
		}
		return outPath, nil
	}

	mixCmd := exec.Command("ffmpeg",
		"-hide_banner",
		"-loglevel", "error",
		"-y",
		"-i", origInput,
		"-i", trInput,
		"-filter_complex", filter,
		"-map", "[mix]",
		"-c:a", "libmp3lame",
		"-b:a", "160k",
		"-shortest",
		outPath,
	)
	if out, err := mixCmd.CombinedOutput(); err != nil {
		mixCmd = exec.Command("ffmpeg",
			"-hide_banner",
			"-loglevel", "error",
			"-y",
			"-i", origInput,
			"-i", trInput,
			"-filter_complex", staticFilter,
			"-map", "[mix]",
			"-c:a", "libmp3lame",
			"-b:a", "160k",
			"-shortest",
			outPath,
		)
		if out2, err2 := mixCmd.CombinedOutput(); err2 != nil {
			// Final compatibility fallback for problematic mp3 inputs.
			plainCmd := exec.Command("ffmpeg",
				"-hide_banner",
				"-loglevel", "error",
				"-y",
				"-i", sourcePath,
				"-i", translatedMP3Path,
				"-filter_complex", "[0:a:0]volume=0.30[a0];[1:a:0]volume=1.8[a1];[a0][a1]amix=inputs=2:duration=first:dropout_transition=2,alimiter=limit=0.94[mix]",
				"-map", "[mix]",
				"-c:a", "libmp3lame",
				"-b:a", "160k",
				"-shortest",
				outPath,
			)
			if out3, err3 := plainCmd.CombinedOutput(); err3 != nil {
				return "", fmt.Errorf("ffmpeg mix failed: %v (%s) | static fallback failed: %v (%s) | plain fallback failed: %v (%s)", err, clipText(strings.TrimSpace(string(out)), 700), err2, clipText(strings.TrimSpace(string(out2)), 700), err3, clipText(strings.TrimSpace(string(out3)), 700))
			}
		}
	}
	return outPath, nil
}

func convertAudioToAudioOnlyMP4(inputPath, outPath string) error {
	cmd := exec.Command("ffmpeg",
		"-hide_banner",
		"-loglevel", "error",
		"-y",
		"-i", inputPath,
		"-map", "0:a:0?",
		"-vn",
		"-ac", "1",
		"-ar", "24000",
		"-c:a", "aac",
		"-b:a", "48k",
		"-movflags", "+faststart",
		outPath,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("ffmpeg audio->audio-only-mp4 failed: %v (%s)", err, clipText(strings.TrimSpace(string(out)), 500))
	}
	return nil
}

func processVoiceTranslateTask(task voiceTranslateTask) {
	if task.Bot == nil || task.ChatID == 0 {
		return
	}
	sendCtx := sendContext{Bot: task.Bot, ChatID: task.ChatID, ReplyTo: task.ReplyTo}
	progress, stopProgress := startMediaDownloadProgress(mediaDownloadTask{
		SendCtx: sendCtx,
		Mode:    "audio",
	})
	defer stopProgress()
	if progress != nil {
		progress.SetFrame(0)
		progress.SetStage("Подготовка")
	}
	var (
		mediaInfo replyMediaInfo
		ok        bool
	)
	if strings.TrimSpace(task.Media.FileID) != "" {
		mediaInfo = task.Media
		ok = true
	} else {
		mediaInfo, _, ok = detectReplyMedia(task.Msg)
	}
	if !ok {
		reply(sendCtx, "Нужен реплай на аудио/видео/voice.", false)
		return
	}
	cacheKey := buildVoiceTranslateCacheKey(mediaInfo.FileID)
	srcLang := normalizeVOTLang(task.SrcLang)
	if srcLang == "" {
		srcLang = normalizeVOTLang(votLangFromEnv("VOICE_TRANSLATE_SRCLANG", "en"))
		if srcLang == "" {
			srcLang = "en"
		}
	}
	resLang := normalizeVOTLang(task.ResLang)
	if resLang == "" || resLang == "auto" {
		resLang = votLangFromEnv("VOICE_TRANSLATE_RESLANG", "ru")
	}
	cacheKey = buildVoiceTranslateCacheKeyWithLang(mediaInfo.FileID, srcLang, resLang)
	if n := sanitizeVoiceBaseName(mediaInfo.Name); n != "" {
		setVoiceCacheBaseName(cacheKey, n)
	}
	providerUsed := "cache"
	sourceURL, err := task.Bot.GetFileDirectURL(mediaInfo.FileID)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "file is too big") {
			reply(sendCtx, "Telegram Bot API не даёт скачать файл больше 20 МБ. Для больших видео нужен local Bot API server/MTProto.", false)
			return
		}
		reply(sendCtx, "Не удалось получить файл для перевода. Попробуйте ещё раз.", false)
		return
	}
	if strings.TrimSpace(sourceURL) == "" {
		reply(sendCtx, "Не удалось получить ссылку на файл для перевода.", false)
		return
	}

	workDir, err := os.MkdirTemp("", "vot_backend_*")
	if err != nil {
		reply(sendCtx, "Не удалось подготовить задачу перевода. Попробуйте позже.", false)
		return
	}
	defer os.RemoveAll(workDir)

	srcExt := ".bin"
	if mediaInfo.HasVideo {
		srcExt = ".mp4"
	} else {
		ext := strings.TrimSpace(mediaInfo.Ext)
		if ext != "" {
			srcExt = ext
		}
	}
	if !strings.HasPrefix(srcExt, ".") {
		srcExt = "." + srcExt
	}
	srcTmp, err := os.CreateTemp("", "vot_source_*"+srcExt)
	if err != nil {
		reply(sendCtx, "Не удалось подготовить исходный файл для перевода.", false)
		return
	}
	sourcePath := srcTmp.Name()
	_ = srcTmp.Close()
	defer os.Remove(sourcePath)
	if progress != nil {
		progress.SetFrame(1)
		progress.SetStage("Скачивание исходника")
	}
	if err := downloadFileToPath(sourceURL, sourcePath); err != nil {
		reply(sendCtx, "Не удалось скачать исходный файл для микширования.", false)
		return
	}

	sourceForVOTAudio := sourcePath
	if cachedSource, ok := getVoiceSourceCache(mediaInfo.FileID); ok {
		sourceForVOTAudio = cachedSource
		if progress != nil {
			progress.SetFrame(2)
			progress.SetStage("Использую кеш mp4 для перевода")
		}
	} else {
		if progress != nil {
			progress.SetFrame(2)
			progress.SetStage("Подготовка mp4 для перевода")
		}
		mp4Tmp, e := os.CreateTemp("", "vot_src_audio_only_*.mp4")
		if e != nil {
			reply(sendCtx, "Не удалось подготовить mp4 для перевода.", false)
			return
		}
		sourceMP4ForVOT := mp4Tmp.Name()
		_ = mp4Tmp.Close()
		defer os.Remove(sourceMP4ForVOT)
		if e := convertAudioToAudioOnlyMP4(sourcePath, sourceMP4ForVOT); e != nil {
			if debugTriggerLogEnabled {
				log.Printf("voice translate build audio-only mp4 failed chat=%d replyTo=%d err=%v", task.ChatID, task.ReplyTo, e)
			}
			reply(sendCtx, "Не удалось подготовить mp4 для перевода.", false)
			return
		}
		if cachePath, cacheErr := setVoiceSourceCache(mediaInfo.FileID, sourceMP4ForVOT); cacheErr == nil && strings.TrimSpace(cachePath) != "" {
			sourceForVOTAudio = cachePath
		} else {
			sourceForVOTAudio = sourceMP4ForVOT
			if debugTriggerLogEnabled && cacheErr != nil {
				log.Printf("voice translate source mp4 cache save failed chat=%d replyTo=%d err=%v", task.ChatID, task.ReplyTo, cacheErr)
			}
		}
	}
	sourceForVOTSubs := sourceForVOTAudio
	if mediaInfo.HasVideo {
		// For subtitles/text, VOT works more reliably with the original video MP4 URL.
		sourceForVOTSubs = sourcePath
	}

	shareSource := sourceForVOTAudio
	if task.Action == "text" || task.Action == "subs" {
		shareSource = sourceForVOTSubs
	}

	shareTTL := time.Duration(envInt("VOICE_TRANSLATE_SHARE_TTL_SEC", 1800)) * time.Second
	shareToken := registerVoiceShareFile(shareSource, shareTTL)
	if shareToken == "" {
		if debugTriggerLogEnabled {
			log.Printf("voice translate share token failed chat=%d replyTo=%d", task.ChatID, task.ReplyTo)
		}
		reply(sendCtx, "Не удалось подготовить публичную ссылку для перевода.", false)
		return
	}
	defer releaseVoiceShareFile(shareToken)
	publicURL := buildVoiceSharePublicURL(shareToken)
	if strings.TrimSpace(publicURL) == "" {
		reply(sendCtx, "Не удалось подготовить публичную ссылку для перевода.", false)
		return
	}

	if task.Action == "text" || task.Action == "subs" {
		if task.Action == "subs" {
			if cached, ok := getFreshFile(voiceSubtitlesCachePath(cacheKey)); ok {
				if progress != nil {
					progress.SetFrame(8)
					progress.SetStage("Отправка результата (кеш)")
				}
				if err := sendDocumentFromFileNamed(sendCtx, cached, voiceOutName(cacheKey, mediaInfo, ".srt"), ""); err != nil {
					reply(sendCtx, "Не удалось отправить subtitle файл.", false)
				}
				return
			}
		} else {
			if cached, ok := getFreshFile(voiceTextCachePath(cacheKey)); ok {
				if progress != nil {
					progress.SetFrame(8)
					progress.SetStage("Отправка результата (кеш)")
				}
				if err := sendDocumentFromFileNamed(sendCtx, cached, voiceOutName(cacheKey, mediaInfo, ".txt"), ""); err != nil {
					reply(sendCtx, "Не удалось отправить текстовый файл перевода.", false)
				}
				return
			}
		}

		// Warm up VOT translation for this URL every time before subtitles/text:
		// after service restarts local cache state may differ from VOT subtitle readiness.
		if progress != nil {
			progress.SetFrame(3)
			progress.SetStage("Голосовой перевод")
		}
		seedOut, seedErr := runVOTCLITranslateLocal(publicURL, workDir, "translated_seed", srcLang, resLang)
		if seedErr != nil {
			if debugTriggerLogEnabled {
				log.Printf("voice translate subtitles warmup failed chat=%d replyTo=%d err=%v", task.ChatID, task.ReplyTo, seedErr)
			}
		} else if strings.TrimSpace(seedOut) != "" {
			cacheDst := filepath.Join("/home/appuser/trigger_admin_bot/static/tmp", "voice_cache_"+fmt.Sprintf("%x", sha1.Sum([]byte(cacheKey)))+".mp3")
			if in, err := os.Open(seedOut); err == nil {
				if out, err2 := os.Create(cacheDst); err2 == nil {
					_, _ = io.Copy(out, in)
					_ = out.Close()
					setVoiceTranslateCache(cacheKey, cacheDst, "vot-cli")
				}
				_ = in.Close()
			}
		}

		if progress != nil {
			progress.SetFrame(4)
			progress.SetStage("Субтитры через VOT")
		}
		subsFormat := "json"
		if task.Action == "subs" {
			subsFormat = "srt"
		}
		subsPath, err := runVOTCLISubtitlesLocal(publicURL, workDir, "translated_subs", srcLang, resLang, subsFormat)
		if err != nil {
			errText := strings.ToLower(err.Error())
			emptySubs := strings.Contains(errText, "subtitles output missing")
			// If subtitles list is empty, force warmup+retry once in the same request.
			if emptySubs {
				if debugTriggerLogEnabled {
					log.Printf("voice translate subtitles empty; forcing warmup retry chat=%d replyTo=%d", task.ChatID, task.ReplyTo)
				}
				if progress != nil {
					progress.SetFrame(3)
					progress.SetStage("Голосовой перевод")
				}
				seedOut, seedErr := runVOTCLITranslateLocal(publicURL, workDir, "translated_seed_retry", srcLang, resLang)
				if seedErr == nil && strings.TrimSpace(seedOut) != "" {
					cacheDst := filepath.Join("/home/appuser/trigger_admin_bot/static/tmp", "voice_cache_"+fmt.Sprintf("%x", sha1.Sum([]byte(cacheKey)))+".mp3")
					if in, openErr := os.Open(seedOut); openErr == nil {
						if out, createErr := os.Create(cacheDst); createErr == nil {
							_, _ = io.Copy(out, in)
							_ = out.Close()
							setVoiceTranslateCache(cacheKey, cacheDst, "vot-cli")
						}
						_ = in.Close()
					}
				} else if debugTriggerLogEnabled && seedErr != nil {
					log.Printf("voice translate subtitles retry warmup failed chat=%d replyTo=%d err=%v", task.ChatID, task.ReplyTo, seedErr)
				}
				if progress != nil {
					progress.SetFrame(4)
					progress.SetStage("Субтитры через VOT")
				}
				subsPath, err = runVOTCLISubtitlesLocal(publicURL, workDir, "translated_subs_retry", srcLang, resLang, subsFormat)
				if err == nil {
					goto HAVE_SUBS
				}
				errText = strings.ToLower(err.Error())
				emptySubs = strings.Contains(errText, "subtitles output missing")
			}

			if debugTriggerLogEnabled {
				log.Printf("voice translate subtitles failed chat=%d replyTo=%d err=%v", task.ChatID, task.ReplyTo, err)
			}
			if emptySubs {
				reply(sendCtx, "VOT не вернул субтитры для этого файла (пустой список). Перевод аудио/микс может работать, а subtitles/text для этого источника недоступны.", false)
			} else {
				reply(sendCtx, "Не удалось получить субтитры через VOT.", false)
			}
			return
		}
	HAVE_SUBS:
		if task.Action == "subs" {
			if progress != nil {
				progress.SetFrame(8)
				progress.SetStage("Отправка результата")
			}
			if strings.HasSuffix(strings.ToLower(strings.TrimSpace(subsPath)), ".json") {
				if srt, convErr := subtitlesJSONToSRT(subsPath); convErr == nil && strings.TrimSpace(srt) != "" {
					tmp, e := os.CreateTemp("", "translate_subs_*.srt")
					if e == nil {
						_ = os.WriteFile(tmp.Name(), []byte(srt), 0o644)
						_ = tmp.Close()
						defer os.Remove(tmp.Name())
						subsPath = tmp.Name()
					}
				}
			}
			if err := sendDocumentFromFileNamed(sendCtx, subsPath, voiceOutName(cacheKey, mediaInfo, ".srt"), ""); err != nil {
				reply(sendCtx, "Не удалось отправить subtitle файл.", false)
			}
			saveCacheFile(voiceSubtitlesCachePath(cacheKey), subsPath)
			return
		}
		txt := strings.TrimSpace(subtitlesToPlainText(subsPath))
		if txt == "" {
			reply(sendCtx, "Не удалось извлечь текст из субтитров VOT.", false)
			return
		}
		// Also persist subtitles cache when text is requested first,
		// so subsequent "subs" can be served from disk cache.
		subsForCachePath := subsPath
		if strings.HasSuffix(strings.ToLower(strings.TrimSpace(subsForCachePath)), ".json") {
			if srt, convErr := subtitlesJSONToSRT(subsForCachePath); convErr == nil && strings.TrimSpace(srt) != "" {
				tmpSubs, e := os.CreateTemp("", "translate_subs_cache_*.srt")
				if e == nil {
					_ = os.WriteFile(tmpSubs.Name(), []byte(srt), 0o644)
					_ = tmpSubs.Close()
					defer os.Remove(tmpSubs.Name())
					subsForCachePath = tmpSubs.Name()
				}
			}
		}
		saveCacheFile(voiceSubtitlesCachePath(cacheKey), subsForCachePath)

		tmp, e := os.CreateTemp("", "translate_text_*.txt")
		if e != nil {
			reply(sendCtx, "Не удалось подготовить текстовый файл перевода.", false)
			return
		}
		_ = tmp.Close()
		defer os.Remove(tmp.Name())
		if err := os.WriteFile(tmp.Name(), []byte(txt+"\n"), 0o644); err != nil {
			reply(sendCtx, "Не удалось сохранить текстовый файл перевода.", false)
			return
		}
		saveCacheFile(voiceTextCachePath(cacheKey), tmp.Name())
		if err := sendDocumentFromFileNamed(sendCtx, tmp.Name(), voiceOutName(cacheKey, mediaInfo, ".txt"), ""); err != nil {
			reply(sendCtx, "Не удалось отправить текстовый файл перевода.", false)
		}
		return
	}

	mp3Path := filepath.Join(workDir, "translated.mp3")
	if cachedMP3, ok := getVoiceTranslateCache(cacheKey); ok {
		if progress != nil {
			progress.SetFrame(6)
			progress.SetStage("Использую кеш перевода")
		}
		mp3Path = cachedMP3
	} else {
		if progress != nil {
			progress.SetFrame(3)
			progress.SetStage("Голосовой перевод")
		}
		cliSourceURL := publicURL
		cliOut, cliErr := runVOTCLITranslateLocal(cliSourceURL, workDir, "translated", srcLang, resLang)
		if cliErr != nil {
			msg := "Не удалось выполнить голосовой перевод."
			errText := strings.ToLower(cliErr.Error())
			if strings.Contains(errText, "timeout") {
				msg = "Перевод занял слишком много времени. Попробуйте позже или возьмите файл короче."
			} else if strings.Contains(errText, "too big") {
				maxMB := envInt("VOICE_TRANSLATE_MAX_MB", 300)
				msg = fmt.Sprintf("Файл слишком большой для перевода. Лимит: до %d МБ.", maxMB)
			}
			if debugTriggerLogEnabled {
				log.Printf("voice translate cli failed chat=%d replyTo=%d err=%v", task.ChatID, task.ReplyTo, cliErr)
			}
			reply(sendCtx, msg, false)
			return
		}
		if renameErr := os.Rename(cliOut, mp3Path); renameErr != nil && cliOut != mp3Path {
			mp3Path = cliOut
		}
		providerUsed = "vot-cli"
	}
	if !strings.HasPrefix(mp3Path, "/home/appuser/trigger_admin_bot/static/tmp/") {
		cacheDst := filepath.Join("/home/appuser/trigger_admin_bot/static/tmp", "voice_cache_"+fmt.Sprintf("%x", sha1.Sum([]byte(cacheKey)))+".mp3")
		if in, err := os.Open(mp3Path); err == nil {
			if out, err2 := os.Create(cacheDst); err2 == nil {
				_, _ = io.Copy(out, in)
				_ = out.Close()
				setVoiceTranslateCache(cacheKey, cacheDst, providerUsed)
			}
			_ = in.Close()
		}
	}

	if debugTriggerLogEnabled {
		log.Printf("voice translate success chat=%d replyTo=%d provider=%s", task.ChatID, task.ReplyTo, providerUsed)
	}

	if task.Action == "audio" {
		if progress != nil {
			progress.SetFrame(8)
			progress.SetStage("Отправка результата")
		}
		if err := sendAudioFromFileNamed(sendCtx, mp3Path, voiceOutName(cacheKey, mediaInfo, ".mp3"), "", ""); err != nil {
			reply(sendCtx, "Не удалось отправить дорожку перевода.", false)
		}
		return
	}
	// default action is mix
	if progress != nil {
		progress.SetFrame(7)
		progress.SetStage("Микширование аудио")
	}
	mixedPath, err := mixTranslatedAudioWithSource(sourcePath, mp3Path, mediaInfo.HasVideo)
	if err != nil {
		if debugTriggerLogEnabled {
			log.Printf("voice translate mix failed chat=%d replyTo=%d err=%v source=%s translated=%s", task.ChatID, task.ReplyTo, err, sourcePath, mp3Path)
		}
		reply(sendCtx, "Не удалось собрать финальный файл с подмешанным переводом.", false)
		return
	}
	defer os.Remove(mixedPath)
	if progress != nil {
		progress.SetFrame(8)
		progress.SetStage("Отправка результата")
	}
	if mediaInfo.HasVideo {
		if err := sendVideoFromFileNamed(sendCtx, mixedPath, voiceOutName(cacheKey, mediaInfo, ".mp4"), ""); err != nil {
			reply(sendCtx, "Не удалось отправить микс.", false)
		}
		return
	}
	if err := sendAudioFromFileNamed(sendCtx, mixedPath, voiceOutName(cacheKey, mediaInfo, ".mp3"), "", ""); err != nil {
		reply(sendCtx, "Не удалось отправить микс.", false)
	}
}
