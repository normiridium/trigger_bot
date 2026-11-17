package app

// Подпись и ветвление карусели VK: та же статья со слайдшоу, что у Instagram,
// но со значком VK и ссылкой на запись со стены.

import (
	"context"
	"strings"
	"testing"

	"trigger-admin-bot/internal/mediadl"
)

func TestVKCarouselCompositionCaption(t *testing.T) {
	items := []mediadl.CarouselItem{
		{Kind: "photo", Path: "/tmp/a.jpg", Index: 1},
		{Kind: "photo", Path: "/tmp/b.jpg", Index: 2},
		{Kind: "video", Path: "/tmp/c.mp4", Index: 3},
	}
	caption := buildCarouselCompositionCaption(mediadl.ServiceVK, "Тренды украшений 2027", "https://vk.com/wall-50930433_228697", items)
	if !strings.Contains(caption, "5463101531819613579") {
		t.Fatalf("в подписи нет значка VK: %s", caption)
	}
	if !strings.Contains(caption, `<a href="https://vk.com/wall-50930433_228697">Тренды украшений 2027</a>`) {
		t.Fatalf("заголовок не ссылка на пост: %s", caption)
	}
	if !strings.Contains(caption, "2 фото + 1 видео") {
		t.Fatalf("состав подписи неверный: %s", caption)
	}
	// Instagram остаётся со своим значком: подпись собирается по сервису.
	instagram := buildCarouselCompositionCaption(mediadl.ServiceInstagram, "x", "https://instagram.com/p/abc", items)
	if strings.Contains(instagram, "5463101531819613579") {
		t.Fatalf("instagram получил значок VK: %s", instagram)
	}
}

func TestSendVKCarouselSkipsNonVKLinks(t *testing.T) {
	cases := []string{
		"https://www.instagram.com/p/DAbc123/",
		"https://vk.com/video-50930433_456239130",
		"https://vk.com/vk.modish",
		"https://example.com/wall-1_2",
	}
	for _, raw := range cases {
		handled, err := sendVKCarousel(context.Background(), sendContext{ChatID: 1}, nil, raw, mediadl.ModeAuto, nil)
		if handled || err != nil {
			t.Fatalf("%s: handled=%v err=%v, ветка VK не должна срабатывать", raw, handled, err)
		}
	}
}

func TestSendVKCarouselPostWithoutDownloader(t *testing.T) {
	// Пост VK без downloader — это ошибка, а не тихий выход в общий путь:
	// иначе yt-dlp вернёт пустой плейлист и в чат уедет непонятная ошибка.
	handled, err := sendVKCarousel(context.Background(), sendContext{ChatID: 1}, nil, "https://vk.ru/wall-50930433_228697", mediadl.ModeAuto, nil)
	if !handled {
		t.Fatal("пост VK должен забирать ветку VK")
	}
	if err == nil {
		t.Fatal("без downloader ждали ошибку")
	}
}
