package mediadl

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

type progressCallbackKey struct{}

// WithProgressCallback injects download progress callback into context.
// Callback receives percent in [0..100].
func WithProgressCallback(ctx context.Context, cb func(percent float64)) context.Context {
	if cb == nil {
		return ctx
	}
	return context.WithValue(ctx, progressCallbackKey{}, cb)
}

func progressCallbackFromContext(ctx context.Context) func(float64) {
	if ctx == nil {
		return nil
	}
	v := ctx.Value(progressCallbackKey{})
	if v == nil {
		return nil
	}
	cb, _ := v.(func(float64))
	return cb
}

var ErrUnsupportedURL = errors.New("unsupported media url")
var ErrTooLarge = errors.New("media file is too large")

const (
	MediaKindAudio MediaKind = "audio"
	MediaKindVideo MediaKind = "video"
	MediaKindPhoto MediaKind = "photo"
)

type MediaKind string

type Downloader struct {
	YTDLPBin           string
	ProxySocks         string
	AudioFormat        string
	AudioQuality       string
	ExtractorArgs      string
	CookiesFile        string
	CookiesFromBrowser string
	MaxSizeMB          int
	MaxHeight          int
}

func (d Downloader) withVKProxyArgs(service Service, args []string) []string {
	if service != ServiceVK {
		return args
	}
	proxy := strings.TrimSpace(d.ProxySocks)
	if proxy == "" {
		return args
	}
	return append([]string{"--proxy", "socks5://" + proxy}, args...)
}

func (d Downloader) ConfiguredMaxSizeMB() int {
	return d.MaxSizeMB
}

func (d Downloader) ConfiguredMaxHeight() int {
	return d.MaxHeight
}

type ProbeResult struct {
	Title       string
	Description string
	Artist      string
	SizeBytes   int64
	Duration    float64
	Thumbnail   string
	Service     Service
	SourceURL   string
	Restricted  bool
}

type SearchTrack struct {
	ID          string
	SourceURL   string
	Artist      string
	Title       string
	DurationSec float64
}

type DownloadResult struct {
	FilePath    string
	Title       string
	Description string
	Artist      string
	MediaKind   MediaKind
	SizeBytes   int64
	Duration    float64
	Service     Service
	SourceURL   string
	Restricted  bool
}

type probeJSON struct {
	Title            string         `json:"title"`
	Description      string         `json:"description"`
	Artist           string         `json:"artist"`
	Uploader         string         `json:"uploader"`
	Channel          string         `json:"channel"`
	Creator          string         `json:"creator"`
	ID               string         `json:"id"`
	WebpageURL       string         `json:"webpage_url"`
	URL              string         `json:"url"`
	Duration         float64        `json:"duration"`
	Thumbnail        string         `json:"thumbnail"`
	Filesize         *int64         `json:"filesize"`
	FilesizeApprox   *int64         `json:"filesize_approx"`
	RequestedFormats []formatRecord `json:"requested_formats"`
	Formats          []formatRecord `json:"formats"`
	Entries          []probeJSON    `json:"entries"`
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
	path, err := d.downloadAudioWithFallbacks(ctx, probe.Service, probe.SourceURL, outTpl)
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
		FilePath:    path,
		Title:       displayTitleForService(probe.Service, MediaKindAudio, probe.Title, probe.Description),
		Description: probe.Description,
		Artist:      probe.Artist,
		MediaKind:   MediaKindAudio,
		SizeBytes:   st.Size(),
		Duration:    probe.Duration,
		Service:     probe.Service,
		SourceURL:   probe.SourceURL,
		Restricted:  probe.Restricted,
	}, nil
}

