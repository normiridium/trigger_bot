package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"trigger-admin-bot/internal/mediadl"
	"trigger-admin-bot/internal/pick"
	"trigger-admin-bot/internal/trigger"
)

func fitVideoToTelegram(ctx context.Context, sourcePath string, maxMB int, heights []int) (string, error) {
	sourcePath = strings.TrimSpace(sourcePath)
	if sourcePath == "" {
		return "", errors.New("empty source video path")
	}
	if maxMB <= 0 {
		return "", fmt.Errorf("%w: TELEGRAM_UPLOAD_MAX_MB is not set", errTelegramUploadTooLarge)
	}
	durationSec, err := probeMediaDurationSec(sourcePath)
	if err != nil {
		return "", err
	}
	if durationSec <= 0 {
		return "", fmt.Errorf("%w: unknown video duration", errTelegramUploadTooLarge)
	}
	if len(heights) == 0 {
		heights = []int{720, 480, 360}
	}
	maxBytes := int64(maxMB) * 1024 * 1024
	dir := filepath.Dir(sourcePath)
	log.Printf("media transcode ladder start source=%q max_mb=%d duration=%.2fs heights=%v", sourcePath, maxMB, durationSec, heights)
	for _, h := range heights {
		videoBitrateK := targetVideoBitrateKbps(maxBytes, durationSec)
		outPath := filepath.Join(dir, fmt.Sprintf("fit-%dp.mp4", h))
		log.Printf("media transcode try height=%dp bitrate=%dk out=%q", h, videoBitrateK, outPath)
		if err := transcodeVideoForLimit(ctx, sourcePath, outPath, h, videoBitrateK); err != nil {
			log.Printf("media transcode failed height=%dp err=%v", h, err)
			continue
		}
		if st, stErr := os.Stat(outPath); stErr == nil {
			log.Printf("media transcode produced height=%dp size=%.2fMB", h, float64(st.Size())/1_000_000.0)
		}
		if err := ensureTelegramUploadLimit(outPath); err == nil {
			log.Printf("media transcode accepted height=%dp file=%q", h, outPath)
			return outPath, nil
		}
		_ = os.Remove(outPath)
		log.Printf("media transcode over limit after height=%dp", h)
	}
	return "", fmt.Errorf("%w: cannot fit video into %d MB", errTelegramUploadTooLarge, maxMB)
}

func transcodeVideoForLimit(ctx context.Context, sourcePath, outPath string, maxHeight int, videoBitrateKbps int) error {
	timeoutSec := envInt("MEDIA_VIDEO_TRANSCODE_TIMEOUT_SEC", 300)
	if timeoutSec < 60 {
		timeoutSec = 60
	}
	tctx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSec)*time.Second)
	defer cancel()
	scaleArg := fmt.Sprintf("scale='if(gt(ih,%d),-2,iw)':'if(gt(ih,%d),%d,ih)'", maxHeight, maxHeight, maxHeight)
	if videoBitrateKbps < 220 {
		videoBitrateKbps = 220
	}
	maxRateKbps := int(float64(videoBitrateKbps) * 1.15)
	bufSizeKbps := videoBitrateKbps * 2
	cmd := exec.CommandContext(tctx,
		"ffmpeg",
		"-nostdin",
		"-y",
		"-i", sourcePath,
		"-vf", scaleArg,
		"-c:v", "libx264",
		"-preset", "veryfast",
		"-pix_fmt", "yuv420p",
		"-b:v", strconv.Itoa(videoBitrateKbps)+"k",
		"-maxrate", strconv.Itoa(maxRateKbps)+"k",
		"-bufsize", strconv.Itoa(bufSizeKbps)+"k",
		"-c:a", "aac",
		"-b:a", "96k",
		"-ac", "2",
		"-ar", "44100",
		"-movflags", "+faststart",
		outPath,
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if errors.Is(tctx.Err(), context.DeadlineExceeded) {
			return fmt.Errorf("ffmpeg transcode timeout")
		}
		return fmt.Errorf("ffmpeg transcode failed: %s", clipText(msg, 400))
	}
	return nil
}

