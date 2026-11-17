package mediadl

// X (x.com / twitter.com): медиа поста.
//
// yt-dlp умеет у X только видео: на фото-постах и на постах без вложений он
// отдаёт «No video could be found in this tweet», и бот сообщал об ошибке даже
// тогда, когда качать было нечего. Поэтому состав поста читаем из публичного
// FixTweet API (api.fxtwitter.com, резерв — api.vxtwitter.com): он отдаёт все
// вложения с прямыми ссылками на оригиналы (pbs.twimg.com?name=orig).
//
// Видео по-прежнему качает прежний путь yt-dlp: здесь только флаг HasVideo,
// чтобы вызывающий код не подменял работающую загрузку видео.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"trigger-admin-bot/internal/bottmp"
)

const (
	xAPITimeoutSec = 20
	// xCarouselLimit — предел слайдшоу Bot API (50 медиа на статью).
	xCarouselLimit = 50
)

// xAPIEndpoint вынесен в переменную, чтобы тесты подменяли адрес на httptest.
var xAPIEndpoint = "https://api.fxtwitter.com/"
var xAPIFallbackEndpoint = "https://api.vxtwitter.com/"

var (
	xStatusPathRe    = regexp.MustCompile(`(?i)^/([A-Za-z0-9_]{1,20})/status(?:es)?/(\d{5,25})`)
	xWebStatusPathRe = regexp.MustCompile(`(?i)^/i(?:/web)?/status/(\d{5,25})`)
)

// xPostRef — разобранная ссылка на пост.
type xPostRef struct {
	ScreenName string // пусто у ссылок вида x.com/i/web/status/<id>
	PostID     string
}

// xHost сообщает, обслуживаем ли мы домен X/Twitter.
func xHost(host string) bool {
	switch strings.ToLower(strings.TrimSpace(host)) {
	case "x.com", "www.x.com", "m.x.com", "mobile.x.com",
		"twitter.com", "www.twitter.com", "m.twitter.com", "mobile.twitter.com":
		return true
	}
	return false
}

// ParseXPostURL разбирает ссылку на пост X. ok=false — это не пост.
func ParseXPostURL(rawURL string) (xPostRef, bool) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return xPostRef{}, false
	}
	u, err := url.Parse(rawURL)
	if err != nil || !xHost(u.Hostname()) {
		return xPostRef{}, false
	}
	path := u.EscapedPath()
	if m := xStatusPathRe.FindStringSubmatch(path); m != nil {
		return xPostRef{ScreenName: m[1], PostID: m[2]}, true
	}
	if m := xWebStatusPathRe.FindStringSubmatch(path); m != nil {
		return xPostRef{PostID: m[1]}, true
	}
	// Без /status/<id> в пути это не пост (профиль, поиск, лента) — не гадаем.
	return xPostRef{}, false
}

// xMediaItem — вложение в ответе FixTweet (поля fxtwitter и vxtwitter
// сведены в одну структуру).
type xMediaItem struct {
	Type         string  `json:"type"` // photo / image / video / gif
	URL          string  `json:"url"`
	ThumbnailURL string  `json:"thumbnail_url"`
	Duration     float64 `json:"duration"`
	DurationMs   float64 `json:"duration_millis"`
	Width        int     `json:"width"`
	Height       int     `json:"height"`
	AltText      string  `json:"altText"`
	Size         struct {
		Width  int `json:"width"`
		Height int `json:"height"`
	} `json:"size"`
}

// xPostData — сведённые данные поста из любого из двух API.
type xPostData struct {
	PostID     string
	ScreenName string
	Text       string
	Media      []xMediaItem
}

type xFxTweetResponse struct {
	Code  int    `json:"code"`
	Error string `json:"error"`
	Tweet *struct {
		ID     string `json:"id"`
		URL    string `json:"url"`
		Text   string `json:"text"`
		Author struct {
			ScreenName string `json:"screen_name"`
			Name       string `json:"name"`
		} `json:"author"`
		Media *struct {
			All []xMediaItem `json:"all"`
		} `json:"media"`
	} `json:"tweet"`
}

type xVxTweetResponse struct {
	TweetID        string       `json:"tweetID"`
	TweetURL       string       `json:"tweetURL"`
	Text           string       `json:"text"`
	UserScreenName string       `json:"user_screen_name"`
	HasMedia       bool         `json:"hasMedia"`
	MediaExtended  []xMediaItem `json:"media_extended"`
}

// XPostMedia скачивает фото поста X и сообщает, есть ли в нём видео.
// supported=false — ссылка не на пост X.
func (d Downloader) XPostMedia(ctx context.Context, rawURL string) (XPostMedia, bool, error) {
	ref, ok := ParseXPostURL(rawURL)
	if !ok {
		return XPostMedia{}, false, nil
	}
	post, err := fetchXPost(ctx, ref)
	if err != nil {
		return XPostMedia{}, true, err
	}
	result := XPostMedia{
		Title:     xPostTitle(post.Text, post.ScreenName),
		SourceURL: xPostSourceURL(post),
		Service:   ServiceX,
		HasMedia:  len(post.Media) > 0,
	}
	photos := make([]string, 0, len(post.Media))
	for _, item := range post.Media {
		switch strings.ToLower(strings.TrimSpace(item.Type)) {
		case "video", "gif", "animated_gif":
			result.HasVideo = true
		case "photo", "image":
			if photoURL := xPhotoURL(item.URL); photoURL != "" {
				photos = append(photos, photoURL)
			}
		}
	}
	if result.HasVideo || len(photos) == 0 {
		// Видео забирает yt-dlp, а если фото нет — качать нечего.
		return result, true, nil
	}

	dir, err := bottmp.MkdirTemp("media-x-post-*")
	if err != nil {
		return XPostMedia{}, true, err
	}
	result.Dir = dir
	var firstErr error
	for position, photoURL := range photos {
		if len(result.Items) >= xCarouselLimit {
			result.Skipped++
			continue
		}
		index := position + 1
		path, dlErr := d.downloadRemoteImageForService(ctx, ServiceX, photoURL, dir,
			fmt.Sprintf("%03d", index), "https://x.com/")
		if dlErr != nil {
			result.Skipped++
			if firstErr == nil {
				firstErr = dlErr
			}
			if debugCarouselLog() {
				fmt.Printf("x post photo %d: %v\n", index, dlErr)
			}
			continue
		}
		result.Items = append(result.Items, CarouselItem{Kind: "photo", Path: path, Index: index})
	}
	if len(result.Items) == 0 {
		_ = os.RemoveAll(result.Dir)
		result.Dir = ""
		if firstErr != nil {
			return result, true, fmt.Errorf("%w: %v", ErrXPostPhotosFailed, firstErr)
		}
		return result, true, nil
	}
	return result, true, nil
}

