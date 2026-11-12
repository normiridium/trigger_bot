package app

// Отправка медиа из постов Bluesky (bsky.app / fxbsky.app).
//
// Медиа забирает mediadl.BlueskyPost через публичный AppView; здесь — только
// доставка в чат теми же путями, что у остальных сервисов.

import (
	"context"
	"errors"
	"log"
	"os"
	"strings"
	"time"

	"trigger-admin-bot/internal/mediadl"
)

var errBlueskyNothing = errors.New("в посте bluesky не нашлось медиа")

// sendBlueskyMedia скачивает и отправляет медиа поста Bluesky.
// handled=false — ссылка не bluesky, дальше работает обычный путь.
func sendBlueskyMedia(ctx context.Context, sendCtx sendContext, dl MediaDownloadPort, rawURL string, mode mediadl.Mode, progress *mediaProgressHandle) (bool, error) {
	if _, service, ok := mediadl.NormalizeSupportedURL(rawURL); !ok || service != mediadl.ServiceBluesky {
		return false, nil
	}
	if mode == mediadl.ModeAudio {
		return false, nil
	}
	if progress != nil {
		progress.SetFrame(0)
		progress.SetStage("Скачивание")
	}

	dlCtx, cancelDl := context.WithTimeout(withMediaDownloadProgress(ctx, progress), 10*time.Minute)
	post, supported, err := dl.BlueskyPost(dlCtx, rawURL)
	cancelDl()
	if !supported {
		return false, nil
	}
	if err != nil {
		return true, err
	}
	if strings.TrimSpace(post.Dir) != "" {
		defer func() {
			if rmErr := os.RemoveAll(post.Dir); rmErr != nil && debugTriggerLogEnabled {
				log.Printf("media bluesky temp cleanup failed dir=%q err=%v", post.Dir, rmErr)
			}
		}()
	}
	if len(post.Paths) == 0 {
		return true, errBlueskyNothing
	}

	title := strings.TrimSpace(post.Title)
	if title == "" {
		title = strings.TrimSpace(rawURL)
	}
	sourceURL := strings.TrimSpace(post.SourceURL)
	if sourceURL == "" {
		sourceURL = strings.TrimSpace(rawURL)
	}
	log.Printf("media bluesky post chat=%d kind=%s files=%d title=%q author=%q",
		sendCtx.ChatID, post.Kind, len(post.Paths), clipText(title, 120), clipText(post.Author, 60))

	switch post.Kind {
	case "video":
		if progress != nil {
			progress.SetFrame(6)
			progress.SetStage("Отправка видео")
		}
		return true, sendVideoFromFile(sendCtx, post.Paths[0], buildMediaVideoCaption(post.Paths[0], title, sourceURL, mediadl.ServiceBluesky))
	default:
		if progress != nil {
			progress.SetFrame(7)
			progress.SetStage("Отправка фото")
		}
		if len(post.Paths) == 1 {
			return true, sendPhotoFromFile(sendCtx, post.Paths[0], buildMediaPhotoCaption(post.Paths[0], title, sourceURL, mediadl.ServiceBluesky))
		}
		return true, sendPhotoAlbumFromFiles(sendCtx, post.Paths, buildMediaAlbumCaption(len(post.Paths), title, sourceURL, mediadl.ServiceBluesky))
	}
}
