package mediadl

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestParseXPostURL(t *testing.T) {
	cases := []struct {
		raw     string
		screen  string
		post    string
		wantOK  bool
		comment string
	}{
		{"https://x.com/Investor_Dayu/status/2121986911933788502", "Investor_Dayu", "2121986911933788502", true, "ссылка из чата"},
		{"https://x.com/NASA/status/2110371804959191288?s=20", "NASA", "2110371804959191288", true, "с query-хвостом"},
		{"https://twitter.com/NASA/status/2110371804959191288", "NASA", "2110371804959191288", true, "twitter.com"},
		{"https://mobile.twitter.com/NASA/statuses/2110371804959191288", "NASA", "2110371804959191288", true, "старая форма /statuses/"},
		{"https://x.com/i/web/status/2121986911933788502", "", "2121986911933788502", true, "ссылка без ника"},
		{"https://x.com/NASA", "", "", false, "профиль"},
		{"https://x.com/home", "", "", false, "лента"},
		{"https://x.com/search?q=nvidia", "", "", false, "поиск"},
		{"https://example.com/NASA/status/2110371804959191288", "", "", false, "чужой домен"},
		{"", "", "", false, "пустая строка"},
	}
	for _, tc := range cases {
		ref, ok := ParseXPostURL(tc.raw)
		if ok != tc.wantOK {
			t.Fatalf("%s: ok=%v, ждали %v", tc.comment, ok, tc.wantOK)
		}
		if !tc.wantOK {
			continue
		}
		if ref.ScreenName != tc.screen || ref.PostID != tc.post {
			t.Fatalf("%s: получили %q/%q, ждали %q/%q", tc.comment, ref.ScreenName, ref.PostID, tc.screen, tc.post)
		}
	}
}

func TestXPhotoURL(t *testing.T) {
	if got := xPhotoURL("https://pbs.twimg.com/media/AAA.jpg"); got != "https://pbs.twimg.com/media/AAA.jpg?name=orig" {
		t.Fatalf("без параметров: %q", got)
	}
	if got := xPhotoURL("https://pbs.twimg.com/media/AAA.jpg?name=orig"); got != "https://pbs.twimg.com/media/AAA.jpg?name=orig" {
		t.Fatalf("уже полный размер: %q", got)
	}
	if got := xPhotoURL("https://pbs.twimg.com/media/AAA.jpg?foo=1"); got != "https://pbs.twimg.com/media/AAA.jpg?foo=1&name=orig" {
		t.Fatalf("с чужим параметром: %q", got)
	}
	if got := xPhotoURL("   "); got != "" {
		t.Fatalf("пустая ссылка: %q", got)
	}
}

func TestXPostTitle(t *testing.T) {
	title := xPostTitle("\n\nFrom an NVIDIA employee:\n\nI have worked…", "Investor_Dayu")
	if title != "From an NVIDIA employee:" {
		t.Fatalf("заголовок %q", title)
	}
	if got := xPostTitle("   ", "NASA"); got != "@NASA" {
		t.Fatalf("фолбек на ник: %q", got)
	}
	if got := xPostTitle(strings.Repeat("я", 300), "NASA"); len([]rune(got)) != 120 {
		t.Fatalf("длинная строка не обрезана: %d рун", len([]rune(got)))
	}
}

// xTestPhotoPost — ответ fxtwitter для поста из двух картинок.
func xTestPhotoPost(imageURL string) string {
	payload := map[string]any{
		"code": 200, "message": "OK",
		"tweet": map[string]any{
			"id":     "2110371804959191288",
			"url":    "https://x.com/NASA/status/2110371804959191288",
			"text":   "The Moon, as seen by Artemis II astronauts\n\nЕщё текст",
			"author": map[string]any{"screen_name": "NASA", "name": "NASA"},
			"media": map[string]any{"all": []any{
				map[string]any{"type": "photo", "url": imageURL + "?name=orig", "width": 1920, "height": 1280},
				map[string]any{"type": "photo", "url": imageURL + "?name=orig", "width": 1920, "height": 1280},
			}},
		},
	}
	body, _ := json.Marshal(payload)
	return string(body)
}

func xTestVideoPost() string {
	payload := map[string]any{
		"code": 200, "message": "OK",
		"tweet": map[string]any{
			"id":     "2105842656429432894",
			"text":   "видео без картинок",
			"author": map[string]any{"screen_name": "NASA"},
			"media": map[string]any{"all": []any{
				map[string]any{"type": "video", "url": "https://video.twimg.com/x.mp4", "duration": 65.3},
				map[string]any{"type": "photo", "url": "https://pbs.twimg.com/media/AAA.jpg?name=orig"},
			}},
		},
	}
	body, _ := json.Marshal(payload)
	return string(body)
}

func xTestNoMediaPost() string {
	return `{"code":200,"message":"OK","tweet":{"id":"2121986911933788502","text":"From an NVIDIA employee: To the Board…","author":{"screen_name":"Investor_Dayu"}}}`
}

func withXEndpoints(t *testing.T, fxBody, vxBody string) {
	t.Helper()
	fx := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/img") {
			rw.Header().Set("Content-Type", "image/jpeg")
			_, _ = rw.Write([]byte{0xff, 0xd8, 0xff, 0xe0, 0x00, 0x10, 0x4a, 0x46, 0x49, 0x46, 0xff, 0xd9})
			return
		}
		if !strings.Contains(r.URL.Path, "/status/") {
			t.Errorf("неожиданный путь %s", r.URL.Path)
		}
		if fxBody == "" {
			rw.WriteHeader(http.StatusBadGateway)
			return
		}
		_, _ = rw.Write([]byte(strings.ReplaceAll(fxBody, "IMG_BASE", "http://"+r.Host)))
	}))
	t.Cleanup(fx.Close)
	vx := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if vxBody == "" {
			rw.WriteHeader(http.StatusBadGateway)
			return
		}
		_, _ = rw.Write([]byte(strings.ReplaceAll(vxBody, "IMG_BASE", "http://"+r.Host)))
	}))
	t.Cleanup(vx.Close)
	oldFx, oldVx := xAPIEndpoint, xAPIFallbackEndpoint
	xAPIEndpoint = fx.URL + "/"
	xAPIFallbackEndpoint = vx.URL + "/"
	t.Cleanup(func() { xAPIEndpoint, xAPIFallbackEndpoint = oldFx, oldVx })
}