// XPostMedia — медиа поста X. HasVideo=true означает, что пост содержит видео
// (или GIF) и его качает обычный путь yt-dlp; тогда Items пуст.
type XPostMedia struct {
	Dir       string
	Items     []CarouselItem
	Title     string
	SourceURL string
	Service   Service
	Skipped   int
	HasVideo  bool
	HasMedia  bool
}

// fetchXPost читает пост из FixTweet, при отказе — из vxTwitter.
func fetchXPost(ctx context.Context, ref xPostRef) (xPostData, error) {
	post, err := fetchXPostFx(ctx, ref)
	if err == nil {
		return post, nil
	}
	fallback, fbErr := fetchXPostVx(ctx, ref)
	if fbErr == nil {
		return fallback, nil
	}
	return xPostData{}, fmt.Errorf("x post: api.fxtwitter.com: %v; api.vxtwitter.com: %v", err, fbErr)
}

func fetchXPostFx(ctx context.Context, ref xPostRef) (xPostData, error) {
	body, err := xAPIGet(ctx, xAPIEndpoint, ref)
	if err != nil {
		return xPostData{}, err
	}
	var parsed xFxTweetResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return xPostData{}, fmt.Errorf("fxtwitter decode: %w", err)
	}
	if parsed.Tweet == nil {
		return xPostData{}, fmt.Errorf("fxtwitter: пост не найден (code=%d %s)", parsed.Code, parsed.Error)
	}
	post := xPostData{
		PostID:     firstNonEmpty(parsed.Tweet.ID, ref.PostID),
		ScreenName: firstNonEmpty(parsed.Tweet.Author.ScreenName, ref.ScreenName),
		Text:       parsed.Tweet.Text,
	}
	if parsed.Tweet.Media != nil {
		post.Media = parsed.Tweet.Media.All
	}
	return post, nil
}

func fetchXPostVx(ctx context.Context, ref xPostRef) (xPostData, error) {
	body, err := xAPIGet(ctx, xAPIFallbackEndpoint, ref)
	if err != nil {
		return xPostData{}, err
	}
	var parsed xVxTweetResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return xPostData{}, fmt.Errorf("vxtwitter decode: %w", err)
	}
	if strings.TrimSpace(parsed.TweetID) == "" {
		return xPostData{}, errors.New("vxtwitter: пост не найден")
	}
	return xPostData{
		PostID:     parsed.TweetID,
		ScreenName: firstNonEmpty(parsed.UserScreenName, ref.ScreenName),
		Text:       parsed.Text,
		Media:      parsed.MediaExtended,
	}, nil
}

func xAPIGet(ctx context.Context, endpoint string, ref xPostRef) ([]byte, error) {
	apiURL := strings.TrimSuffix(endpoint, "/") + "/"
	if ref.ScreenName != "" {
		apiURL += url.PathEscape(ref.ScreenName) + "/"
	}
	apiURL += "status/" + url.PathEscape(ref.PostID)
	reqCtx, cancel := context.WithTimeout(ctx, xAPITimeoutSec*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, apiURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "trigger-admin-bot/1.0")
	req.Header.Set("Accept", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if readErr != nil {
		return nil, readErr
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("статус %d", resp.StatusCode)
	}
	return body, nil
}

// xPhotoURL превращает ссылку картинки в полноразмерную: у vxtwitter она
// приходит без параметра name, и CDN отдаёт уменьшенную копию.
func xPhotoURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if strings.Contains(raw, "name=") {
		return raw
	}
	sep := "?"
	if strings.Contains(raw, "?") {
		sep = "&"
	}
	return raw + sep + "name=orig"
}

// xPostSourceURL собирает каноническую ссылку на пост.
func xPostSourceURL(post xPostData) string {
	if strings.TrimSpace(post.ScreenName) == "" || strings.TrimSpace(post.PostID) == "" {
		return ""
	}
	return fmt.Sprintf("https://x.com/%s/status/%s", post.ScreenName, post.PostID)
}

// xPostTitle — заголовок для подписи: первая строка текста, иначе ник автора.
func xPostTitle(text, screenName string) string {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		runes := []rune(line)
		if len(runes) > 120 {
			return strings.TrimSpace(string(runes[:120]))
		}
		return line
	}
	if screen := strings.TrimSpace(screenName); screen != "" {
		return "@" + screen
	}
	return ""
}

// ErrXPostPhotosFailed — фото в посте есть, но ни одно не скачалось:
// это честная ошибка загрузки, а не «в посте нет медиа».
var ErrXPostPhotosFailed = errors.New("не удалось скачать фото из поста X")