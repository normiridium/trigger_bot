package mediadl

import (
	"errors"
	"strings"
	"testing"
)

func TestDownloaderDefaults(t *testing.T) {
	d := Downloader{}
	if got := d.audioFormat(); got != "mp3" {
		t.Fatalf("unexpected format: %q", got)
	}
	if got := d.audioQuality(); got != "320K" {
		t.Fatalf("unexpected quality: %q", got)
	}
	if got := d.maxSizeMB(); got != 100 {
		t.Fatalf("unexpected max size: %d", got)
	}
	if got := d.maxHeight(); got != 720 {
		t.Fatalf("unexpected max height: %d", got)
	}
	if got := d.extractorArgs(); got != "youtube:player_client=android,web" {
		t.Fatalf("unexpected extractor args: %q", got)
	}
	if got := d.audioFormatSelector(); !strings.Contains(got, "height<=720") {
		t.Fatalf("unexpected format selector: %q", got)
	}
	if got := d.videoFormatSelector(); !strings.Contains(got, "bestvideo[height<=720]+bestaudio") {
		t.Fatalf("unexpected video selector: %q", got)
	}
}

func TestDownloaderBuildDownloadArgs(t *testing.T) {
	d := Downloader{
		AudioFormat:        "m4a",
		AudioQuality:       "192K",
		ExtractorArgs:      "youtube:player_client=web",
		CookiesFromBrowser: "firefox",
		MaxSizeMB:          77,
		MaxHeight:          480,
		ProxySocks:         "127.0.0.1:1234",
	}
	args := d.buildDownloadArgs(ServiceVK, "https://vk.com/audio-2000703018_12703018", "/tmp/%(title)s.%(ext)s")
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--audio-format m4a") || !strings.Contains(joined, "--audio-quality 192K") {
		t.Fatalf("expected audio args, got: %s", joined)
	}
	if !strings.Contains(joined, "--extractor-args youtube:player_client=web") {
		t.Fatalf("expected extractor args, got: %s", joined)
	}
	if !strings.Contains(joined, "--max-filesize 77M") {
		t.Fatalf("expected max-filesize arg, got: %s", joined)
	}
	if !strings.Contains(joined, "--cookies-from-browser firefox") {
		t.Fatalf("expected cookies-from-browser arg, got: %s", joined)
	}
	if !strings.Contains(joined, "height<=480") {
		t.Fatalf("expected max height in format selector, got: %s", joined)
	}
}

func TestWithVKProxyArgs(t *testing.T) {
	d := Downloader{ProxySocks: "127.0.0.1:1234"}
	base := []string{"--quiet", "https://vk.com/audio-1_2"}
	vk := d.withVKProxyArgs(ServiceVK, append([]string{}, base...))
	if got := strings.Join(vk, " "); !strings.Contains(got, "--proxy socks5://127.0.0.1:1234") {
		t.Fatalf("expected vk proxy args, got: %s", got)
	}
	yt := d.withVKProxyArgs(ServiceYouTube, append([]string{}, base...))
	if got := strings.Join(yt, " "); strings.Contains(got, "--proxy") {
		t.Fatalf("did not expect proxy for youtube, got: %s", got)
	}
}

func TestAudioFormatSelectorsForRetry(t *testing.T) {
	d := Downloader{MaxHeight: 480}
	got := strings.Join(d.audioFormatSelectorsForRetry(), " | ")
	if !strings.Contains(got, "bestaudio/best[height<=480]/best") {
		t.Fatalf("expected primary selector with max height, got: %s", got)
	}
	if !strings.Contains(got, "18/best") {
		t.Fatalf("expected compat selector, got: %s", got)
	}
}

func TestDownloaderBuildVideoDownloadArgs(t *testing.T) {
	d := Downloader{MaxSizeMB: 55, MaxHeight: 720}
	args := d.buildVideoDownloadArgs(ServiceYouTube, "https://youtu.be/abc", "/tmp/%(title)s.%(ext)s")
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--merge-output-format mp4") {
		t.Fatalf("expected mp4 merge, got: %s", joined)
	}
	if !strings.Contains(joined, "bestvideo[height<=720]+bestaudio") {
		t.Fatalf("expected video selector, got: %s", joined)
	}
	if !strings.Contains(joined, "--max-filesize 55M") {
		t.Fatalf("expected max-filesize arg, got: %s", joined)
	}
}

