package app

import (
	"bytes"
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
}

type replyMediaInfo struct {
	FileID   string
	HasVideo bool
	Ext      string
}

type voiceTranslateQueue struct {
	ch chan voiceTranslateTask
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
}

type voiceTranslateCacheDiskEntry struct {
	Key       string    `json:"key"`
	MP3Path   string    `json:"mp3_path"`
	ExpiresAt time.Time `json:"expires_at"`
	CreatedAt time.Time `json:"created_at"`
	Provider  string    `json:"provider,omitempty"`
}

var (
	voiceTranslateCacheMu sync.Mutex
	voiceTranslateCache   = map[string]voiceTranslateCacheEntry{}
	voiceCacheLoaded      bool
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
	srcLang := votLangFromEnv("VOICE_TRANSLATE_SRCLANG", "en")
	resLang := votLangFromEnv("VOICE_TRANSLATE_RESLANG", "ru")
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
		if key == "" || path == "" || !r.ExpiresAt.After(now) {
			continue
		}
		if _, statErr := os.Stat(path); statErr != nil {
			continue
		}
		voiceTranslateCache[key] = voiceTranslateCacheEntry{
			mp3Path:   path,
			expiresAt: r.ExpiresAt,
			createdAt: r.CreatedAt,
			provider:  strings.TrimSpace(r.Provider),
		}
	}
}