func probeMediaDurationSec(path string) (float64, error) {
	out, err := exec.Command(
		"ffprobe",
		"-v", "error",
		"-show_entries", "format=duration",
		"-of", "default=noprint_wrappers=1:nokey=1",
		path,
	).Output()
	if err != nil {
		return 0, fmt.Errorf("ffprobe duration failed: %w", err)
	}
	val := strings.TrimSpace(string(out))
	if val == "" {
		return 0, errors.New("empty duration")
	}
	dur, err := strconv.ParseFloat(val, 64)
	if err != nil {
		return 0, fmt.Errorf("bad duration %q: %w", val, err)
	}
	return dur, nil
}

func targetVideoBitrateKbps(limitBytes int64, durationSec float64) int {
	if limitBytes <= 0 || durationSec <= 1 {
		return 1200
	}
	// Reserve overhead and audio stream (~96kbps) for predictable final size.
	totalKbps := int((float64(limitBytes) * 8.0) / (durationSec * 1000.0) * 0.94)
	videoKbps := totalKbps - 96
	if videoKbps < 220 {
		return 220
	}
	return videoKbps
}

func processSpotifyPick(ctx context.Context, sendCtx sendContext, dl SpotifyDownloadPort, req pick.PickRequest) error {
	if strings.TrimSpace(req.TrackID) == "" {
		return errors.New("empty track id")
	}
	query := strings.TrimSpace(req.Artist + " - " + req.Title)
	query = strings.Trim(query, " -")
	if query == "" {
		query = strings.TrimSpace(req.Artist + " " + req.Title)
	}
	if query == "" {
		return errors.New("empty track search query")
	}
	dlCtx, cancelDl := context.WithTimeout(ctx, 3*time.Minute)
	filePath, err := dl.DownloadByQuery(dlCtx, query)
	cancelDl()
	if err != nil {
		return err
	}
	defer func() {
		if rmErr := os.Remove(filePath); rmErr != nil && debugTriggerLogEnabled {
			log.Printf("spotify temp cleanup failed path=%q err=%v", filePath, rmErr)
		}
	}()
	performer := strings.TrimSpace(req.Artist)
	title := strings.TrimSpace(req.Title)
	return sendAudioFromFile(sendCtx, filePath, performer, title)
}

type spotifyPickTask struct {
	SendCtx  sendContext
	Req      pick.PickRequest
	DL       SpotifyDownloadPort
	Msg      *tgbotapi.Message
	Trigger  *Trigger
	Idle     *trigger.IdleTracker
	ReportTo int64
}

type spotifyPickQueue struct {
	ch chan spotifyPickTask
}

func newSpotifyPickQueue(workers, size int) *spotifyPickQueue {
	if workers < 1 {
		workers = 1
	}
	if size < 1 {
		size = workers * 2
	}
	q := &spotifyPickQueue{ch: make(chan spotifyPickTask, size)}
	for i := 0; i < workers; i++ {
		go func() {
			for task := range q.ch {
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
				err := processSpotifyPick(ctx, task.SendCtx, task.DL, task.Req)
				cancel()
				if err != nil {
					chatID := task.ReportTo
					if chatID == 0 {
						chatID = task.SendCtx.ChatID
					}
					if errors.Is(err, errTelegramUploadTooLarge) {
						reportChatFailure(task.SendCtx.Bot, chatID, "аудио слишком большое для отправки в Telegram", err)
						continue
					}
					log.Printf("spotify queue send failed chat=%d err=%v", chatID, err)
					reportChatFailure(task.SendCtx.Bot, chatID, "ошибка отправки аудио Spotify", err)
					continue
				}
				if task.Idle != nil {
					task.Idle.MarkActivity(task.SendCtx.ChatID, time.Now())
				}
				if task.Msg != nil && task.Trigger != nil {
					deleteTriggerSourceMessage(task.SendCtx.Bot, task.Msg, task.Trigger)
				}
			}
		}()
	}
	return q
}

