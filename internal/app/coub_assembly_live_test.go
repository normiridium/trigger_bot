package app

// Живой прогон сборки кубика через настоящий код бота: показывает, что именно
// возвращает buildCoubLoopedVideo (ошибка видна напрямую, а не проглатывается).
//
//	LIVE_COUB=/path/to/src.mp4 COUB_FAST_LOOP=0 go test -run TestCoubAssemblyLive -v ./internal/app

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestCoubAssemblyLive(t *testing.T) {
	path := os.Getenv("LIVE_COUB")
	if path == "" {
		t.Skip("LIVE_COUB не задан")
	}
	if _, err := os.Stat(path); err != nil {
		t.Skipf("нет файла: %v", err)
	}

	audioDur, err := probeAudioDurationSec(path)
	if err != nil || audioDur <= 0 {
		audioDur, err = probeMediaDurationSec(path)
		if err != nil {
			t.Fatalf("длительность: %v", err)
		}
	}
	videoDur, _ := probeVideoDurationSec(path)
	t.Logf("источник: видео %.2fs, аудио %.2fs, fast=%v", videoDur, audioDur, envBool("COUB_FAST_LOOP", true))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	started := time.Now()
	out, err := buildCoubLoopedVideo(ctx, path, videoDur, 0, nil)
	if err != nil {
		t.Fatalf("сборка вернула ошибку: %v", err)
	}
	defer os.Remove(out)

	gotVideo, _ := probeVideoDurationSec(out)
	gotAudio, _ := probeAudioDurationSec(out)
	st, _ := os.Stat(out)
	sizeMB := 0.0
	if st != nil {
		sizeMB = float64(st.Size()) / 1_000_000.0
	}
	t.Logf("готово за %.1fs: видео %.2fs, аудио %.2fs, %.2f МБ", time.Since(started).Seconds(), gotVideo, gotAudio, sizeMB)

	if gotVideo < audioDur*0.9 {
		t.Fatalf("видеодорожка %.2fs короче аудио %.2fs — именно это даёт стоп-кадр в плеере", gotVideo, audioDur)
	}
}
