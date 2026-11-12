package mediadl

// Bluesky (AT Protocol): картинки и видео постов.
//
// Ссылки вида bsky.app/profile/<handle>/post/<rkey> и fxbsky.app/... — это один
// и тот же пост. Медиа берём из публичного AppView без авторизации
// (app.bsky.feed.getPostThread): картинки с cdn.bsky.app в полном размере,
// видео — HLS-плейлист video.bsky.app, который забирает ffmpeg.

import (
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
	"regexp"
	"strings"
	"time"

	"trigger-admin-bot/internal/bottmp"
)

// BlueskyMedia — результат разбора поста.
type BlueskyMedia struct {
	Kind      string // "photo" или "video"
	Dir       string
	Paths     []string
	Title     string
	Author    string
	SourceURL string
	Service   Service
}

const (
	blueskyAPIBase     = "https://public.api.bsky.app/xrpc"
	blueskyUserAgent   = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"
	blueskyMaxImages   = 10
	blueskyMaxImageMB  = 20
	blueskyMaxVideoSec = 600
)

var (
	blueskyPostPathRe = regexp.MustCompile(`/profile/([^/]+)/post/([A-Za-z0-9]+)`)
	errBlueskyNoMedia = errors.New("в посте bluesky нет видео или картинок")
)

// IsBlueskyHost сообщает, обслуживаем ли мы этот хост.
func IsBlueskyHost(host string) bool {
	switch strings.ToLower(strings.TrimSpace(host)) {
	case "bsky.app", "www.bsky.app", "fxbsky.app", "www.fxbsky.app",
		"bsky.social", "www.bsky.social", "main.bsky.dev":
		return true
	}
	return false
}

// blueskyPostRef разбирает ссылку на пост: handle и rkey.
func blueskyPostRef(rawURL string) (handle, rkey string, ok bool) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return "", "", false
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", "", false
	}
	m := blueskyPostPathRe.FindStringSubmatch(u.Path)
	if m == nil {
		return "", "", false
	}
	return strings.TrimSpace(m[1]), strings.TrimSpace(m[2]), true
}

type blueskyImage struct {
	Fullsize string `json:"fullsize"`
	Thumb    string `json:"thumb"`
	Alt      string `json:"alt"`
}

type blueskyExternal struct {
	URI   string `json:"uri"`
	Thumb string `json:"thumb"`
}

type blueskyPostView struct {
	URI    string `json:"uri"`
	Author struct {
		Handle      string `json:"handle"`
		DisplayName string `json:"displayName"`
	} `json:"author"`
	Record struct {
		Text string `json:"text"`
	} `json:"record"`
	Embed *blueskyEmbed `json:"embed"`
}

type blueskyEmbed struct {
	Type      string           `json:"$type"`
	Images    []blueskyImage   `json:"images"`
	Playlist  string           `json:"playlist"`
	Thumbnail string           `json:"thumbnail"`
	Media     *blueskyEmbed    `json:"media"`
	External  *blueskyExternal `json:"external"`
	Record    *struct {
		Record *blueskyPostView `json:"record"`
	} `json:"record"`
}

type blueskyThreadResponse struct {
	Thread struct {
		Post *blueskyPostView `json:"post"`
	} `json:"thread"`
}

func blueskyHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout}
}

// blueskyThreadURI собирает AT-URI поста из handle и rkey.
func blueskyThreadURI(handle, rkey string) string {
	return "at://" + handle + "/app.bsky.feed.post/" + rkey
}

// blueskyFetchPost читает пост через публичный AppView.
func blueskyFetchPost(ctx context.Context, handle, rkey string) (*blueskyPostView, error) {
	endpoint := blueskyAPIBase + "/app.bsky.feed.getPostThread?uri=" + url.QueryEscape(blueskyThreadURI(handle, rkey)) + "&depth=0"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", blueskyUserAgent)
	req.Header.Set("Accept", "application/json")
	resp, err := blueskyHTTPClient(30 * time.Second).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("bluesky: HTTP %d (%s)", resp.StatusCode, clipBlueskyText(string(body), 160))
	}
	var parsed blueskyThreadResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("bluesky: разбор ответа: %w", err)
	}
	if parsed.Thread.Post == nil {
		return nil, errors.New("bluesky: пост не найден")
	}
	return parsed.Thread.Post, nil
}

// blueskyCollectMedia обходит вложения поста (включая recordWithMedia и
// цитируемый пост) и складывает ссылки на картинки и видео.
func blueskyCollectMedia(embed *blueskyEmbed, images *[]string, video *string, seen map[string]bool) {
	if embed == nil {
		return
	}
	if embed.Playlist != "" && *video == "" {
		*video = embed.Playlist
	}
	for _, image := range embed.Images {
		full := strings.TrimSpace(image.Fullsize)
		if full == "" {
			full = strings.TrimSpace(image.Thumb)
		}
		if full == "" || seen[full] || len(*images) >= blueskyMaxImages {
			continue
		}
		seen[full] = true
		*images = append(*images, full)
	}
	blueskyCollectMedia(embed.Media, images, video, seen)
	if embed.Record != nil && embed.Record.Record != nil {
		blueskyCollectMedia(embed.Record.Record.Embed, images, video, seen)
	}
}