func (d Downloader) SearchSoundCloudTracks(ctx context.Context, query string, limit int) ([]SearchTrack, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, errors.New("empty soundcloud search query")
	}
	if limit < 1 {
		limit = 10
	}
	if limit > 20 {
		limit = 20
	}
	meta, err := d.runJSON(ctx, d.buildSoundCloudSearchArgs(query, limit))
	if err != nil {
		return nil, err
	}
	tracks := make([]SearchTrack, 0, len(meta.Entries))
	seen := make(map[string]struct{}, len(meta.Entries))
	for _, entry := range meta.Entries {
		sourceURL := strings.TrimSpace(entry.WebpageURL)
		if sourceURL == "" && isSoundCloudHTTPURL(entry.URL) {
			sourceURL = strings.TrimSpace(entry.URL)
		}
		if sourceURL == "" {
			continue
		}
		if _, ok := seen[sourceURL]; ok {
			continue
		}
		seen[sourceURL] = struct{}{}
		artist := firstNonEmpty(strings.TrimSpace(entry.Artist), strings.TrimSpace(entry.Uploader), strings.TrimSpace(entry.Creator), strings.TrimSpace(entry.Channel))
		tracks = append(tracks, SearchTrack{
			ID:          strings.TrimSpace(entry.ID),
			SourceURL:   sourceURL,
			Artist:      artist,
			Title:       strings.TrimSpace(entry.Title),
			DurationSec: entry.Duration,
		})
		if len(tracks) >= limit {
			break
		}
	}
	return tracks, nil
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
	path, err := d.downloadVideoWithFallbacks(ctx, probe.Service, probe.SourceURL, outTpl)
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
		FilePath:    path,
		Title:       displayTitleForService(probe.Service, MediaKindVideo, probe.Title, probe.Description),
		Description: probe.Description,
		Artist:      probe.Artist,
		MediaKind:   MediaKindVideo,
		SizeBytes:   st.Size(),
		Duration:    probe.Duration,
		Service:     probe.Service,
		SourceURL:   probe.SourceURL,
		Restricted:  probe.Restricted,
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
	args := d.buildGenericDownloadArgsForService(probe.Service, probe.SourceURL, outTpl)
	path, err := d.runDownload(ctx, args)
	if err != nil && probe.Service == ServicePinterest && isYTDLPNoVideoFormats(err) && strings.TrimSpace(probe.Thumbnail) != "" {
		path, err = d.downloadPinterestThumbnail(ctx, probe.Thumbnail, tmpDir)
	}
	if err != nil && probe.Service == ServiceTikTok && isYTDLPFormatUnavailable(err) {
		// Fallback to generic auto-selection only when explicit AV merge is unavailable.
		path, err = d.runDownload(ctx, d.buildGenericDownloadArgs(probe.Service, probe.SourceURL, outTpl))
	}
	if err != nil && probe.Service == ServiceTikTok && isYTDLPTikTokNoDataBlocks(err) {
		// Transient TikTok CDN glitch: force IPv4 and retry once.
		path, err = d.runDownload(ctx, d.withTikTokRetryArgs(args))
	}
	if err != nil && probe.Service == ServiceTikTok && isYTDLPTikTokNoDataBlocks(err) {
		// Last fallback: generic selector + IPv4.
		path, err = d.runDownload(ctx, d.withTikTokRetryArgs(d.buildGenericDownloadArgs(probe.Service, probe.SourceURL, outTpl)))
	}
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
	mediaKind := inferMediaKindByPath(path)
	return DownloadResult{
		FilePath:    path,
		Title:       displayTitleForService(probe.Service, mediaKind, probe.Title, probe.Description),
		Description: probe.Description,
		Artist:      probe.Artist,
		MediaKind:   mediaKind,
		SizeBytes:   st.Size(),
		Duration:    probe.Duration,
		Service:     probe.Service,
		SourceURL:   probe.SourceURL,
		Restricted:  probe.Restricted,
	}, nil
}

func (d Downloader) buildGenericDownloadArgsForService(service Service, url, outTpl string) []string {
	switch service {
	case ServiceTikTok:
		return d.buildTikTokAudioSafeArgs(service, url, outTpl)
	default:
		return d.buildGenericDownloadArgs(service, url, outTpl)
	}
}

