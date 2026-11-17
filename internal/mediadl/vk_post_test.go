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

func TestParseVKWallPostURL(t *testing.T) {
	cases := []struct {
		raw     string
		owner   int64
		post    int64
		wantOK  bool
		comment string
	}{
		{"https://vk.ru/wall-50930433_228697", -50930433, 228697, true, "vk.ru из примера юзера"},
		{"https://vk.com/wall-50930433_228697", -50930433, 228697, true, "vk.com"},
		{"https://m.vk.com/wall-1_2", -1, 2, true, "мобильный домен"},
		{"http://www.vk.com/wall42_7?reply=4", 42, 7, true, "личная стена + query"},
		{"https://vk.com/vk.modish?w=wall-50930433_228697", -50930433, 228697, true, "ссылка с экрана группы"},
		{"https://vk.com/audio-200123_45", 0, 0, false, "аудио — не пост"},
		{"https://vk.com/video-50930433_456239130", 0, 0, false, "видео — не пост"},
		{"https://vk.com/feed", 0, 0, false, "лента"},
		{"https://example.com/wall-1_2", 0, 0, false, "чужой домен"},
		{"", 0, 0, false, "пустая строка"},
	}
	for _, tc := range cases {
		ref, ok := ParseVKWallPostURL(tc.raw)
		if ok != tc.wantOK {
			t.Fatalf("%s: ok=%v, ждали %v", tc.comment, ok, tc.wantOK)
		}
		if !tc.wantOK {
			continue
		}
		if ref.OwnerID != tc.owner || ref.PostID != tc.post {
			t.Fatalf("%s: получили %d_%d, ждали %d_%d", tc.comment, ref.OwnerID, ref.PostID, tc.owner, tc.post)
		}
	}
}

func TestPickVKPhotoURLPrefersLargest(t *testing.T) {
	sizes := []vkPhotoSize{
		{Type: "s", URL: "https://cdn/s.jpg", Width: 75, Height: 100},
		{Type: "x", URL: "https://cdn/x.jpg", Width: 604, Height: 807},
		{Type: "z", URL: "https://cdn/z.jpg", Width: 1080, Height: 1440},
		{Type: "w", URL: "https://cdn/w.jpg", Width: 1440, Height: 1920},
		{Type: "base", URL: "https://cdn/base.jpg", Width: 2000, Height: 2600},
	}
	if got := pickVKPhotoURL(sizes); got != "https://cdn/w.jpg" {
		t.Fatalf("ждали размер w, получили %q", got)
	}
	// Без известных типов остаётся выбор по площади.
	fallback := []vkPhotoSize{
		{Type: "base", URL: "https://cdn/base.jpg", Width: 2000, Height: 2600},
		{Type: "custom", URL: "https://cdn/custom.jpg", Width: 100, Height: 100},
	}
	if got := pickVKPhotoURL(fallback); got != "https://cdn/base.jpg" {
		t.Fatalf("фолбек по площади дал %q", got)
	}
	if got := pickVKPhotoURL(nil); got != "" {
		t.Fatalf("пустой список размеров дал %q", got)
	}
}

func TestVKPostTitle(t *testing.T) {
	if got := vkPostTitle("\n\nТренды украшений 2027 ⚡️\n\nдальше текст"); got != "Тренды украшений 2027 ⚡️" {
		t.Fatalf("заголовок %q", got)
	}
	if got := vkPostTitle(strings.Repeat("я", 300)); len([]rune(got)) != 120 {
		t.Fatalf("длинная строка не обрезана: %d рун", len([]rune(got)))
	}
	if got := vkPostTitle("   \n  "); got != "" {
		t.Fatalf("пустой пост дал %q", got)
	}
}

func TestVKVideoURL(t *testing.T) {
	if got := vkVideoURL(-50930433, 456239130, ""); got != "https://vk.com/video-50930433_456239130" {
		t.Fatalf("без access_key: %q", got)
	}
	if got := vkVideoURL(-1, 2, "abc"); got != "https://vk.com/video-1_2_abc" {
		t.Fatalf("с access_key: %q", got)
	}
}

// vkTestPost собирает ответ wall.getById: фото со ссылками на локальный сервер,
// видео без идентификатора (скачать нельзя) и вложение-ссылку.
func vkTestPost(imageURL string) string {
	payload := map[string]any{
		"response": map[string]any{
			"items": []any{map[string]any{
				"owner_id": -50930433,
				"id":       228697,
				"text":     "Тренды украшений 2027 ⚡️",
				"attachments": []any{
					map[string]any{"type": "photo", "photo": map[string]any{"sizes": []any{
						map[string]any{"type": "x", "url": imageURL, "width": 604, "height": 807},
						map[string]any{"type": "w", "url": imageURL, "width": 1440, "height": 1920},
					}}},
					map[string]any{"type": "photo", "photo": map[string]any{"sizes": []any{
						map[string]any{"type": "w", "url": imageURL, "width": 1440, "height": 1920},
					}}},
					map[string]any{"type": "video", "video": map[string]any{"owner_id": -50930433, "id": 0}},
					map[string]any{"type": "link", "link": map[string]any{"url": "https://example.com"}},
				},
			}},
		},
	}
	body, _ := json.Marshal(payload)
	return string(body)
}

