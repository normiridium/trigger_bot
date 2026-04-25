package yandexmusic

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
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
	FFmpegBin     string
	ForceMP3      bool
	EmbedLyrics   bool
}

func (d Downloader) SearchTracks(ctx context.Context, query string, limit int) ([]SearchTrack, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, errors.New("empty yandex music search query")
	}
	token := strings.TrimSpace(d.Token)
	if token == "" {
		return nil, errors.New("YA_MUSIC_TOKEN is not set")
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
	results, err := client.SearchTracks(ctx, query, 0)
	if err != nil {
		return nil, err
	}
	if len(results) == 0 {
		return nil, nil
	}
	if limit <= 0 {
		limit = 10
	}
	if len(results) > limit {
		results = results[:limit]
	}
	out := make([]SearchTrack, 0, len(results))
	for _, t := range results {
		id := int64(t.ID)
		if id <= 0 {
			continue
		}
		artist := ""
		if len(t.Artists) > 0 {
			artist = strings.TrimSpace(t.Artists[0].Name)
		}
		title := strings.TrimSpace(t.Title)
		albumID := 0
		if len(t.Albums) > 0 {
			albumID = t.Albums[0].ID
		}
		url := buildTrackURL(id, albumID)
		out = append(out, SearchTrack{
			ID:          id,
			Artist:      artist,
			Title:       title,
			URL:         url,
			DurationSec: float64(t.DurationMs) / 1000.0,
		})
	}
	return out, nil
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
	lyrics := ""
	if d.EmbedLyrics {
		lyrics, _ = client.TrackLyrics(ctx, trackID)
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
	sourceExt := extFromCodec(di.Codec)
	base := buildTrackBaseName(trackID, track)
	sourcePath := filepath.Join(tmpDir, base+".source."+sourceExt)
	if err := os.WriteFile(sourcePath, data, 0o644); err != nil {
		return "", err
	}
	meta := extractTrackMeta(track, lyrics)
	wantMP3 := d.ForceMP3
	targetExt := sourceExt
	if wantMP3 {
		targetExt = "mp3"
	}
	targetPath := filepath.Join(tmpDir, base+"."+targetExt)
	if err := transcodeAndTagAudio(ctx, d.ffmpegBin(), sourcePath, targetPath, wantMP3, meta); err != nil {
		if wantMP3 {
			return "", err
		}
		if renameErr := os.Rename(sourcePath, targetPath); renameErr != nil {
			return "", renameErr
		}
		return targetPath, nil
	}
	_ = os.Remove(sourcePath)
	return targetPath, nil
}

type trackMeta struct {
	Title   string
	Artist  string
	Album   string
	Year    int
	TrackNo int
	Lyrics  string
}

func extractTrackMeta(track *Track, lyrics string) trackMeta {
	if track == nil {
		return trackMeta{Lyrics: strings.TrimSpace(lyrics)}
	}
	out := trackMeta{
		Title:  strings.TrimSpace(track.Title),
		Lyrics: strings.TrimSpace(lyrics),
	}
	if len(track.Artists) > 0 {
		out.Artist = strings.TrimSpace(track.Artists[0].Name)
	}
	if len(track.Albums) > 0 {
		al := track.Albums[0]
		out.Album = strings.TrimSpace(al.Title)
		out.Year = al.Year
		if al.TrackPosition != nil && al.TrackPosition.Index > 0 {
			out.TrackNo = al.TrackPosition.Index
		}
	}
	return out
}

func (d Downloader) ffmpegBin() string {
	if v := strings.TrimSpace(d.FFmpegBin); v != "" {
		return v
	}
	return "ffmpeg"
}

func transcodeAndTagAudio(ctx context.Context, ffmpegBin, sourcePath, targetPath string, toMP3 bool, meta trackMeta) error {
	ffmpegBin = strings.TrimSpace(ffmpegBin)
	if ffmpegBin == "" {
		ffmpegBin = "ffmpeg"
	}
	args := []string{
		"-nostdin",
		"-y",
		"-i", sourcePath,
		"-map_metadata", "-1",
		"-vn",
	}
	if toMP3 {
		args = append(args,
			"-acodec", "libmp3lame",
			"-q:a", "2",
			"-id3v2_version", "3",
			"-write_id3v1", "0",
		)
	} else {
		args = append(args, "-codec", "copy")
	}
	if v := sanitizeMetaValue(meta.Title, 256); v != "" {
		args = append(args, "-metadata", "title="+v)
	}
	if v := sanitizeMetaValue(meta.Artist, 256); v != "" {
		args = append(args, "-metadata", "artist="+v)
	}
	if v := sanitizeMetaValue(meta.Album, 256); v != "" {
		args = append(args, "-metadata", "album="+v)
	}
	if meta.Year > 0 {
		args = append(args, "-metadata", "date="+strconv.Itoa(meta.Year))
	}
	if meta.TrackNo > 0 {
		args = append(args, "-metadata", "track="+strconv.Itoa(meta.TrackNo))
	}
	if v := sanitizeMetaValue(meta.Lyrics, 6000); v != "" {
		lang := normalizeID3LyricsLang(os.Getenv("YANDEX_MUSIC_LYRICS_LANG"))
		args = append(args, "-metadata", "lyrics-"+lang+"="+v)
		args = append(args, "-metadata", "lyrics="+v)
		args = append(args, "-metadata", "TEXT="+v)
		args = append(args, "-metadata", "comment="+v)
		args = append(args, "-metadata", "lyricist="+v)
	}
	args = append(args, targetPath)

	cmd := exec.CommandContext(ctx, ffmpegBin, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("ffmpeg yandex tag/transcode failed: %s", msg)
	}
	return nil
}

func sanitizeMetaValue(v string, maxRunes int) string {
	v = strings.ToValidUTF8(strings.TrimSpace(v), "")
	if v == "" {
		return ""
	}
	v = strings.ReplaceAll(v, "\x00", "")
	v = strings.ReplaceAll(v, "\u00A0", " ")
	v = strings.ReplaceAll(v, "\r\n", "\n")
	v = strings.ReplaceAll(v, "\r", "\n")
	if maxRunes > 0 {
		r := []rune(v)
		if len(r) > maxRunes {
			v = string(r[:maxRunes])
		}
	}
	return strings.TrimSpace(v)
}

func normalizeID3LyricsLang(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	if len(v) != 3 {
		return "eng"
	}
	for _, r := range v {
		if r < 'a' || r > 'z' {
			return "eng"
		}
	}
	return v
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

func buildTrackURL(trackID int64, albumID int) string {
	if albumID > 0 {
		return "https://music.yandex.ru/album/" + strconv.Itoa(albumID) + "/track/" + strconv.FormatInt(trackID, 10)
	}
	return "https://music.yandex.ru/track/" + strconv.FormatInt(trackID, 10)
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
