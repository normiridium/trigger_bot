package yandexmusic

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

type Downloader struct {
	Token         string
	Quality       int
	TimeoutSec    int
	Tries         int
	RetryDelaySec int
}

func (d Downloader) DownloadByURL(ctx context.Context, rawURL string) (string, error) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return "", errors.New("empty yandex music url")
	}
	token := strings.TrimSpace(d.Token)
	if token == "" {
		return "", errors.New("YA_MUSIC_TOKEN is not set")
	}
	trackID, err := extractTrackID(rawURL)
	if err != nil {
		return "", err
	}

	tmpDir, err := os.MkdirTemp("", "yandex-music-*")
	if err != nil {
		return "", err
	}

	quality := d.Quality
	if quality < 0 || quality > 2 {
		quality = 1
	}
	timeoutSec := d.TimeoutSec
	if timeoutSec <= 0 {
		timeoutSec = 45
	}
	tries := d.Tries
	if tries <= 0 {
		tries = 6
	}
	retryDelaySec := d.RetryDelaySec
	if retryDelaySec <= 0 {
		retryDelaySec = 2
	}
	client := NewClient(token, timeoutSec, tries, retryDelaySec)

	track, err := client.Track(ctx, trackID)
	if err != nil {
		return "", fmt.Errorf("yandex track lookup failed: %w", err)
	}
	var (
		di      *DownloadInfo
		lastErr error
	)
	for _, q := range qualityFallbacks(quality) {
		di, err = client.GetFileInfo(ctx, trackID, apiQuality(q))
		if err == nil && di != nil && len(di.URLs) > 0 {
			lastErr = nil
			break
		}
		lastErr = err
	}
	if lastErr != nil {
		return "", fmt.Errorf("yandex get-file-info failed: %w", lastErr)
	}
	data, err := client.DownloadTrack(ctx, di)
	if err != nil {
		return "", fmt.Errorf("yandex track download failed: %w", err)
	}
	if len(data) == 0 {
		return "", errors.New("yandex track download is empty")
	}
	ext := extFromCodec(di.Codec)
	base := buildTrackBaseName(trackID, track)
	path := filepath.Join(tmpDir, base+"."+ext)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", err
	}
	return path, nil
}

func apiQuality(q int) string {
	switch q {
	case 0:
		return "lq"
	case 2:
		return "lossless"
	default:
		return "nq"
	}
}

func qualityFallbacks(preferred int) []int {
	base := []int{0, 1, 2}
	out := make([]int, 0, 3)
	seen := map[int]struct{}{}
	if preferred >= 0 && preferred <= 2 {
		out = append(out, preferred)
		seen[preferred] = struct{}{}
	}
	for _, q := range base {
		if _, ok := seen[q]; ok {
			continue
		}
		out = append(out, q)
	}
	return out
}

func extFromCodec(codec string) string {
	switch strings.ToLower(strings.TrimSpace(codec)) {
	case "flac", "flac-mp4":
		return "flac"
	case "aac", "aac-mp4", "he-aac", "he-aac-mp4":
		return "m4a"
	case "mp3":
		return "mp3"
	default:
		return "mp3"
	}
}

var trackIDRe = regexp.MustCompile(`(?:^|/)track/(\d+)(?:$|[/?#])`)

func extractTrackID(raw string) (int64, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return 0, fmt.Errorf("bad yandex music url: %w", err)
	}
	host := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(u.Hostname())), "www.")
	if host != "music.yandex.ru" && host != "music.yandex.com" {
		return 0, errors.New("url is not yandex music")
	}
	m := trackIDRe.FindStringSubmatch(strings.TrimSpace(u.Path))
	if len(m) != 2 {
		return 0, errors.New("track id is missing in yandex music url")
	}
	id, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil || id <= 0 {
		return 0, errors.New("invalid track id in yandex music url")
	}
	return id, nil
}

func safeName(s string) string {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, "/", "-")
	s = strings.ReplaceAll(s, "\\", "-")
	s = strings.ReplaceAll(s, ":", "-")
	s = strings.ReplaceAll(s, "\"", "")
	s = strings.ReplaceAll(s, "*", "")
	s = strings.ReplaceAll(s, "?", "")
	s = strings.ReplaceAll(s, "<", "")
	s = strings.ReplaceAll(s, ">", "")
	s = strings.ReplaceAll(s, "|", "")
	s = strings.TrimSpace(s)
	if len(s) > 64 {
		s = s[:64]
	}
	return s
}

func buildTrackBaseName(trackID int64, track *Track) string {
	if track == nil {
		return strconv.FormatInt(trackID, 10)
	}
	title := safeName(track.Title)
	artist := ""
	if len(track.Artists) > 0 {
		artist = safeName(track.Artists[0].Name)
	}
	switch {
	case artist != "" && title != "":
		return artist + " - " + title
	case title != "":
		return title
	default:
		return strconv.FormatInt(trackID, 10)
	}
}
