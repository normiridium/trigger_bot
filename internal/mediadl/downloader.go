package mediadl

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

var ErrUnsupportedURL = errors.New("unsupported media url")
var ErrTooLarge = errors.New("media file is too large")

const (
	MediaKindAudio = "audio"
	MediaKindVideo = "video"
	MediaKindPhoto = "photo"
)

type Downloader struct {
	YTDLPBin      string
	ProxySocks    string
	AudioFormat   string
	AudioQuality  string
	ExtractorArgs string
	MaxSizeMB     int
	MaxHeight     int
}

type ProbeResult struct {
	Title      string
	Artist     string
	SizeBytes  int64
	Duration   float64
	Service    string
	SourceURL  string
	Restricted bool
}

type DownloadResult struct {
	FilePath   string
	Title      string
	Artist     string
	MediaKind  string
	SizeBytes  int64
	Duration   float64
	Service    string
	SourceURL  string
	Restricted bool
}

type probeJSON struct {
	Title            string         `json:"title"`
	Artist           string         `json:"artist"`
	Uploader         string         `json:"uploader"`
	Channel          string         `json:"channel"`
	Creator          string         `json:"creator"`
	Duration         float64        `json:"duration"`
	Filesize         *int64         `json:"filesize"`
	FilesizeApprox   *int64         `json:"filesize_approx"`
	RequestedFormats []formatRecord `json:"requested_formats"`
	Formats          []formatRecord `json:"formats"`
}

type formatRecord struct {
	FormatID       string  `json:"format_id"`
	Acodec         string  `json:"acodec"`
	Vcodec         string  `json:"vcodec"`
	Height         int     `json:"height"`
	Filesize       *int64  `json:"filesize"`
	FilesizeApprox *int64  `json:"filesize_approx"`
	Duration       float64 `json:"duration"`
}

func (d Downloader) DownloadAudioFromURL(ctx context.Context, rawURL string) (DownloadResult, error) {
	probe, err := d.probeWithFormat(ctx, rawURL, d.audioFormatSelector())
	if err != nil {
		return DownloadResult{}, err
	}
	if limit := d.maxSizeMB(); limit > 0 && probe.SizeBytes > int64(limit)*1024*1024 {
		return DownloadResult{}, fmt.Errorf("%w: %d > %d MB", ErrTooLarge, probe.SizeBytes, limit)
	}

	tmpDir, err := os.MkdirTemp("", "media-audio-*")
	if err != nil {
		return DownloadResult{}, err
	}
	outTpl := filepath.Join(tmpDir, "%(title)s.%(ext)s")
	args := d.buildDownloadArgs(probe.SourceURL, outTpl)

	path, err := d.runDownload(ctx, args)
	if err != nil {
		return DownloadResult{}, err
	}
	st, err := os.Stat(path)
	if err != nil {
		return DownloadResult{}, fmt.Errorf("downloaded file missing: %w", err)
	}
	if limit := d.maxSizeMB(); limit > 0 && st.Size() > int64(limit)*1024*1024 {
		_ = os.Remove(path)
		return DownloadResult{}, fmt.Errorf("%w: %d > %d MB", ErrTooLarge, st.Size(), limit)
	}
	return DownloadResult{
		FilePath:   path,
		Title:      probe.Title,
		Artist:     probe.Artist,
		MediaKind:  MediaKindAudio,
		SizeBytes:  st.Size(),
		Duration:   probe.Duration,
		Service:    probe.Service,
		SourceURL:  probe.SourceURL,
		Restricted: probe.Restricted,
	}, nil
}

