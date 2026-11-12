package mediadl

// Instagram-карусель: пост из нескольких элементов, включая видео.
//
// yt-dlp отдаёт такой пост как плейлист: у видео-элементов есть форматы, у
// фото — только миниатюры. Поэтому делаем два прохода: видео качаем как видео,
// фото — как максимальную миниатюру, а порядок восстанавливаем по номеру
// элемента плейлиста.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"trigger-admin-bot/internal/bottmp"
)

// CarouselItem — один элемент карусели.
type CarouselItem struct {
	Kind  string // "photo" или "video"
	Path  string
	Index int
}

// Carousel — готовая карусель.
type Carousel struct {
	Items     []CarouselItem
	Dir       string
	Title     string
	Caption   string
	SourceURL string
	Service   Service
	Skipped   int // элементы, которые не влезли в лимит
}

// InstagramCarousel скачивает все элементы карусели в порядке поста.
// supported=false — это не мультиэлементный пост Instagram.
func (d Downloader) InstagramCarousel(ctx context.Context, rawURL string) (Carousel, bool, error) {
	normURL, service, ok := NormalizeSupportedURL(rawURL)
	if !ok || service != ServiceInstagram {
		return Carousel{}, false, nil
	}
	probe, err := d.runJSON(ctx, d.buildPhotoAlbumProbeArgs(normURL))
	if err != nil {
		return Carousel{}, true, fmt.Errorf("instagram carousel probe: %w", err)
	}
	if len(probe.Entries) < 2 {
		return Carousel{}, false, nil
	}

	dir, err := bottmp.MkdirTemp("media-ig-carousel-*")
	if err != nil {
		return Carousel{}, true, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }

	// 1. Видео-элементы: обычная загрузка видео, ошибки по отдельным
	//    элементам не валят всю карусель.
	if err := d.downloadInstagramCarouselVideos(ctx, normURL, dir); err != nil && debugCarouselLog() {
		fmt.Printf("instagram carousel videos: %v\n", err)
	}
	// 2. Фото-элементы: максимальная миниатюра для каждого элемента.
	//    Для видео-элементов миниатюры тоже появятся, но видео имеет приоритет.
	if err := d.downloadInstagramCarouselThumbs(ctx, normURL, dir); err != nil && debugCarouselLog() {
		fmt.Printf("instagram carousel thumbs: %v\n", err)
	}

	byIndex, err := groupCarouselFiles(dir)
	if err != nil {
		cleanup()
		return Carousel{}, true, err
	}

	result := Carousel{
		Dir:       dir,
		Title:     displayTitleForService(ServiceInstagram, MediaKindPhoto, probe.Title, probe.Description),
		Caption:   strings.TrimSpace(probe.Description),
		SourceURL: normURL,
		Service:   ServiceInstagram,
	}
	limit := int64(d.maxSizeMB()) << 20
	for index := 1; index <= len(probe.Entries); index++ {
		files := byIndex[index]
		if len(files) == 0 {
			continue
		}
		item := pickCarouselItem(files)
		if item.Path == "" {
			continue
		}
		if limit > 0 {
			if st, statErr := os.Stat(item.Path); statErr == nil && st.Size() > limit {
				result.Skipped++
				_ = os.Remove(item.Path)
				continue
			}
		}
		item.Index = index
		result.Items = append(result.Items, item)
	}
	if len(result.Items) < 2 {
		cleanup()
		return Carousel{}, false, nil
	}
	return result, true, nil
}

func (d Downloader) downloadInstagramCarouselVideos(ctx context.Context, url, dir string) error {
	outTpl := filepath.Join(dir, "%(playlist_index)03d.%(id)s.%(ext)s")
	args := []string{
		"--ignore-errors",
		"--no-warnings",
		"--no-progress",
		"--extractor-args", d.extractorArgs(),
		"-f", "bv*+ba/b",
		"-o", outTpl,
	}
	args = append(d.ytDLPAuthArgs(ServiceInstagram), args...)
	args = append(args, url)
	vctx, cancel := context.WithTimeout(ctx, 8*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(vctx, d.binary(), args...)
	cmd.Env = append(os.Environ(), "PYTHONUNBUFFERED=1")
	output, runErr := cmd.CombinedOutput()
	// Часть элементов намеренно без видео — это не ошибка карусели.
	if runErr != nil && ctx.Err() != nil {
		return fmt.Errorf("instagram carousel videos: %w", runErr)
	}
	_ = output
	return nil
}

func (d Downloader) downloadInstagramCarouselThumbs(ctx context.Context, url, dir string) error {
	outTpl := filepath.Join(dir, "%(playlist_index)03d.%(id)s.%(ext)s")
	args := []string{
		"--ignore-no-formats-error",
		"--skip-download",
		"--write-thumbnail",
		"--ignore-errors",
		"--no-warnings",
		"--no-progress",
		"--extractor-args", d.extractorArgs(),
		"-o", outTpl,
	}
	args = append(d.ytDLPAuthArgs(ServiceInstagram), args...)
	args = append(args, url)
	tctx, cancel := context.WithTimeout(ctx, 4*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(tctx, d.binary(), args...)
	cmd.Env = append(os.Environ(), "PYTHONUNBUFFERED=1")
	var errBuf strings.Builder
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil && ctx.Err() != nil {
		return fmt.Errorf("instagram carousel thumbs: %w: %s", err, lastNonEmptyLine(errBuf.String()))
	}
	return nil
}

// groupCarouselFiles раскладывает файлы по номеру элемента плейлиста.
func groupCarouselFiles(dir string) (map[int][]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	out := make(map[int][]string)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		prefix, _, ok := strings.Cut(name, ".")
		if !ok {
			continue
		}
		index, convErr := strconv.Atoi(prefix)
		if convErr != nil || index <= 0 {
			continue
		}
		out[index] = append(out[index], filepath.Join(dir, name))
	}
	for index := range out {
		sort.Strings(out[index])
	}
	return out, nil
}

// pickCarouselItem выбирает видео, если оно скачалось, иначе фото.
func pickCarouselItem(files []string) CarouselItem {
	photo := ""
	for _, path := range files {
		switch strings.ToLower(filepath.Ext(path)) {
		case ".mp4", ".mkv", ".webm", ".mov":
			if st, err := os.Stat(path); err == nil && st.Size() > 0 {
				return CarouselItem{Kind: "video", Path: path}
			}
		case ".jpg", ".jpeg", ".png", ".webp":
			if photo == "" {
				photo = path
			}
		}
	}
	if photo == "" {
		return CarouselItem{}
	}
	return CarouselItem{Kind: "photo", Path: photo}
}

func debugCarouselLog() bool {
	return strings.TrimSpace(os.Getenv("DEBUG_TRIGGER_LOG")) == "true"
}

var errCarouselEmpty = errors.New("instagram carousel: нет элементов")
