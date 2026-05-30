package vkaudio

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	vkmusic "github.com/normiridium/vk-music-bot-api/vkmusic"
)

const defaultUserAgent = "VKAndroidApp/8.120-13180 (Android 13; SDK 33; arm64-v8a; Google Pixel 6 Pro; ru; 320dpi)"

type Track struct {
	ID          string
	Artist      string
	Title       string
	DurationSec float64
}

type DownloadResult struct {
	FilePath string
	Artist   string
	Title    string
	TrackID  string
}

type Downloader struct {
	Client       *vkmusic.Client
	UserAgent    string
	FFmpegBin    string
	MaxSizeMB    int
	TimeoutSec   int
	RetryCount   int
	RetryDelayMs int
	CookiesFile  string
	ProxyURL     string
	WebUserID    int
}

func NewDownloader(token, userAgent string) (Downloader, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return Downloader{UserAgent: userAgent}, nil
	}
	client, err := vkmusic.NewClient(token, userAgent)
	if err != nil {
		return Downloader{}, err
	}
	return Downloader{Client: client, UserAgent: userAgent}, nil
}

func (d Downloader) SearchTracks(ctx context.Context, query string, limit int) ([]Track, error) {
	if strings.TrimSpace(d.CookiesFile) != "" {
		tracks, err := d.webSearchTracks(ctx, query, limit)
		if err == nil || d.Client == nil {
			return tracks, err
		}
	}
	if d.Client == nil {
		return nil, errors.New("vk client is not configured")
	}
	items, err := d.Client.SearchTracks(ctx, query, limit)
	if err != nil {
		return nil, err
	}
	out := make([]Track, 0, len(items))
	for _, item := range items {
		out = append(out, Track{
			ID:          strings.TrimSpace(item.ID),
			Artist:      strings.TrimSpace(item.Artist),
			Title:       strings.TrimSpace(item.Title),
			DurationSec: float64(item.Duration),
		})
	}
	return out, nil
}

func (d Downloader) DownloadTrack(ctx context.Context, trackID string) (DownloadResult, error) {
	trackID = strings.TrimSpace(trackID)
	if trackID == "" {
		return DownloadResult{}, errors.New("empty vk track id")
	}
	if strings.TrimSpace(d.CookiesFile) != "" {
		res, err := d.webDownloadTrack(ctx, trackID)
		if err == nil || d.Client == nil {
			return res, err
		}
	}
	if d.Client == nil {
		return DownloadResult{}, errors.New("vk client is not configured")
	}
	lookupCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	song, err := d.Client.GetAudioURL(lookupCtx, trackID)
	cancel()
	if err != nil {
		return DownloadResult{}, err
	}
	if song == nil || strings.TrimSpace(song.URL) == "" {
		return DownloadResult{}, errors.New("vk returned empty audio url")
	}
	path, err := d.downloadAudioURL(ctx, song.URL)
	if err != nil {
		return DownloadResult{}, err
	}
	return DownloadResult{
		FilePath: path,
		Artist:   strings.TrimSpace(song.Artist),
		Title:    strings.TrimSpace(song.Title),
		TrackID:  strings.TrimSpace(song.TrackID),
	}, nil
}