func (d Downloader) DownloadVideoFromURL(ctx context.Context, rawURL string) (DownloadResult, error) {
	probe, err := d.probeWithFormat(ctx, rawURL, d.videoFormatSelector())
	if err != nil {
		return DownloadResult{}, err
	}
	if limit := d.maxSizeMB(); limit > 0 && probe.SizeBytes > int64(limit)*1024*1024 {
		return DownloadResult{}, fmt.Errorf("%w: %d > %d MB", ErrTooLarge, probe.SizeBytes, limit)
	}
	tmpDir, err := os.MkdirTemp("", "media-video-*")
	if err != nil {
		return DownloadResult{}, err
	}
	outTpl := filepath.Join(tmpDir, "%(title)s.%(ext)s")
	args := d.buildVideoDownloadArgs(probe.SourceURL, outTpl)
	path, err := d.runDownload(ctx, args)
	if err != nil {
		return DownloadResult{}, err
	}
	st, err := os.Stat(path)
	if err != nil {
		return DownloadResult{}, fmt.Errorf("downloaded file missing: %w", err)
	}
	if limit := d.maxSizeMB(); limit > 0 && st.Size() > int64(limit)*1024*1024 {
		_ = os.Remove(path)
		return DownloadResult{}, fmt.Errorf("%w: %d > %d MB", ErrTooLarge, st.Size(), limit)
	}
	return DownloadResult{
		FilePath:   path,
		Title:      probe.Title,
		Artist:     probe.Artist,
		MediaKind:  MediaKindVideo,
		SizeBytes:  st.Size(),
		Duration:   probe.Duration,
		Service:    probe.Service,
		SourceURL:  probe.SourceURL,
		Restricted: probe.Restricted,
	}, nil
}

func (d Downloader) DownloadMediaAutoFromURL(ctx context.Context, rawURL string) (DownloadResult, error) {
	probe, err := d.probeWithFormat(ctx, rawURL, "")
	if err != nil {
		return DownloadResult{}, err
	}
	if limit := d.maxSizeMB(); limit > 0 && probe.SizeBytes > int64(limit)*1024*1024 {
		return DownloadResult{}, fmt.Errorf("%w: %d > %d MB", ErrTooLarge, probe.SizeBytes, limit)
	}
	tmpDir, err := os.MkdirTemp("", "media-auto-*")
	if err != nil {
		return DownloadResult{}, err
	}
	outTpl := filepath.Join(tmpDir, "%(title)s.%(ext)s")
	args := d.buildGenericDownloadArgs(probe.SourceURL, outTpl)
	path, err := d.runDownload(ctx, args)
	if err != nil {
		return DownloadResult{}, err
	}
	st, err := os.Stat(path)
	if err != nil {
		return DownloadResult{}, fmt.Errorf("downloaded file missing: %w", err)
	}
	if limit := d.maxSizeMB(); limit > 0 && st.Size() > int64(limit)*1024*1024 {
		_ = os.Remove(path)
		return DownloadResult{}, fmt.Errorf("%w: %d > %d MB", ErrTooLarge, st.Size(), limit)
	}
	return DownloadResult{
		FilePath:   path,
		Title:      probe.Title,
		Artist:     probe.Artist,
		MediaKind:  inferMediaKindByPath(path),
		SizeBytes:  st.Size(),
		Duration:   probe.Duration,
		Service:    probe.Service,
		SourceURL:  probe.SourceURL,
		Restricted: probe.Restricted,
	}, nil
}

func (d Downloader) Probe(ctx context.Context, rawURL string) (ProbeResult, error) {
	return d.probeWithFormat(ctx, rawURL, d.audioFormatSelector())
}

func (d Downloader) probeWithFormat(ctx context.Context, rawURL, formatSelector string) (ProbeResult, error) {
	normURL, service, ok := NormalizeSupportedURL(rawURL)
	if !ok {
		return ProbeResult{}, ErrUnsupportedURL
	}
	args := d.buildProbeArgs(normURL, formatSelector)
	out, err := d.runJSON(ctx, args)
	if err != nil {
		return ProbeResult{}, err
	}
	artist := firstNonEmpty(strings.TrimSpace(out.Artist), strings.TrimSpace(out.Uploader), strings.TrimSpace(out.Channel), strings.TrimSpace(out.Creator))
	return ProbeResult{
		Title:      strings.TrimSpace(out.Title),
		Artist:     artist,
		SizeBytes:  pickSize(out),
		Duration:   out.Duration,
		Service:    service,
		SourceURL:  normURL,
		Restricted: false,
	}, nil
}

