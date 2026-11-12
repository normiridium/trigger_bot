package app

// Сборка кубика быстрым путём.
//
// Кубик — короткое видео, которое повторяется под длинную звуковую дорожку
// (в живом примере: 2.12 с видео на 260 с аудио). Старая схема кодировала
// видео дважды и не учитывала лимит загрузки, после чего запускался ещё и
// транскод под лимит Telegram.
//
// Здесь: луп кодируется ОДИН раз сразу по битрейту, посчитанному из лимита
// загрузки и длительности, а копии склеиваются потоком (-c copy). Замер на
// 2 ядрах: 6 с вместо 116 с, файл 48.5 МБ при лимите 50 МБ.

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// coubTargetVideoKbps — сколько можно отдать видео, чтобы файл влез в лимит.
func coubTargetVideoKbps(targetDurationSec float64, limitMB, audioKbps int) int {
	if targetDurationSec <= 0 || limitMB <= 0 {
		return 0
	}
	// бит/сек на весь файл, с запасом 5% на контейнер и неточности VBR
	budgetBitsPerSec := float64(limitMB) * 1_000_000 * 8 * 0.95 / targetDurationSec
	video := int(budgetBitsPerSec/1000) - audioKbps
	if video < 300 {
		video = 300
	}
	return video
}

// coubLoopGOP — длина GOP в кадрах: каждый повтор лупа начинается с ключевого
// кадра, иначе склейка без перекодирования дёргается при перемотке.
func coubLoopGOP(fps float64) int {
	gop := int(math.Round(fps))
	if gop < 25 {
		gop = 25
	}
	return gop
}

// buildCoubLoopFast собирает кубик без повторного кодирования каждого лупа.
func buildCoubLoopFast(
	ctx context.Context,
	inputPath string,
	videoDuration, audioDuration, loopDuration, targetDuration, fps float64,
	progress *mediaProgressHandle,
) (string, error) {
	if strings.TrimSpace(inputPath) == "" || targetDuration <= 0 || loopDuration <= 0 {
		return "", errors.New("coub fast: некорректные длительности")
	}

	limitMB := envInt("COUB_TARGET_MB", 0)
	if limitMB <= 0 {
		limitMB = envInt("TELEGRAM_UPLOAD_MAX_MB", 50)
	}
	audioKbps := envInt("COUB_AUDIO_KBPS", 128)
	preset := envOr("COUB_FFMPEG_PRESET", "veryfast")
	videoKbps := coubTargetVideoKbps(targetDuration, limitMB, audioKbps)
	if videoKbps <= 0 {
		return "", errors.New("coub fast: не удалось посчитать битрейт")
	}
	gop := coubLoopGOP(fps)

	dir := filepath.Dir(inputPath)
	loopTmp, err := os.CreateTemp(dir, "coub-fast-loop-*.mp4")
	if err != nil {
		return "", fmt.Errorf("coub fast: temp loop: %w", err)
	}
	loopPath := loopTmp.Name()
	_ = loopTmp.Close()
	defer func() { _ = os.Remove(loopPath) }()

	started := time.Now()

	// 1) Один проход по лупу: обрезаем (если луп короче исходника) и кодируем
	// сразу под целевой битрейт, с ключевым кадром на каждом повторе.
	loopArgs := []string{
		"-nostdin", "-y",
		"-progress", "pipe:1",
		"-nostats",
		"-i", inputPath,
		"-t", fmt.Sprintf("%.6f", loopDuration),
		"-an",
		"-c:v", "libx264",
		"-preset", preset,
		"-b:v", fmt.Sprintf("%dk", videoKbps),
		"-maxrate", fmt.Sprintf("%dk", videoKbps*3/2),
		"-bufsize", fmt.Sprintf("%dk", videoKbps*2),
		"-g", fmt.Sprintf("%d", gop),
		"-keyint_min", fmt.Sprintf("%d", gop),
		"-sc_threshold", "0",
		"-pix_fmt", "yuv420p",
		loopPath,
	}
	if err := runFFmpegWithProgress(ctx, loopArgs, loopDuration, progress, 1, 4, "coub fast loop encode"); err != nil {
		return "", err
	}
	loopDur := loopDuration
	if videoDuration > 0 && videoDuration < loopDur {
		loopDur = videoDuration
	}

	// 2) Список повторов и склейка потоком — без перекодирования.
	copies := int(math.Ceil(targetDuration/loopDur)) + 1
	if copies < 1 {
		copies = 1
	}
	listTmp, err := os.CreateTemp(dir, "coub-fast-list-*.txt")
	if err != nil {
		return "", fmt.Errorf("coub fast: temp list: %w", err)
	}
	listPath := listTmp.Name()
	defer func() { _ = os.Remove(listPath) }()
	var listBuilder strings.Builder
	for i := 0; i < copies; i++ {
		listBuilder.WriteString("file '" + loopPath + "'\n")
	}
	if _, err := listTmp.WriteString(listBuilder.String()); err != nil {
		_ = listTmp.Close()
		return "", fmt.Errorf("coub fast: list write: %w", err)
	}
	if err := listTmp.Close(); err != nil {
		return "", fmt.Errorf("coub fast: list close: %w", err)
	}

	outTmp, err := os.CreateTemp(dir, "coub-fast-out-*.mp4")
	if err != nil {
		return "", fmt.Errorf("coub fast: temp out: %w", err)
	}
	outPath := outTmp.Name()
	_ = outTmp.Close()

	concatArgs := []string{
		"-nostdin", "-y",
		"-progress", "pipe:1",
		"-nostats",
		"-f", "concat", "-safe", "0",
		"-i", listPath,
		"-i", inputPath,
		"-map", "0:v:0",
		"-map", "1:a:0",
		"-t", fmt.Sprintf("%.6f", targetDuration),
		"-c:v", "copy",
		"-c:a", "aac",
		"-b:a", fmt.Sprintf("%dk", audioKbps),
		"-movflags", "+faststart",
		outPath,
	}
	if err := runFFmpegWithProgress(ctx, concatArgs, targetDuration, progress, 5, 7, "coub fast concat"); err != nil {
		_ = os.Remove(outPath)
		return "", err
	}

	if st, statErr := os.Stat(outPath); statErr == nil {
		log.Printf("coub fast: готов за %.2fs, %.0f s при %d kbps (+%d k аудио), лупов %d, размер %.2f МБ (лимит %d МБ)",
			time.Since(started).Seconds(), targetDuration, videoKbps, audioKbps, copies,
			float64(st.Size())/1_000_000.0, limitMB)
	}
	return outPath, nil
}