func (d Downloader) DownloadFirstByQuery(ctx context.Context, query string, limit int) (DownloadResult, error) {
	tracks, err := d.SearchTracks(ctx, query, limit)
	if err != nil {
		return DownloadResult{}, err
	}
	if len(tracks) == 0 {
		return DownloadResult{}, errors.New("nothing found in VK Music")
	}
	var lastErr error
	for _, track := range tracks {
		if strings.TrimSpace(track.ID) == "" {
			continue
		}
		res, err := d.DownloadTrack(ctx, track.ID)
		if err == nil {
			if res.Artist == "" {
				res.Artist = track.Artist
			}
			if res.Title == "" {
				res.Title = track.Title
			}
			return res, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = errors.New("no downloadable VK tracks found")
	}
	return DownloadResult{}, lastErr
}

func (d Downloader) downloadAudioURL(ctx context.Context, audioURL string) (string, error) {
	audioURL = strings.TrimSpace(audioURL)
	if audioURL == "" {
		return "", errors.New("empty vk audio url")
	}
	dir, err := os.MkdirTemp("", "vk-audio-*")
	if err != nil {
		return "", err
	}
	outPath := filepath.Join(dir, "audio.mp3")

	retries := d.retryCount()
	var runErr error
	for attempt := 1; attempt <= retries; attempt++ {
		_ = os.Remove(outPath)
		runCtx, cancel := context.WithTimeout(ctx, time.Duration(d.timeoutSec())*time.Second)
		err := d.runFFmpeg(runCtx, audioURL, outPath)
		cancel()
		if err == nil {
			runErr = nil
			break
		}
		runErr = err
		if attempt < retries {
			time.Sleep(time.Duration(d.retryDelayMs()*attempt) * time.Millisecond)
		}
	}
	if runErr != nil {
		_ = os.RemoveAll(dir)
		return "", runErr
	}
	st, err := os.Stat(outPath)
	if err != nil {
		_ = os.RemoveAll(dir)
		return "", err
	}
	if st.Size() <= 0 {
		_ = os.RemoveAll(dir)
		return "", errors.New("ffmpeg produced empty VK audio file")
	}
	limit := int64(d.maxSizeMB()) << 20
	if st.Size() > limit {
		_ = os.RemoveAll(dir)
		return "", fmt.Errorf("vk audio too large: %d bytes (limit %d MB)", st.Size(), d.maxSizeMB())
	}
	return outPath, nil
}

func (d Downloader) runFFmpeg(ctx context.Context, audioURL, outPath string) error {
	args := []string{
		"-nostdin",
		"-hide_banner",
		"-loglevel", "warning",
		"-y",
		"-reconnect", "1",
		"-reconnect_streamed", "1",
		"-reconnect_at_eof", "1",
		"-reconnect_delay_max", "5",
		"-rw_timeout", "15000000",
		"-http_persistent", "0",
		"-headers", "Referer: https://vk.com/\r\nOrigin: https://vk.com\r\nAccept: */*\r\n",
		"-user_agent", d.userAgent(),
		"-protocol_whitelist", "file,http,https,tcp,tls,crypto",
		"-allowed_extensions", "ALL",
		"-i", audioURL,
		"-vn",
		"-acodec", "libmp3lame",
		"-b:a", "192k",
		outPath,
	}
	cmd := exec.CommandContext(ctx, d.ffmpegBin(), args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		if ctx.Err() == context.DeadlineExceeded {
			return errors.New("ffmpeg timeout")
		}
		return fmt.Errorf("ffmpeg failed: %s", clipText(msg, 400))
	}
	return nil
}

func (d Downloader) userAgent() string {
	if v := strings.TrimSpace(d.UserAgent); v != "" {
		return v
	}
	return defaultUserAgent
}

func (d Downloader) ffmpegBin() string {
	if v := strings.TrimSpace(d.FFmpegBin); v != "" {
		return v
	}
	return "ffmpeg"
}

func (d Downloader) maxSizeMB() int {
	if d.MaxSizeMB >= 5 {
		return d.MaxSizeMB
	}
	return 60
}

func (d Downloader) timeoutSec() int {
	if d.TimeoutSec >= 30 {
		return d.TimeoutSec
	}
	return 120
}

func (d Downloader) retryCount() int {
	if d.RetryCount >= 1 {
		return d.RetryCount
	}
	return 3
}

func (d Downloader) retryDelayMs() int {
	if d.RetryDelayMs >= 100 {
		return d.RetryDelayMs
	}
	return 500
}

func clipText(s string, max int) string {
	s = strings.TrimSpace(s)
	if max <= 0 || len([]rune(s)) <= max {
		return s
	}
	r := []rune(s)
	return string(r[:max]) + "..."
}