func TestVideoFormatSelectorsForRetry(t *testing.T) {
	d := Downloader{MaxHeight: 720}
	got := strings.Join(d.videoFormatSelectorsForRetry(), " | ")
	if !strings.Contains(got, "bestvideo[height<=720]+bestaudio") {
		t.Fatalf("expected primary selector, got: %s", got)
	}
	if !strings.Contains(got, "best[height<=720]/best") {
		t.Fatalf("expected constrained best selector, got: %s", got)
	}
	if !strings.Contains(got, "bestvideo+bestaudio/best") || !strings.Contains(got, "best") {
		t.Fatalf("expected generic fallback selectors, got: %s", got)
	}
}

func TestDownloaderBuildProbeArgsWithoutFormat(t *testing.T) {
	d := Downloader{CookiesFile: "/tmp/cookies.txt"}
	args := d.buildProbeArgs(ServiceInstagram, "https://instagram.com/reel/abc", "")
	joined := strings.Join(args, " ")
	if strings.Contains(joined, " -f ") || strings.HasPrefix(joined, "-f ") {
		t.Fatalf("unexpected format flag in probe args: %s", joined)
	}
	if !strings.Contains(joined, "--cookies /tmp/cookies.txt") {
		t.Fatalf("expected cookies arg, got: %s", joined)
	}
}

func TestNormalizeSupportedURL(t *testing.T) {
	cases := []struct {
		in      string
		service Service
		ok      bool
	}{
		{in: "https://www.youtube.com/watch?v=abc", service: ServiceYouTube, ok: true},
		{in: "https://youtu.be/abc", service: ServiceYouTube, ok: true},
		{in: "https://vk.com/audio-2000703018_12703018", service: ServiceVK, ok: true},
		{in: "https://m.vk.com/video-1_456239017", service: ServiceVK, ok: true},
		{in: "https://www.instagram.com/reel/abc/", service: ServiceInstagram, ok: true},
		{in: "https://www.tiktok.com/@artist/video/123456789", service: ServiceTikTok, ok: true},
		{in: "https://vm.tiktok.com/ZM123abc/", service: ServiceTikTok, ok: true},
		{in: "https://soundcloud.com/artist/track", service: ServiceSoundCloud, ok: true},
		{in: "https://coub.com/view/2x6x9z", service: ServiceCoub, ok: true},
		{in: "https://x.com/artist/status/1234567890", service: ServiceX, ok: true},
		{in: "https://twitter.com/artist/status/1234567890", service: ServiceX, ok: true},
		{in: "https://example.org/video", service: ServiceUnknown, ok: false},
	}
	for _, tc := range cases {
		_, service, ok := NormalizeSupportedURL(tc.in)
		if ok != tc.ok || service != tc.service {
			t.Fatalf("NormalizeSupportedURL(%q) => ok=%v service=%q; want ok=%v service=%q", tc.in, ok, service, tc.ok, tc.service)
		}
	}
}

func TestPickSize(t *testing.T) {
	v1 := int64(11)
	v2 := int64(22)
	meta := probeJSON{FilesizeApprox: &v2}
	if got := pickSize(meta); got != 22 {
		t.Fatalf("unexpected size from root fields: %d", got)
	}
	meta = probeJSON{RequestedFormats: []formatRecord{{Filesize: &v1}, {FilesizeApprox: &v2}}}
	if got := pickSize(meta); got != 33 {
		t.Fatalf("unexpected size from requested formats: %d", got)
	}
}

func TestInferMediaKindByPath(t *testing.T) {
	if got := inferMediaKindByPath("/tmp/a.jpg"); got != MediaKindPhoto {
		t.Fatalf("jpg should be photo, got %q", got)
	}
	if got := inferMediaKindByPath("/tmp/a.mp4"); got != MediaKindVideo {
		t.Fatalf("mp4 should be video, got %q", got)
	}
	if got := inferMediaKindByPath("/tmp/a.mp3"); got != MediaKindAudio {
		t.Fatalf("mp3 should be audio, got %q", got)
	}
}

func TestIsYTDLPYouTubeRetryable(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{name: "format unavailable", err: errors.New("ERROR: requested format is not available"), want: true},
		{name: "not a bot", err: errors.New("Sign in to confirm you're not a bot"), want: true},
		{name: "n challenge", err: errors.New("n challenge solving failed"), want: true},
		{name: "storyboard only", err: errors.New("Only images are available for download"), want: true},
		{name: "fatal", err: errors.New("http 500"), want: false},
		{name: "nil", err: nil, want: false},
	}
	for _, tc := range cases {
		got := isYTDLPYouTubeRetryable(tc.err)
		if got != tc.want {
			t.Fatalf("%s: got %v want %v", tc.name, got, tc.want)
		}
	}
}
