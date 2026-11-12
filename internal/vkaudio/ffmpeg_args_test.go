package vkaudio

// Наборы аргументов ffmpeg: HLS получает свои опции, обычный mp3 — свои.
// Смешивать нельзя: ffmpeg падает с «Option ... not found».

import (
	"strings"
	"testing"
)

func candidatesFor(url string) [][]string {
	d := Downloader{UserAgent: "test-agent"}
	return d.ffmpegArgCandidates(url, "/tmp/out.mp3", "https://vk.com")
}

func TestFFmpegArgsPlainMP3(t *testing.T) {
	cands := candidatesFor("https://cdn.example/audio.mp3?extra=1")
	if len(cands) < 2 {
		t.Fatalf("ожидали несколько вариантов, получили %d", len(cands))
	}
	first := strings.Join(cands[0], " ")
	for _, unwanted := range []string{"-allowed_extensions", "-protocol_whitelist", "-http_persistent"} {
		if strings.Contains(first, unwanted) {
			t.Fatalf("для mp3 попала опция HLS/лишняя %q: %s", unwanted, first)
		}
	}
	if !strings.Contains(first, "-reconnect 1") {
		t.Fatalf("для http ожидали reconnect: %s", first)
	}
	if !strings.Contains(first, "-i https://cdn.example/audio.mp3?extra=1") {
		t.Fatalf("неверный ввод: %s", first)
	}
	last := strings.Join(cands[len(cands)-1], " ")
	for _, unwanted := range []string{"-reconnect", "-rw_timeout", "-allowed_extensions", "-protocol_whitelist"} {
		if strings.Contains(last, unwanted) {
			t.Fatalf("в минимальном наборе остался %q: %s", unwanted, last)
		}
	}
}

func TestFFmpegArgsHLS(t *testing.T) {
	cands := candidatesFor("https://cdn.example/index.m3u8?token=1")
	first := strings.Join(cands[0], " ")
	for _, want := range []string{"-allowed_extensions ALL", "-protocol_whitelist file,http,https,tcp,tls,crypto"} {
		if !strings.Contains(first, want) {
			t.Fatalf("для HLS нет %q: %s", want, first)
		}
	}
	if strings.Contains(first, "-http_persistent") {
		t.Fatalf("http_persistent не должен попадать в HLS-набор: %s", first)
	}
}

func TestFFmpegArgsNoHTTPOptionsForLocalFile(t *testing.T) {
	cands := candidatesFor("/tmp/cached-audio.mp3")
	first := strings.Join(cands[0], " ")
	for _, unwanted := range []string{"-reconnect", "-rw_timeout", "-allowed_extensions", "-protocol_whitelist"} {
		if strings.Contains(first, unwanted) {
			t.Fatalf("для локального файла попала опция %q: %s", unwanted, first)
		}
	}
}

func TestIsFFmpegUnknownOption(t *testing.T) {
	if !isFFmpegUnknownOption(errText("ffmpeg failed: Option allowed_extensions not found.")) {
		t.Fatal("не распознал неизвестную опцию")
	}
	if isFFmpegUnknownOption(errText("ffmpeg failed: Server returned 403 Forbidden")) {
		t.Fatal("сетевая ошибка принята за неизвестную опцию")
	}
}

type errText string

func (e errText) Error() string { return string(e) }