func (q *spotifyPickQueue) enqueue(task spotifyPickTask) bool {
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

type yandexMusicTask struct {
	SendCtx  sendContext
	URL      string
	DL       YandexMusicDownloadPort
	Msg      *tgbotapi.Message
	Trigger  *Trigger
	Idle     *trigger.IdleTracker
	ReportTo int64
}

type yandexMusicQueue struct {
	ch chan yandexMusicTask
}

func newYandexMusicQueue(workers, size int) *yandexMusicQueue {
	if workers < 1 {
		workers = 1
	}
	if size < 1 {
		size = workers * 2
	}
	q := &yandexMusicQueue{ch: make(chan yandexMusicTask, size)}
	for i := 0; i < workers; i++ {
		go func() {
			for task := range q.ch {
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
				err := processYandexMusic(ctx, task.SendCtx, task.DL, task.URL)
				cancel()
				if err != nil {
					chatID := task.ReportTo
					if chatID == 0 {
						chatID = task.SendCtx.ChatID
					}
					log.Printf("yandex queue send failed chat=%d err=%v", chatID, err)
					reportChatFailure(task.SendCtx.Bot, chatID, "ошибка скачивания Yandex Music", err)
					continue
				}
				if task.Idle != nil {
					task.Idle.MarkActivity(task.SendCtx.ChatID, time.Now())
				}
				if task.Msg != nil && task.Trigger != nil {
					deleteTriggerSourceMessage(task.SendCtx.Bot, task.Msg, task.Trigger)
				}
			}
		}()
	}
	return q
}

func (q *yandexMusicQueue) enqueue(task yandexMusicTask) bool {
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

func processYandexMusic(ctx context.Context, sendCtx sendContext, dl YandexMusicDownloadPort, rawURL string) error {
	if dl == nil {
		return errors.New("yandex downloader is not configured")
	}
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return errors.New("empty yandex music url")
	}
	path, err := dl.DownloadByURL(ctx, rawURL)
	if err != nil {
		return err
	}
	defer func() {
		if rmErr := os.RemoveAll(filepath.Dir(path)); rmErr != nil && debugTriggerLogEnabled {
			log.Printf("yandex temp cleanup failed path=%q err=%v", path, rmErr)
		}
	}()
	performer, title := yandexPerformerTitleFromPath(path)
	return sendAudioFromFileWithMeta(sendCtx, path, performer, title, rawURL, "yandex_music")
}

func yandexPerformerTitleFromPath(path string) (string, string) {
	base := strings.TrimSpace(filepath.Base(strings.TrimSpace(path)))
	if base == "" {
		return "", ""
	}
	ext := strings.TrimSpace(filepath.Ext(base))
	name := strings.TrimSpace(strings.TrimSuffix(base, ext))
	if name == "" {
		return "", ""
	}
	parts := strings.SplitN(name, " - ", 2)
	if len(parts) == 2 {
		performer := strings.TrimSpace(parts[0])
		title := strings.TrimSpace(parts[1])
		return performer, title
	}
	return "", name
}

type mediaDownloadTask struct {
	SendCtx  sendContext
	URL      string
	Mode     string
	DL       MediaDownloadPort
	Msg      *tgbotapi.Message
	Trigger  *Trigger
	Idle     *trigger.IdleTracker
	ReportTo int64
}

type mediaDownloadQueue struct {
	ch chan mediaDownloadTask
}

func newMediaDownloadQueue(workers, size int) *mediaDownloadQueue {
	if workers < 1 {
		workers = 1
	}
	if size < 1 {
		size = workers * 2
	}
	q := &mediaDownloadQueue{ch: make(chan mediaDownloadTask, size)}
	for i := 0; i < workers; i++ {
		workerID := i + 1
		go func(id int) {
			for task := range q.ch {
				log.Printf("media worker=%d start mode=%s chat=%d replyTo=%d url=%q", id, task.Mode, task.SendCtx.ChatID, task.SendCtx.ReplyTo, clipText(task.URL, 220))
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
				err := processMediaDownload(ctx, task.SendCtx, task.DL, task.URL, task.Mode)
				cancel()
				if err != nil {
					if errors.Is(err, mediadl.ErrTooLarge) {
						if debugTriggerLogEnabled {
							log.Printf("media download skipped by size limit url=%q err=%v", clipText(task.URL, 180), err)
						}
						continue
					}
					if errors.Is(err, errTelegramUploadTooLarge) {
						if debugTriggerLogEnabled {
							log.Printf("media download skipped by telegram upload limit url=%q err=%v", clipText(task.URL, 180), err)
						}
						continue
					}
					if errors.Is(err, mediadl.ErrUnsupportedURL) {
						if debugTriggerLogEnabled {
							log.Printf("media download skipped unsupported url=%q", clipText(task.URL, 180))
						}
						continue
					}
					chatID := task.ReportTo
					if chatID == 0 {
						chatID = task.SendCtx.ChatID
					}
					log.Printf("media queue send failed chat=%d err=%v", chatID, err)
					reportChatFailure(task.SendCtx.Bot, chatID, "ошибка скачивания аудио", err)
					continue
				}
				log.Printf("media worker=%d success mode=%s chat=%d url=%q", id, task.Mode, task.SendCtx.ChatID, clipText(task.URL, 220))
				if task.Idle != nil {
					task.Idle.MarkActivity(task.SendCtx.ChatID, time.Now())
				}
				if task.Msg != nil && task.Trigger != nil {
					deleteTriggerSourceMessage(task.SendCtx.Bot, task.Msg, task.Trigger)
				}
			}
		}(workerID)
	}
	return q
}

func (q *mediaDownloadQueue) enqueue(task mediaDownloadTask) bool {
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

func processMediaDownload(ctx context.Context, sendCtx sendContext, dl MediaDownloadPort, rawURL string, mode string) error {
	mode = strings.TrimSpace(strings.ToLower(mode))
	if mode == "" {
		mode = mediadl.ModeAudio
	}
	log.Printf(
		"media process start mode=%s chat=%d url=%q limits(download=%dMB upload=%dMB max_h=%dp)",
		mode,
		sendCtx.ChatID,
		clipText(rawURL, 220),
		dl.ConfiguredMaxSizeMB(),
		envInt("TELEGRAM_UPLOAD_MAX_MB", 50),
		dl.ConfiguredMaxHeight(),
	)
	if mode == mediadl.ModeVideo {
		dlCtx, cancelDl := context.WithTimeout(ctx, 3*time.Minute)
		res, err := dl.DownloadVideoFromURL(dlCtx, rawURL)
		cancelDl()
		if err != nil {
			return err
		}
		if st, stErr := os.Stat(res.FilePath); stErr == nil {
			log.Printf("media video downloaded chat=%d path=%q size=%.2fMB title=%q duration=%.0fs", sendCtx.ChatID, res.FilePath, float64(st.Size())/1_000_000.0, clipText(res.Title, 120), res.Duration)
		}
		videoPath := res.FilePath
		defer func() {
			if rmErr := os.Remove(res.FilePath); rmErr != nil && debugTriggerLogEnabled {
				log.Printf("media temp cleanup failed path=%q err=%v", res.FilePath, rmErr)
			}
			if videoPath != res.FilePath {
				if rmErr := os.Remove(videoPath); rmErr != nil && debugTriggerLogEnabled {
					log.Printf("media temp cleanup failed path=%q err=%v", videoPath, rmErr)
				}
			}
		}()
		if err := ensureTelegramUploadLimit(videoPath); err != nil {
			if !errors.Is(err, errTelegramUploadTooLarge) {
				return err
			}
			log.Printf("media video over telegram limit chat=%d path=%q, starting transcode ladder", sendCtx.ChatID, videoPath)
			fitted, fitErr := fitVideoToTelegram(ctx, videoPath, envInt("TELEGRAM_UPLOAD_MAX_MB", 50), videoFallbackHeights(dl.ConfiguredMaxHeight()))
			if fitErr != nil {
				return fitErr
			}
			videoPath = fitted
			if st, stErr := os.Stat(videoPath); stErr == nil {
				log.Printf("media video fitted chat=%d path=%q size=%.2fMB", sendCtx.ChatID, videoPath, float64(st.Size())/1_000_000.0)
			}
		}
		title := strings.TrimSpace(res.Title)
		if title == "" {
			title = strings.TrimSpace(rawURL)
		}
		return sendVideoFromFile(sendCtx, videoPath, buildMediaVideoCaption(videoPath, title, res.SourceURL, res.Service))
	}
	if mode == mediadl.ModeAuto {
		dlCtx, cancelDl := context.WithTimeout(ctx, 3*time.Minute)
		res, err := dl.DownloadMediaAutoFromURL(dlCtx, rawURL)
		cancelDl()
		if err != nil {
			return err
		}
		if st, stErr := os.Stat(res.FilePath); stErr == nil {
			log.Printf("media auto downloaded chat=%d kind=%s path=%q size=%.2fMB title=%q duration=%.0fs", sendCtx.ChatID, res.MediaKind, res.FilePath, float64(st.Size())/1_000_000.0, clipText(res.Title, 120), res.Duration)
		}
		defer func() {
			if rmErr := os.Remove(res.FilePath); rmErr != nil && debugTriggerLogEnabled {
				log.Printf("media temp cleanup failed path=%q err=%v", res.FilePath, rmErr)
			}
		}()
		title := strings.TrimSpace(res.Title)
		if title == "" {
			title = strings.TrimSpace(rawURL)
		}
		switch res.MediaKind {
		case mediadl.MediaKindPhoto:
			return sendPhotoFromFile(sendCtx, res.FilePath, buildMediaPhotoCaption(res.FilePath, title, res.SourceURL, res.Service))
		case mediadl.MediaKindAudio:
			return sendAudioFromFileWithMeta(sendCtx, res.FilePath, strings.TrimSpace(res.Artist), buildMediaAudioTitle(title, res.SourceURL, res.Service), res.SourceURL, res.Service)
		default:
			if err := ensureTelegramUploadLimit(res.FilePath); err != nil {
				return err
			}
			return sendVideoFromFile(sendCtx, res.FilePath, buildMediaVideoCaption(res.FilePath, title, res.SourceURL, res.Service))
		}
	}
	dlCtx, cancelDl := context.WithTimeout(ctx, 3*time.Minute)
	res, err := dl.DownloadAudioFromURL(dlCtx, rawURL)
	cancelDl()
	if err != nil {
		return err
	}
	if st, stErr := os.Stat(res.FilePath); stErr == nil {
		log.Printf("media audio downloaded chat=%d path=%q size=%.2fMB title=%q duration=%.0fs", sendCtx.ChatID, res.FilePath, float64(st.Size())/1_000_000.0, clipText(res.Title, 120), res.Duration)
	}
	defer func() {
		if rmErr := os.Remove(res.FilePath); rmErr != nil && debugTriggerLogEnabled {
			log.Printf("media temp cleanup failed path=%q err=%v", res.FilePath, rmErr)
		}
	}()
	title := strings.TrimSpace(res.Title)
	if title == "" {
		title = strings.TrimSpace(rawURL)
	}
	return sendAudioFromFileWithMeta(sendCtx, res.FilePath, strings.TrimSpace(res.Artist), buildMediaAudioTitle(title, res.SourceURL, res.Service), res.SourceURL, res.Service)
}

func videoFallbackHeights(maxHeight int) []int {
	if maxHeight <= 0 {
		maxHeight = 720
	}
	levels := []int{maxHeight}
	if maxHeight > 720 {
		levels = append(levels, 720, 480, 360)
	} else if maxHeight > 480 {
		levels = append(levels, 480, 360)
	} else if maxHeight > 360 {
		levels = append(levels, 360)
	}
	seen := make(map[int]struct{}, len(levels))
	out := make([]int, 0, len(levels))
	for _, v := range levels {
		if v <= 0 {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}

func buildAudioCaption(path string, service string, sourceURL string) string {
	stats, ok := probeAudioStats(path)
	if !ok || stats.SizeBytes <= 0 {
		return ""
	}
	sourceURL = strings.TrimSpace(sourceURL)
	sizeMB := float64(stats.SizeBytes) / 1_000_000.0
	dur := pick.FormatDuration(stats.DurationSec)
	bitrateKbps := stats.BitrateKbps
	if bitrateKbps <= 0 && stats.DurationSec > 0 {
		bitrateKbps = int64(float64(stats.SizeBytes*8)/stats.DurationSec/1000.0 + 0.5)
	}
	emoji := mediaServiceEmoji(service, mediadl.ModeAudio)
	durToken := dur
	if durToken != "" && sourceURL != "" {
		durToken = buildSourceLinkHTML(sourceURL, durToken)
	}
	if dur == "" || bitrateKbps <= 0 {
		if sourceURL != "" {
			return fmt.Sprintf("%s %s | %.2fMB", emoji, buildSourceLinkHTML(sourceURL, "ссылка"), sizeMB)
		}
		return fmt.Sprintf("%s %.2f MB", emoji, sizeMB)
	}
	return fmt.Sprintf("%s %s | %.2fMB | %dKbps", emoji, durToken, sizeMB, bitrateKbps)
}

type audioStats struct {
	SizeBytes   int64
	DurationSec float64
	BitrateKbps int64
}

func probeAudioStats(path string) (audioStats, bool) {
	info, err := os.Stat(path)
	if err != nil {
		return audioStats{}, false
	}
	stats := audioStats{SizeBytes: info.Size()}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		return stats, true
	}
	out, err := exec.Command("ffprobe",
		"-v", "error",
		"-show_entries", "format=duration,bit_rate",
		"-of", "default=nw=1:nk=1",
		path,
	).Output()
	if err != nil {
		return stats, true
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) > 0 {
		if v, err := strconv.ParseFloat(strings.TrimSpace(lines[0]), 64); err == nil && v > 0 {
			stats.DurationSec = v
		}
	}
	if len(lines) > 1 {
		if v, err := strconv.ParseInt(strings.TrimSpace(lines[1]), 10, 64); err == nil && v > 0 {
			stats.BitrateKbps = v / 1000
		}
	}
	return stats, true
}

func downloadAudioToTempFile(audioURL string) (string, error) {
	tmp, err := os.CreateTemp("", "vk-audio-*.mp3")
	if err != nil {
		return "", err
	}
	tmpPath := tmp.Name()
	_ = tmp.Close()
	tmpSrcPath := ""
	maxMB := envInt("SPOTIFY_AUDIO_MAX_MB", 60)
	if maxMB < 5 {
		maxMB = 5
	}
	ffmpegTimeout := envInt("SPOTIFY_AUDIO_FFMPEG_TIMEOUT_SEC", 120)
	if ffmpegTimeout < 30 {
		ffmpegTimeout = 30
	}
	ua := strings.TrimSpace(os.Getenv("SPOTIFY_USER_AGENT"))
	if ua == "" {
		ua = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0 Safari/537.36"
	}
	retries := envInt("SPOTIFY_AUDIO_RETRY_COUNT", 3)
	if retries < 1 {
		retries = 1
	}
	dlThreads := envInt("SPOTIFY_AUDIO_DL_THREADS", 1)
	if dlThreads < 1 {
		dlThreads = 1
	}
	useMultiDownload := dlThreads > 1 && !strings.Contains(strings.ToLower(audioURL), ".m3u8")
	var runErr error
	for attempt := 1; attempt <= retries; attempt++ {
		_ = os.Remove(tmpPath)
		ctx, cancel := context.WithTimeout(context.Background(), time.Duration(ffmpegTimeout)*time.Second)
		var err error
		if useMultiDownload {
			if tmpSrcPath == "" {
				if f, ferr := os.CreateTemp("", "vk-audio-src-*.bin"); ferr == nil {
					tmpSrcPath = f.Name()
					_ = f.Close()
				}
			}
			if tmpSrcPath != "" {
				_ = os.Remove(tmpSrcPath)
			}
			err = downloadAudioMultiPart(ctx, audioDownloadRequest{
				AudioURL:  audioURL,
				OutPath:   tmpSrcPath,
				Threads:   dlThreads,
				UserAgent: ua,
			})
			if err == nil {
				err = runFFmpegAudioDownloadFromFile(ctx, tmpSrcPath, tmpPath)
			}
		} else {
			err = runFFmpegAudioDownload(ctx, audioURL, tmpPath, ua)
		}
		cancel()
		if err == nil {
			runErr = nil
			break
		}
		runErr = err
		if debugTriggerLogEnabled {
			log.Printf("ffmpeg audio attempt=%d/%d failed: %v", attempt, retries, err)
		}
		time.Sleep(time.Duration(attempt) * 500 * time.Millisecond)
	}
	if runErr != nil {
		_ = os.Remove(tmpPath)
		if tmpSrcPath != "" {
			_ = os.Remove(tmpSrcPath)
		}
		return "", runErr
	}
	st, err := os.Stat(tmpPath)
	if err != nil {
		_ = os.Remove(tmpPath)
		if tmpSrcPath != "" {
			_ = os.Remove(tmpSrcPath)
		}
		return "", err
	}
	size := st.Size()
	limit := int64(maxMB) << 20
	if size <= 0 {
		_ = os.Remove(tmpPath)
		if tmpSrcPath != "" {
			_ = os.Remove(tmpSrcPath)
		}
		return "", errors.New("ffmpeg produced empty audio file")
	}
	if size > limit {
		_ = os.Remove(tmpPath)
		if tmpSrcPath != "" {
			_ = os.Remove(tmpSrcPath)
		}
		return "", fmt.Errorf("audio too large: %d bytes (limit %d MB)", size, maxMB)
	}
	if tmpSrcPath != "" {
		_ = os.Remove(tmpSrcPath)
	}
	return tmpPath, nil
}

func runFFmpegAudioDownload(ctx context.Context, audioURL, outPath, userAgent string) error {
	if strings.Contains(strings.ToLower(audioURL), ".m3u8") {
		return runFFmpegAudioDownloadFromM3U8(ctx, audioURL, outPath, userAgent)
	}
	return runFFmpegAudioDownloadDirect(ctx, audioURL, outPath, userAgent)
}

func runFFmpegAudioDownloadFromM3U8(ctx context.Context, audioURL, outPath, userAgent string) error {
	tmpTS, err := os.CreateTemp("", "vk-audio-*.ts")
	if err != nil {
		return err
	}
	tmpTSPath := tmpTS.Name()
	_ = tmpTS.Close()
	defer os.Remove(tmpTSPath)

	var stderr1 bytes.Buffer
	copyCmd := exec.CommandContext(ctx,
		"ffmpeg",
		"-nostdin",
		"-hide_banner",
		"-loglevel", "warning",
		"-y",
		"-http_persistent", "false",
		"-reconnect", "1",
		"-reconnect_streamed", "1",
		"-reconnect_at_eof", "1",
		"-reconnect_delay_max", "5",
		"-rw_timeout", "15000000",
		"-user_agent", userAgent,
		"-protocol_whitelist", "file,http,https,tcp,tls,crypto",
		"-allowed_extensions", "ALL",
		"-i", audioURL,
		"-vn",
		"-c", "copy",
		tmpTSPath,
	)
	copyCmd.Stderr = &stderr1
	if err := copyCmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr1.String())
		if msg == "" {
			msg = err.Error()
		}
		if ctx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("ffmpeg timeout")
		}
		return fmt.Errorf("ffmpeg m3u8 copy failed: %s", clipText(msg, 400))
	}

	var stderr2 bytes.Buffer
	transcodeCmd := exec.CommandContext(ctx,
		"ffmpeg",
		"-nostdin",
		"-hide_banner",
		"-loglevel", "warning",
		"-y",
		"-i", tmpTSPath,
		"-vn",
		"-acodec", "libmp3lame",
		"-b:a", "192k",
		outPath,
	)
	transcodeCmd.Stderr = &stderr2
	if err := transcodeCmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr2.String())
		if msg == "" {
			msg = err.Error()
		}
		if ctx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("ffmpeg timeout")
		}
		return fmt.Errorf("ffmpeg m3u8 transcode failed: %s", clipText(msg, 400))
	}
	return nil
}

