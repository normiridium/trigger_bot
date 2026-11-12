package app

// Живая проверка прогресс-виджета: отправляет бар в чат владельца и проходит
// все кадры, как это делает медиа-задача. По умолчанию пропускается.
//
//	LIVE_WIDGET=1 go test -run TestProgressWidgetLive -v ./internal/app

import (
	"bufio"
	"os"
	"strings"
	"testing"
	"time"
)

func loadDotEnvForTest(t *testing.T, path string) {
	t.Helper()
	fh, err := os.Open(path)
	if err != nil {
		t.Skipf("нет %s: %v", path, err)
	}
	defer fh.Close()
	scanner := bufio.NewScanner(fh)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") || !strings.Contains(line, "=") {
			continue
		}
		key, value, _ := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		if os.Getenv(key) == "" {
			_ = os.Setenv(key, value)
		}
	}
}

func TestProgressWidgetLive(t *testing.T) {
	if strings.TrimSpace(os.Getenv("LIVE_WIDGET")) != "1" {
		t.Skip("LIVE_WIDGET!=1")
	}
	loadDotEnvForTest(t, "/home/faline/trigger_admin_bot/.env")
	_ = os.Setenv("MEDIA_PROGRESS_WIDGET", "1")

	owner := strings.TrimSpace(os.Getenv("OWNER_ID"))
	if owner == "" {
		t.Skip("нет OWNER_ID")
	}
	chatID := int64(0)
	for _, ch := range owner {
		if ch < '0' || ch > '9' {
			t.Fatalf("OWNER_ID не число: %q", owner)
		}
		chatID = chatID*10 + int64(ch-'0')
	}

	task := mediaDownloadTask{SendCtx: sendContext{ChatID: chatID}}
	progress, stop := startMediaDownloadProgress(task)
	if progress == nil || progress.widget == nil {
		t.Fatalf("виджет не поднялся")
	}
	t.Logf("виджет отправлен в %d", chatID)

	stages := []string{"Подготовка", "Скачивание видео", "Скачивание видео", "Распознаю речь",
		"Распознаю речь", "Перевожу", "Перевожу", "Озвучиваю", "Отправляю"}
	for i, stage := range stages {
		progress.SetFrame(i)
		progress.SetStage(stage)
		time.Sleep(1100 * time.Millisecond)
	}
	t.Logf("кадры прокручены; сообщение оставлено для просмотра")
	_ = stop
}
