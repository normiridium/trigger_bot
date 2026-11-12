package mediadl

// Reddit: видео и картинки.
//
// yt-dlp для Reddit теперь требует авторизации («Account authentication is
// required»), а публичный .json отдаёт 403. Зато .rss открыт без cookies и
// содержит всё нужное: заголовок, автора, ссылки на i.redd.it (полный размер)
// и v.redd.it (видео). Видео качается с HLS-плейлиста v.redd.it через ffmpeg —
// там же и звук, отдельная дорожка не нужна.

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"trigger-admin-bot/internal/bottmp"
)

// RedditMedia — результат разбора поста.
type RedditMedia struct {
	Kind      string // "photo" или "video"
	Dir       string
	Paths     []string
	Title     string
	Author    string
	SourceURL string
	Service   Service
}

const (
	redditUserAgent   = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"
	redditMaxImages   = 10
	redditMaxImageMB  = 20
	redditMaxVideoSec = 600
)

var (
	redditPostIDRe   = regexp.MustCompile(`/comments/([a-z0-9]{4,12})`)
	redditShortRe    = regexp.MustCompile(`^/([a-z0-9]{4,12})/?$`)
	redditMediaRe    = regexp.MustCompile(`https?://(?:i|preview|v)\.redd\.it/[^\s"'<>\\)]+`)
	redditVideoRe    = regexp.MustCompile(`v\.redd\.it/([A-Za-z0-9]+)`)
	errRedditNoMedia = errors.New("в посте нет видео или картинок")
)

// IsRedditHost сообщает, обслуживаем ли мы этот хост.
func IsRedditHost(host string) bool {
	switch strings.ToLower(strings.TrimSpace(host)) {
	case "reddit.com", "www.reddit.com", "old.reddit.com", "m.reddit.com",
		"np.reddit.com", "ru.reddit.com", "redd.it", "www.redd.it",
		"i.redd.it", "preview.redd.it", "v.redd.it":
		return true
	}
	return false
}

// redditPostRef разбирает ссылку: id поста или прямой медиа-URL.
func redditPostRef(rawURL string) (postID string, directMedia string, ok bool) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return "", "", false
	}
	host, path := "", ""
	if m := regexp.MustCompile(`^https?://([^/]+)(/[^?#]*)`).FindStringSubmatch(rawURL); m != nil {
		host, path = strings.ToLower(m[1]), m[2]
	} else {
		return "", "", false
	}
	host = strings.TrimPrefix(host, "www.")
	switch host {
	case "i.redd.it", "preview.redd.it":
		return "", rawURL, true
	case "v.redd.it":
		return "", rawURL, true
	case "redd.it":
		if m := redditShortRe.FindStringSubmatch(path); m != nil {
			return m[1], "", true
		}
	}
	if m := redditPostIDRe.FindStringSubmatch(path); m != nil {
		return m[1], "", true
	}
	return "", "", false
}

// redditRSSURL собирает адрес ленты поста.
func redditRSSURL(postID string) string {
	return fmt.Sprintf("https://www.reddit.com/comments/%s/.rss", postID)
}

func redditHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout}
}

// redditGet читает ленту с повтором на 429/5xx: Reddit ограничивает частоту,
// а повторы с паузой снимают случайные отказы.
func redditGet(ctx context.Context, rawURL string) ([]byte, error) {
	delays := []time.Duration{0, 3 * time.Second, 8 * time.Second}
	var lastErr error
	for _, delay := range delays {
		if delay > 0 {
			select {
			case <-time.After(delay):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		body, err, retryable := redditGetOnce(ctx, rawURL)
		if err == nil {
			return body, nil
		}
		lastErr = err
		if !retryable {
			return nil, err
		}
	}
	if lastErr == nil {
		lastErr = errors.New("reddit: лента недоступна")
	}
	return nil, lastErr
}

func redditGetOnce(ctx context.Context, rawURL string) ([]byte, error, bool) {
	client := redditHTTPClient(45 * time.Second)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err, false
	}
	req.Header.Set("User-Agent", redditUserAgent)
	req.Header.Set("Accept", "application/atom+xml,application/xml,text/xml,*/*")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err, true
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		retryable := resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500
		return nil, fmt.Errorf("reddit: HTTP %d", resp.StatusCode), retryable
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err, true
	}
	return body, nil, false
}

