package app

import (
	"bytes"
	"compress/zlib"
	"encoding/json"
	"regexp"
	"strings"
	"testing"
)

func TestBuildProgressWidgetFitsBudget(t *testing.T) {
	html, ok := buildProgressWidget()
	if !ok {
		t.Fatalf("виджет не собрался: кадры или картинки недоступны")
	}
	if len(html) > 32768 {
		t.Fatalf("HTML %d байт, лимит 32768", len(html))
	}
	var buf bytes.Buffer
	zw, err := zlib.NewWriterLevel(&buf, 9)
	if err != nil {
		t.Fatalf("zlib: %v", err)
	}
	if _, err := zw.Write([]byte(html)); err != nil {
		t.Fatalf("zlib write: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zlib close: %v", err)
	}
	if buf.Len() > widgetSizeLimit {
		t.Fatalf("сжатый HTML %d байт, лимит %d", buf.Len(), widgetSizeLimit)
	}
	if !strings.Contains(html, "chatDataChanged") {
		t.Fatalf("виджет не подписан на изменение состояния")
	}
}

func TestBuildProgressWidgetFramesMatchMediaFrames(t *testing.T) {
	html, ok := buildProgressWidget()
	if !ok {
		t.Fatalf("виджет не собрался")
	}
	// вытаскиваем массивы из HTML и сверяем число кадров с текстовым прогрессом
	framesRe := regexp.MustCompile(`FRAMES=(\[\[.*?\]\])`)
	match := framesRe.FindStringSubmatch(html)
	if match == nil {
		t.Fatalf("в HTML нет массива FRAMES")
	}
	var frames [][]int
	if err := json.Unmarshal([]byte(match[1]), &frames); err != nil {
		t.Fatalf("FRAMES не JSON: %v", err)
	}
	if want := len(mediaProgressFrames()); len(frames) != want {
		t.Fatalf("кадров в виджете %d, в текстовом прогрессе %d", len(frames), want)
	}
	for i, frame := range frames {
		if len(frame) != 6 {
			t.Fatalf("кадр %d состоит из %d частей вместо 6", i, len(frame))
		}
		for _, idx := range frame {
			if idx < 0 || idx > 5 {
				t.Fatalf("кадр %d ссылается на картинку %d", i, idx)
			}
		}
	}
}

func TestProgressWidgetStateJSON(t *testing.T) {
	payload, err := json.Marshal(widgetProgressState{Frame: 4, Stage: "Распознаю речь"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(payload) != `{"f":4,"s":"Распознаю речь"}` {
		t.Fatalf("неожиданный вид состояния: %s", payload)
	}
	if len(payload) > 1024 {
		t.Fatalf("состояние %d байт, лимит chat_data 1024", len(payload))
	}
}

func TestProgressWidgetEndpoint(t *testing.T) {
	t.Setenv("MEDIA_PROGRESS_WIDGET_API", "https://api.telegram.org/beta")
	if got := progressWidgetAPI(); got != "https://api.telegram.org/beta/bot%s/%s" {
		t.Fatalf("эндпоинт не нормализован: %q", got)
	}
	t.Setenv("MEDIA_PROGRESS_WIDGET_API", "https://api.telegram.org/beta/bot%s/%s")
	if got := progressWidgetAPI(); got != "https://api.telegram.org/beta/bot%s/%s" {
		t.Fatalf("готовый шаблон испорчен: %q", got)
	}
}

func TestStartWidgetProgressSkipsBlockedChat(t *testing.T) {
	// чат, где сервер отказал, не должен даже пытаться отправить виджет
	const chatID int64 = -1009999999999
	widgetBlockedChats.Store(chatID, true)
	defer widgetBlockedChats.Delete(chatID)

	t.Setenv("MEDIA_PROGRESS_WIDGET", "1")
	handle, stop := startWidgetProgress(mediaDownloadTask{SendCtx: sendContext{ChatID: chatID}})
	if handle != nil {
		t.Fatalf("для заблокированного чата виджет не должен создаваться")
	}
	stop() // не должно паниковать
}
