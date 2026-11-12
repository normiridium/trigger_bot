package app

// Контекст из YouTube: разбор вывода yt-dlp, VTT, выбор языка, таймауты.

import (
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestIsYouTubeContextURL(t *testing.T) {
	yes := []string{
		"https://www.youtube.com/watch?v=jNQXAC9IVRw",
		"https://youtube.com/watch?v=jNQXAC9IVRw",
		"https://m.youtube.com/watch?v=jNQXAC9IVRw",
		"https://music.youtube.com/watch?v=jNQXAC9IVRw",
		"https://youtu.be/jNQXAC9IVRw",
		"https://youtu.be/wqwbttNANao?si=duDtIc3nPK2dZq7Q",
		"https://www.youtube-nocookie.com/embed/jNQXAC9IVRw",
	}
	for _, raw := range yes {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatalf("разбор %q: %v", raw, err)
		}
		if !isYouTubeContextURL(u) {
			t.Fatalf("ссылка не распознана как YouTube: %s", raw)
		}
	}
	for _, raw := range []string{"https://example.com/watch?v=x", "https://vk.com/video-1_2", "https://notyoutube.com/watch?v=1"} {
		u, _ := url.Parse(raw)
		if isYouTubeContextURL(u) {
			t.Fatalf("чужая ссылка принята за YouTube: %s", raw)
		}
	}
}

func TestParseYouTubePrintOutput(t *testing.T) {
	title, description := parseYouTubePrintOutput("Me at the zoo\x1fThe first video on YouTube.\n")
	if title != "Me at the zoo" {
		t.Fatalf("заголовок %q", title)
	}
	if description != "The first video on YouTube." {
		t.Fatalf("описание %q", description)
	}
}

func TestParseYouTubePrintOutputWithoutDescription(t *testing.T) {
	title, description := parseYouTubePrintOutput("Just a title\x1f\n")
	if title != "Just a title" || description != "" {
		t.Fatalf("получили %q / %q", title, description)
	}
	if title, description := parseYouTubePrintOutput(""); title != "" || description != "" {
		t.Fatalf("пустой вывод дал %q / %q", title, description)
	}
}

func TestVTTToPlainTextRealSample(t *testing.T) {
	raw := `WEBVTT
Kind: captions
Language: en

00:00:01.200 --> 00:00:03.360
All right, so here we are, in front of the
elephants

00:00:05.318 --> 00:00:07.974
the cool thing about these guys is that they
have really long trunks
`
	got := vttToPlainText(raw)
	want := "All right, so here we are, in front of the elephants the cool thing about these guys is that they have really long trunks"
	if got != want {
		t.Fatalf("получили %q, ждали %q", got, want)
	}
}

func TestVTTToPlainTextDropsAutoCaptionRepeats(t *testing.T) {
	raw := `WEBVTT

1
00:00:00.000 --> 00:00:02.000
привет мир
привет мир

2
00:00:02.000 --> 00:00:04.000
привет мир
как дела <c>друг</c>
`
	got := vttToPlainText(raw)
	if strings.Count(got, "привет мир") != 1 {
		t.Fatalf("повтор не убран: %q", got)
	}
	if !strings.Contains(got, "как дела друг") {
		t.Fatalf("теги не вычищены: %q", got)
	}
	if strings.Contains(got, "-->") || strings.Contains(got, "WEBVTT") {
		t.Fatalf("служебные строки остались: %q", got)
	}
}

func TestVTTToPlainTextUnescapesEntities(t *testing.T) {
	raw := "WEBVTT\n\n00:00:00.000 --> 00:00:01.000\ntea &amp; coffee &#39;ok&#39; &lt;3\n"
	if got := vttToPlainText(raw); got != "tea & coffee 'ok' <3" {
		t.Fatalf("сущности не раскрыты: %q", got)
	}
}

func TestVTTToPlainTextEmptyInput(t *testing.T) {
	if got := vttToPlainText("WEBVTT\n\n"); got != "" {
		t.Fatalf("ожидали пустоту, получили %q", got)
	}
}

func TestLinkContextTimeoutForYouTube(t *testing.T) {
	plain := linkContextTimeout([]string{"https://example.com/page"})
	if plain != 12*time.Second {
		t.Fatalf("для обычной страницы ждали 12с, получили %s", plain)
	}
	yt := linkContextTimeout([]string{"https://example.com/page", "https://youtu.be/jNQXAC9IVRw"})
	if yt < 60*time.Second {
		t.Fatalf("для YouTube ждали не меньше минуты, получили %s", yt)
	}
}

func TestYouTubeSubtitleLanguageRanking(t *testing.T) {
	// ru предпочтительнее ru-orig, а оба — предпочтительнее английских.
	order := youTubeSubLangOrder
	if len(order) != 4 || order[0] != "ru" || order[1] != "ru-orig" {
		t.Fatalf("порядок языков изменился: %v", order)
	}
	if len(youTubeSubAttempts) < 2 {
		t.Fatalf("нет повторов на 429: %v", youTubeSubAttempts)
	}
	last := youTubeSubAttempts[len(youTubeSubAttempts)-1]
	if last.Langs != "all" {
		t.Fatalf("последняя попытка должна брать любые языки, получили %q", last.Langs)
	}
}