// blueskyFullsizeURL добавляет к CDN-ссылке формат файла: без суффикса
// cdn.bsky.app отдаёт webp, а с @jpeg — обычный JPEG для Telegram.
func blueskyFullsizeURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if !strings.Contains(raw, "/img/feed_fullsize/") || strings.Contains(raw, "@") {
		return raw
	}
	return raw + "@jpeg"
}

// downloadBlueskyImage сохраняет картинку в каталог.
func downloadBlueskyImage(ctx context.Context, dir, imageURL string, index int) (string, error) {
	target := blueskyFullsizeURL(imageURL)
	outPath := filepath.Join(dir, fmt.Sprintf("bluesky-%02d.jpg", index+1))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", blueskyUserAgent)
	req.Header.Set("Accept", "image/jpeg,image/*,*/*")
	resp, err := blueskyHTTPClient(60 * time.Second).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("bluesky image: HTTP %d", resp.StatusCode)
	}
	limit := int64(blueskyMaxImageMB) << 20
	fh, err := os.Create(outPath)
	if err != nil {
		return "", err
	}
	written, copyErr := io.Copy(fh, io.LimitReader(resp.Body, limit))
	closeErr := fh.Close()
	if copyErr != nil {
		_ = os.Remove(outPath)
		return "", copyErr
	}
	if closeErr != nil {
		_ = os.Remove(outPath)
		return "", closeErr
	}
	if written == 0 {
		_ = os.Remove(outPath)
		return "", errors.New("bluesky image: пустой файл")
	}
	return outPath, nil
}

// downloadBlueskyVideo забирает HLS-плейлист video.bsky.app. Сначала пробуем
// скопировать дорожки без перекодирования, при отказе — перекодируем в h264/aac.
func downloadBlueskyVideo(ctx context.Context, dir, playlist string) (string, error) {
	outPath := filepath.Join(dir, "bluesky-video.mp4")
	variants := [][]string{
		{"-map", "0:v:0?", "-map", "0:a:0?", "-c", "copy", "-movflags", "+faststart"},
		{"-map", "0:v:0?", "-map", "0:a:0?", "-c:v", "libx264", "-preset", "veryfast", "-crf", "23", "-c:a", "aac", "-b:a", "128k", "-movflags", "+faststart"},
	}
	var lastErr error
	for _, tail := range variants {
		args := append([]string{
			"-nostdin", "-y", "-loglevel", "error",
			"-user_agent", blueskyUserAgent,
			"-i", playlist,
		}, tail...)
		args = append(args, outPath)
		_ = os.Remove(outPath) // следы предыдущей попытки
		vctx, cancel := context.WithTimeout(ctx, blueskyMaxVideoSec*time.Second)
		output, err := exec.CommandContext(vctx, "ffmpeg", args...).CombinedOutput()
		cancel()
		if err != nil {
			lastErr = fmt.Errorf("bluesky video: %w (%s)", err, clipBlueskyText(string(output), 200))
			continue
		}
		if st, statErr := os.Stat(outPath); statErr == nil && st.Size() > 0 {
			return outPath, nil
		}
		lastErr = errors.New("bluesky video: ffmpeg не создал файл")
	}
	if lastErr == nil {
		lastErr = errors.New("bluesky video: плейлист недоступен")
	}
	return "", lastErr
}

func clipBlueskyText(s string, limit int) string {
	runes := []rune(strings.TrimSpace(s))
	if len(runes) <= limit {
		return string(runes)
	}
	return string(runes[:limit]) + "…"
}

// BlueskyPost скачивает медиа поста Bluesky. supported=false — ссылка не наша.
func (d Downloader) BlueskyPost(ctx context.Context, rawURL string) (BlueskyMedia, bool, error) {
	normURL, service, ok := NormalizeSupportedURL(rawURL)
	if !ok || service != ServiceBluesky {
		return BlueskyMedia{}, false, nil
	}
	handle, rkey, ok := blueskyPostRef(normURL)
	if !ok {
		return BlueskyMedia{}, true, errors.New("bluesky: не понял ссылку (нужен /profile/<handle>/post/<rkey>)")
	}

	post, err := blueskyFetchPost(ctx, handle, rkey)
	if err != nil {
		return BlueskyMedia{}, true, err
	}

	var images []string
	var video string
	blueskyCollectMedia(post.Embed, &images, &video, map[string]bool{})

	dir, err := bottmp.MkdirTemp("media-bluesky-*")
	if err != nil {
		return BlueskyMedia{}, true, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }

	title := strings.TrimSpace(post.Record.Text)
	author := strings.TrimSpace(post.Author.DisplayName)
	if author == "" {
		author = strings.TrimSpace(post.Author.Handle)
	}
	result := BlueskyMedia{Dir: dir, Title: title, Author: author, SourceURL: normURL, Service: ServiceBluesky}

	if len(images) > 0 {
		for i, imageURL := range images {
			path, err := downloadBlueskyImage(ctx, dir, imageURL, i)
			if err != nil {
				cleanup()
				return BlueskyMedia{}, true, err
			}
			result.Paths = append(result.Paths, path)
		}
		result.Kind = "photo"
		return result, true, nil
	}
	if video != "" {
		path, err := downloadBlueskyVideo(ctx, dir, video)
		if err != nil {
			cleanup()
			return BlueskyMedia{}, true, err
		}
		result.Kind, result.Paths = "video", []string{path}
		return result, true, nil
	}

	cleanup()
	return BlueskyMedia{}, true, errBlueskyNoMedia
}

var _ = errBlueskyNoMedia