func (d Downloader) buildTikTokAudioSafeArgs(service Service, url, outTpl string) []string {
	args := []string{
		// Prefer TikTok "download" rendition first: it is usually the most stable AV mux.
		// Then fallback to generic formats with explicit audio requirement.
		"-f", "download/best[acodec!=none][vcodec!=none]/best[acodec!=none]/best",
		"--merge-output-format", "mp4",
		"--no-playlist",
		"--quiet",
		"--no-warnings",
		"--retries", "8",
		"--fragment-retries", "8",
		"--file-access-retries", "3",
		"--extractor-args", d.extractorArgs(),
		"--print", "after_move:__FILE__%(filepath)s",
		"-o", outTpl,
	}
	args = append(d.ytDLPAuthArgs(), args...)
	if maxMB := d.maxSizeMB(); maxMB > 0 {
		args = append(args, "--max-filesize", strconv.Itoa(maxMB)+"M")
	}
	args = append(args, url)
	return args
}

func (d Downloader) Probe(ctx context.Context, rawURL string) (ProbeResult, error) {
	return d.probeWithFormat(ctx, rawURL, d.audioFormatSelector())
}

func (d Downloader) probeWithFormat(ctx context.Context, rawURL, formatSelector string) (ProbeResult, error) {
	normURL, service, ok := NormalizeSupportedURL(rawURL)
	if !ok {
		return ProbeResult{}, ErrUnsupportedURL
	}
	if service != ServiceYouTube {
		// Non-YouTube extractors often don't expose/accept height constraints for audio probing.
		formatSelector = ""
	}
	args := d.withVKProxyArgs(service, d.buildProbeArgs(service, normURL, formatSelector))
	out, err := d.runJSON(ctx, args)
	if err != nil {
		if strings.TrimSpace(formatSelector) != "" && isYTDLPFormatUnavailable(err) {
			// Some YouTube videos expose inconsistent format maps for selected clients.
			// Retry metadata probe without explicit -f selector.
			out, err = d.runJSON(ctx, d.withVKProxyArgs(service, d.buildProbeArgs(service, normURL, "")))
		}
	}
	if err == nil && service == ServiceYouTube && !hasPlayableFormats(out) {
		out, err = d.runJSON(ctx, d.buildProbeArgsYouTubeAndroidNoAuth(normURL, formatSelector))
		if err != nil && strings.TrimSpace(formatSelector) != "" && isYTDLPFormatUnavailable(err) {
			out, err = d.runJSON(ctx, d.buildProbeArgsYouTubeAndroidNoAuth(normURL, ""))
		}
	}
	if err != nil {
		return ProbeResult{}, err
	}
	artist := firstNonEmpty(strings.TrimSpace(out.Artist), strings.TrimSpace(out.Uploader), strings.TrimSpace(out.Channel), strings.TrimSpace(out.Creator))
	return ProbeResult{
		Title:       strings.TrimSpace(out.Title),
		Description: cleanProbeDescription(out.Description),
		Artist:      artist,
		SizeBytes:   pickSize(out),
		Duration:    out.Duration,
		Thumbnail:   strings.TrimSpace(out.Thumbnail),
		Service:     service,
		SourceURL:   normURL,
		Restricted:  false,
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
	args = d.withProgressArgs(args)
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
	progressCB := progressCallbackFromContext(ctx)
	var outPath string
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		sc := bufio.NewScanner(stderr)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line == "" {
				continue
			}
			if progressCB != nil {
				if p, ok := parseYTDLPProgressPercent(line); ok {
					progressCB(p)
				}
			}
			errBuf.WriteString(line)
			errBuf.WriteByte('\n')
		}
	}()
	go func() {
		defer wg.Done()
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line == "" {
				continue
			}
			if progressCB != nil {
				if p, ok := parseYTDLPProgressPercent(line); ok {
					progressCB(p)
					continue
				}
			}
			if strings.HasPrefix(line, "__FILE__") {
				outPath = strings.TrimSpace(strings.TrimPrefix(line, "__FILE__"))
				continue
			}
			outPath = line
		}
	}()
	wg.Wait()
	if err := cmd.Wait(); err != nil {
		return "", fmt.Errorf("yt-dlp failed: %w: %s", err, strings.TrimSpace(errBuf.String()))
	}
	if outPath == "" {
		return "", errors.New("yt-dlp returned empty output path")
	}
	return outPath, nil
}

