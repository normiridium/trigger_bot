package app

// X (x.com / twitter.com): фото-посты отдаём каруселью, как у VK и Instagram.
//
// Видео у X качает прежний путь yt-dlp (там он работает), а этот файл берёт на
// себя то, чего yt-dlp не умеет: пост с картинками и пост вообще без вложений.
// Твит без медиа — это не сбой загрузки, а состояние поста: бот отвечает одной
// строкой вместо «ошибка скачивания, подробности в логах».

import (
	"context"
	"errors"
	"log"
	"os"
	"strings"
	"time"

	"trigger-admin-bot/internal/mediadl"
)

// errXPostNoMedia — твит без вложений: воркер отвечает по-человечески и не
// удаляет исходную ссылку из чата.
var errXPostNoMedia = errors.New("в этом твите нет ни видео, ни картинок")

// sendXPostMedia разбирает пост X и отправляет фото каруселью.
// handled=false — в посте есть видео (его качает yt-dlp) или это не пост X.
func sendXPostMedia(ctx context.Context, sendCtx sendContext, dl MediaDownloadPort, rawURL string, mode mediadl.Mode, progress *mediaProgressHandle) (bool, error) {
	if _, service, ok := mediadl.NormalizeSupportedURL(rawURL); !ok || service != mediadl.ServiceX {
		return false, nil
	}
	if mode == mediadl.ModeAudio {
		return false, nil
	}
	if _, isPost := mediadl.ParseXPostURL(rawURL); !isPost {
		return false, nil
	}
	if dl == nil {
		return true, errors.New("media downloader is not configured")
	}
	if progress != nil {
		progress.SetFrame(0)
		progress.SetStage("Разбор поста")
	}

	dlCtx, cancelDl := context.WithTimeout(withMediaDownloadProgress(ctx, progress), 3*time.Minute)
	post, supported, err := dl.XPostMedia(dlCtx, rawURL)
	cancelDl()
	if !supported {
		return false, nil
	}
	if err != nil {
		if errors.Is(err, mediadl.ErrXPostPhotosFailed) {
			return true, err
		}
		// FixTweet недоступен: не отбираем у yt-dlp видео-посты, пусть пробует.
		log.Printf("media x post probe failed chat=%d url=%q err=%v", sendCtx.ChatID, clipText(rawURL, 200), err)
		return false, nil
	}
	if strings.TrimSpace(post.Dir) != "" {
		defer func() {
			if rmErr := os.RemoveAll(post.Dir); rmErr != nil && debugTriggerLogEnabled {
				log.Printf("media x post cleanup failed dir=%q err=%v", post.Dir, rmErr)
			}
		}()
	}
	if post.HasVideo {
		// Видео (и GIF) отдаём обычному пути скачивания.
		return false, nil
	}
	if !post.HasMedia {
		return true, errXPostNoMedia
	}
	if len(post.Items) == 0 {
		return true, errors.New("в посте X не удалось скачать ни одной картинки")
	}

	for _, item := range post.Items {
		if err := ensureTelegramUploadLimit(item.Path); err != nil {
			return true, err
		}
	}

	title := strings.TrimSpace(post.Title)
	sourceURL := strings.TrimSpace(post.SourceURL)
	if sourceURL == "" {
		sourceURL = strings.TrimSpace(rawURL)
	}
	if title == "" {
		title = sourceURL
	}

	if len(post.Items) == 1 {
		item := post.Items[0]
		if progress != nil {
			progress.SetFrame(7)
			progress.SetStage("Отправка фото")
		}
		if err := sendPhotoFromFile(sendCtx, item.Path, buildMediaPhotoCaption(item.Path, title, sourceURL, mediadl.ServiceX)); err != nil {
			return true, err
		}
		log.Printf("media x post sent chat=%d photos=1 skipped=%d title=%q", sendCtx.ChatID, post.Skipped, clipText(title, 120))
		return true, nil
	}

	caption := buildCarouselCompositionCaption(mediadl.ServiceX, title, sourceURL, post.Items)
	html, specs := buildCarouselSlideshow(caption, post.Items)
	if progress != nil {
		progress.SetFrame(7)
		progress.SetStage("Отправка карусели")
	}
	sent, err := sendRichArticleSlideshow(sendCtx, html, carouselRichMedia(specs))
	if err != nil {
		return true, err
	}
	log.Printf("media x carousel sent chat=%d msg=%d photos=%d skipped=%d title=%q",
		sendCtx.ChatID, sent.MessageID, len(post.Items), post.Skipped, clipText(title, 120))
	return true, nil
}