type redditAtom struct {
	Entries []struct {
		Title   string `xml:"title"`
		Content string `xml:"content"`
		Author  struct {
			Name string `xml:"name"`
			URI  string `xml:"uri"`
		} `xml:"author"`
		Link struct {
			Href string `xml:"href,attr"`
		} `xml:"link"`
	} `xml:"entry"`
}

// redditParsePost достаёт из ленты заголовок, автора и ссылки на медиа.
func redditParsePost(raw []byte) (title, author, link string, images []string, videoID string, err error) {
	var feed redditAtom
	if err := xml.Unmarshal(raw, &feed); err != nil {
		return "", "", "", nil, "", fmt.Errorf("reddit: разбор ленты: %w", err)
	}
	if len(feed.Entries) == 0 {
		return "", "", "", nil, "", errRedditNoMedia
	}
	// Первая запись — сам пост; остальные — комментарии (их медиа не берём).
	entry := feed.Entries[0]
	title = strings.TrimSpace(entry.Title)
	author = strings.TrimSpace(strings.TrimPrefix(entry.Author.Name, "/u/"))
	link = strings.TrimSpace(entry.Link.Href)

	body := html.UnescapeString(entry.Content)
	seen := map[string]bool{}
	for _, match := range redditMediaRe.FindAllString(body, -1) {
		clean := strings.TrimRight(match, ".,;")
		if vm := redditVideoRe.FindStringSubmatch(clean); vm != nil {
			if videoID == "" {
				videoID = vm[1]
			}
			continue
		}
		if strings.Contains(clean, "preview.redd.it/") {
			continue // миниатюра: полный размер обычно есть рядом в i.redd.it
		}
		if idx := strings.IndexAny(clean, "?#"); idx > 0 {
			clean = clean[:idx]
		}
		if seen[clean] || len(images) >= redditMaxImages {
			continue
		}
		seen[clean] = true
		images = append(images, clean)
	}
	return title, author, link, images, videoID, nil
}

// downloadRedditImage сохраняет картинку в каталог.
func downloadRedditImage(ctx context.Context, dir, imageURL string) (string, error) {
	if idx := strings.IndexAny(imageURL, "?#"); idx > 0 {
		imageURL = imageURL[:idx]
	}
	ext := strings.ToLower(filepath.Ext(imageURL))
	switch ext {
	case ".jpeg", ".jpg", ".png", ".webp", ".gif":
	default:
		ext = ".jpg"
	}
	name := filepath.Base(imageURL)
	if name == "" || name == "." || name == "/" {
		name = fmt.Sprintf("image%s", ext)
	}
	if !strings.HasSuffix(strings.ToLower(name), ext) {
		name += ext
	}
	outPath := filepath.Join(dir, name)

	client := redditHTTPClient(60 * time.Second)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, imageURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", redditUserAgent)
	req.Header.Set("Accept", "image/avif,image/webp,image/*,*/*")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("reddit image: HTTP %d", resp.StatusCode)
	}
	limit := int64(redditMaxImageMB) << 20
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
		return "", errors.New("reddit image: пустой файл")
	}
	return outPath, nil
}