func (d Downloader) runJSON(ctx context.Context, args []string) (probeJSON, error) {
	cmd := exec.CommandContext(ctx, d.binary(), args...)
	cmd.Env = append(os.Environ(), "PYTHONUNBUFFERED=1")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return probeJSON{}, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return probeJSON{}, err
	}
	if err := cmd.Start(); err != nil {
		return probeJSON{}, err
	}
	var body []byte
	var errRaw []byte
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		body, _ = io.ReadAll(stdout)
	}()
	go func() {
		defer wg.Done()
		errRaw, _ = io.ReadAll(stderr)
	}()
	wg.Wait()
	if err := cmd.Wait(); err != nil {
		return probeJSON{}, fmt.Errorf("yt-dlp probe failed: %w: %s", err, strings.TrimSpace(string(errRaw)))
	}
	var out probeJSON
	if err := json.Unmarshal(body, &out); err != nil {
		return probeJSON{}, fmt.Errorf("yt-dlp probe decode failed: %w", err)
	}
	return out, nil
}

func (d Downloader) runDownload(ctx context.Context, args []string) (string, error) {
	cmd := exec.CommandContext(ctx, d.binary(), args...)
	cmd.Env = append(os.Environ(), "PYTHONUNBUFFERED=1")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return "", err
	}
	if err := cmd.Start(); err != nil {
		return "", err
	}

	errBuf := new(strings.Builder)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		raw, _ := io.ReadAll(stderr)
		errBuf.Write(raw)
	}()
	var outPath string
	scan := bufio.NewScanner(stdout)
	for scan.Scan() {
		line := strings.TrimSpace(scan.Text())
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "__FILE__") {
			outPath = strings.TrimSpace(strings.TrimPrefix(line, "__FILE__"))
			continue
		}
		outPath = line
	}
	wg.Wait()
	if err := cmd.Wait(); err != nil {
		return "", fmt.Errorf("yt-dlp failed: %w: %s", err, strings.TrimSpace(errBuf.String()))
	}
	if outPath == "" {
		return "", errors.New("yt-dlp returned empty output path")
	}
	return outPath, nil
}

func (d Downloader) buildProbeArgs(url, formatSelector string) []string {
	args := []string{
		"--no-playlist",
		"--skip-download",
		"--quiet",
		"--no-warnings",
		"--extractor-args", d.extractorArgs(),
		"--dump-single-json",
		url,
	}
	if strings.TrimSpace(formatSelector) != "" {
		args = append(args[:len(args)-1], "-f", formatSelector, url)
	}
	if proxy := strings.TrimSpace(d.ProxySocks); proxy != "" {
		args = append([]string{"--proxy", "socks5://" + proxy}, args...)
	}
	return args
}

func (d Downloader) buildDownloadArgs(url, outTpl string) []string {
	args := []string{
		"-f", d.audioFormatSelector(),
		"-x",
		"--audio-format", d.audioFormat(),
		"--audio-quality", d.audioQuality(),
		"--no-playlist",
		"--quiet",
		"--no-warnings",
		"--extractor-args", d.extractorArgs(),
		"--print", "after_move:__FILE__%(filepath)s",
		"-o", outTpl,
	}
	if maxMB := d.maxSizeMB(); maxMB > 0 {
		args = append(args, "--max-filesize", strconv.Itoa(maxMB)+"M")
	}
	args = append(args, url)
	if proxy := strings.TrimSpace(d.ProxySocks); proxy != "" {
		args = append([]string{"--proxy", "socks5://" + proxy}, args...)
	}
	return args
}

