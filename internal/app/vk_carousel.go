package app

// VK: карусель поста как статья со слайдшоу — по аналогии с Instagram.
//
// Одиночное вложение (одно фото или одно видео) уходит обычным сообщением:
// слайдшоу из одного элемента Telegram показывает как карусель из одной
// страницы, это лишняя обёртка.

import (
	"context"
	"errors"
	"log"
	"os"
	"strings"
	"time"

	"trigger-admin-bot/internal/mediadl"
)

var errVKCarouselNothing = errors.New("в посте VK не нашлось фото или видео")

// sendVKCarousel скачивает и отправляет карусель поста VK.
// handled=false — ссылка не на пост VK, дальше работает обычный путь.
func sendVKCarousel(ctx context.Context, sendCtx sendContext, dl MediaDownloadPort, rawURL string, mode mediadl.Mode, progress *mediaProgressHandle) (bool, error) {
	if _, service, ok := mediadl.NormalizeSupportedURL(rawURL); !ok || service != mediadl.ServiceVK {
		return false, nil
	}
	if mode == mediadl.ModeAudio {
		return false, nil
	}
	if _, isPost := mediadl.ParseVKWallPostURL(rawURL); !isPost {
		return false, nil
	}
	if dl == nil {
		return true, errors.New("media downloader is not configured")
	}
	if progress != nil {
		progress.SetFrame(0)
		progress.SetStage("Скачивание")
	}

	dlCtx, cancelDl := context.WithTimeout(withMediaDownloadProgress(ctx, progress), 12*time.Minute)
	carousel, supported, err := dl.VKWallPostCarousel(dlCtx, rawURL)
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
				log.Printf("media vk carousel cleanup failed dir=%q err=%v", carousel.Dir, rmErr)
			}
		}()
	}
	if len(carousel.Items) == 0 {
		return true, errVKCarouselNothing
	}

	for _, item := range carousel.Items {
		if err := ensureTelegramUploadLimit(item.Path); err != nil {
			return true, err
		}
	}

	title := strings.TrimSpace(carousel.Title)
	sourceURL := strings.TrimSpace(carousel.SourceURL)
	if sourceURL == "" {
		sourceURL = strings.TrimSpace(rawURL)
	}
	if title == "" {
		title = sourceURL
	}

	if len(carousel.Items) == 1 {
		item := carousel.Items[0]
		if progress != nil {
			progress.SetFrame(7)
			progress.SetStage("Отправка")
		}
		if item.Kind == "video" {
			err = sendVideoFromFile(sendCtx, item.Path, buildMediaVideoCaption(item.Path, title, sourceURL, mediadl.ServiceVK))
		} else {
			err = sendPhotoFromFile(sendCtx, item.Path, buildMediaPhotoCaption(item.Path, title, sourceURL, mediadl.ServiceVK))
		}
		if err != nil {
			return true, err
		}
		log.Printf("media vk post sent chat=%d kind=%s skipped=%d title=%q",
			sendCtx.ChatID, item.Kind, carousel.Skipped, clipText(title, 120))
		return true, nil
	}

	caption := buildCarouselCompositionCaption(mediadl.ServiceVK, title, sourceURL, carousel.Items)
	html, specs := buildCarouselSlideshow(caption, carousel.Items)

	if progress != nil {
		progress.SetFrame(7)
		progress.SetStage("Отправка карусели")
	}
	sent, err := sendRichArticleSlideshow(sendCtx, html, carouselRichMedia(specs))
	if err != nil {
		return true, err
	}
	log.Printf("media vk carousel sent chat=%d msg=%d items=%d videos=%d skipped=%d title=%q",
		sendCtx.ChatID, sent.MessageID, len(carousel.Items), countCarouselVideos(specs), carousel.Skipped, clipText(title, 120))
	return true, nil
}
