package app

// Живая проверка быстрой сборки кубика на реальном файле.
//
//	LIVE_COUB=/tmp/coubbench/src.mp4 go test -run TestCoubFastLive -v ./internal/app

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCoubFastLive(t *testing.T) {
	path := os.Getenv("LIVE_COUB")
	if path == "" {
		t.Skip("LIVE_COUB не задан")
	}
	if _, err := os.Stat(path); err != nil {
		t.Skipf("нет файла %s: %v", path, err)
	}

	audioDur, err := probeAudioDurationSec(path)
	if err != nil || audioDur <= 0 {
		audioDur, err = probeMediaDurationSec(path)
		if err != nil {
			t.Fatalf("длительность: %v", err)
		}
	}
	videoDur, _ := probeVideoDurationSec(path)
	fps, err := probeVideoFPS(path)
	if err != nil || fps <= 0 {
		fps = 25
	}

	t.Logf("источник: видео %.2fs, аудио %.2fs, fps %.2f", videoDur, audioDur, fps)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	started := time.Now()
	out, err := buildCoubLoopFast(ctx, path, videoDur, audioDur, videoDur, audioDur, fps, nil)
	if err != nil {
		t.Fatalf("сборка: %v", err)
	}
	defer os.Remove(out)

	st, err := os.Stat(out)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	gotDur, _ := probeVideoDurationSec(out)
	t.Logf("готово за %.1fs: %s, %.2f МБ, длительность %.2fs",
		time.Since(started).Seconds(), filepath.Base(out), float64(st.Size())/1_000_000.0, gotDur)

	limit := envInt("TELEGRAM_UPLOAD_MAX_MB", 50)
	if st.Size() > int64(limit)*1_000_000 {
		t.Fatalf("файл %.2f МБ больше лимита %d МБ", float64(st.Size())/1_000_000.0, limit)
	}
	if gotDur < audioDur-2 {
		t.Fatalf("длительность %.2fs короче аудио %.2fs", gotDur, audioDur)
	}
}