func (d Downloader) withProgressArgs(args []string) []string {
	// Keep quiet/no-warnings behavior, but force structured progress lines.
	// Marker is parsed in runDownload and not forwarded as output path.
	out := make([]string, 0, len(args)+6)
	out = append(out, args...)
	out = append(out,
		"--newline",
		"--progress",
		"--progress-template", "download:__YTDLP_PROGRESS__%(progress._percent_str)s",
	)
	return out
}

func parseYTDLPProgressPercent(line string) (float64, bool) {
	const marker = "__YTDLP_PROGRESS__"
	idx := strings.Index(line, marker)
	if idx < 0 {
		return 0, false
	}
	v := strings.TrimSpace(line[idx+len(marker):])
	v = strings.TrimSuffix(v, "%")
	v = strings.TrimSpace(v)
	if v == "" || strings.EqualFold(v, "N/A") {
		return 0, false
	}
	p, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return 0, false
	}
	if p < 0 {
		p = 0
	}
	if p > 100 {
		p = 100
	}
	return p, true
}

func (d Downloader) buildProbeArgs(service Service, url, formatSelector string) []string {
	args := []string{
		"--no-playlist",
		"--skip-download",
		"--quiet",
		"--no-warnings",
		"--ignore-no-formats-error",
		"--extractor-args", d.extractorArgs(),
		"--dump-single-json",
		url,
	}
	args = append(d.ytDLPAuthArgs(), args...)
	if strings.TrimSpace(formatSelector) != "" {
		args = append(args[:len(args)-1], "-f", formatSelector, url)
	}
	return args
}

func (d Downloader) buildSoundCloudSearchArgs(query string, limit int) []string {
	if limit < 1 {
		limit = 10
	}
	args := []string{
		"--flat-playlist",
		"--no-playlist",
		"--skip-download",
		"--quiet",
		"--no-warnings",
		"--dump-single-json",
		fmt.Sprintf("scsearch%d:%s", limit, strings.TrimSpace(query)),
	}
	args = append(d.ytDLPAuthArgs(), args...)
	return args
}

func isSoundCloudHTTPURL(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return false
	}
	host := strings.ToLower(strings.TrimSpace(u.Hostname()))
	return host == "soundcloud.com" || host == "www.soundcloud.com" || host == "m.soundcloud.com"
}

func (d Downloader) buildDownloadArgs(service Service, url, outTpl string) []string {
	return d.buildDownloadArgsWithOptions(service, url, outTpl, d.audioFormatSelector(), d.extractorArgs(), true)
}

func (d Downloader) buildDownloadArgsWithOptions(service Service, url, outTpl, formatSelector, extractorArgs string, includeAuth bool) []string {
	args := []string{
		"-f", formatSelector,
		"-x",
		"--audio-format", d.audioFormat(),
		"--audio-quality", d.audioQuality(),
		"--no-playlist",
		"--quiet",
		"--no-warnings",
		"--extractor-args", extractorArgs,
		"--print", "after_move:__FILE__%(filepath)s",
		"-o", outTpl,
	}
	if includeAuth {
		args = append(d.ytDLPAuthArgs(), args...)
	}
	if maxMB := d.maxSizeMB(); maxMB > 0 {
		args = append(args, "--max-filesize", strconv.Itoa(maxMB)+"M")
	}
	args = append(args, url)
	return args
}

func (d Downloader) buildVideoDownloadArgs(service Service, url, outTpl string) []string {
	return d.buildVideoDownloadArgsWithSelector(service, url, outTpl, d.videoFormatSelector())
}

func (d Downloader) buildVideoDownloadArgsWithSelector(service Service, url, outTpl, formatSelector string) []string {
	return d.buildVideoDownloadArgsWithOptions(service, url, outTpl, formatSelector, d.extractorArgs(), true)
}

