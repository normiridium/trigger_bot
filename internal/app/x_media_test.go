package app

// X: фото-пост уходит каруселью, пост без вложений — одной строкой в чат.

import (
	"context"
	"strings"
	"testing"

	"trigger-admin-bot/internal/mediadl"
)

func TestXCarouselCaptionUsesXEmoji(t *testing.T) {
	items := []mediadl.CarouselItem{
		{Kind: "photo", Path: "/tmp/a.jpg", Index: 1},
		{Kind: "photo", Path: "/tmp/b.jpg", Index: 2},
	}
	caption := buildCarouselCompositionCaption(mediadl.ServiceX, "Луна", "https://x.com/NASA/status/1", items)
	if !strings.Contains(caption, "5465453979896913711") {
		t.Fatalf("в подписи нет значка X: %s", caption)
	}
	if !strings.Contains(caption, "2 фото") {
		t.Fatalf("состав подписи неверный: %s", caption)
	}
	if strings.Contains(caption, "5463101531819613579") {
		t.Fatalf("подпись X получила значок VK: %s", caption)
	}
}

func TestSendXPostMediaSkipsNonPostLinks(t *testing.T) {
	cases := []string{
		"https://x.com/NASA",
		"https://x.com/home",
		"https://vk.ru/wall-50930433_228697",
		"https://www.instagram.com/p/DAbc123/",
		"https://example.com/a/status/1",
	}
	for _, raw := range cases {
		handled, err := sendXPostMedia(context.Background(), sendContext{ChatID: 1}, nil, raw, mediadl.ModeVideo, nil)
		if handled || err != nil {
			t.Fatalf("%s: handled=%v err=%v, ветка X не должна срабатывать", raw, handled, err)
		}
	}
}

func TestSendXPostMediaNilDownloader(t *testing.T) {
	handled, err := sendXPostMedia(context.Background(), sendContext{ChatID: 1}, nil, "https://x.com/NASA/status/2110371804959191288", mediadl.ModeVideo, nil)
	if !handled {
		t.Fatal("пост X должен забирать ветку X")
	}
	if err == nil {
		t.Fatal("без downloader ждали ошибку")
	}
}

func TestErrXPostNoMediaIsRecognised(t *testing.T) {
	// Воркер отличает «в посте нет медиа» от сбоя загрузки по этому значению.
	if !strings.Contains(errXPostNoMedia.Error(), "твит") {
		t.Fatalf("текст для чата: %q", errXPostNoMedia.Error())
	}
}
