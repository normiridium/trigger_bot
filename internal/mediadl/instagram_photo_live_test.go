package mediadl

// Живая проверка альбома Instagram: одиночное фото и карусель.
//
//	IG_PHOTO_URL=https://www.instagram.com/p/DXl4Pl1EmdS/ \
//	YTDLP_BIN=/opt/ytdlp-venv/bin/yt-dlp IG_COOKIES_FILE=… \
//	  go test -run TestInstagramPhotoAlbumLive -v ./internal/mediadl

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

func TestInstagramPhotoAlbumLive(t *testing.T) {
	rawURL := strings.TrimSpace(os.Getenv("IG_PHOTO_URL"))
	if rawURL == "" {
		t.Skip("IG_PHOTO_URL не задан")
	}
	dl := Downloader{
		YTDLPBin:      strings.TrimSpace(os.Getenv("YTDLP_BIN")),
		CookiesFile:   strings.TrimSpace(os.Getenv("IG_COOKIES_FILE")),
		ExtractorArgs: strings.TrimSpace(os.Getenv("YTDLP_EXTRACTOR_ARGS")),
		Impersonate:   strings.TrimSpace(os.Getenv("YTDLP_IMPERSONATE")),
		MaxSizeMB:     50,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	album, supported, err := dl.InstagramPhotoAlbum(ctx, rawURL)
	if !supported {
		t.Fatalf("%s не распознан как фото-пост", rawURL)
	}
	if err != nil {
		t.Fatalf("альбом: %v", err)
	}
	if album.Dir != "" {
		defer os.RemoveAll(album.Dir)
	}
	if len(album.Paths) == 0 {
		t.Fatal("фото не скачались")
	}
	for _, path := range album.Paths {
		st, statErr := os.Stat(path)
		if statErr != nil {
			t.Fatalf("файл %q: %v", path, statErr)
		}
		t.Logf("фото %.2f МБ: %s", float64(st.Size())/1_000_000.0, path)
	}
	t.Logf("итого фото=%d заголовок=%q", len(album.Paths), album.Title)
}