func (d Downloader) buildVideoDownloadArgs(url, outTpl string) []string {
	args := []string{
		"-f", d.videoFormatSelector(),
		"--merge-output-format", "mp4",
		"--no-playlist",
		"--quiet",
		"--no-warnings",
		"--extractor-args", d.extractorArgs(),
		"--print", "after_move:__FILE__%(filepath)s",
		"-o", outTpl,
	}
	if maxMB := d.maxSizeMB(); maxMB > 0 {
		args = append(args, "--max-filesize", strconv.Itoa(maxMB)+"M")
	}
	args = append(args, url)
	if proxy := strings.TrimSpace(d.ProxySocks); proxy != "" {
		args = append([]string{"--proxy", "socks5://" + proxy}, args...)
	}
	return args
}

func (d Downloader) buildGenericDownloadArgs(url, outTpl string) []string {
	args := []string{
		"--no-playlist",
		"--quiet",
		"--no-warnings",
		"--extractor-args", d.extractorArgs(),
		"--print", "after_move:__FILE__%(filepath)s",
		"-o", outTpl,
	}
	if maxMB := d.maxSizeMB(); maxMB > 0 {
		args = append(args, "--max-filesize", strconv.Itoa(maxMB)+"M")
	}
	args = append(args, url)
	if proxy := strings.TrimSpace(d.ProxySocks); proxy != "" {
		args = append([]string{"--proxy", "socks5://" + proxy}, args...)
	}
	return args
}

func pickSize(meta probeJSON) int64 {
	if meta.Filesize != nil && *meta.Filesize > 0 {
		return *meta.Filesize
	}
	if meta.FilesizeApprox != nil && *meta.FilesizeApprox > 0 {
		return *meta.FilesizeApprox
	}
	if len(meta.RequestedFormats) > 0 {
		var sum int64
		for _, f := range meta.RequestedFormats {
			sz := formatSize(f)
			if sz <= 0 {
				return 0
			}
			sum += sz
		}
		if sum > 0 {
			return sum
		}
	}
	for _, f := range meta.Formats {
		sz := formatSize(f)
		if sz > 0 {
			return sz
		}
	}
	return 0
}

func formatSize(f formatRecord) int64 {
	if f.Filesize != nil && *f.Filesize > 0 {
		return *f.Filesize
	}
	if f.FilesizeApprox != nil && *f.FilesizeApprox > 0 {
		return *f.FilesizeApprox
	}
	return 0
}

func (d Downloader) binary() string {
	bin := strings.TrimSpace(d.YTDLPBin)
	if bin == "" {
		return "yt-dlp"
	}
	return bin
}

func (d Downloader) audioFormat() string {
	if v := strings.TrimSpace(d.AudioFormat); v != "" {
		return v
	}
	return "mp3"
}

func (d Downloader) audioQuality() string {
	if v := strings.TrimSpace(d.AudioQuality); v != "" {
		return v
	}
	return "320K"
}

func (d Downloader) extractorArgs() string {
	if v := strings.TrimSpace(d.ExtractorArgs); v != "" {
		return v
	}
	return "youtube:player_client=android,web"
}

func (d Downloader) maxSizeMB() int {
	if d.MaxSizeMB > 0 {
		return d.MaxSizeMB
	}
	return 100
}

func (d Downloader) maxHeight() int {
	if d.MaxHeight > 0 {
		return d.MaxHeight
	}
	return 720
}

func (d Downloader) audioFormatSelector() string {
	// Keep audio-first for speed; fallback to <=maxHeight video where service has no pure audio stream.
	return fmt.Sprintf("bestaudio/best[height<=%d]/best", d.maxHeight())
}

func (d Downloader) videoFormatSelector() string {
	return fmt.Sprintf("bestvideo[height<=%d]+bestaudio/best[height<=%d]/best", d.maxHeight(), d.maxHeight())
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func inferMediaKindByPath(path string) string {
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(strings.TrimSpace(path)), "."))
	switch ext {
	case "jpg", "jpeg", "png", "webp", "avif":
		return MediaKindPhoto
	case "mp4", "m4v", "mov", "webm", "mkv":
		return MediaKindVideo
	case "mp3", "m4a", "flac", "wav", "opus", "ogg":
		return MediaKindAudio
	default:
		return MediaKindVideo
	}
}