func (d Downloader) buildVideoDownloadArgsWithOptions(service Service, url, outTpl, formatSelector, extractorArgs string, includeAuth bool) []string {
	args := []string{
		"-f", formatSelector,
		"--merge-output-format", "mp4",
		"--no-playlist",
		"--quiet",
		"--no-warnings",
		"--extractor-args", extractorArgs,
		"--print", "after_move:__FILE__%(filepath)s",
		"-o", outTpl,
	}
	if includeAuth {
		args = append(d.ytDLPAuthArgs(), args...)
	}
	if maxMB := d.maxSizeMB(); maxMB > 0 {
		args = append(args, "--max-filesize", strconv.Itoa(maxMB)+"M")
	}
	args = append(args, url)
	return args
}

func (d Downloader) buildGenericDownloadArgs(service Service, url, outTpl string) []string {
	args := []string{
		"--no-playlist",
		"--quiet",
		"--no-warnings",
		"--extractor-args", d.extractorArgs(),
		"--print", "after_move:__FILE__%(filepath)s",
		"-o", outTpl,
	}
	args = append(d.ytDLPAuthArgs(), args...)
	if maxMB := d.maxSizeMB(); maxMB > 0 {
		args = append(args, "--max-filesize", strconv.Itoa(maxMB)+"M")
	}
	args = append(args, url)
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

func (d Downloader) ytDLPAuthArgs() []string {
	if v := strings.TrimSpace(d.CookiesFromBrowser); v != "" {
		return []string{"--cookies-from-browser", v}
	}
	if v := strings.TrimSpace(d.CookiesFile); v != "" {
		return []string{"--cookies", v}
	}
	return nil
}

func isYTDLPFormatUnavailable(err error) bool {
	msg := strings.ToLower(strings.TrimSpace(err.Error()))
	return strings.Contains(msg, "requested format is not available")
}

func isYTDLPNoVideoFormats(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(strings.TrimSpace(err.Error()))
	return strings.Contains(msg, "no video formats found")
}

func isYTDLPTikTokNoDataBlocks(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(strings.TrimSpace(err.Error()))
	return strings.Contains(msg, "did not get any data blocks")
}

func (d Downloader) withTikTokRetryArgs(args []string) []string {
	if len(args) == 0 {
		return args
	}
	// URL is expected as the last arg in our builders.
	head := append([]string{}, args[:len(args)-1]...)
	tail := args[len(args)-1]
	head = append(head, "--force-ipv4")
	head = append(head, tail)
	return head
}

func isYTDLPYouTubeRetryable(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(strings.TrimSpace(err.Error()))
	if strings.Contains(msg, "requested format is not available") {
		return true
	}
	if strings.Contains(msg, "sign in to confirm you're not a bot") || strings.Contains(msg, "sign in to confirm you’re not a bot") {
		return true
	}
	if strings.Contains(msg, "n challenge solving failed") {
		return true
	}
	if strings.Contains(msg, "only images are available for download") || strings.Contains(msg, "storyboard") {
		return true
	}
	return false
}

func hasPlayableFormats(meta probeJSON) bool {
	for _, f := range meta.Formats {
		if strings.EqualFold(strings.TrimSpace(f.Acodec), "none") && strings.EqualFold(strings.TrimSpace(f.Vcodec), "none") {
			continue
		}
		return true
	}
	return false
}

func (d Downloader) buildProbeArgsYouTubeAndroidNoAuth(url, formatSelector string) []string {
	args := []string{
		"--no-playlist",
		"--skip-download",
		"--quiet",
		"--no-warnings",
		"--ignore-no-formats-error",
		"--extractor-args", "youtube:player_client=android",
		"--dump-single-json",
		url,
	}
	if strings.TrimSpace(formatSelector) != "" {
		args = append(args[:len(args)-1], "-f", formatSelector, url)
	}
	return args
}

func (d Downloader) buildDownloadArgsYouTubeAndroidNoAuth(url, outTpl string) []string {
	return d.buildDownloadArgsWithOptions(ServiceYouTube, url, outTpl, d.audioFormatSelector(), "youtube:player_client=android", false)
}

func (d Downloader) audioFormatSelectorsForRetry() []string {
	return []string{
		d.audioFormatSelector(),
		"bestaudio/best",
		"18/best",
	}
}

func (d Downloader) downloadAudioWithFallbacks(ctx context.Context, service Service, url, outTpl string) (string, error) {
	if service != ServiceYouTube {
		return d.runDownload(ctx, d.withVKProxyArgs(service, d.buildDownloadArgsWithOptions(service, url, outTpl, "bestaudio/best", d.extractorArgs(), true)))
	}
	type candidate struct {
		format      string
		extractor   string
		includeAuth bool
	}
	ids, useAndroidNoAuth := d.discoverYouTubeAudioFormatIDs(ctx, url)
	candidates := make([]candidate, 0, 6)
	primaryExtractor := d.extractorArgs()
	primaryAuth := true
	if useAndroidNoAuth {
		primaryExtractor = "youtube:player_client=android"
		primaryAuth = false
	}
	for _, f := range ids {
		ff := strings.TrimSpace(f)
		if ff == "" {
			continue
		}
		candidates = append(candidates, candidate{format: ff, extractor: primaryExtractor, includeAuth: primaryAuth})
		if len(candidates) >= 3 {
			break
		}
	}
	for _, f := range d.audioFormatSelectorsForRetry() {
		ff := strings.TrimSpace(f)
		if ff == "" {
			continue
		}
		candidates = append(candidates, candidate{format: ff, extractor: primaryExtractor, includeAuth: primaryAuth})
	}
	seen := make(map[string]struct{}, len(candidates))
	var lastErr error
	attempts := 0
	for _, c := range candidates {
		key := fmt.Sprintf("%s|%s|%t", c.format, c.extractor, c.includeAuth)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		attempts++
		if attempts > 5 {
			break
		}
		path, err := d.runDownload(ctx, d.withVKProxyArgs(service, d.buildDownloadArgsWithOptions(service, url, outTpl, c.format, c.extractor, c.includeAuth)))
		if err == nil {
			return path, nil
		}
		lastErr = err
		if !isYTDLPYouTubeRetryable(err) {
			return "", err
		}
	}
	if !useAndroidNoAuth {
		// Final compact fallback to reduce request spam.
		for _, f := range []string{"bestaudio/best", "18/best"} {
			path, err := d.runDownload(ctx, d.withVKProxyArgs(service, d.buildDownloadArgsWithOptions(service, url, outTpl, f, "youtube:player_client=android", false)))
			if err == nil {
				return path, nil
			}
			lastErr = err
			if !isYTDLPYouTubeRetryable(err) {
				return "", err
			}
		}
	}
	if lastErr == nil {
		lastErr = errors.New("no yt-dlp candidates were attempted")
	}
	return "", lastErr
}

func (d Downloader) videoFormatSelectorsForRetry() []string {
	h := d.maxHeight()
	return []string{
		fmt.Sprintf("bestvideo[height<=%d]+bestaudio/best[height<=%d]/best", h, h),
		fmt.Sprintf("best[height<=%d]/best", h),
		"bestvideo+bestaudio/best",
		"best",
	}
}

func (d Downloader) downloadVideoWithFallbacks(ctx context.Context, service Service, url, outTpl string) (string, error) {
	if service == ServiceYouTube {
		return d.downloadYouTubeVideoWithFallbacks(ctx, service, url, outTpl)
	}
	selectors := d.videoFormatSelectorsForRetry()
	seen := make(map[string]struct{}, len(selectors))
	var lastErr error
	for _, s := range selectors {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		path, err := d.runDownload(ctx, d.withVKProxyArgs(service, d.buildVideoDownloadArgsWithSelector(service, url, outTpl, s)))
		if err == nil {
			return path, nil
		}
		lastErr = err
		if !isYTDLPFormatUnavailable(err) {
			return "", err
		}
	}
	if lastErr == nil {
		lastErr = errors.New("no yt-dlp video candidates were attempted")
	}
	return "", lastErr
}

func (d Downloader) downloadYouTubeVideoWithFallbacks(ctx context.Context, service Service, url, outTpl string) (string, error) {
	type candidate struct {
		format      string
		extractor   string
		includeAuth bool
	}

	ids, useAndroidNoAuth := d.discoverYouTubeVideoFormatIDs(ctx, url)
	candidates := make([]candidate, 0, 10)
	primaryExtractor := d.extractorArgs()
	primaryAuth := true
	if useAndroidNoAuth {
		primaryExtractor = "youtube:player_client=android"
		primaryAuth = false
	}

	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		candidates = append(candidates, candidate{format: id, extractor: primaryExtractor, includeAuth: primaryAuth})
		if len(candidates) >= 4 {
			break
		}
	}
	for _, s := range d.videoFormatSelectorsForRetry() {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		candidates = append(candidates, candidate{format: s, extractor: primaryExtractor, includeAuth: primaryAuth})
	}

	seen := make(map[string]struct{}, len(candidates))
	var lastErr error
	attempts := 0
	for _, c := range candidates {
		key := fmt.Sprintf("%s|%s|%t", c.format, c.extractor, c.includeAuth)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		attempts++
		if attempts > 7 {
			break
		}
		path, err := d.runDownload(ctx, d.withVKProxyArgs(service, d.buildVideoDownloadArgsWithOptions(service, url, outTpl, c.format, c.extractor, c.includeAuth)))
		if err == nil {
			return path, nil
		}
		lastErr = err
		if !isYTDLPYouTubeRetryable(err) {
			return "", err
		}
	}

	if !useAndroidNoAuth {
		for _, s := range []string{
			fmt.Sprintf("best[height<=%d]/best", d.maxHeight()),
			"best",
		} {
			path, err := d.runDownload(ctx, d.withVKProxyArgs(service, d.buildVideoDownloadArgsWithOptions(service, url, outTpl, s, "youtube:player_client=android", false)))
			if err == nil {
				return path, nil
			}
			lastErr = err
			if !isYTDLPYouTubeRetryable(err) {
				return "", err
			}
		}
	}

	if lastErr == nil {
		lastErr = errors.New("no yt-dlp video candidates were attempted")
	}
	return "", lastErr
}