func TestXPostMediaPhotos(t *testing.T) {
	withXEndpoints(t, xTestPhotoPost("IMG_BASE/img.jpg"), "")
	dl := Downloader{MaxSizeMB: 50}
	post, supported, err := dl.XPostMedia(context.Background(), "https://x.com/NASA/status/2110371804959191288")
	if !supported || err != nil {
		t.Fatalf("supported=%v err=%v", supported, err)
	}
	if post.Dir != "" {
		defer os.RemoveAll(post.Dir)
	}
	if post.HasVideo {
		t.Fatal("в посте только фото, видео быть не должно")
	}
	if !post.HasMedia || len(post.Items) != 2 {
		t.Fatalf("медиа=%v элементов=%d: %+v", post.HasMedia, len(post.Items), post.Items)
	}
	for i, item := range post.Items {
		if item.Kind != "photo" {
			t.Fatalf("элемент %d не фото: %+v", i, item)
		}
		if st, statErr := os.Stat(item.Path); statErr != nil || st.Size() == 0 {
			t.Fatalf("файл элемента %d не скачался: %v", i, statErr)
		}
	}
	if post.Title != "The Moon, as seen by Artemis II astronauts" {
		t.Fatalf("заголовок %q", post.Title)
	}
	if post.SourceURL != "https://x.com/NASA/status/2110371804959191288" {
		t.Fatalf("ссылка источника %q", post.SourceURL)
	}
}

func TestXPostMediaVideoStaysOnYTDLP(t *testing.T) {
	withXEndpoints(t, xTestVideoPost(), "")
	dl := Downloader{MaxSizeMB: 50}
	post, supported, err := dl.XPostMedia(context.Background(), "https://x.com/NASA/status/2105842656429432894")
	if !supported || err != nil {
		t.Fatalf("supported=%v err=%v", supported, err)
	}
	if !post.HasVideo {
		t.Fatal("видео в посте не распознано")
	}
	if post.Dir != "" || len(post.Items) != 0 {
		t.Fatalf("для видео-поста ничего качать не должны: dir=%q items=%d", post.Dir, len(post.Items))
	}
}

func TestXPostMediaWithoutAttachments(t *testing.T) {
	withXEndpoints(t, xTestNoMediaPost(), "")
	dl := Downloader{MaxSizeMB: 50}
	post, supported, err := dl.XPostMedia(context.Background(), "https://x.com/Investor_Dayu/status/2121986911933788502")
	if !supported || err != nil {
		t.Fatalf("supported=%v err=%v", supported, err)
	}
	if post.HasMedia || post.HasVideo || len(post.Items) != 0 {
		t.Fatalf("пост без вложений: %+v", post)
	}
	if post.Title != "From an NVIDIA employee: To the Board…" {
		t.Fatalf("заголовок %q", post.Title)
	}
}

func TestXPostMediaFallsBackToVx(t *testing.T) {
	vxPost := `{"tweetID":"2110371804959191288","tweetURL":"https://x.com/NASA/status/2110371804959191288","text":"Луна","user_screen_name":"NASA","hasMedia":true,"media_extended":[{"type":"image","url":"IMG_BASE/img.jpg","size":{"width":1920,"height":1280}}]}`
	withXEndpoints(t, "", vxPost)
	dl := Downloader{MaxSizeMB: 50}
	post, supported, err := dl.XPostMedia(context.Background(), "https://x.com/NASA/status/2110371804959191288")
	if !supported || err != nil {
		t.Fatalf("supported=%v err=%v", supported, err)
	}
	if post.Dir != "" {
		defer os.RemoveAll(post.Dir)
	}
	if len(post.Items) != 1 || post.Items[0].Kind != "photo" {
		t.Fatalf("из резервного API ждали одно фото: %+v", post.Items)
	}
	if post.Title != "Луна" {
		t.Fatalf("заголовок %q", post.Title)
	}
}

func TestXPostMediaBothAPIsFail(t *testing.T) {
	withXEndpoints(t, "", "")
	dl := Downloader{MaxSizeMB: 50}
	_, supported, err := dl.XPostMedia(context.Background(), "https://x.com/NASA/status/2110371804959191288")
	if !supported {
		t.Fatal("пост X должен попадать в ветку X")
	}
	if err == nil {
		t.Fatal("при отказе обоих API ждали ошибку")
	}
	if !strings.Contains(err.Error(), "fxtwitter") || !strings.Contains(err.Error(), "vxtwitter") {
		t.Fatalf("в ошибке нет обоих источников: %v", err)
	}
}

func TestXPostMediaNotAPost(t *testing.T) {
	dl := Downloader{}
	if _, supported, err := dl.XPostMedia(context.Background(), "https://x.com/NASA"); supported || err != nil {
		t.Fatalf("профиль не пост: supported=%v err=%v", supported, err)
	}
	if _, supported, _ := dl.XPostMedia(context.Background(), "https://example.com/a/status/1"); supported {
		t.Fatal("чужая ссылка не пост")
	}
}
