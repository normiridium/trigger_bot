package app

// Сборка слайдшоу (карусели) для Instagram: порядок элементов и разметка.

import (
	"strings"
	"testing"

	"trigger-admin-bot/internal/mediadl"
)

func carouselItems() []mediadl.CarouselItem {
	return []mediadl.CarouselItem{
		{Kind: "photo", Path: "/tmp/a.jpg", Index: 1},
		{Kind: "video", Path: "/tmp/b.mp4", Index: 2},
		{Kind: "photo", Path: "/tmp/c.jpg", Index: 3},
	}
}

func TestBuildCarouselSlideshow(t *testing.T) {
	html, specs := buildCarouselSlideshow("подпись", carouselItems())
	if !strings.HasPrefix(html, "<tg-slideshow>") || !strings.HasSuffix(html, "</tg-slideshow>") {
		t.Fatalf("нет блока слайдшоу: %s", html)
	}
	want := `<img src="tg://photo?id=m1"/><video src="tg://video?id=m2"/><img src="tg://photo?id=m3"/>`
	if !strings.Contains(html, want) {
		t.Fatalf("порядок медиа нарушен:\n получили %s\n ждали   %s", html, want)
	}
	if !strings.Contains(html, "<figcaption>подпись</figcaption>") {
		t.Fatalf("подпись не в figcaption: %s", html)
	}
	if len(specs) != 3 || specs[1].Kind != "video" || specs[1].ID != "m2" {
		t.Fatalf("неверные элементы: %+v", specs)
	}
}

func TestBuildCarouselSlideshowWithoutCaption(t *testing.T) {
	html, _ := buildCarouselSlideshow("   ", carouselItems())
	if strings.Contains(html, "figcaption") {
		t.Fatalf("пустая подпись не должна попадать в статью: %s", html)
	}
}

func TestBuildCarouselSlideshowLongCaptionClipped(t *testing.T) {
	long := strings.Repeat("я", 2000)
	html, _ := buildCarouselSlideshow(long, carouselItems())
	inside := html[strings.Index(html, "<figcaption>")+len("<figcaption>") : strings.Index(html, "</figcaption>")]
	if runes := []rune(inside); len(runes) > 1024 {
		t.Fatalf("подпись не обрезана: %d символов", len(runes))
	}
}

func TestCarouselRichMediaKinds(t *testing.T) {
	_, specs := buildCarouselSlideshow("x", carouselItems())
	media := carouselRichMedia(specs)
	if len(media) != 3 {
		t.Fatalf("медиа %d", len(media))
	}
	if media[1].ID != "m2" {
		t.Fatalf("id видео не совпадает: %+v", media[1])
	}
	if got := countCarouselVideos(specs); got != 1 {
		t.Fatalf("видео насчитали %d", got)
	}
}