func runFFmpegAudioDownloadDirect(ctx context.Context, audioURL, outPath, userAgent string) error {
	var stderr bytes.Buffer
	headers := "Referer: https://vk.com/\r\nOrigin: https://vk.com\r\nAccept: */*\r\n"
	cmd := exec.CommandContext(ctx,
		"ffmpeg",
		"-nostdin",
		"-hide_banner",
		"-loglevel", "warning",
		"-y",
		// Network resilience for HLS.
		"-reconnect", "1",
		"-reconnect_streamed", "1",
		"-reconnect_at_eof", "1",
		"-reconnect_delay_max", "5",
		"-rw_timeout", "15000000",
		"-http_persistent", "0",
		"-headers", headers,
		"-user_agent", userAgent,
		"-protocol_whitelist", "file,http,https,tcp,tls,crypto",
		"-allowed_extensions", "ALL",
		"-http_seekable", "0",
		"-fflags", "+discardcorrupt",
		"-i", audioURL,
		// Be tolerant to damaged frames in VK-protected streams.
		"-err_detect", "ignore_err",
		"-vn",
		"-acodec", "libmp3lame",
		"-b:a", "192k",
		outPath,
	)
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		if ctx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("ffmpeg timeout")
		}
		return fmt.Errorf("ffmpeg failed: %s", clipText(msg, 400))
	}
	return nil
}

