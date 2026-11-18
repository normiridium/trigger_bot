package app

import (
	"os"
	"strings"
	"testing"
)

func TestSerpImagesRequestedIsSix(t *testing.T) {
	// Юзер попросил сузить выбор до шести результатов выдачи.
	if serpImagesRequested != 6 {
		t.Fatalf("просим у SerpAPI %d результатов, ждали 6", serpImagesRequested)
	}
}

func TestCollectSerpImageCandidates(t *testing.T) {
	results := []serpImageResult{
		{Original: "https://cdn/a.jpg", Thumbnail: "https://cdn/a_thumb.jpg", Link: "https://news.example/a"},
		{Original: "https://cdn/a.jpg", Thumbnail: "https://cdn/a_thumb.jpg", Link: "https://news.example/a"},
		{Link: "https://news.example/b"},
	}
	candidates := collectSerpImageCandidates(results)
	// Первый результат даёт три ссылки (original, thumbnail, link) — все со
	// страницей-источником; второй результат дублирует их и пропускается;
	// третий даёт только link.
	if len(candidates) != 4 {
		t.Fatalf("кандидатов %d, ждали 4: %+v", len(candidates), candidates)
	}
	if candidates[0].ImageURL != "https://cdn/a.jpg" || candidates[0].SourceURL != "https://news.example/a" {
		t.Fatalf("original без источника: %+v", candidates[0])
	}
	if candidates[1].ImageURL != "https://cdn/a_thumb.jpg" || candidates[1].SourceURL != "https://news.example/a" {
		t.Fatalf("thumbnail без источника: %+v", candidates[1])
	}
	if candidates[2].ImageURL != "https://news.example/a" || candidates[2].SourceURL != "https://news.example/a" {
		t.Fatalf("link как картинка: %+v", candidates[2])
	}
	if candidates[3].ImageURL != "https://news.example/b" || candidates[3].SourceURL != "https://news.example/b" {
		t.Fatalf("единственная ссылка третьего результата: %+v", candidates[3])
	}
}

func TestCollectSerpImageCandidatesEmpty(t *testing.T) {
	if got := collectSerpImageCandidates(nil); len(got) != 0 {
		t.Fatalf("пустая выдача дала %+v", got)
	}
	if got := collectSerpImageCandidates([]serpImageResult{{Original: "  "}}); len(got) != 0 {
		t.Fatalf("пустые ссылки дали %+v", got)
	}
}

func TestSearchImageCaption(t *testing.T) {
	if got := searchImageCaption("https://news.example/a"); got != "Источник: https://news.example/a" {
		t.Fatalf("подпись %q", got)
	}
	if got := searchImageCaption("   "); got != "" {
		t.Fatalf("пустой источник дал подпись %q", got)
	}
	long := "https://example.com/" + strings.Repeat("a", 2000)
	if got := searchImageCaption(long); len([]rune(got)) > 1024 {
		t.Fatalf("длинная ссылка не влезла в лимит подписи: %d символов", len([]rune(got)))
	}
}

// TestSearchImageLive — живой запрос в SerpAPI: проверяет, что картинка
// скачивается, и что вместе с ней приходит страница-источник для подписи.
//
//	SERPAPI_KEY=… SERP_IMAGE_QUERY="Штефанов портрет фото" \
//	  go test -run TestSearchImageLive -v ./internal/app
func TestSearchImageLive(t *testing.T) {
	query := strings.TrimSpace(os.Getenv("SERP_IMAGE_QUERY"))
	if query == "" || strings.TrimSpace(os.Getenv("SERPAPI_KEY")) == "" {
		t.Skip("нужны SERP_IMAGE_QUERY и SERPAPI_KEY")
	}
	img, err := searchImageInSerpAPI(query)
	if err != nil {
		t.Fatalf("поиск картинки: %v", err)
	}
	if len(img.Bytes) == 0 {
		t.Fatal("картинка пустая")
	}
	t.Logf("байт=%d источник=%q", len(img.Bytes), img.SourceURL)
	t.Logf("подпись=%q", searchImageCaption(img.SourceURL))
	if strings.TrimSpace(img.SourceURL) == "" {
		t.Fatal("источник не пришёл — подписи не будет")
	}
}