func TestVKWallPostCarousel(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/img") {
			rw.Header().Set("Content-Type", "image/jpeg")
			// Минимальный JPEG: заголовок SOI и EOI; расширение задаёт имя файла.
			_, _ = rw.Write([]byte{0xff, 0xd8, 0xff, 0xe0, 0x00, 0x10, 0x4a, 0x46, 0x49, 0x46, 0xff, 0xd9})
			return
		}
		if !strings.HasPrefix(r.URL.Path, "/wall.getById") {
			t.Errorf("неожиданный путь %s", r.URL.Path)
			return
		}
		if r.URL.Query().Get("access_token") == "" {
			t.Errorf("токен не передан в VK API")
		}
		if got := r.URL.Query().Get("posts"); got != "-50930433_228697" {
			t.Errorf("posts=%q", got)
		}
		_, _ = rw.Write([]byte(vkTestPost(server.URL + "/img.jpg")))
	}))
	defer server.Close()

	oldEndpoint := vkAPIEndpoint
	vkAPIEndpoint = server.URL + "/"
	defer func() { vkAPIEndpoint = oldEndpoint }()

	dl := Downloader{VKToken: "test-token", MaxSizeMB: 50}
	carousel, supported, err := dl.VKWallPostCarousel(context.Background(), "https://vk.ru/wall-50930433_228697")
	if !supported {
		t.Fatal("пост VK должен считаться поддерживаемым")
	}
	if err != nil {
		t.Fatalf("карусель: %v", err)
	}
	if carousel.Dir != "" {
		defer os.RemoveAll(carousel.Dir)
	}
	if len(carousel.Items) != 2 {
		t.Fatalf("элементов %d, ждали 2 (видео без id и ссылка пропускаются): %+v", len(carousel.Items), carousel.Items)
	}
	for i, item := range carousel.Items {
		if item.Kind != "photo" {
			t.Fatalf("элемент %d не фото: %+v", i, item)
		}
		if st, statErr := os.Stat(item.Path); statErr != nil || st.Size() == 0 {
			t.Fatalf("файл элемента %d не скачался: %v", i, statErr)
		}
	}
	if carousel.Title != "Тренды украшений 2027 ⚡️" {
		t.Fatalf("заголовок %q", carousel.Title)
	}
	if carousel.SourceURL != "https://vk.com/wall-50930433_228697" {
		t.Fatalf("ссылка источника %q", carousel.SourceURL)
	}
	if carousel.Service != ServiceVK {
		t.Fatalf("сервис %q", carousel.Service)
	}
}

func TestVKWallPostCarouselNotAPost(t *testing.T) {
	dl := Downloader{VKToken: "test-token"}
	if _, supported, err := dl.VKWallPostCarousel(context.Background(), "https://vk.com/video-50930433_456239130"); supported || err != nil {
		t.Fatalf("видео-ссылка не должна считаться постом: supported=%v err=%v", supported, err)
	}
	if _, supported, _ := dl.VKWallPostCarousel(context.Background(), "https://example.com/wall-1_2"); supported {
		t.Fatal("чужая ссылка не должна считаться постом")
	}
}

func TestVKWallPostCarouselTokenMissing(t *testing.T) {
	dl := Downloader{}
	_, supported, err := dl.VKWallPostCarousel(context.Background(), "https://vk.com/wall-1_2")
	if !supported {
		t.Fatal("пост должен попадать в ветку VK даже без токена")
	}
	if err == nil || !strings.Contains(err.Error(), "VK_TOKEN") {
		t.Fatalf("ждали ошибку про VK_TOKEN, получили %v", err)
	}
}

func TestVKWallPostAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		_, _ = rw.Write([]byte(`{"error":{"error_code":5,"error_msg":"User authorization failed: invalid access_token."}}`))
	}))
	defer server.Close()
	oldEndpoint := vkAPIEndpoint
	vkAPIEndpoint = server.URL + "/"
	defer func() { vkAPIEndpoint = oldEndpoint }()

	dl := Downloader{VKToken: "bad"}
	_, supported, err := dl.VKWallPostCarousel(context.Background(), "https://vk.com/wall-1_2")
	if !supported || err == nil {
		t.Fatalf("ошибка API должна доходить наверх: supported=%v err=%v", supported, err)
	}
	if !strings.Contains(err.Error(), "invalid access_token") {
		t.Fatalf("текст ошибки %v", err)
	}
}

func TestVKWallPostCarouselAllItemsFailed(t *testing.T) {
	// Ничего не скачалось — юзер должен увидеть причину, а не «в посте нет медиа».
	payload := `{"response":{"items":[{"owner_id":-1,"id":2,"text":"видео","attachments":[{"type":"video","video":{"owner_id":-1,"id":0}}]}]}}`
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		_, _ = rw.Write([]byte(payload))
	}))
	defer server.Close()
	oldEndpoint := vkAPIEndpoint
	vkAPIEndpoint = server.URL + "/"
	defer func() { vkAPIEndpoint = oldEndpoint }()

	dl := Downloader{VKToken: "test-token", MaxSizeMB: 50}
	_, supported, err := dl.VKWallPostCarousel(context.Background(), "https://vk.com/wall-1_2")
	if !supported || err == nil {
		t.Fatalf("ждали ошибку: supported=%v err=%v", supported, err)
	}
	if !strings.Contains(err.Error(), "не удалось скачать вложения VK") {
		t.Fatalf("текст ошибки %v", err)
	}
	if !strings.Contains(err.Error(), "вложение видео пустое") {
		t.Fatalf("в ошибке нет причины: %v", err)
	}
}