func saveVoiceTranslateCacheLocked() {
	rows := make([]voiceTranslateCacheDiskEntry, 0, len(voiceTranslateCache))
	for k, v := range voiceTranslateCache {
		if strings.TrimSpace(k) == "" || strings.TrimSpace(v.mp3Path) == "" {
			continue
		}
		rows = append(rows, voiceTranslateCacheDiskEntry{
			Key:       k,
			MP3Path:   v.mp3Path,
			ExpiresAt: v.expiresAt,
			CreatedAt: v.createdAt,
			Provider:  strings.TrimSpace(v.provider),
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
	}
	saveVoiceTranslateCacheLocked()
}

func runVOTCLITranslateLocal(sourcePath, outputDir, outputFile string) (string, error) {
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
	if resLang := strings.TrimSpace(os.Getenv("VOICE_TRANSLATE_RESLANG")); resLang != "" {
		args = append(args, "--reslang="+resLang)
	}
	if srcLang := strings.TrimSpace(os.Getenv("VOICE_TRANSLATE_SRCLANG")); srcLang != "" {
		args = append(args, "--lang="+srcLang)
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

func newVoiceTranslateQueue(workers, size int) *voiceTranslateQueue {
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

func detectReplyMedia(msg *tgbotapi.Message) (info replyMediaInfo, sizeBytes int64, ok bool) {
	if msg == nil || msg.ReplyToMessage == nil {
		return replyMediaInfo{}, 0, false
	}
	src := msg.ReplyToMessage
	if src.Voice != nil && strings.TrimSpace(src.Voice.FileID) != "" {
		return replyMediaInfo{FileID: strings.TrimSpace(src.Voice.FileID), HasVideo: false, Ext: ".ogg"}, int64(src.Voice.FileSize), true
	}
	if src.Audio != nil && strings.TrimSpace(src.Audio.FileID) != "" {
		ext := ".m4a"
		mime := strings.ToLower(strings.TrimSpace(src.Audio.MimeType))
		if strings.Contains(mime, "mpeg") || strings.Contains(mime, "mp3") {
			ext = ".mp3"
		} else if strings.Contains(mime, "ogg") || strings.Contains(mime, "opus") {
			ext = ".ogg"
		}
		return replyMediaInfo{FileID: strings.TrimSpace(src.Audio.FileID), HasVideo: false, Ext: ext}, int64(src.Audio.FileSize), true
	}
	if src.Video != nil && strings.TrimSpace(src.Video.FileID) != "" {
		return replyMediaInfo{FileID: strings.TrimSpace(src.Video.FileID), HasVideo: true, Ext: ".mp4"}, int64(src.Video.FileSize), true
	}
	if src.VideoNote != nil && strings.TrimSpace(src.VideoNote.FileID) != "" {
		return replyMediaInfo{FileID: strings.TrimSpace(src.VideoNote.FileID), HasVideo: true, Ext: ".mp4"}, int64(src.VideoNote.FileSize), true
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
			}, int64(src.Document.FileSize), true
		}
	}
	return replyMediaInfo{}, 0, false
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

func runVOTBackendTranslate(sourceURL string) (votProviderResult, error) {
	from := votLangFromEnv("VOICE_TRANSLATE_SRCLANG", "en")
	to := votLangFromEnv("VOICE_TRANSLATE_RESLANG", "ru")
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

func convertAudioToMP4ForVOT(inputPath, outPath string) error {
	cmd := exec.Command("ffmpeg",
		"-y",
		"-f", "lavfi",
		"-i", "color=c=black:s=640x360:r=25",
		"-i", inputPath,
		"-c:v", "libx264",
		"-preset", "veryfast",
		"-tune", "stillimage",
		"-c:a", "aac",
		"-shortest",
		outPath,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("ffmpeg audio->mp4 failed: %v (%s)", err, clipText(strings.TrimSpace(string(out)), 500))
	}
	return nil
}

func processVoiceTranslateTask(task voiceTranslateTask) {
	if task.Bot == nil || task.Msg == nil || task.ChatID == 0 {
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
	mediaInfo, _, ok := detectReplyMedia(task.Msg)
	if !ok {
		reply(sendCtx, "Нужен реплай на аудио/видео/voice.", false)
		return
	}
	cacheKey := buildVoiceTranslateCacheKey(mediaInfo.FileID)
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

	sourceForVOT := sourcePath
	if progress != nil {
		progress.SetFrame(2)
		if mediaInfo.HasVideo {
			progress.SetStage("Подготовка видео для перевода")
		} else {
			progress.SetStage("Подготовка аудио для перевода")
		}
	}

	mp3Path := filepath.Join(workDir, "translated.mp3")
	if cachedMP3, ok := getVoiceTranslateCache(cacheKey); ok {
		if progress != nil {
			progress.SetFrame(6)
			progress.SetStage("Использую кеш перевода")
		}
		mp3Path = cachedMP3
	} else {
		shareTTL := time.Duration(envInt("VOICE_TRANSLATE_SHARE_TTL_SEC", 1800)) * time.Second
		token := registerVoiceShareFile(sourceForVOT, shareTTL)
		if token == "" {
			if debugTriggerLogEnabled {
				log.Printf("voice translate share token failed chat=%d replyTo=%d", task.ChatID, task.ReplyTo)
			}
			reply(sendCtx, "Не удалось подготовить публичную ссылку для перевода.", false)
			return
		}
		publicURL := buildVoiceSharePublicURL(token)
		defer releaseVoiceShareFile(token)
		if strings.TrimSpace(publicURL) == "" {
			reply(sendCtx, "Не удалось подготовить публичную ссылку для перевода.", false)
			return
		}

		if progress != nil {
			progress.SetFrame(3)
			progress.SetStage("Голосовой перевод")
		}
		translated, err := runVOTBackendTranslate(publicURL)
		translatedByCLI := false
		if err != nil {
			if debugTriggerLogEnabled {
				log.Printf("voice translate backend failed chat=%d replyTo=%d err=%v publicURL=%s", task.ChatID, task.ReplyTo, err, clipText(publicURL, 200))
			}
			cliOut, cliErr := runVOTCLITranslateLocal(publicURL, workDir, "translated")
			if cliErr != nil {
				msg := "Не удалось выполнить голосовой перевод."
				errText := strings.ToLower(err.Error())
				if strings.Contains(errText, "timeout") {
					msg = "Перевод занял слишком много времени. Попробуйте позже или возьмите файл короче."
				} else if strings.Contains(errText, "too big") {
					maxMB := envInt("VOICE_TRANSLATE_MAX_MB", 300)
					msg = fmt.Sprintf("Файл слишком большой для перевода. Лимит: до %d МБ.", maxMB)
				}
				if debugTriggerLogEnabled {
					log.Printf("voice translate cli fallback failed chat=%d replyTo=%d err=%v", task.ChatID, task.ReplyTo, cliErr)
				}
				reply(sendCtx, msg, false)
				return
			}
			if renameErr := os.Rename(cliOut, mp3Path); renameErr != nil && cliOut != mp3Path {
				mp3Path = cliOut
			}
			translatedByCLI = true
		}

		if !translatedByCLI {
			providerUsed = translated.providerUsed
			if progress != nil {
				progress.SetFrame(5)
				progress.SetStage("Скачивание дорожки перевода")
			}
			if err := downloadFileToPath(translated.translatedURL, mp3Path); err != nil {
				reply(sendCtx, "Не удалось скачать аудио перевода.", false)
				return
			}
		} else {
			providerUsed = "vot-cli"
		}
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

	if debugTriggerLogEnabled {
		log.Printf("voice translate success chat=%d replyTo=%d provider=%s", task.ChatID, task.ReplyTo, providerUsed)
	}

	if progress != nil {
		progress.SetFrame(8)
		progress.SetStage("Отправка результата")
	}
	if mediaInfo.HasVideo {
		if err := sendVideoFromFile(sendCtx, mixedPath, "Перевод подмешан в исходный звук."); err != nil {
			reply(sendCtx, "Не удалось отправить видео с переводом.", false)
			return
		}
		return
	}
	if err := sendAudioFromFile(sendCtx, mixedPath, "", "Перевод подмешан в исходный звук"); err != nil {
		reply(sendCtx, "Не удалось отправить аудио с переводом.", false)
		return
	}
}