func runFFmpegAudioDownloadFromFile(ctx context.Context, inPath, outPath string) error {
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx,
		"ffmpeg",
		"-nostdin",
		"-hide_banner",
		"-loglevel", "warning",
		"-y",
		"-i", inPath,
		"-vn",
		"-acodec", "libmp3lame",
		"-b:a", "192k",
		outPath,
	)
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		if ctx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("ffmpeg timeout")
		}
		return fmt.Errorf("ffmpeg file transcode failed: %s", clipText(msg, 400))
	}
	return nil
}

type audioDownloadRequest struct {
	AudioURL  string
	OutPath   string
	Threads   int
	UserAgent string
}

func downloadAudioMultiPart(ctx context.Context, req audioDownloadRequest) error {
	if strings.TrimSpace(req.OutPath) == "" {
		return errors.New("empty temp path for multipart download")
	}
	if req.Threads < 2 {
		return errors.New("multipart download requires threads >= 2")
	}
	if _, err := exec.LookPath("aria2c"); err != nil {
		return fmt.Errorf("aria2c not found")
	}
	headerUA := "User-Agent: " + req.UserAgent
	args := []string{
		"-c",
		"-x", strconv.Itoa(req.Threads),
		"-s", strconv.Itoa(req.Threads),
		"-k", "1M",
		"--max-tries=3",
		"--retry-wait=1",
		"--timeout=15",
		"--connect-timeout=10",
		"--file-allocation=none",
		"--header", headerUA,
		"-o", filepath.Base(req.OutPath),
		"-d", filepath.Dir(req.OutPath),
		req.AudioURL,
	}
	cmd := exec.CommandContext(ctx, "aria2c", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		if ctx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("aria2c timeout")
		}
		return fmt.Errorf("aria2c failed: %s", clipText(msg, 400))
	}
	return nil
}