func (d Downloader) discoverYouTubeAudioFormatIDs(ctx context.Context, url string) ([]string, bool) {
	seen := map[string]struct{}{}
	out := make([]string, 0, 8)
	appendIDs := func(meta probeJSON) {
		for _, id := range playableAudioFormatIDs(meta, d.maxHeight()) {
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			out = append(out, id)
		}
	}
	if meta, err := d.runJSON(ctx, d.buildProbeArgs(ServiceYouTube, url, "")); err == nil {
		appendIDs(meta)
		if len(out) > 0 {
			return out, false
		}
	}
	if meta, err := d.runJSON(ctx, d.buildProbeArgsYouTubeAndroidNoAuth(url, "")); err == nil {
		appendIDs(meta)
		return out, true
	}
	return out, false
}

func (d Downloader) discoverYouTubeVideoFormatIDs(ctx context.Context, url string) ([]string, bool) {
	seen := map[string]struct{}{}
	out := make([]string, 0, 8)
	appendIDs := func(meta probeJSON) {
		for _, id := range playableVideoFormatIDs(meta, d.maxHeight()) {
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			out = append(out, id)
		}
	}
	if meta, err := d.runJSON(ctx, d.buildProbeArgs(ServiceYouTube, url, "")); err == nil {
		appendIDs(meta)
		if len(out) > 0 {
			return out, false
		}
	}
	if meta, err := d.runJSON(ctx, d.buildProbeArgsYouTubeAndroidNoAuth(url, "")); err == nil {
		appendIDs(meta)
		return out, true
	}
	return out, false
}

