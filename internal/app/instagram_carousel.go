package app

// Instagram-карусель как статья (rich message) со слайдшоу: фото и видео
// листаются в одном сообщении.
//
// По спеке Bot API слайдшоу — это блок <tg-slideshow>, внутри которого идут
// <img src="tg://photo?id=…"/> и <video src="tg://video?id=…"/>, а подпись —
// <figcaption>. Просто картинки в тексте статьи каруселью не становятся.

import (
	"context"
	"errors"
	"fmt"
	"html"
	"log"
	"os"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"trigger-admin-bot/internal/mediadl"
)

var errInstagramCarouselNothing = errors.New("в карусели instagram не нашлось элементов")

// buildCarouselSlideshow собирает HTML статьи: слайдшоу из фото и видео.
func buildCarouselSlideshow(caption string, items []mediadl.CarouselItem) (string, []carouselMediaSpec) {
	specs := make([]carouselMediaSpec, 0, len(items))
	var page strings.Builder
	page.WriteString("<tg-slideshow>")
	for i, item := range items {
		id := fmt.Sprintf("m%d", i+1)
		if item.Kind == "video" {
			page.WriteString(`<video src="tg://video?id=` + id + `"/>`)
		} else {
			page.WriteString(`<img src="tg://photo?id=` + id + `"/>`)
		}
		specs = append(specs, carouselMediaSpec{ID: id, Kind: item.Kind, Path: item.Path})
	}
	if caption = strings.TrimSpace(caption); caption != "" {
		// clipText добавляет многоточие, поэтому оставляем запас.
		page.WriteString("<figcaption>" + clipText(caption, 1021) + "</figcaption>")
	}
	page.WriteString("</tg-slideshow>")
	return page.String(), specs
}

// carouselMediaSpec — элемент слайдшоу: id, вид и файл.
type carouselMediaSpec struct {
	ID   string
	Kind string
	Path string
}

// carouselRichMedia превращает элементы в медиа статьи.
func carouselRichMedia(specs []carouselMediaSpec) []tgbotapi.InputRichMessageMedia {
	media := make([]tgbotapi.InputRichMessageMedia, 0, len(specs))
	for _, spec := range specs {
		if spec.Kind == "video" {
			video := tgbotapi.NewInputMediaVideo(tgbotapi.FilePath(spec.Path))
			video.SupportsStreaming = true
			media = append(media, tgbotapi.NewInputRichMessageMedia(spec.ID, video))
			continue
		}
		media = append(media, tgbotapi.NewInputRichMessageMediaPhoto(spec.ID, tgbotapi.FilePath(spec.Path)))
	}
	return media
}

// sendRichArticleSlideshow отправляет статью со слайдшоу.
func sendRichArticleSlideshow(ctx sendContext, html string, media []tgbotapi.InputRichMessageMedia) (tgbotapi.Message, error) {
	if ctx.Bot == nil {
		return tgbotapi.Message{}, errors.New("telegram bot is nil")
	}
	cfg := tgbotapi.NewRichMessageHTMLWithMedia(ctx.ChatID, html, media)
	if ctx.ReplyTo > 0 {
		cfg.ReplyToMessageID = ctx.ReplyTo
		cfg.AllowSendingWithoutReply = true
	}
	return sendRichMessageWithFloodRetry(ctx, cfg)
}

// sendInstagramCarousel публикует мультиэлементный пост Instagram слайдшоу.
// handled=false — ссылка не instagram или пост не карусель.
func sendInstagramCarousel(ctx context.Context, sendCtx sendContext, dl MediaDownloadPort, rawURL string, mode mediadl.Mode, progress *mediaProgressHandle) (bool, error) {
	if _, service, ok := mediadl.NormalizeSupportedURL(rawURL); !ok || service != mediadl.ServiceInstagram {
		return false, nil
	}
	if mode == mediadl.ModeAudio {
		return false, nil
	}
	if progress != nil {
		progress.SetFrame(0)
		progress.SetStage("Скачивание")
	}

	dlCtx, cancelDl := context.WithTimeout(withMediaDownloadProgress(ctx, progress), 12*time.Minute)
	carousel, supported, err := dl.InstagramCarousel(dlCtx, rawURL)
	cancelDl()
	if !supported {
		return false, nil
	}
	if err != nil {
		return true, err
	}
	if strings.TrimSpace(carousel.Dir) != "" {
		defer func() {
			if rmErr := os.RemoveAll(carousel.Dir); rmErr != nil && debugTriggerLogEnabled {
				log.Printf("media instagram carousel cleanup failed dir=%q err=%v", carousel.Dir, rmErr)
			}
		}()
	}
	if len(carousel.Items) < 2 {
		return true, errInstagramCarouselNothing
	}

	for _, item := range carousel.Items {
		if err := ensureTelegramUploadLimit(item.Path); err != nil {
			return true, err
		}
	}

	caption := buildCarouselCompositionCaption(carousel.Title, carousel.SourceURL, carousel.Items)
	html, specs := buildCarouselSlideshow(caption, carousel.Items)

	if progress != nil {
		progress.SetFrame(7)
		progress.SetStage("Отправка карусели")
	}
	sent, err := sendRichArticleSlideshow(sendCtx, html, carouselRichMedia(specs))
	if err != nil {
		return true, err
	}
	log.Printf("media instagram carousel sent chat=%d msg=%d items=%d videos=%d skipped=%d title=%q",
		sendCtx.ChatID, sent.MessageID, len(carousel.Items), countCarouselVideos(specs), carousel.Skipped, clipText(carousel.Title, 120))
	return true, nil
}

func countCarouselVideos(specs []carouselMediaSpec) int {
	total := 0
	for _, spec := range specs {
		if spec.Kind == "video" {
			total++
		}
	}
	return total
}

// buildCarouselCompositionCaption подписывает карусель по составу:
// «3 фото + 1 видео» вместо неверного «4 фото».
func buildCarouselCompositionCaption(title, sourceURL string, items []mediadl.CarouselItem) string {
	photos, videos := 0, 0
	for _, item := range items {
		if item.Kind == "video" {
			videos++
		} else {
			photos++
		}
	}
	title = strings.TrimSpace(title)
	sourceURL = strings.TrimSpace(sourceURL)
	emoji := mediaServiceEmoji(mediadl.ServiceInstagram, mediadl.ModeVideo)
	stats := carouselCompositionStats(photos, videos)
	head := html.EscapeString(title)
	if sourceURL != "" {
		if head == "" {
			return emoji + " " + buildSourceLinkHTML(sourceURL, "ссылка") + " · " + stats
		}
		head = `<a href="` + html.EscapeString(sourceURL) + `">` + head + `</a>`
	}
	if head == "" {
		return emoji + " " + stats
	}
	return emoji + " " + head + " · " + stats
}

// carouselCompositionStats — человеческий счёт элементов карусели.
func carouselCompositionStats(photos, videos int) string {
	switch {
	case videos == 0:
		return fmt.Sprintf("%d фото", photos)
	case photos == 0:
		return fmt.Sprintf("%d видео", videos)
	default:
		return fmt.Sprintf("%d фото + %d видео", photos, videos)
	}
}
