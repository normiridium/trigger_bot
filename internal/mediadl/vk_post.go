package mediadl

// VK: карусель поста — несколько фото и/или видео в одной записи.
//
// yt-dlp такой пост не разбирает: его экстрактор vk:wallpost возвращает пустой
// плейлист, поэтому элементы карусели собираем сами. VK API wall.getById с
// токеном отдаёт текст поста и attachments с готовыми ссылками на полноразмерные
// картинки и с идентификаторами видео. Фото качаем напрямую (ссылки CDN
// открыты), видео — обычным путём yt-dlp по ссылке vk.com/video<owner>_<id>.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"trigger-admin-bot/internal/bottmp"
)

const (
	vkAPIVersion = "5.199"
	// vkSlideShowLimit — предел слайдшоу в Bot API: 50 медиа-вложений на статью.
	vkSlideShowLimit = 50
	vkAPITimeoutSec  = 25
)

// vkAPIEndpoint вынесен в переменную, чтобы тесты подменяли адрес на httptest.
var vkAPIEndpoint = "https://api.vk.com/method/"

var (
	vkWallPathRe  = regexp.MustCompile(`(?i)^/wall(-?\d+)_(\d+)`)
	vkWallQueryRe = regexp.MustCompile(`(?i)[?&]w=wall(-?\d+)_(\d+)`)
)

// vkWallPostRef — разобранная ссылка на пост: владелец и номер записи.
type vkWallPostRef struct {
	OwnerID int64
	PostID  int64
}

// vkHost возвращает true для доменов VK, включая мобильные и vk.ru.
func vkHost(host string) bool {
	switch strings.ToLower(strings.TrimSpace(host)) {
	case "vk.com", "www.vk.com", "m.vk.com", "vk.ru", "www.vk.ru", "m.vk.ru", "new.vk.com":
		return true
	}
	return false
}

// ParseVKWallPostURL разбирает ссылку на пост VK: и прямую
// (vk.ru/wall-50930433_228697), и пришедшую с экрана группы
// (vk.com/vk.modish?w=wall-50930433_228697). ok=false — это не пост.
func ParseVKWallPostURL(rawURL string) (vkWallPostRef, bool) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return vkWallPostRef{}, false
	}
	u, err := url.Parse(rawURL)
	if err != nil || !vkHost(u.Hostname()) {
		return vkWallPostRef{}, false
	}
	if m := vkWallPathRe.FindStringSubmatch(u.EscapedPath()); m != nil {
		owner, ownerErr := strconv.ParseInt(m[1], 10, 64)
		post, postErr := strconv.ParseInt(m[2], 10, 64)
		if ownerErr == nil && postErr == nil && post > 0 {
			return vkWallPostRef{OwnerID: owner, PostID: post}, true
		}
	}
	if m := vkWallQueryRe.FindStringSubmatch(rawURL); m != nil {
		owner, ownerErr := strconv.ParseInt(m[1], 10, 64)
		post, postErr := strconv.ParseInt(m[2], 10, 64)
		if ownerErr == nil && postErr == nil && post > 0 {
			return vkWallPostRef{OwnerID: owner, PostID: post}, true
		}
	}
	return vkWallPostRef{}, false
}