func playableAudioFormatIDs(meta probeJSON, maxHeight int) []string {
	ids := make([]string, 0, 8)
	seen := map[string]struct{}{}
	add := func(id string) {
		id = strings.TrimSpace(id)
		if id == "" {
			return
		}
		if _, ok := seen[id]; ok {
			return
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	// Prefer audio-only formats first.
	for _, f := range meta.Formats {
		if strings.EqualFold(strings.TrimSpace(f.Acodec), "none") {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(f.Vcodec), "none") {
			continue
		}
		add(f.FormatID)
	}
	// Then progressive streams with embedded audio.
	for _, f := range meta.Formats {
		if strings.EqualFold(strings.TrimSpace(f.Acodec), "none") || strings.EqualFold(strings.TrimSpace(f.Vcodec), "none") {
			continue
		}
		if maxHeight > 0 && f.Height > 0 && f.Height > maxHeight {
			continue
		}
		add(f.FormatID)
	}
	return ids
}

func playableVideoFormatIDs(meta probeJSON, maxHeight int) []string {
	ids := make([]string, 0, 8)
	seen := map[string]struct{}{}
	add := func(id string) {
		id = strings.TrimSpace(id)
		if id == "" {
			return
		}
		if _, ok := seen[id]; ok {
			return
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	// Prefer progressive video+audio formats first (single file is more stable).
	for _, f := range meta.Formats {
		if strings.EqualFold(strings.TrimSpace(f.Vcodec), "none") || strings.EqualFold(strings.TrimSpace(f.Acodec), "none") {
			continue
		}
		if maxHeight > 0 && f.Height > 0 && f.Height > maxHeight {
			continue
		}
		add(f.FormatID)
	}
	// Then allow video-only IDs combined with bestaudio.
	for _, f := range meta.Formats {
		if strings.EqualFold(strings.TrimSpace(f.Vcodec), "none") {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(f.Acodec), "none") {
			continue
		}
		if maxHeight > 0 && f.Height > 0 && f.Height > maxHeight {
			continue
		}
		add(fmt.Sprintf("%s+bestaudio/best", strings.TrimSpace(f.FormatID)))
	}
	return ids
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

func (d Downloader) downloadPinterestThumbnail(ctx context.Context, rawURL, dir string) (string, error) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return "", errors.New("empty pinterest thumbnail url")
	}
	ext := mediaExtensionFromURL(rawURL, ".jpg")
	path := filepath.Join(dir, "pinterest-image"+ext)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("pinterest thumbnail download status=%d", resp.StatusCode)
	}
	out, err := os.Create(path)
	if err != nil {
		return "", err
	}
	limit := int64(d.maxSizeMB())*1024*1024 + 1
	written, copyErr := io.Copy(out, io.LimitReader(resp.Body, limit))
	closeErr := out.Close()
	if copyErr != nil {
		_ = os.Remove(path)
		return "", copyErr
	}
	if closeErr != nil {
		_ = os.Remove(path)
		return "", closeErr
	}
	if written <= 0 {
		_ = os.Remove(path)
		return "", errors.New("pinterest thumbnail download is empty")
	}
	if max := int64(d.maxSizeMB()) * 1024 * 1024; max > 0 && written > max {
		_ = os.Remove(path)
		return "", fmt.Errorf("%w: %d > %d MB", ErrTooLarge, written, d.maxSizeMB())
	}
	return path, nil
}

func mediaExtensionFromURL(rawURL, fallback string) string {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return fallback
	}
	ext := strings.ToLower(filepath.Ext(u.Path))
	switch ext {
	case ".jpg", ".jpeg", ".png", ".webp", ".avif", ".gif":
		return ext
	default:
		return fallback
	}
}

func displayTitleForService(service Service, mediaKind MediaKind, title, description string) string {
	title = strings.TrimSpace(title)
	_ = description
	if service != ServicePinterest || !isPinterestGeneratedTitle(title) {
		return title
	}
	switch mediaKind {
	case MediaKindPhoto:
		return "Pinterest photo"
	case MediaKindVideo:
		return "Pinterest video"
	case MediaKindAudio:
		return "Pinterest audio"
	default:
		return "Pinterest"
	}
}

func isPinterestGeneratedTitle(title string) bool {
	title = strings.TrimSpace(title)
	lower := strings.ToLower(title)
	if !strings.HasPrefix(lower, "pinterest") {
		return false
	}
	return strings.Contains(lower, "#") ||
		strings.Contains(lower, "video") ||
		strings.Contains(lower, "pin") ||
		strings.Contains(lower, "image")
}

func cleanProbeDescription(description string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(description)), " ")
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func inferMediaKindByPath(path string) MediaKind {
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