// downloadRedditVideo тянет HLS-плейлист v.redd.it через ffmpeg (видео+звук).
func downloadRedditVideo(ctx context.Context, dir, videoID string) (string, error) {
	outPath := filepath.Join(dir, "reddit-"+videoID+".mp4")
	playlists := []string{
		"https://v.redd.it/" + videoID + "/HLSPlaylist.m3u8",
		"https://v.redd.it/" + videoID + "/DASHPlaylist.mpd",
	}
	var lastErr error
	for _, playlist := range playlists {
		args := []string{
			"-nostdin", "-y", "-loglevel", "error",
			"-user_agent", redditUserAgent,
			"-i", playlist,
			"-map", "0:v:0?", "-map", "0:a:0?",
			"-c", "copy",
			"-movflags", "+faststart",
			outPath,
		}
		vctx, cancel := context.WithTimeout(ctx, redditMaxVideoSec*time.Second)
		cmd := exec.CommandContext(vctx, "ffmpeg", args...)
		output, err := cmd.CombinedOutput()
		cancel()
		if err == nil {
			if st, statErr := os.Stat(outPath); statErr == nil && st.Size() > 0 {
				return outPath, nil
			}
		} else {
			lastErr = fmt.Errorf("reddit video: %w (%s)", err, strings.TrimSpace(clipRedditText(string(output), 200)))
			_ = os.Remove(outPath)
		}
	}
	if lastErr == nil {
		lastErr = errors.New("reddit video: плейлист недоступен")
	}
	return "", lastErr
}

func clipRedditText(s string, limit int) string {
	runes := []rune(strings.TrimSpace(s))
	if len(runes) <= limit {
		return string(runes)
	}
	return string(runes[:limit]) + "…"
}

// RedditPost скачивает медиа поста Reddit. supported=false — ссылка не наша.
func (d Downloader) RedditPost(ctx context.Context, rawURL string) (RedditMedia, bool, error) {
	normURL, service, ok := NormalizeSupportedURL(rawURL)
	if !ok || service != ServiceReddit {
		return RedditMedia{}, false, nil
	}
	postID, direct, ok := redditPostRef(normURL)
	if !ok {
		return RedditMedia{}, true, errors.New("reddit: не понял ссылку")
	}

	dir, err := bottmp.MkdirTemp("media-reddit-*")
	if err != nil {
		return RedditMedia{}, true, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }

	result := RedditMedia{Dir: dir, SourceURL: normURL, Service: ServiceReddit}

	// Прямая ссылка на файл.
	if direct != "" {
		if strings.Contains(direct, "v.redd.it/") {
			if m := redditVideoRe.FindStringSubmatch(direct); m != nil {
				path, err := downloadRedditVideo(ctx, dir, m[1])
				if err != nil {
					cleanup()
					return RedditMedia{}, true, err
				}
				result.Kind, result.Paths = "video", []string{path}
				return result, true, nil
			}
		}
		path, err := downloadRedditImage(ctx, dir, direct)
		if err != nil {
			cleanup()
			return RedditMedia{}, true, err
		}
		result.Kind, result.Paths = "photo", []string{path}
		return result, true, nil
	}

	feed, err := redditGet(ctx, redditRSSURL(postID))
	if err != nil {
		cleanup()
		return RedditMedia{}, true, err
	}
	title, author, link, images, videoID, err := redditParsePost(feed)
	if err != nil {
		cleanup()
		return RedditMedia{}, true, err
	}
	result.Title, result.Author = title, author
	if link != "" {
		result.SourceURL = link
	}

	if videoID != "" {
		path, err := downloadRedditVideo(ctx, dir, videoID)
		if err != nil {
			cleanup()
			return RedditMedia{}, true, err
		}
		result.Kind, result.Paths = "video", []string{path}
		return result, true, nil
	}

	for _, imageURL := range images {
		path, err := downloadRedditImage(ctx, dir, imageURL)
		if err != nil {
			continue
		}
		result.Paths = append(result.Paths, path)
	}
	if len(result.Paths) == 0 {
		cleanup()
		return RedditMedia{}, true, errRedditNoMedia
	}
	result.Kind = "photo"
	return result, true, nil
}