// vkPhotoSize — один размер картинки в ответе VK API.
type vkPhotoSize struct {
	Type   string `json:"type"`
	URL    string `json:"url"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

// vkAttachment — вложение поста; заполнено только поле своего типа.
type vkAttachment struct {
	Type  string `json:"type"`
	Photo struct {
		ID    int64         `json:"id"`
		Owner int64         `json:"owner_id"`
		Sizes []vkPhotoSize `json:"sizes"`
	} `json:"photo"`
	Video struct {
		OwnerID   int64  `json:"owner_id"`
		ID        int64  `json:"id"`
		AccessKey string `json:"access_key"`
		Title     string `json:"title"`
		Duration  int    `json:"duration"`
	} `json:"video"`
}

// vkWallPost — запись со стены в объёме, нужном для карусели.
type vkWallPost struct {
	OwnerID     int64          `json:"owner_id"`
	ID          int64          `json:"id"`
	FromID      int64          `json:"from_id"`
	Date        int64          `json:"date"`
	Text        string         `json:"text"`
	Attachments []vkAttachment `json:"attachments"`
	CopyHistory []vkWallPost   `json:"copy_history"`
}

type vkWallResponse struct {
	Response struct {
		Items []vkWallPost `json:"items"`
	} `json:"response"`
	Error *struct {
		Code int    `json:"error_code"`
		Msg  string `json:"error_msg"`
	} `json:"error"`
}

// vkFetchWallPost читает одну запись через wall.getById.
func (d Downloader) vkFetchWallPost(ctx context.Context, ref vkWallPostRef) (vkWallPost, error) {
	token := strings.TrimSpace(d.VKToken)
	if token == "" {
		return vkWallPost{}, errors.New("vk post: VK_TOKEN is not configured")
	}
	apiURL := fmt.Sprintf("%swall.getById?posts=%d_%d&access_token=%s&v=%s",
		vkAPIEndpoint, ref.OwnerID, ref.PostID, url.QueryEscape(token), vkAPIVersion)
	reqCtx, cancel := context.WithTimeout(ctx, vkAPITimeoutSec*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, apiURL, nil)
	if err != nil {
		return vkWallPost{}, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return vkWallPost{}, fmt.Errorf("vk wall.getById: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return vkWallPost{}, fmt.Errorf("vk wall.getById read: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return vkWallPost{}, fmt.Errorf("vk wall.getById status=%d", resp.StatusCode)
	}
	var parsed vkWallResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return vkWallPost{}, fmt.Errorf("vk wall.getById decode: %w", err)
	}
	if parsed.Error != nil {
		return vkWallPost{}, fmt.Errorf("vk wall.getById error %d: %s", parsed.Error.Code, parsed.Error.Msg)
	}
	if len(parsed.Response.Items) == 0 {
		return vkWallPost{}, errors.New("vk post: запись не найдена или удалена")
	}
	return parsed.Response.Items[0], nil
}

// vkPhotoSizeOrder — предпочтение размеров: от самого крупного к мелкому.
// Порядок фиксирован, потому что площадь у вариантов бывает не монотонной.
var vkPhotoSizeOrder = []string{"w", "z", "y", "x", "r", "q", "p", "o", "m", "s"}

// pickVKPhotoURL выбирает самую крупную доступную ссылку картинки.
func pickVKPhotoURL(sizes []vkPhotoSize) string {
	for _, want := range vkPhotoSizeOrder {
		for _, size := range sizes {
			if size.Type == want && strings.TrimSpace(size.URL) != "" {
				return strings.TrimSpace(size.URL)
			}
		}
	}
	best, bestArea := "", 0
	for _, size := range sizes {
		if strings.TrimSpace(size.URL) == "" {
			continue
		}
		if area := size.Width * size.Height; area > bestArea {
			best, bestArea = strings.TrimSpace(size.URL), area
		}
	}
	return best
}

// vkVideoURL собирает ссылку на видео для yt-dlp.
func vkVideoURL(ownerID, videoID int64, accessKey string) string {
	raw := fmt.Sprintf("https://vk.com/video%d_%d", ownerID, videoID)
	if key := strings.TrimSpace(accessKey); key != "" {
		raw += "_" + key
	}
	return raw
}

// vkPostTitle — первая строка текста поста: она и служит заголовком карусели.
func vkPostTitle(text string) string {
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
	return ""
}

// VKWallPostCarousel скачивает все фото и видео поста VK в порядке записи.
// supported=false — ссылка не на пост VK, дальше работает обычный путь.
func (d Downloader) VKWallPostCarousel(ctx context.Context, rawURL string) (Carousel, bool, error) {
	ref, ok := ParseVKWallPostURL(rawURL)
	if !ok {
		return Carousel{}, false, nil
	}
	post, err := d.vkFetchWallPost(ctx, ref)
	if err != nil {
		return Carousel{}, true, err
	}
	attachments := post.Attachments
	if len(attachments) == 0 && len(post.CopyHistory) > 0 {
		// Репост галереи: элементы лежат в исходной записи.
		attachments = post.CopyHistory[0].Attachments
	}

	dir, err := bottmp.MkdirTemp("media-vk-carousel-*")
	if err != nil {
		return Carousel{}, true, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }

	result := Carousel{
		Dir:       dir,
		Title:     vkPostTitle(post.Text),
		Caption:   strings.TrimSpace(post.Text),
		SourceURL: fmt.Sprintf("https://vk.com/wall%d_%d", ref.OwnerID, ref.PostID),
		Service:   ServiceVK,
	}
	// firstErr помнит первую причину отказа: если не скачалось ничего,
	// юзер должен увидеть её, а не «в посте нет фото или видео».
	var firstErr error
	skip := func(err error) {
		result.Skipped++
		if firstErr == nil {
			firstErr = err
		}
	}
	for position, attachment := range attachments {
		if len(result.Items) >= vkSlideShowLimit {
			result.Skipped++
			continue
		}
		index := position + 1
		switch strings.ToLower(strings.TrimSpace(attachment.Type)) {
		case "photo":
			photoURL := pickVKPhotoURL(attachment.Photo.Sizes)
			if photoURL == "" {
				skip(errors.New("у фото нет ссылки на картинку"))
				continue
			}
			path, dlErr := d.downloadRemoteImageForService(ctx, ServiceVK, photoURL, dir,
				fmt.Sprintf("%03d", index), "https://vk.com/")
			if dlErr != nil {
				skip(dlErr)
				if debugCarouselLog() {
					fmt.Printf("vk carousel photo %d: %v\n", index, dlErr)
				}
				continue
			}
			result.Items = append(result.Items, CarouselItem{Kind: "photo", Path: path, Index: index})
		case "video":
			path, dlErr := d.downloadVKCarouselVideo(ctx, attachment, dir, index)
			if dlErr != nil {
				skip(dlErr)
				if debugCarouselLog() {
					fmt.Printf("vk carousel video %d: %v\n", index, dlErr)
				}
				continue
			}
			result.Items = append(result.Items, CarouselItem{Kind: "video", Path: path, Index: index})
		}
	}
	if len(result.Items) == 0 {
		cleanup()
		if firstErr != nil {
			return Carousel{}, true, fmt.Errorf("не удалось скачать вложения VK: %w", firstErr)
		}
		return Carousel{}, true, errVKCarouselEmpty
	}
	return result, true, nil
}

// downloadVKCarouselVideo качает видео поста в каталог карусели, чтобы
// очистка каталога убирала и его тоже.
func (d Downloader) downloadVKCarouselVideo(ctx context.Context, attachment vkAttachment, dir string, index int) (string, error) {
	video := attachment.Video
	if video.ID == 0 || video.OwnerID == 0 {
		return "", errors.New("вложение видео пустое")
	}
	rawURL := vkVideoURL(video.OwnerID, video.ID, video.AccessKey)
	res, err := d.DownloadVideoFromURL(ctx, rawURL)
	if err != nil {
		return "", err
	}
	srcDir := filepath.Dir(res.FilePath)
	target := filepath.Join(dir, fmt.Sprintf("%03d%s", index, strings.ToLower(filepath.Ext(res.FilePath))))
	if err := os.Rename(res.FilePath, target); err != nil {
		_ = os.Remove(res.FilePath)
		return "", err
	}
	_ = os.RemoveAll(srcDir)
	if st, statErr := os.Stat(target); statErr != nil || st.Size() == 0 {
		_ = os.Remove(target)
		return "", errors.New("видео скачалось пустым")
	}
	return target, nil
}

var errVKCarouselEmpty = errors.New("в посте VK нет фото или видео")
