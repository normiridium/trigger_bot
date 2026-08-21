package app

import (
	"bytes"
	"encoding/base64"
	"errors"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"trigger-admin-bot/internal/mediadl"
	"trigger-admin-bot/internal/trigger"
)

func TestApplyCapturingTemplate(t *testing.T) {
	got := applyCapturingTemplate("доброе фото {{capturing_text}}", "  навальный  ", "", false)
	if got != "доброе фото навальный" {
		t.Fatalf("unexpected template result: %q", got)
	}
	if applyCapturingTemplate("", "x", "", false) != "" {
		t.Fatalf("empty template must stay empty")
	}
	if applyCapturingTemplate("без плейсхолдера", "x", "", false) != "без плейсхолдера" {
		t.Fatalf("template without placeholder must stay unchanged")
	}
}

func TestMusicCommandSpecByCommand(t *testing.T) {
	cases := []struct {
		cmd     string
		wantOK  bool
		want    string
		usage   string
		service string
	}{
		{cmd: cmdSpotifySearch, wantOK: true, want: "spotify", usage: cmdSpotifySearch, service: "Spotify"},
		{cmd: cmdSpotifySearchAlt, wantOK: true, want: "spotify", usage: cmdSpotifySearch, service: "Spotify"},
		{cmd: cmdYandexMusicSearch, wantOK: true, want: "yandex", usage: cmdYandexMusicSearch, service: "Yandex Music"},
		{cmd: cmdYandexMusicFind, wantOK: true, want: "yandex", usage: cmdYandexMusicSearch, service: "Yandex Music"},
		{cmd: cmdVKMusicSearch, wantOK: true, want: "vk", usage: cmdVKMusicSearch, service: "VK"},
		{cmd: cmdVKMusicFind, wantOK: true, want: "vk", usage: cmdVKMusicSearch, service: "VK"},
		{cmd: cmdSoundCloudSearch, wantOK: true, want: "soundcloud", usage: cmdSoundCloudSearch, service: "SoundCloud"},
		{cmd: cmdSoundCloudFind, wantOK: true, want: "soundcloud", usage: cmdSoundCloudSearch, service: "SoundCloud"},
		{cmd: "unknown", wantOK: false},
	}
	for _, tc := range cases {
		got, ok := musicCommandSpecByCommand(tc.cmd)
		if ok != tc.wantOK {
			t.Fatalf("musicCommandSpecByCommand(%q) ok=%v want=%v", tc.cmd, ok, tc.wantOK)
		}
		if !ok {
			continue
		}
		if got.Provider != tc.want || got.UsageCommand != tc.usage || got.ServiceName != tc.service {
			t.Fatalf("musicCommandSpecByCommand(%q)=%#v", tc.cmd, got)
		}
	}
}

func TestApplyCapturingTemplateChoice(t *testing.T) {
	pattern := `^\\s*((?:уби|обня|(?:😘 )?поцелова))ть\\s*$`
	got := applyCapturingTemplate("{{capturing_choice}}", "поцелова", pattern, false)
	if got != "😘 поцелова" {
		t.Fatalf("unexpected choice: %q", got)
	}
	got = applyCapturingTemplate("{{capturing_option}}", "обня", pattern, false)
	if got != "обня" {
		t.Fatalf("unexpected option: %q", got)
	}
}

func TestApplyCapturingTemplateChoiceWithEmojiPrefix(t *testing.T) {
	pattern := `^\\s*((?:☠? ?ᛁ? ?уби|🤗? ?ᛁ? ?обня|😘? ?ᛁ? ?поцелова))ть\\s*$`
	got := applyCapturingTemplate("{{capturing_choice}}", "обня", pattern, false)
	if got != "🤗 ᛁ обня" {
		t.Fatalf("unexpected choice with emoji: %q", got)
	}
}

func TestApplyCapturingTemplateChoicePipeSplitIndex(t *testing.T) {
	pattern := `^\\s*((?:☠? ?ᛁ? ?уби|🤗? ?ᛁ? ?обня|😘? ?ᛁ? ?поцелова))ть\\s*$`
	got := applyCapturingTemplate("{{capturing_choice | split \"ᛁ\" | index 0}}", "обня", pattern, false)
	if got != "🤗" {
		t.Fatalf("unexpected pipe index0: %q", got)
	}
	got = applyCapturingTemplate("{{capturing_choice | split \"ᛁ\" | index 1}}", "обня", pattern, false)
	if got != "обня" {
		t.Fatalf("unexpected pipe index1: %q", got)
	}
}

func TestApplyCapturingTemplateChoice_PoliticsAvoidsSuffixNoise(t *testing.T) {
	pattern := `^[^\n]{0,140}((?:Алексей )?Навальный|(?: Илья)? Яшин(?:а|у|ым|е)?)`
	got := applyCapturingTemplate("{{capturing_choice}}", "Навальный", pattern, false)
	if got != "Алексей Навальный" && got != "Навальный" {
		t.Fatalf("unexpected politics choice: %q", got)
	}
	got = applyCapturingTemplate("{{capturing_choice}}", "Яшина", pattern, false)
	if strings.TrimSpace(got) == "а" {
		t.Fatalf("capturing_choice must not degrade to short suffix: %q", got)
	}
}

func TestBuildImageSearchQueryFromMessage(t *testing.T) {
	msg := &tgbotapi.Message{
		Chat: &tgbotapi.Chat{ID: -1001, Title: "Чат"},
		From: &tgbotapi.User{ID: 7, FirstName: "Аня", UserName: "anya"},
		Text: "покажи кота",
	}

	gotDefault := buildImageSearchQueryFromMessage(templateContext{
		Msg: msg,
	}, "")
	if gotDefault != "покажи кота" {
		t.Fatalf("default query mismatch: %q", gotDefault)
	}

	got := buildImageSearchQueryFromMessage(templateContext{
		Msg:           msg,
		CapturingText: "кац",
	}, "доброе фото {{capturing_text}} для {{user_first_name}}")
	if got != "доброе фото кац для Аня" {
		t.Fatalf("query mismatch: %q", got)
	}
}

func TestCollectSerpImageCandidateURLsIncludesThumbnails(t *testing.T) {
	got := collectSerpImageCandidateURLs([]serpImageResult{
		{
			Original:  "https://example.com/original.jpg",
			Thumbnail: "https://example.com/thumb.jpg",
			Link:      "https://example.com/page",
		},
		{
			Original:  "https://example.com/original.jpg",
			Thumbnail: " https://example.com/second-thumb.jpg ",
		},
	})
	want := []string{
		"https://example.com/original.jpg",
		"https://example.com/thumb.jpg",
		"https://example.com/page",
		"https://example.com/second-thumb.jpg",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("candidates mismatch:\nwant=%#v\n got=%#v", want, got)
	}
}

func TestImageSearchRetryQueries(t *testing.T) {
	msg := &tgbotapi.Message{
		Chat: &tgbotapi.Chat{ID: -1001, Title: "Чат"},
		From: &tgbotapi.User{ID: 7, FirstName: "Аня", UserName: "anya"},
		Text: "навальный",
	}
	got := imageSearchRetryQueries(templateContext{
		Msg:           msg,
		CapturingText: "Навальный",
	}, "Алексей Навальный портрет фото")
	want := []string{"Алексей Навальный портрет фото", "Навальный"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("retry queries mismatch:\nwant=%#v\n got=%#v", want, got)
	}
}

func TestIsImageSearchNoResults(t *testing.T) {
	if !isImageSearchNoResults(errors.New("Google Images hasn't returned any results for this query.")) {
		t.Fatalf("expected serpapi no-results error")
	}
	if isImageSearchNoResults(errors.New("serpapi status=401 body=bad key")) {
		t.Fatalf("auth/status errors must not be treated as no-results")
	}
}

func TestBuildSpotifyMusicQueryFromMessage(t *testing.T) {
	msg := &tgbotapi.Message{
		Chat: &tgbotapi.Chat{ID: -1001, Title: "Чат"},
		From: &tgbotapi.User{ID: 7, FirstName: "Аня", UserName: "anya"},
		Text: "найди песню",
	}

	gotDefault := buildSpotifyMusicQueryFromMessage(templateContext{
		Msg: msg,
	}, "")
	if gotDefault != "найди песню" {
		t.Fatalf("default query mismatch: %q", gotDefault)
	}

	got := buildSpotifyMusicQueryFromMessage(templateContext{
		Msg:           msg,
		CapturingText: "летов",
	}, "играй {{capturing_text}}")
	if got != "играй летов" {
		t.Fatalf("query mismatch: %q", got)
	}
}

func TestBuildMediaDownloadQueryFromMessage(t *testing.T) {
	msg := &tgbotapi.Message{
		Chat: &tgbotapi.Chat{ID: -1001, Title: "Чат"},
		From: &tgbotapi.User{ID: 7, FirstName: "Аня", UserName: "anya"},
		Text: "скачай https://youtu.be/abc",
	}
	gotDefault := buildMediaDownloadQueryFromMessage(templateContext{
		Msg: msg,
	}, "")
	if gotDefault != "скачай https://youtu.be/abc" {
		t.Fatalf("default query mismatch: %q", gotDefault)
	}
	got := buildMediaDownloadQueryFromMessage(templateContext{
		Msg:           msg,
		CapturingText: "https://soundcloud.com/a/b",
	}, "{{capturing_text}}")
	if got != "https://soundcloud.com/a/b" {
		t.Fatalf("template query mismatch: %q", got)
	}
}

func TestExtractSupportedMediaURL(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{in: "скачай https://www.youtube.com/watch?v=abc123", want: "https://www.youtube.com/watch?v=abc123"},
		{in: "музыка https://music.youtube.com/watch?v=abc123", want: "https://music.youtube.com/watch?v=abc123"},
		{in: "медиа https://media.youtube.com/watch?v=abc123", want: "https://media.youtube.com/watch?v=abc123"},
		{in: "линк https://instagram.com/reel/ABCDEF/?igsh=123", want: "https://instagram.com/reel/ABCDEF/?igsh=123"},
		{in: "пин https://www.pinterest.com/pin/1234567890/", want: "https://www.pinterest.com/pin/1234567890/"},
		{in: "короткий пин https://pin.it/abc123", want: "https://pin.it/abc123"},
		{in: "вот https://www.tiktok.com/@artist/video/123456789", want: "https://www.tiktok.com/@artist/video/123456789"},
		{in: "вк https://vk.com/audio-2000703018_12703018", want: "https://vk.com/audio-2000703018_12703018"},
		{in: "https://soundcloud.com/artist/track", want: "https://soundcloud.com/artist/track"},
		{in: "смотри https://x.com/artist/status/1234567890", want: "https://x.com/artist/status/1234567890"},
		{in: "https://example.org/video", want: ""},
	}
	for _, tc := range cases {
		if got := extractSupportedMediaURL(tc.in); got != tc.want {
			t.Fatalf("extractSupportedMediaURL(%q)=%q want=%q", tc.in, got, tc.want)
		}
	}
}

func TestExtractSupportedMediaURL_SecondMatch(t *testing.T) {
	in := "смотри https://example.org/one и вот https://youtu.be/abc"
	if got := extractSupportedMediaURL(in); got != "https://youtu.be/abc" {
		t.Fatalf("expected second supported url, got %q", got)
	}
}

func TestExtractSupportedMediaURLByService(t *testing.T) {
	in := "смотри https://youtu.be/abc и https://www.tiktok.com/@artist/video/123"
	if got := extractSupportedMediaURLByService(in, "tiktok"); got != "https://www.tiktok.com/@artist/video/123" {
		t.Fatalf("expected tiktok url, got %q", got)
	}
	if got := extractSupportedMediaURLByService(in, "instagram"); got != "" {
		t.Fatalf("expected empty for missing service, got %q", got)
	}
	if got := extractSupportedMediaURLByService("x https://twitter.com/acct/status/1", "x"); got != "https://twitter.com/acct/status/1" {
		t.Fatalf("expected x url, got %q", got)
	}
}

func TestExtractYandexMusicURL(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{in: "трек https://music.yandex.ru/album/123/track/456", want: "https://music.yandex.ru/album/123/track/456"},
		{in: "трек https://music.yandex.com/track/456", want: "https://music.yandex.com/track/456"},
		{in: "альбом https://music.yandex.ru/album/12345", want: ""},
		{in: "плейлист https://music.yandex.ru/users/user/playlists/10", want: ""},
		{in: "не то https://example.org/album/1/track/2", want: ""},
	}
	for _, tc := range cases {
		if got := extractYandexMusicURL(tc.in); got != tc.want {
			t.Fatalf("extractYandexMusicURL(%q)=%q want=%q", tc.in, got, tc.want)
		}
	}
}

func TestExtractVKAudioTrackID(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{in: "https://vk.com/audio-2000703018_12703018", want: "-2000703018_12703018"},
		{in: "https://m.vk.com/audio123_456", want: "123_456"},
		{in: "вк https://vk.ru/audio-2000703018_12703018", want: "-2000703018_12703018"},
		{in: "https://m.vk.ru/audio123_456", want: "123_456"},
		{in: "audio-1_2", want: "-1_2"},
		{in: "https://vk.com/video-1_2", want: ""},
	}
	for _, tc := range cases {
		if got := extractVKAudioTrackID(tc.in); got != tc.want {
			t.Fatalf("extractVKAudioTrackID(%q)=%q want=%q", tc.in, got, tc.want)
		}
	}
}

func TestVideoFallbackHeights(t *testing.T) {
	cases := []struct {
		in   int
		want []int
	}{
		{in: 720, want: []int{720, 480, 360}},
		{in: 480, want: []int{480, 360}},
		{in: 360, want: []int{360}},
		{in: 0, want: []int{720, 480, 360}},
	}
	for _, tc := range cases {
		got := videoFallbackHeights(tc.in)
		if len(got) != len(tc.want) {
			t.Fatalf("videoFallbackHeights(%d) len=%d want=%d (%v)", tc.in, len(got), len(tc.want), got)
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Fatalf("videoFallbackHeights(%d) = %v, want %v", tc.in, got, tc.want)
			}
		}
	}
}

func TestVideoFallbackHeightsAbove720(t *testing.T) {
	got := videoFallbackHeights(1080)
	want := []int{1080, 720, 480, 360}
	if len(got) != len(want) {
		t.Fatalf("unexpected fallback len: got %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("videoFallbackHeights(1080)=%v want=%v", got, want)
		}
	}
}

func TestTargetVideoBitrateKbps(t *testing.T) {
	if got := targetVideoBitrateKbps(50*1024*1024, 120); got < 220 {
		t.Fatalf("bitrate too low: %d", got)
	}
	if got := targetVideoBitrateKbps(0, 120); got != 1200 {
		t.Fatalf("unexpected fallback bitrate: %d", got)
	}
}

func TestBuildMediaAudioTitle(t *testing.T) {
	got := buildMediaAudioTitle("Track Name", "https://youtu.be/abc", "youtube")
	if got != "Track Name" {
		t.Fatalf("unexpected media audio title: %q", got)
	}
}

func TestFilterPassThroughTriggers(t *testing.T) {
	all := []Trigger{
		{ID: 1, Title: "normal 1", PassThrough: false},
		{ID: 2, Title: "pass 1", PassThrough: true},
		{ID: 3, Title: "normal 2", PassThrough: false},
		{ID: 4, Title: "pass 2", PassThrough: true},
	}
	got := filterPassThroughTriggers(all)
	if len(got) != 2 {
		t.Fatalf("unexpected len: got=%d want=2", len(got))
	}
	if got[0].ID != 2 || got[1].ID != 4 {
		t.Fatalf("unexpected ids: got=%v", []int64{got[0].ID, got[1].ID})
	}
}

func TestFilterNonPassThroughTriggers(t *testing.T) {
	all := []Trigger{
		{ID: 1, Title: "normal 1", PassThrough: false},
		{ID: 2, Title: "pass 1", PassThrough: true},
		{ID: 3, Title: "normal 2", PassThrough: false},
	}
	got := filterNonPassThroughTriggers(all)
	if len(got) != 2 {
		t.Fatalf("unexpected len: got=%d want=2", len(got))
	}
	if got[0].ID != 1 || got[1].ID != 3 {
		t.Fatalf("unexpected ids: got=%v", []int64{got[0].ID, got[1].ID})
	}
}

func TestFilterUnusedTriggersThenPassThrough(t *testing.T) {
	all := []Trigger{
		{ID: 1, PassThrough: false},
		{ID: 2, PassThrough: true},
		{ID: 3, PassThrough: true},
	}
	used := map[int64]struct{}{2: {}}
	got := filterPassThroughTriggers(filterUnusedTriggers(all, used))
	if len(got) != 1 {
		t.Fatalf("unexpected len: got=%d want=1", len(got))
	}
	if got[0].ID != 3 {
		t.Fatalf("unexpected id: got=%d want=3", got[0].ID)
	}
}

func TestTriggerDisplayName(t *testing.T) {
	tr := &Trigger{ID: 7, Title: "  Мой триггер  ", MatchText: "abc"}
	if got := triggerDisplayName(tr); got != "Мой триггер" {
		t.Fatalf("unexpected trigger display name: %q", got)
	}
	tr = &Trigger{ID: 8, Title: " ", MatchText: "   message   "}
	if got := triggerDisplayName(tr); got != "message" {
		t.Fatalf("unexpected fallback trigger name: %q", got)
	}
	if got := triggerDisplayName(nil); got != "без названия" {
		t.Fatalf("unexpected nil trigger name: %q", got)
	}
}

func TestBuildSourceLinkHTML(t *testing.T) {
	got := buildSourceLinkHTML("https://instagram.com/reel/abc", "ссылка")
	if !strings.Contains(got, `<a href="https://instagram.com/reel/abc">ссылка</a>`) {
		t.Fatalf("unexpected source link html: %q", got)
	}
}

func TestBuildSourceLinkHTMLEscape(t *testing.T) {
	got := buildSourceLinkHTML(`https://x.test/?a=1&b=2`, `A&B`)
	if !strings.Contains(got, "A&amp;B") {
		t.Fatalf("expected escaped label, got %q", got)
	}
	if !strings.Contains(got, "a=1&amp;b=2") {
		t.Fatalf("expected escaped href, got %q", got)
	}
}

func TestBuildAudioCaption_AttachesSourceLink(t *testing.T) {
	f, err := os.CreateTemp("", "audio-cap-*.mp3")
	if err != nil {
		t.Fatalf("create temp audio: %v", err)
	}
	path := f.Name()
	defer os.Remove(path)
	if _, err := f.WriteString("fake-mp3-content-for-caption-test"); err != nil {
		_ = f.Close()
		t.Fatalf("write temp audio: %v", err)
	}
	_ = f.Close()

	caption := buildAudioCaption(path, "youtube", "https://youtu.be/abc")
	if !strings.Contains(caption, `<a href="https://youtu.be/abc">`) {
		t.Fatalf("expected source link in caption, got %q", caption)
	}
	if strings.Contains(caption, "\n") {
		t.Fatalf("audio caption must stay single-line, got %q", caption)
	}
}

func TestCleanSourceURLForCaption(t *testing.T) {
	raw := "https://www.tiktok.com/@vladlen.k_life/video/7650899329523125534?_r=1&u_code=e29jbe9ec0am3j&preview_pb=0&sharer_language=ru&_d=f1e6fi4mimgcjh&share_item_id=7650899323125534&source=h5_m&timestamp=1781641448&user_id=7109721649335239686&sec_user_id=MS4wLjABAAAA&item_author_type=2&social_share_type=2&utm_source=telegram&utm_campaign=client_share&utm_medium=android&share_iid=7600316765428123410&share_link_id=e2eafda3-2efa-447c-b8f2-68795ed0eaa5"
	got := cleanSourceURLForCaption(raw)
	want := "https://www.tiktok.com/@vladlen.k_life/video/7650899329523125534"
	if got != want {
		t.Fatalf("unexpected cleaned tiktok url:\n got %q\nwant %q", got, want)
	}
	yt := cleanSourceURLForCaption("https://www.youtube.com/watch?v=abc&utm_source=telegram&t=12s&si=nope")
	if yt != "https://www.youtube.com/watch?t=12s&v=abc" {
		t.Fatalf("unexpected cleaned youtube url: %q", yt)
	}
}

func TestMediaServiceEmoji(t *testing.T) {
	if got := mediaServiceEmoji(mediadl.ServiceYouTube, mediadl.ModeVideo); !strings.Contains(got, "5463206079913533096") {
		t.Fatalf("unexpected youtube video emoji: %q", got)
	}
	if got := mediaServiceEmoji(mediadl.ServiceYouTube, mediadl.ModeAudio); !strings.Contains(got, "5463206079913533096") {
		t.Fatalf("unexpected youtube audio emoji: %q", got)
	}
	if got := mediaServiceEmoji(mediadl.ServiceInstagram, mediadl.ModeVideo); !strings.Contains(got, "5463238270693416950") {
		t.Fatalf("unexpected instagram video emoji: %q", got)
	}
	if got := mediaServiceEmoji(mediadl.ServiceX, mediadl.ModeVideo); got != `<tg-emoji emoji-id="5465453979896913711">💬</tg-emoji>` {
		t.Fatalf("unexpected x video emoji: %q", got)
	}
	if got := mediaServiceEmoji(mediadl.ServiceSoundCloud, mediadl.ModeAudio); !strings.Contains(got, "5359614685664523140") {
		t.Fatalf("unexpected soundcloud emoji: %q", got)
	}
}

func TestExtractImageFileID(t *testing.T) {
	t.Setenv("GPT_IMAGE_CONTEXT_MAX_MB", "5")
	msg := &tgbotapi.Message{
		Photo: []tgbotapi.PhotoSize{
			{FileID: "small", FileSize: 10},
			{FileID: "large", FileSize: 99},
		},
	}
	if got := extractImageFileID(msg); got != "large" {
		t.Fatalf("expected largest photo id, got %q", got)
	}
	docMsg := &tgbotapi.Message{
		Document: &tgbotapi.Document{
			FileID:   "docimg",
			MimeType: "image/png",
		},
	}
	if got := extractImageFileID(docMsg); got != "docimg" {
		t.Fatalf("expected image document id, got %q", got)
	}
}

func TestExtractImageFileID_SkipsSVGDocumentForBitmapContext(t *testing.T) {
	t.Setenv("GPT_IMAGE_CONTEXT_MAX_MB", "5")
	msg := &tgbotapi.Message{
		Document: &tgbotapi.Document{
			FileID:   "svg-doc",
			FileName: "deer.svg",
			MimeType: "image/svg+xml",
			FileSize: 1006,
		},
	}
	if got := extractImageFileID(msg); got != "" {
		t.Fatalf("expected SVG to be skipped for bitmap image context, got %q", got)
	}
	fileID, fileName := extractSVGFileID(msg)
	if fileID != "svg-doc" || fileName != "deer.svg" {
		t.Fatalf("unexpected SVG extraction fileID=%q fileName=%q", fileID, fileName)
	}
}

func TestResolveMessageImageURL_CurrentPhotoReplyToBotUsesConfiguredFileEndpoint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/getMe"):
			_, _ = w.Write([]byte(`{"ok":true,"result":{"id":999,"is_bot":true,"first_name":"Оле-ням","username":"olenyam_bot"}}`))
		case strings.HasSuffix(r.URL.Path, "/getFile"):
			_, _ = w.Write([]byte(`{"ok":true,"result":{"file_id":"photo-id","file_unique_id":"u","file_size":123,"file_path":"photos/file_0.jpg"}}`))
		default:
			t.Fatalf("unexpected request path: %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	const token = "123456:ABCDEF"
	bot, err := tgbotapi.NewBotAPIWithAPIEndpoint(token, srv.URL+"/bot%s/%s")
	if err != nil {
		t.Fatalf("create bot: %v", err)
	}
	t.Setenv("TELEGRAM_BOT_FILE_ENDPOINT", srv.URL+"/file/bot%s/%s")

	msg := &tgbotapi.Message{
		Photo:   []tgbotapi.PhotoSize{{FileID: "photo-id", FileSize: 123}},
		Caption: "Оле-ням, оцени фото",
		ReplyToMessage: &tgbotapi.Message{
			From: &tgbotapi.User{ID: bot.Self.ID, IsBot: true, UserName: bot.Self.UserName},
			Text: "кидай",
		},
	}
	got, ok := resolveMessageImageURL(bot, msg)
	if !ok {
		t.Fatalf("expected image URL")
	}
	want := srv.URL + "/file/bot" + token + "/photos/file_0.jpg"
	if got != want {
		t.Fatalf("unexpected image URL:\n got %q\nwant %q", got, want)
	}
}

func TestBuildOpenAITelegramImageDataURL_AllowsLocalTelegramFileEndpoint(t *testing.T) {
	png, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+/p9sAAAAASUVORK5CYII=")
	if err != nil {
		t.Fatalf("decode png: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(png)
	}))
	defer srv.Close()

	got, err := buildOpenAITelegramImageDataURL(srv.URL + "/file/botTOKEN/photos/file_0.png")
	if err != nil {
		t.Fatalf("build data URL: %v", err)
	}
	if !strings.HasPrefix(got, "data:image/png;base64,") {
		t.Fatalf("expected png data URL, got %q", got[:min(len(got), 40)])
	}
}

func TestBuildOpenAITelegramImageDataURL_AllowsOctetStreamTelegramPhoto(t *testing.T) {
	png, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+/p9sAAAAASUVORK5CYII=")
	if err != nil {
		t.Fatalf("decode png: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(png)
	}))
	defer srv.Close()

	got, err := buildOpenAITelegramImageDataURL(srv.URL + "/file/botTOKEN/photos/file_0.jpg")
	if err != nil {
		t.Fatalf("build data URL: %v", err)
	}
	if !strings.HasPrefix(got, "data:image/png;base64,") {
		t.Fatalf("expected detected png data URL, got %q", got[:min(len(got), 40)])
	}
}

func TestBuildTelegramSVGContextFromURL_AllowsOctetStreamSVG(t *testing.T) {
	svg := `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 10 10"><circle cx="5" cy="5" r="4"/></svg>`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write([]byte(svg))
	}))
	defer srv.Close()

	got, err := buildTelegramSVGContextFromURL(srv.URL+"/file/botTOKEN/documents/deer.svg", "deer.svg")
	if err != nil {
		t.Fatalf("build SVG context: %v", err)
	}
	for _, want := range []string{`Файл "deer.svg"`, "не инструкции", "```svg", "<circle"} {
		if !strings.Contains(got, want) {
			t.Fatalf("SVG context does not contain %q: %s", want, got)
		}
	}
}

func TestExtractImageFileID_SkipsOversizedImages(t *testing.T) {
	t.Setenv("GPT_IMAGE_CONTEXT_MAX_MB", "5")
	tooLarge := 6 << 20
	msg := &tgbotapi.Message{
		Photo: []tgbotapi.PhotoSize{
			{FileID: "large", FileSize: tooLarge},
		},
	}
	if got := extractImageFileID(msg); got != "" {
		t.Fatalf("expected oversized photo to be skipped, got %q", got)
	}
	docMsg := &tgbotapi.Message{
		Document: &tgbotapi.Document{
			FileID:   "docimg",
			MimeType: "image/png",
			FileSize: tooLarge,
		},
	}
	if got := extractImageFileID(docMsg); got != "" {
		t.Fatalf("expected oversized image document to be skipped, got %q", got)
	}
}

func TestExtractImageFileID_NonImageDocument(t *testing.T) {
	docMsg := &tgbotapi.Message{
		Document: &tgbotapi.Document{
			FileID:   "docfile",
			MimeType: "application/pdf",
		},
	}
	if got := extractImageFileID(docMsg); got != "" {
		t.Fatalf("expected empty for non-image document, got %q", got)
	}
}

func TestFirstNonEmptyUserText(t *testing.T) {
	msg := &tgbotapi.Message{Caption: "из подписи"}
	if got := firstNonEmptyUserText(msg); got != "из подписи" {
		t.Fatalf("expected caption fallback, got %q", got)
	}
}

func TestBuildMediaPhotoCaption(t *testing.T) {
	f, err := os.CreateTemp("", "photo-cap-*.jpg")
	if err != nil {
		t.Fatalf("create temp: %v", err)
	}
	path := f.Name()
	_, _ = f.Write([]byte(strings.Repeat("a", 1024*64)))
	_ = f.Close()
	defer os.Remove(path)

	caption := buildMediaPhotoCaption(path, "Demo", "https://instagram.com/reel/abc", "instagram")
	if !strings.Contains(caption, `<a href="https://instagram.com/reel/abc">Demo</a>`) {
		t.Fatalf("expected linked title in caption, got %q", caption)
	}
	if !strings.Contains(caption, "MB") {
		t.Fatalf("expected size in caption, got %q", caption)
	}
}

func TestMediaModeAndInteractivity(t *testing.T) {
	mode, interactive := mediaModeAndInteractivity(mediadl.ServiceSoundCloud, true)
	if mode != "audio" || interactive {
		t.Fatalf("soundcloud must force audio/no interactive, got mode=%q interactive=%v", mode, interactive)
	}
	mode, interactive = mediaModeAndInteractivity(mediadl.ServiceInstagram, true)
	if mode != "auto" || interactive {
		t.Fatalf("instagram must force auto/no interactive, got mode=%q interactive=%v", mode, interactive)
	}
	mode, interactive = mediaModeAndInteractivity(mediadl.ServicePinterest, true)
	if mode != "auto" || interactive {
		t.Fatalf("pinterest must force auto/no interactive, got mode=%q interactive=%v", mode, interactive)
	}
	mode, interactive = mediaModeAndInteractivity(mediadl.ServiceTikTok, true)
	if mode != "auto" || interactive {
		t.Fatalf("tiktok must force auto/no interactive, got mode=%q interactive=%v", mode, interactive)
	}
	mode, interactive = mediaModeAndInteractivity(mediadl.ServiceX, true)
	if mode != "video" || interactive {
		t.Fatalf("x must force video/no interactive, got mode=%q interactive=%v", mode, interactive)
	}
	mode, interactive = mediaModeAndInteractivity(mediadl.ServiceVK, true)
	if mode != "auto" || interactive {
		t.Fatalf("vk must force auto/no interactive, got mode=%q interactive=%v", mode, interactive)
	}
	mode, interactive = mediaModeAndInteractivity(mediadl.ServiceYouTube, true)
	if mode != "audio" || !interactive {
		t.Fatalf("youtube should keep interactive when enabled, got mode=%q interactive=%v", mode, interactive)
	}
	mode, interactive = mediaModeAndInteractivity(mediadl.ServiceYouTube, false)
	if mode != "audio" || interactive {
		t.Fatalf("youtube should keep interactive=false, got mode=%q interactive=%v", mode, interactive)
	}
}

func TestEstimateGPTReplyHumanPause_Disabled(t *testing.T) {
	t.Setenv("GPT_HUMAN_PAUSE", "false")
	if got := estimateGPTReplyHumanPause("hello"); got != 0 {
		t.Fatalf("expected zero pause when disabled, got %v", got)
	}
}

func TestEstimateGPTReplyHumanPause_Bounded(t *testing.T) {
	t.Setenv("GPT_HUMAN_PAUSE", "true")
	t.Setenv("GPT_HUMAN_PAUSE_MIN_MS", "1200")
	t.Setenv("GPT_HUMAN_PAUSE_MAX_MS", "1200")
	if got := estimateGPTReplyHumanPause("short"); got != 1200*time.Millisecond {
		t.Fatalf("expected fixed 1200ms pause, got %v", got)
	}
}

func TestBuildMediaVideoCaption(t *testing.T) {
	f, err := os.CreateTemp("", "video-cap-*.mp4")
	if err != nil {
		t.Fatalf("create temp: %v", err)
	}
	path := f.Name()
	_, _ = f.Write([]byte(strings.Repeat("a", 1024*64)))
	_ = f.Close()
	defer os.Remove(path)

	caption := buildMediaVideoCaption(path, "My Reel", "https://instagram.com/reel/abc", "instagram")
	if !strings.Contains(caption, `<a href="https://instagram.com/reel/abc">My Reel</a>`) {
		t.Fatalf("expected linked title in caption, got %q", caption)
	}
	if !strings.Contains(caption, "MB") {
		t.Fatalf("expected size in caption, got %q", caption)
	}
	if !strings.Contains(caption, "5463238270693416950") {
		t.Fatalf("expected instagram camera emoji in caption, got %q", caption)
	}
}

func TestBuildPromptFromMessageTemplateAndFallback(t *testing.T) {
	msg := &tgbotapi.Message{
		Chat: &tgbotapi.Chat{ID: -1001, Title: "Чат"},
		From: &tgbotapi.User{ID: 7, FirstName: "Аня", UserName: "anya"},
		Text: "привет",
	}
	ctx := templateContext{Msg: msg}
	withTemplate := buildPromptFromMessage(ctx, "Скажи привет {{user_first_name}}")
	if withTemplate != "Скажи привет Аня" {
		t.Fatalf("template prompt mismatch: %q", withTemplate)
	}
	noTemplate := buildPromptFromMessage(ctx, "Ответь коротко")
	if !strings.Contains(noTemplate, "Сообщение пользователя") || !strings.Contains(noTemplate, "привет") {
		t.Fatalf("prompt fallback missing message: %q", noTemplate)
	}
}

func TestBuildPromptFromMessageKeepsDrawRequestRaw(t *testing.T) {
	msg := &tgbotapi.Message{
		Chat: &tgbotapi.Chat{ID: -1001, Title: "Чат"},
		From: &tgbotapi.User{ID: 7, FirstName: "Аня", UserName: "anya"},
		Text: "оленям нарисуй оленя",
	}
	got := buildPromptFromMessage(templateContext{Msg: msg}, "Ответь {{message}}")
	if strings.Contains(got, "следующий объект или сцену в виде SVG") || got != "Ответь оленям нарисуй оленя" {
		t.Fatalf("draw instruction must live in the trigger template, got %q", got)
	}
}

func TestExtractFirstSVGFromGPTResponse(t *testing.T) {
	got, ok := extractFirstSVGFromGPTResponse("текст\n```xml\n<svg viewBox=\"0 0 10 10\"><circle cx=\"5\" cy=\"5\" r=\"4\"/></svg>\n```\nхвост")
	if !ok {
		t.Fatalf("expected SVG response to be detected")
	}
	if !strings.Contains(got, "<circle") || strings.Contains(got, "хвост") {
		t.Fatalf("unexpected extracted SVG: %q", got)
	}
}

func TestValidateGPTSVGResponseRejectsExecutableContent(t *testing.T) {
	for _, src := range []string{
		`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`,
		`<svg xmlns="http://www.w3.org/2000/svg"><rect onclick="alert(1)"/></svg>`,
		`<svg xmlns="http://www.w3.org/2000/svg"><image href="https://example.com/a.png"/></svg>`,
	} {
		if err := validateGPTSVGResponse(src); err == nil {
			t.Fatalf("expected unsafe SVG to be rejected: %s", src)
		}
	}
	if err := validateGPTSVGResponse(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 10 10"><circle cx="5" cy="5" r="4"/></svg>`); err != nil {
		t.Fatalf("safe SVG rejected: %v", err)
	}
}

func TestValidateGPTSVGResponseAllowsInternalReferences(t *testing.T) {
	src := `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 100 80">
  <defs>
    <linearGradient id="bg"><stop offset="0%" stop-color="#fff"/><stop offset="100%" stop-color="#eee"/></linearGradient>
    <circle id="star" r="4" fill="gold"/>
  </defs>
  <rect width="100" height="80" fill="url(#bg)"/>
  <use href="#star" x="20" y="20"/>
  <use xlink:href="#star" x="40" y="30"/>
</svg>`
	if err := validateGPTSVGResponse(src); err != nil {
		t.Fatalf("safe internal SVG refs rejected: %v", err)
	}
}

func TestNormalizeGPTSVGForRenderDropsInvalidXMLComments(t *testing.T) {
	src := `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 100 80">
  <!-- Shelf details -- abstract -->
  <rect width="100" height="80" fill="white"/>
</svg>`
	got := normalizeGPTSVGForRender(src)
	if strings.Contains(got, "<!--") || strings.Contains(got, "-->") {
		t.Fatalf("SVG comments must be stripped before rendering: %q", got)
	}
	if !strings.Contains(got, "<rect") {
		t.Fatalf("rendered SVG content was removed: %q", got)
	}
}

func TestGPTSVGRenderSizeUsesViewBoxForPercentDimensions(t *testing.T) {
	width, height := gptSVGRenderSize(`<svg viewBox="0 0 1500 520" width="100%" height="100%" xmlns="http://www.w3.org/2000/svg"></svg>`)
	if width != 1500 || height != 520 {
		t.Fatalf("unexpected SVG render size from percentage dimensions: %dx%d", width, height)
	}
}

func TestRenderGPTSVGResponseToPNGStripsInvalidXMLComments(t *testing.T) {
	if _, err := systemDiagramBin("TRIGGER_BOT_RSVG_CONVERT_BIN", "rsvg-convert"); err != nil {
		t.Skipf("rsvg-convert is not available: %v", err)
	}
	_, err := renderGPTSVGResponseToPNG(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 120 80" width="120" height="80">
  <!-- Shelf details -- abstract -->
  <rect width="120" height="80" fill="white"/>
  <circle cx="60" cy="40" r="25" fill="#b17d4a"/>
</svg>`)
	if err != nil {
		t.Fatalf("render SVG with invalid XML comment: %v", err)
	}
}

func TestRenderGPTSVGResponseToPNGWithRsvgConvert(t *testing.T) {
	if _, err := systemDiagramBin("TRIGGER_BOT_RSVG_CONVERT_BIN", "rsvg-convert"); err != nil {
		t.Skipf("rsvg-convert is not available: %v", err)
	}
	longTmp := t.TempDir() + "/" + strings.Repeat("deep-render-path-", 5)
	if err := os.MkdirAll(longTmp, 0o700); err != nil {
		t.Fatalf("create long tmp dir: %v", err)
	}
	t.Setenv("TRIGGER_BOT_TMP_DIR", longTmp)
	pngBytes, err := renderGPTSVGResponseToPNG(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 120 80" width="120" height="80"><rect width="120" height="80" fill="white"/><circle cx="60" cy="40" r="25" fill="#b17d4a"/></svg>`)
	if err != nil {
		t.Fatalf("render SVG: %v", err)
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(pngBytes))
	if err != nil {
		t.Fatalf("decode rendered PNG: %v", err)
	}
	if cfg.Width != 120 || cfg.Height != 80 {
		t.Fatalf("unexpected rendered PNG size: %dx%d", cfg.Width, cfg.Height)
	}
}

func TestMessageWithEffectiveUserTextForVoiceTranscription(t *testing.T) {
	msg := &tgbotapi.Message{
		MessageID: 42,
		Chat:      &tgbotapi.Chat{ID: -1001},
		From:      &tgbotapi.User{ID: 7, FirstName: "Аня"},
		Voice:     &tgbotapi.Voice{FileID: "voice-file", Duration: 19},
	}

	got := messageWithEffectiveUserText(msg, "Оленям, ответь на голосовое")
	if got == nil {
		t.Fatal("expected effective message")
	}
	if got == msg {
		t.Fatal("voice message with synthetic text must be copied")
	}
	if got.Text != "Оленям, ответь на голосовое" {
		t.Fatalf("unexpected effective text: %q", got.Text)
	}
	if firstNonEmptyUserText(got) != "Оленям, ответь на голосовое" {
		t.Fatalf("effective text must be visible to template/trigger helpers, got %q", firstNonEmptyUserText(got))
	}
	if got.MessageID != msg.MessageID || got.Voice == nil {
		t.Fatalf("effective message must preserve original metadata: %#v", got)
	}
}

func TestBuildResponseFromMessageCapturingChoice(t *testing.T) {
	pattern := `^\\s*((?:уби|обня|поцелова))ть\\s*$`
	ctx := templateContext{
		Msg:           &tgbotapi.Message{Text: "обнять"},
		CapturingText: "обня",
		MatchText:     pattern,
	}
	got := buildResponseFromMessage(ctx, "{{capturing_choice}}")
	if got != "обня" {
		t.Fatalf("capturing choice mismatch: %q", got)
	}
}

func TestBuildResponseFromMessage_CommonTemplateFuncs(t *testing.T) {
	ctx := templateContext{
		Msg: &tgbotapi.Message{
			Chat: &tgbotapi.Chat{ID: -1001, Title: "Чат"},
			From: &tgbotapi.User{ID: 7, FirstName: "аня", UserName: "anya"},
			Text: "  Привет Мир  ",
		},
	}

	if got := buildResponseFromMessage(ctx, `{{ .missing | default "друг" }}`); got != "друг" {
		t.Fatalf("default mismatch: %q", got)
	}
	if got := buildResponseFromMessage(ctx, `{{ .message | trim | lower }}`); got != "привет мир" {
		t.Fatalf("trim/lower mismatch: %q", got)
	}
	if got := buildResponseFromMessage(ctx, `{{ .user_first_name | upper }}`); got != "АНЯ" {
		t.Fatalf("upper mismatch: %q", got)
	}
	if got := buildResponseFromMessage(ctx, `{{ .message | trim | title }}`); got != "Привет Мир" {
		t.Fatalf("title mismatch: %q", got)
	}
	if got := buildResponseFromMessage(ctx, `{{ .message | trim | replace " " "_" }}`); got != "Привет_Мир" {
		t.Fatalf("replace mismatch: %q", got)
	}
	if got := buildResponseFromMessage(ctx, `{{ .message | trim | truncate 6 }}`); got != "Привет" {
		t.Fatalf("truncate mismatch: %q", got)
	}
	if got := buildResponseFromMessage(ctx, `{{ .message | trim | split " " | join "-" }}`); got != "Привет-Мир" {
		t.Fatalf("join mismatch: %q", got)
	}
	if got := buildResponseFromMessage(ctx, `{{ .message | trim | split " " | first }}`); got != "Привет" {
		t.Fatalf("first mismatch: %q", got)
	}
	if got := buildResponseFromMessage(ctx, `{{ .message | trim | split " " | last }}`); got != "Мир" {
		t.Fatalf("last mismatch: %q", got)
	}
	if got := buildResponseFromMessage(ctx, `{{ "2026-04-19T23:00:00Z" | date "2006-01-02" }}`); got != "2026-04-19" {
		t.Fatalf("date mismatch: %q", got)
	}
	if got := buildResponseFromMessage(ctx, `{{ now | date "2006" }}`); got == "" {
		t.Fatalf("now/date must not be empty")
	}
}

func TestBuildResponseFromMessage_EscapesUserHTMLInPlainVars(t *testing.T) {
	ctx := templateContext{
		Msg: &tgbotapi.Message{
			Chat: &tgbotapi.Chat{ID: -1001, Title: "Чат"},
			From: &tgbotapi.User{ID: 7, FirstName: "Аня", UserName: "anya"},
			Text: `1'); DROP TABLE users; <tig-emoji emoji-id="1">🙂</tig-emoji>`,
		},
	}
	got := buildResponseFromMessage(ctx, `{{ .message }}`)
	if strings.Contains(got, "<tig-emoji") {
		t.Fatalf("message html must be escaped, got=%q", got)
	}
	if !strings.Contains(got, "&lt;tig-emoji") {
		t.Fatalf("escaped html marker not found, got=%q", got)
	}
}

func TestBuildResponseFromMessage_UserLinkRemainsHTML(t *testing.T) {
	ctx := templateContext{
		Msg: &tgbotapi.Message{
			Chat: &tgbotapi.Chat{ID: -1001, Title: "Чат"},
			From: &tgbotapi.User{ID: 7, FirstName: `<b>Ann</b>`, UserName: "anya"},
			Text: "test",
		},
	}
	got := buildResponseFromMessage(ctx, `{{ .user_link }}`)
	if !strings.Contains(got, `<a href="tg://user?id=7">`) {
		t.Fatalf("expected anchor in user_link, got=%q", got)
	}
	if strings.Contains(got, "<b>Ann</b>") {
		t.Fatalf("link text must be escaped, got=%q", got)
	}
	if !strings.Contains(got, "&lt;b&gt;Ann&lt;/b&gt;") {
		t.Fatalf("escaped link text not found, got=%q", got)
	}
}

func TestResolveGenderVariant(t *testing.T) {
	variants := genderVariants{
		Male:    "он",
		Female:  "она",
		Neuter:  "оно",
		Plural:  "они",
		Unknown: "кто-то",
	}
	if got := resolveGenderVariant("he", variants); got != "он" {
		t.Fatalf("male mismatch: %q", got)
	}
	if got := resolveGenderVariant("божий сын ОН/ЕГО", variants); got != "он" {
		t.Fatalf("male title+pronouns mismatch: %q", got)
	}
	if got := resolveGenderVariant("she", variants); got != "она" {
		t.Fatalf("female mismatch: %q", got)
	}
	if got := resolveGenderVariant("прозвище она/её", variants); got != "она" {
		t.Fatalf("female title+pronouns mismatch: %q", got)
	}
	if got := resolveGenderVariant("it", variants); got != "оно" {
		t.Fatalf("neuter mismatch: %q", got)
	}
	if got := resolveGenderVariant("оно", variants); got != "оно" {
		t.Fatalf("neutral pronoun mismatch: %q", got)
	}
	if got := resolveGenderVariant("они", variants); got != "они" {
		t.Fatalf("plural mismatch: %q", got)
	}
	if got := resolveGenderVariant("ник они/их", variants); got != "они" {
		t.Fatalf("plural title+pronouns mismatch: %q", got)
	}
	if got := resolveGenderVariant("unknown", variants); got != "кто-то" {
		t.Fatalf("unknown mismatch: %q", got)
	}
}

func TestModerationActionVerbByTag(t *testing.T) {
	if got := moderationActionVerb("ban", "he"); got != "забанил" {
		t.Fatalf("male ban verb mismatch: %q", got)
	}
	if got := moderationActionVerb("ban", "she"); got != "забанила" {
		t.Fatalf("female ban verb mismatch: %q", got)
	}
	if got := moderationActionVerb("ban", "unknown"); got != "забанил(а)" {
		t.Fatalf("unknown ban verb mismatch: %q", got)
	}
}

func TestModerationReadonlyStateVerbByTag(t *testing.T) {
	if got := moderationReadonlyStateVerb(true, "he"); got != "включил режим только чтения" {
		t.Fatalf("male readonly on mismatch: %q", got)
	}
	if got := moderationReadonlyStateVerb(false, "she"); got != "выключила режим только чтения" {
		t.Fatalf("female readonly off mismatch: %q", got)
	}
	if got := moderationReadonlyStateVerb(true, "none"); got != "включил(а) режим только чтения" {
		t.Fatalf("unknown readonly on mismatch: %q", got)
	}
}

func TestChatIdleTrackerFlow(t *testing.T) {
	tr := trigger.NewIdleTracker()
	chatID := int64(-42)
	base := time.Unix(1_700_000_000, 0)

	tr.Seen(chatID, base)
	if tr.ShouldAutoReply(chatID, time.Hour, base.Add(59*time.Minute)) {
		t.Fatalf("must not auto-reply before idle threshold")
	}
	if !tr.ShouldAutoReply(chatID, time.Hour, base.Add(60*time.Minute)) {
		t.Fatalf("must auto-reply at idle threshold")
	}

	tr.MarkActivity(chatID, base.Add(61*time.Minute))
	if tr.ShouldAutoReply(chatID, time.Hour, base.Add(120*time.Minute)) {
		t.Fatalf("must not auto-reply right after activity")
	}
	if !tr.ShouldAutoReply(chatID, time.Hour, base.Add(121*time.Minute)) {
		t.Fatalf("must auto-reply after new idle period")
	}
}

func TestSelectIdleAutoReplyTrigger(t *testing.T) {
	msg := &tgbotapi.Message{
		MessageID: 1,
		Chat:      &tgbotapi.Chat{ID: -1001},
		From:      &tgbotapi.User{ID: 100},
		Text:      "привет",
	}
	items := []Trigger{
		{ID: 1, Enabled: true, MatchType: "idle", MatchText: "90", ActionType: "send", TriggerMode: "all", AdminMode: "anybody", Chance: 100},
		{ID: 2, Enabled: true, MatchType: "idle", MatchText: "abc", ActionType: "gpt_prompt", TriggerMode: "all", AdminMode: "anybody", Chance: 100},
		{ID: 3, Enabled: true, MatchType: "idle", MatchText: "30", ActionType: "gpt_prompt", TriggerMode: "all", AdminMode: "admins", Chance: 100},
		{ID: 4, Enabled: true, MatchType: "idle", MatchText: "45", ActionType: "gpt_prompt", TriggerMode: "all", AdminMode: "anybody", Chance: 100},
	}

	got, after := trigger.SelectIdleAutoReplyTrigger(nil, msg, items, func() bool { return false })
	if got == nil || got.ID != 4 {
		t.Fatalf("expected trigger id=4, got=%#v", got)
	}
	if after != 45*time.Minute {
		t.Fatalf("expected 45m idle duration, got %s", after)
	}
}

func TestExtractCustomEmojiFromRaw(t *testing.T) {
	raw := &rawMessageWithEmoji{
		Text: "x🙂y",
		Entities: []rawMessageEntity{
			{Type: "custom_emoji", CustomEmojiID: "111", Offset: 1, Length: 2},
			{Type: "custom_emoji", CustomEmojiID: "111"},
			{Type: "bold"},
		},
		Caption: "z🦌w",
		CaptionEntities: []rawMessageEntity{
			{Type: "custom_emoji", CustomEmojiID: "222", Offset: 1, Length: 2},
			{Type: "custom_emoji"},
		},
	}

	hits, count := extractCustomEmojiFromRaw(raw)
	if count != 4 {
		t.Fatalf("expected count=4, got %d", count)
	}
	if len(hits) != 2 {
		t.Fatalf("expected 2 unique IDs, got %d", len(hits))
	}
	if hits[0].ID != "111" || hits[1].ID != "222" {
		t.Fatalf("unexpected ids order/content: %#v", hits)
	}
	if hits[0].Fallback != "🙂" || hits[1].Fallback != "🦌" {
		t.Fatalf("unexpected fallbacks: %#v", hits)
	}
}

func TestCanonicalizeTGEmojiTags(t *testing.T) {
	in := `Aга, вот: \"<tg-emoji emoji-id=\"5247191236632152397\">\"</tg-emoji>`
	got := canonicalizeTGEmojiTags(in)
	if want := `<tg-emoji emoji-id="5247191236632152397">🙂</tg-emoji>`; !strings.Contains(got, want) {
		t.Fatalf("canonical tg-emoji not found, got=%q", got)
	}
}

func TestCanonicalizeTGEmojiTags_FixesTrEmojiTypo(t *testing.T) {
	in := `<tr-emoji emoji-id="5247191236632152397">🦌</tg-emoji>`
	got := canonicalizeTGEmojiTags(in)
	want := `<tg-emoji emoji-id="5247191236632152397">🦌</tg-emoji>`
	if got != want {
		t.Fatalf("unexpected canonicalized typo tag: got=%q want=%q", got, want)
	}
}

func TestReplaceTGEmojiTagsWithFallback(t *testing.T) {
	in := `Привет <tg-emoji emoji-id="1">🦌</tg-emoji> мир`
	got := replaceTGEmojiTagsWithFallback(in)
	if got != "Привет 🦌 мир" {
		t.Fatalf("unexpected fallback replace: %q", got)
	}
}

func TestOpenAIChatTimeout(t *testing.T) {
	t.Setenv("OPENAI_CHAT_TIMEOUT_SEC", "")
	if got := openAIChatTimeout(); got != 90*time.Second {
		t.Fatalf("unexpected default timeout: %s", got)
	}
	t.Setenv("OPENAI_CHAT_TIMEOUT_SEC", "5")
	if got := openAIChatTimeout(); got != 25*time.Second {
		t.Fatalf("unexpected min timeout: %s", got)
	}
	t.Setenv("OPENAI_CHAT_TIMEOUT_SEC", "600")
	if got := openAIChatTimeout(); got != 300*time.Second {
		t.Fatalf("unexpected max timeout: %s", got)
	}
	t.Setenv("OPENAI_CHAT_TIMEOUT_SEC", "120")
	if got := openAIChatTimeout(); got != 120*time.Second {
		t.Fatalf("unexpected configured timeout: %s", got)
	}
}

func TestContainsTelegramHTMLMarkup(t *testing.T) {
	if !containsTelegramHTMLMarkup(`Привет <b>мир</b>`) {
		t.Fatalf("expected true for <b> tag")
	}
	if !containsTelegramHTMLMarkup(`<tg-emoji emoji-id="1">💗</tg-emoji>`) {
		t.Fatalf("expected true for tg-emoji tag")
	}
	if containsTelegramHTMLMarkup(`**markdown** без html`) {
		t.Fatalf("expected false for pure markdown")
	}
}

func TestExtractStickerCode(t *testing.T) {
	msg := &tgbotapi.Message{
		Sticker: &tgbotapi.Sticker{
			FileID:  "sticker_file_id",
			SetName: "my_set_name",
		},
	}
	hit, ok := extractStickerCode(msg)
	if !ok {
		t.Fatalf("expected sticker code hit")
	}
	if hit.FileID != "sticker_file_id" {
		t.Fatalf("unexpected file id: %q", hit.FileID)
	}
	if hit.SetID != "my_set_name" {
		t.Fatalf("unexpected set id: %q", hit.SetID)
	}
	if got := buildStickerPairCode(hit); got != "sticker_file_id:my_set_name" {
		t.Fatalf("unexpected pair code: %q", got)
	}
}

func TestExtractStickerCodeEmpty(t *testing.T) {
	msg := &tgbotapi.Message{
		Sticker: &tgbotapi.Sticker{},
	}
	if _, ok := extractStickerCode(msg); ok {
		t.Fatalf("expected no sticker code hit when file id is empty")
	}
}

func TestExtractAnimationCode(t *testing.T) {
	msg := &tgbotapi.Message{
		Animation: &tgbotapi.Animation{
			FileID: "gif_file_id_1",
		},
		Caption: "  подпись  ",
	}
	hit, ok := extractAnimationCode(msg)
	if !ok {
		t.Fatalf("expected animation code hit")
	}
	if hit.FileID != "gif_file_id_1" {
		t.Fatalf("unexpected file id: %q", hit.FileID)
	}
	if hit.Caption != "подпись" {
		t.Fatalf("unexpected caption: %q", hit.Caption)
	}
	if got := buildAnimationReplyText(hit); got != "gif_file_id_1\nподпись" {
		t.Fatalf("unexpected animation reply text: %q", got)
	}
}

func TestExtractAnimationCodeEmpty(t *testing.T) {
	msg := &tgbotapi.Message{
		Animation: &tgbotapi.Animation{},
	}
	if _, ok := extractAnimationCode(msg); ok {
		t.Fatalf("expected no animation code hit when file id is empty")
	}
}

func TestExtractStickerFileIDFromTemplate(t *testing.T) {
	raw := "<code>CAACAgIAAxkBAAMnaeQ1_jOjPuH6zZsuFC1qwh0Q0NYAAntOAAIuXRhLED6vCxOdgOw7BA:Nokotanfx</code>"
	got := extractStickerFileIDFromTemplate(raw)
	want := "CAACAgIAAxkBAAMnaeQ1_jOjPuH6zZsuFC1qwh0Q0NYAAntOAAIuXRhLED6vCxOdgOw7BA"
	if got != want {
		t.Fatalf("unexpected sticker file id: got=%q want=%q", got, want)
	}
}

func TestMarkdownToTelegramHTMLLite(t *testing.T) {
	in := "# Заголовок\n> Цитата первая\n> Вторая строка\n---\nКод:\n```python\nprint('hi')\n```\nИ `x=1` и [сайт](https://example.com)\n**жирный** *курсив* __подчеркнутый__ ~~зачеркнутый~~"
	got := markdownToTelegramHTMLLite(in)
	if !strings.Contains(got, `<b>§ Заголовок</b>`) {
		t.Fatalf("heading not converted: %q", got)
	}
	if !strings.Contains(got, `<blockquote>Цитата первая`+"\n"+`Вторая строка</blockquote>`) {
		t.Fatalf("blockquote not converted: %q", got)
	}
	if !strings.Contains(got, strings.Repeat(`<tg-emoji emoji-id="5213083123218147891">〰️</tg-emoji>`, 11)) {
		t.Fatalf("hr not converted to divider emojis: %q", got)
	}
	if !strings.Contains(got, `<pre><code class="language-python">`) {
		t.Fatalf("fenced code not converted: %q", got)
	}
	if !strings.Contains(got, `<code>x=1</code>`) {
		t.Fatalf("inline code not converted: %q", got)
	}
	if !strings.Contains(got, `<a href="https://example.com">сайт</a>`) {
		t.Fatalf("link not converted: %q", got)
	}
	if !strings.Contains(got, `<b>жирный</b>`) {
		t.Fatalf("bold not converted: %q", got)
	}
	if !strings.Contains(got, `<i>курсив</i>`) {
		t.Fatalf("italic not converted: %q", got)
	}
	if !strings.Contains(got, `<u>подчеркнутый</u>`) {
		t.Fatalf("underline not converted: %q", got)
	}
	if !strings.Contains(got, `<s>зачеркнутый</s>`) {
		t.Fatalf("strike not converted: %q", got)
	}
}

func TestContainsMarkdownLiteMarkup(t *testing.T) {
	if !containsMarkdownLiteMarkup(`**markdown** без html`) {
		t.Fatalf("expected true for markdown text")
	}
	if !containsMarkdownLiteMarkup(`# заголовок`) {
		t.Fatalf("expected true for markdown heading")
	}
	if !containsMarkdownLiteMarkup(`> цитата`) {
		t.Fatalf("expected true for markdown quote")
	}
	if !containsMarkdownLiteMarkup(`---`) {
		t.Fatalf("expected true for markdown divider")
	}
	if containsMarkdownLiteMarkup(`обычный текст`) {
		t.Fatalf("expected false for plain text")
	}
}

func TestContainsRichArticleMarkup(t *testing.T) {
	positive := []string{
		"# заголовок",
		"- пункт списка",
		"1. пункт списка",
		"> цитата",
		"```go\nfmt.Println(1)\n```",
		"| A | B |\n|---|---|\n| 1 | 2 |",
		"<details><summary>ещё</summary>текст</details>",
		`$x^2 + y^2$`,
		`$$E = mc^2$$`,
		`\(\frac{a}{b}\)`,
		`\[E = mc^2\]`,
		`<tg-math>x^2</tg-math>`,
	}
	for _, in := range positive {
		if !containsRichArticleMarkup(in) {
			t.Fatalf("expected rich article for %q", in)
		}
	}

	negative := []string{
		"обычный текст",
		"**просто жирный**",
		"цена $100 и ещё $20",
		"$100$",
		`<tg-emoji emoji-id="123">🦌</tg-emoji> короткая реплика`,
	}
	for _, in := range negative {
		if containsRichArticleMarkup(in) {
			t.Fatalf("expected non-rich text for %q", in)
		}
	}
}

func TestResponseToRichMarkdownArticle_CustomEmoji(t *testing.T) {
	in := `<tg-emoji emoji-id="123">🦌</tg-emoji> привет` + "\n# Заголовок"
	got := responseToRichMarkdownArticle(in)
	if !strings.Contains(got, `![🦌](tg://emoji?id=123)`) {
		t.Fatalf("custom emoji must be converted to rich markdown image syntax: %q", got)
	}
	if !strings.Contains(got, "# Заголовок") {
		t.Fatalf("heading must be preserved: %q", got)
	}
}

func TestNormalizeTelegramLineBreaks_PreservesLatexCommands(t *testing.T) {
	in := `\text{Зевс} + \text{Майя} \rightarrow \text{Гермес} \neq \nu \rho`
	if got := normalizeTelegramLineBreaks(in); got != in {
		t.Fatalf("latex commands must not be split as escaped line breaks:\n got %q\nwant %q", got, in)
	}
}

func TestNormalizeTelegramLineBreaks_ConvertsLiteralEscapedBreaks(t *testing.T) {
	tests := map[string]string{
		`a\r\nb`:  "a\nb",
		`a\n- b`:  "a\n- b",
		`a\nЕсли`: "a\nЕсли",
		`a\r b`:   "a\n b",
	}
	for in, want := range tests {
		if got := normalizeTelegramLineBreaks(in); got != want {
			t.Fatalf("normalizeTelegramLineBreaks(%q): got %q want %q", in, got, want)
		}
	}
}

func TestRenderUnsupportedRichArticleMath_RendersBracketDisplayBlock(t *testing.T) {
	requireSystemLatexRenderer(t)
	in := "До\n\n\\[E = mc^2\\]\n\nПосле"
	got, attachments, err := renderUnsupportedRichArticleMath(in)
	if err != nil {
		t.Fatalf("render unsupported math: %v", err)
	}
	if len(attachments) != 1 {
		t.Fatalf("expected 1 rendered formula attachment, got %d", len(attachments))
	}
	if len(attachments[0].PNG) == 0 {
		t.Fatalf("rendered formula attachment is empty")
	}
	if !strings.Contains(got, richArticleRenderedFormulaMarkdown(attachments[0].ID)) {
		t.Fatalf("formula placeholder missing: %q", got)
	}
	if strings.Contains(got, `\[E = mc^2\]`) {
		t.Fatalf("unsupported latex block must be replaced: %q", got)
	}
}

func TestBuildStandaloneLatexDocument_UsesLightCanvas(t *testing.T) {
	got := buildStandaloneLatexDocument(`E = mc^2`)
	if !strings.Contains(got, `\pagecolor[HTML]{FFFFFF}`) {
		t.Fatalf("standalone latex document must use white canvas: %q", got)
	}
	if !strings.Contains(got, `\color[HTML]{111111}`) {
		t.Fatalf("standalone latex document must use dark text: %q", got)
	}
}

func TestBuildStandaloneLatexDocument_KeepsAlignStarStandalone(t *testing.T) {
	got := buildStandaloneLatexDocument(`\begin{align*}a &= b\end{align*}`)
	if strings.Contains(got, "\\[\n\\begin{align*}") {
		t.Fatalf("align* must be standalone, not wrapped into display math: %q", got)
	}
	if !strings.Contains(got, `\begin{align*}a &= b\end{align*}`) {
		t.Fatalf("align* body missing: %q", got)
	}
}

func TestValidateRichArticleLatexSourceRejectsDangerousInput(t *testing.T) {
	bad := []string{
		`\input{/etc/passwd}`,
		`\write18{curl https://example.org/x}`,
		`\begin{document}owned\end{document}`,
		`\includegraphics{/home/faline/.env}`,
		`^^5cinput{/etc/passwd}`,
		`\href{https://example.org}{x}`,
	}
	for _, in := range bad {
		if err := validateRichArticleLatexSource(in); err == nil {
			t.Fatalf("validateRichArticleLatexSource(%q) must reject dangerous input", in)
		}
	}
	if err := validateRichArticleLatexSource(`\[\text{Зевс} \xrightarrow{\text{Майя}} \text{Гермес}\]`); err != nil {
		t.Fatalf("safe latex source rejected: %v", err)
	}
}

func TestValidateRichArticleDiagramSourceRejectsExternalInputs(t *testing.T) {
	badMermaid := []string{
		"flowchart TD\nA-->B\nclick A href \"https://example.org\"",
		"flowchart TD\nA[<img src=file:///etc/passwd>]",
		"%%{init: {'securityLevel': 'loose'}}%%\nflowchart TD\nA-->B",
	}
	for _, in := range badMermaid {
		if err := validateRichArticleMermaidSource(in); err == nil {
			t.Fatalf("validateRichArticleMermaidSource(%q) must reject dangerous input", in)
		}
	}
	if err := validateRichArticleMermaidSource("flowchart TD\nA[Оленька] --> B[PNG]"); err != nil {
		t.Fatalf("safe mermaid source rejected: %v", err)
	}

	badGraphviz := []string{
		`digraph G { A [image="/etc/passwd"]; }`,
		`digraph G { A [URL="https://example.org"]; }`,
		`digraph G { imagepath="/home/faline"; }`,
	}
	for _, in := range badGraphviz {
		if err := validateRichArticleGraphvizSource(in); err == nil {
			t.Fatalf("validateRichArticleGraphvizSource(%q) must reject dangerous input", in)
		}
	}
	if err := validateRichArticleGraphvizSource(`digraph G { A[label="Оленька"]; A -> B; }`); err != nil {
		t.Fatalf("safe graphviz source rejected: %v", err)
	}
}

func TestRenderUnsupportedRichArticleMath_RendersUnsupportedDollarBlock(t *testing.T) {
	requireSystemLatexRenderer(t)
	in := "До\n\n$$\\begin{array}{cc}a&b\\\\c&d\\end{array}$$\n\nПосле"
	got, attachments, err := renderUnsupportedRichArticleMath(in)
	if err != nil {
		t.Fatalf("render unsupported math: %v", err)
	}
	if len(attachments) != 1 {
		t.Fatalf("expected 1 rendered formula attachment, got %d", len(attachments))
	}
	if !strings.Contains(got, richArticleRenderedFormulaMarkdown(attachments[0].ID)) {
		t.Fatalf("formula placeholder missing: %q", got)
	}
}

func TestRenderUnsupportedRichArticleMath_RendersChemistryAlignBlock(t *testing.T) {
	requireSystemLatexRenderer(t)
	in := "До\n\n\\[\n\\begin{align*}\n&\\text{L-триптофан} \\xrightarrow{\\text{триптофан-гидроксилаза}} \\text{5-гидрокситриптофан} \\\\\n&\\text{C}_{11}\\text{H}_{12}\\text{N}_2\\text{O}_2 \\xrightarrow{\\text{+O}_2,~\\text{tetrahydrobiopterin}} \\text{C}_{11}\\text{H}_{12}\\text{N}_2\\text{O}_3 \\\\\n\\\\\n&\\text{5-гидрокситриптофан} \\xrightarrow{\\text{декарбоксилаза}} \\text{серотонин} \\\\\n&\\text{C}_{11}\\text{H}_{12}\\text{N}_2\\text{O}_3 \\xrightarrow{\\text{-CO}_2} \\text{C}_{10}\\text{H}_{12}\\text{N}_2\\text{O}\n\\end{align*}\n\\]\n\nПосле"
	got, attachments, err := renderUnsupportedRichArticleMath(in)
	if err != nil {
		t.Fatalf("render chemistry align math: %v", err)
	}
	if len(attachments) != 1 {
		t.Fatalf("expected 1 rendered formula attachment, got %d", len(attachments))
	}
	if !isPNGBytes(attachments[0].PNG) {
		t.Fatalf("rendered chemistry attachment is not png")
	}
	if !strings.Contains(got, richArticleRenderedFormulaMarkdown(attachments[0].ID)) {
		t.Fatalf("formula placeholder missing: %q", got)
	}
}

func TestRenderUnsupportedRichArticleMath_RendersXRightArrowBlock(t *testing.T) {
	requireSystemLatexRenderer(t)
	in := "До\n\n\\[\\text{Уран} \\xrightarrow{\\text{+Гея}} \\text{Кронос}\\]\n\nПосле"
	got, attachments, err := renderUnsupportedRichArticleMath(in)
	if err != nil {
		t.Fatalf("render xrightarrow math: %v", err)
	}
	if len(attachments) != 1 {
		t.Fatalf("expected 1 rendered formula attachment, got %d", len(attachments))
	}
	if !isPNGBytes(attachments[0].PNG) {
		t.Fatalf("rendered xrightarrow attachment is not png")
	}
	if !strings.Contains(got, richArticleRenderedFormulaMarkdown(attachments[0].ID)) {
		t.Fatalf("formula placeholder missing: %q", got)
	}
}

func TestRenderUnsupportedRichArticleMath_RendersTikZTree(t *testing.T) {
	requireSystemLatexRenderer(t)
	in := "До\n\n\\[\\begin{tikzpicture}\\Tree [.Геномика [.Эволюция ] ]\\end{tikzpicture}\\]\n\nПосле"
	got, attachments, err := renderUnsupportedRichArticleMath(in)
	if err != nil {
		t.Fatalf("render tikz tree: %v", err)
	}
	if len(attachments) != 1 {
		t.Fatalf("expected 1 rendered formula attachment, got %d", len(attachments))
	}
	if !isPNGBytes(attachments[0].PNG) {
		t.Fatalf("rendered tikz attachment is not png")
	}
	if !strings.Contains(got, richArticleRenderedFormulaMarkdown(attachments[0].ID)) {
		t.Fatalf("formula placeholder missing: %q", got)
	}
}

func TestRenderUnsupportedRichArticleMath_RendersPGFPlotsAxis(t *testing.T) {
	requireSystemLatexRenderer(t)
	in := "До\n\n\\[\n\\begin{tikzpicture}\n\\begin{axis}[axis lines=middle,xlabel=$x$,ylabel={$y$}]\n\\addplot[domain=-2:2,samples=100,color=blue]{x^2};\n\\end{axis}\n\\end{tikzpicture}\n\\]\n\nПосле"
	got, attachments, err := renderUnsupportedRichArticleMath(in)
	if err != nil {
		t.Fatalf("render pgfplots axis: %v", err)
	}
	if len(attachments) != 1 {
		t.Fatalf("expected 1 rendered pgfplots attachment, got %d", len(attachments))
	}
	if !isPNGBytes(attachments[0].PNG) {
		t.Fatalf("rendered pgfplots attachment is not png")
	}
	if !strings.Contains(got, richArticleRenderedFormulaMarkdown(attachments[0].ID)) {
		t.Fatalf("formula placeholder missing: %q", got)
	}
}

func TestRenderUnsupportedRichArticleMath_RendersForestDollarBlock(t *testing.T) {
	requireSystemLatexRenderer(t)
	in := "До\n\n$$\\begin{forest}[Зевс[Афина][Аполлон][Артемида]]\\end{forest}$$\n\nПосле"
	got, attachments, err := renderUnsupportedRichArticleMath(in)
	if err != nil {
		t.Fatalf("render forest tree: %v", err)
	}
	if len(attachments) != 1 {
		t.Fatalf("expected 1 rendered formula attachment, got %d", len(attachments))
	}
	if !isPNGBytes(attachments[0].PNG) {
		t.Fatalf("rendered forest attachment is not png")
	}
	if !strings.Contains(got, richArticleRenderedFormulaMarkdown(attachments[0].ID)) {
		t.Fatalf("formula placeholder missing: %q", got)
	}
}

func TestRenderRichArticleMediaBlocks_RendersMermaidBlock(t *testing.T) {
	requireSystemMermaidRenderer(t)
	in := "До\n\n```mermaid\nflowchart TD\n  A[Оленька] --> B{Диаграмма?}\n  B -->|да| C[PNG]\n```\n\nПосле"
	got, attachments, err := renderRichArticleMediaBlocks(in)
	if err != nil {
		t.Fatalf("render mermaid diagram: %v", err)
	}
	if len(attachments) != 1 {
		t.Fatalf("expected 1 rendered diagram attachment, got %d", len(attachments))
	}
	if !isPNGBytes(attachments[0].PNG) {
		t.Fatalf("rendered mermaid attachment is not png")
	}
	if !strings.Contains(got, richArticleRenderedFormulaMarkdown(attachments[0].ID)) {
		t.Fatalf("diagram placeholder missing: %q", got)
	}
	if strings.Contains(got, "```mermaid") {
		t.Fatalf("mermaid block must be replaced: %q", got)
	}
}

func TestRenderRichArticleMediaBlocks_RendersLatexFenceAsOneImage(t *testing.T) {
	requireSystemLatexRenderer(t)
	in := "До\n\n```latex\n\\[A \\xrightarrow{1} B\\]\n\n% многоступенчатый вариант\n\\[C \\xrightarrow{2} D\\]\n```\n\nПосле"
	got, attachments, err := renderRichArticleMediaBlocks(in)
	if err != nil {
		t.Fatalf("render latex fence: %v", err)
	}
	if len(attachments) != 1 {
		t.Fatalf("expected 1 rendered latex fence attachment, got %d", len(attachments))
	}
	if !isPNGBytes(attachments[0].PNG) {
		t.Fatalf("rendered latex fence attachment is not png")
	}
	if !strings.Contains(got, richArticleRenderedFormulaMarkdown(attachments[0].ID)) {
		t.Fatalf("latex fence placeholder missing: %q", got)
	}
	if strings.Contains(got, "```latex") || strings.Contains(got, `\[A`) {
		t.Fatalf("latex fence must be replaced as a whole, not patched inside code: %q", got)
	}
}

func TestRenderRichArticleMediaBlocks_DoesNotPatchMathInsidePlainCodeFence(t *testing.T) {
	requireSystemLatexRenderer(t)
	in := "До\n\n```text\n\\[E = mc^2\\]\n```\n\nПосле"
	got, attachments, err := renderRichArticleMediaBlocks(in)
	if err != nil {
		t.Fatalf("render rich media: %v", err)
	}
	if len(attachments) != 0 {
		t.Fatalf("plain code fence must not render attachments, got %d", len(attachments))
	}
	if got != in {
		t.Fatalf("plain code fence changed:\n got %q\nwant %q", got, in)
	}
}

func TestSanitizeRichArticlePhotoLinks_AllowsRenderedAttachment(t *testing.T) {
	attachments := []richArticleRenderedAttachment{
		{ID: "formula_safe_1", PNG: []byte{1}},
	}
	in := "До\n\n![](tg://photo?id=formula_safe_1)\n\nПосле"
	got := sanitizeRichArticlePhotoLinks(in, attachments)
	if got != in {
		t.Fatalf("rendered attachment link must stay unchanged:\n got %q\nwant %q", got, in)
	}
}

func TestSanitizeRichArticlePhotoLinks_EscapesInjectedPhotoLink(t *testing.T) {
	attachments := []richArticleRenderedAttachment{
		{ID: "formula_safe_1", PNG: []byte{1}},
	}
	in := "До\n\n![](tg://photo?id=formula_safe_1)\n\n![](tg://photo?id=attacker)\n\ntg://photo?id=attacker2"
	got := sanitizeRichArticlePhotoLinks(in, attachments)
	if !strings.Contains(got, "![](tg://photo?id=formula_safe_1)") {
		t.Fatalf("allowed rendered link missing: %q", got)
	}
	if strings.Contains(got, "\n\n![](tg://photo?id=attacker)") {
		t.Fatalf("injected markdown photo link must not stay active: %q", got)
	}
	if strings.Contains(got, "\n\ntg://photo?id=attacker2") {
		t.Fatalf("injected plain photo url must not stay active: %q", got)
	}
	if !strings.Contains(got, "`![](tg://photo?id=attacker)`") || !strings.Contains(got, "`tg://photo?id=attacker2`") {
		t.Fatalf("injected photo links must be preserved as literal text: %q", got)
	}
}

func TestSanitizeRichArticlePhotoLinks_PreservesCodeFenceLiteral(t *testing.T) {
	in := "До\n\n```text\n![](tg://photo?id=attacker)\ntg://photo?id=attacker2\n```\n\nПосле"
	got := sanitizeRichArticlePhotoLinks(in, nil)
	if got != in {
		t.Fatalf("photo-like text inside code fence must stay literal:\n got %q\nwant %q", got, in)
	}
}

func TestRenderRichArticleMediaBlocks_RendersGraphvizBlock(t *testing.T) {
	requireSystemGraphvizRenderer(t)
	in := "До\n\n```dot\ndigraph G {\n  rankdir=LR;\n  A[label=\"Оленька\"];\n  B[label=\"Graphviz\"];\n  A -> B;\n}\n```\n\nПосле"
	got, attachments, err := renderRichArticleMediaBlocks(in)
	if err != nil {
		t.Fatalf("render graphviz diagram: %v", err)
	}
	if len(attachments) != 1 {
		t.Fatalf("expected 1 rendered graphviz attachment, got %d", len(attachments))
	}
	if !isPNGBytes(attachments[0].PNG) {
		t.Fatalf("rendered graphviz attachment is not png")
	}
	if !strings.Contains(got, richArticleRenderedFormulaMarkdown(attachments[0].ID)) {
		t.Fatalf("graphviz placeholder missing: %q", got)
	}
	if strings.Contains(got, "```dot") {
		t.Fatalf("dot block must be replaced: %q", got)
	}
}

func TestRenderRichArticleMediaBlocks_RendersFENBoard(t *testing.T) {
	in := "Позиция:\n\n```fen\nrnbqkbnr/pppppppp/8/8/8/8/PPPPPPPP/RNBQKBNR w KQkq - 0 1\n```\n\nХод белых."
	got, attachments, err := renderRichArticleMediaBlocks(in)
	if err != nil {
		t.Fatalf("render FEN board: %v", err)
	}
	if len(attachments) != 1 {
		t.Fatalf("expected 1 rendered FEN attachment, got %d", len(attachments))
	}
	if !isPNGBytes(attachments[0].PNG) {
		t.Fatalf("rendered FEN attachment is not png")
	}
	if !strings.Contains(got, richArticleRenderedFormulaMarkdown(attachments[0].ID)) {
		t.Fatalf("FEN board placeholder missing: %q", got)
	}
	if strings.Contains(got, "```fen") || strings.Contains(got, "rnbqkbnr") {
		t.Fatalf("FEN block must be replaced: %q", got)
	}
}

func TestRenderRichArticleMediaBlocks_RendersNumberedFENLines(t *testing.T) {
	in := "Позиции:\n\n1.   rnbqkbnr/pppppppp/8/8/8/8/PPPPPPPP/RNBQKBNR w KQkq - 0 1\n2.   rnbqkb1r/pppppppp/5n2/8/4P3/8/PPPP1PPP/RNBQKBNR w KQkq - 1 2\n\nТекст после."
	got, attachments, err := renderRichArticleMediaBlocks(in)
	if err != nil {
		t.Fatalf("render numbered FEN lines: %v", err)
	}
	if len(attachments) != 2 {
		t.Fatalf("expected 2 rendered FEN attachments, got %d", len(attachments))
	}
	for _, attachment := range attachments {
		if !isPNGBytes(attachment.PNG) {
			t.Fatalf("rendered numbered FEN attachment is not png")
		}
		if !strings.Contains(got, richArticleRenderedFormulaMarkdown(attachment.ID)) {
			t.Fatalf("numbered FEN board placeholder missing: %q", got)
		}
	}
	if strings.Contains(got, "rnbqkbnr") || strings.Contains(got, "rnbqkb1r") {
		t.Fatalf("numbered FEN lines must be replaced: %q", got)
	}
	if !strings.Contains(got, "1.") || !strings.Contains(got, "2.") {
		t.Fatalf("numbered FEN prefixes must be preserved: %q", got)
	}
}

func TestParseRichArticleFENRejectsInvalidBoard(t *testing.T) {
	for _, in := range []string{
		"8/8/8/8/8/8/8 w - - 0 1",
		"9/8/8/8/8/8/8/8 w - - 0 1",
		"8/8/8/8/8/8/8/7X w - - 0 1",
		"8/8/8/8/8/8/8/8 x - - 0 1",
		"8/8/8/8/8/8/8/8 w KQkqK - 0 1",
	} {
		if _, err := parseRichArticleFEN(in); err == nil {
			t.Fatalf("parseRichArticleFEN(%q) must fail", in)
		}
	}
	if _, err := parseRichArticleFEN("8/8/8/8/8/8/8/8"); err != nil {
		t.Fatalf("board-only FEN must be accepted: %v", err)
	}
}

func TestChessArticleStateAndMove(t *testing.T) {
	attachment := richArticleRenderedAttachment{ID: chessArticleImageID(chessInitialFEN, false, "старт", "@alice")}
	markdown := renderChessArticleMarkdown(attachment, chessInitialFEN, "старт", "@alice")
	state, err := parseChessArticleState(markdown)
	if err != nil {
		t.Fatalf("parse chess article state: %v\n%s", err, markdown)
	}
	if state.FEN != chessInitialFEN {
		t.Fatalf("unexpected parsed FEN:\n got %q\nwant %q", state.FEN, chessInitialFEN)
	}
	if state.WhiteBottom {
		t.Fatalf("expected white orientation at top")
	}
	if strings.Contains(markdown, "FEN:") || strings.Contains(markdown, "Белые:") || strings.Contains(markdown, "Ход:") {
		t.Fatalf("chess article has redundant labels: %q", markdown)
	}
	if !strings.Contains(markdown, ": старт (@alice)") {
		t.Fatalf("chess article move label is unexpected: %q", markdown)
	}
	move, ok := parseChessMoveText("e2:e4")
	if !ok {
		t.Fatalf("expected move to parse")
	}
	pos, err := parseChessPositionFEN(state.FEN)
	if err != nil {
		t.Fatalf("parse initial FEN: %v", err)
	}
	next, err := applyChessMove(pos, move)
	if err != nil {
		t.Fatalf("apply e2-e4: %v", err)
	}
	want := "rnbqkbnr/pppppppp/8/8/4P3/8/PPPP1PPP/RNBQKBNR b KQkq e3 0 1"
	if got := next.FEN(); got != want {
		t.Fatalf("unexpected FEN after e2-e4:\n got %q\nwant %q", got, want)
	}
}

func TestChessArticleStateFindsInlineFEN(t *testing.T) {
	text := "карточка rnbqkbnr/pppppppp/8/8/8/8/PPPPPPPP/RNBQKBNR w KQkq - 0 1 Ход: старт (@alice)"
	state, err := parseChessArticleState(text)
	if err != nil {
		t.Fatalf("parse inline chess article state: %v", err)
	}
	if state.FEN != chessInitialFEN {
		t.Fatalf("unexpected inline FEN:\n got %q\nwant %q", state.FEN, chessInitialFEN)
	}
	if !state.WhiteBottom {
		t.Fatalf("expected white orientation at bottom")
	}
}

func TestChessUserLabelPrefersDisplayName(t *testing.T) {
	got := chessUserLabel(&tgbotapi.User{
		ID:        100,
		FirstName: "Ann",
		LastName:  "Vanderberg (она/её)",
		UserName:  "ann_handle",
	})
	if got != "Ann Vanderberg (она/её)" {
		t.Fatalf("expected display name instead of username, got %q", got)
	}
}

func TestApplyChessMoveRussianKnightNotation(t *testing.T) {
	move, ok := parseChessMoveText("Кf3")
	if !ok {
		t.Fatalf("expected Russian knight notation to parse")
	}
	pos, err := parseChessPositionFEN(chessInitialFEN)
	if err != nil {
		t.Fatalf("parse initial FEN: %v", err)
	}
	resolved, err := resolveChessMove(pos, move)
	if err != nil {
		t.Fatalf("resolve Кf3: %v", err)
	}
	if resolved.From != "g1" || resolved.To != "f3" {
		t.Fatalf("unexpected resolved move: %#v", resolved)
	}
	next, err := applyChessMove(pos, move)
	if err != nil {
		t.Fatalf("apply Кf3: %v", err)
	}
	want := "rnbqkbnr/pppppppp/8/8/8/5N2/PPPPPPPP/RNBQKB1R b KQkq - 1 1"
	if got := next.FEN(); got != want {
		t.Fatalf("unexpected FEN after Кf3:\n got %q\nwant %q", got, want)
	}
}

func TestApplyChessMoveEnglishKnightNotation(t *testing.T) {
	move, ok := parseChessMoveText("Nf3")
	if !ok {
		t.Fatalf("expected English knight notation to parse")
	}
	pos, err := parseChessPositionFEN(chessInitialFEN)
	if err != nil {
		t.Fatalf("parse initial FEN: %v", err)
	}
	next, err := applyChessMove(pos, move)
	if err != nil {
		t.Fatalf("apply Nf3: %v", err)
	}
	want := "rnbqkbnr/pppppppp/8/8/8/5N2/PPPPPPPP/RNBQKB1R b KQkq - 1 1"
	if got := next.FEN(); got != want {
		t.Fatalf("unexpected FEN after Nf3:\n got %q\nwant %q", got, want)
	}
}

func TestApplyChessMoveKingsideCastlingNotation(t *testing.T) {
	move, ok := parseChessMoveText("0-0")
	if !ok {
		t.Fatalf("expected kingside castling notation to parse")
	}
	pos, err := parseChessPositionFEN("r3k2r/8/8/8/8/8/8/R3K2R w KQkq - 0 1")
	if err != nil {
		t.Fatalf("parse castling FEN: %v", err)
	}
	resolved, err := resolveChessMove(pos, move)
	if err != nil {
		t.Fatalf("resolve 0-0: %v", err)
	}
	if resolved.From != "e1" || resolved.To != "g1" {
		t.Fatalf("unexpected resolved castling move: %#v", resolved)
	}
	next, err := applyChessMove(pos, move)
	if err != nil {
		t.Fatalf("apply 0-0: %v", err)
	}
	want := "r3k2r/8/8/8/8/8/8/R4RK1 b kq - 1 1"
	if got := next.FEN(); got != want {
		t.Fatalf("unexpected FEN after 0-0:\n got %q\nwant %q", got, want)
	}
}

func TestApplyChessMoveQueensideCastlingNotation(t *testing.T) {
	move, ok := parseChessMoveText("O-O-O")
	if !ok {
		t.Fatalf("expected queenside castling notation to parse")
	}
	pos, err := parseChessPositionFEN("r3k2r/8/8/8/8/8/8/R3K2R b KQkq - 0 1")
	if err != nil {
		t.Fatalf("parse castling FEN: %v", err)
	}
	next, err := applyChessMove(pos, move)
	if err != nil {
		t.Fatalf("apply O-O-O: %v", err)
	}
	want := "2kr3r/8/8/8/8/8/8/R3K2R w KQ - 1 2"
	if got := next.FEN(); got != want {
		t.Fatalf("unexpected FEN after O-O-O:\n got %q\nwant %q", got, want)
	}
}

func TestApplyChessMoveRejectsWrongSide(t *testing.T) {
	pos, err := parseChessPositionFEN(chessInitialFEN)
	if err != nil {
		t.Fatalf("parse initial FEN: %v", err)
	}
	if _, err := applyChessMove(pos, chessMove{From: "e7", To: "e5"}); err == nil {
		t.Fatalf("black move on white turn must fail")
	}
}

func TestRenderRichArticleMediaBlocks_RendersMathAndDiagram(t *testing.T) {
	requireSystemLatexRenderer(t)
	requireSystemGraphvizRenderer(t)
	in := "Формула:\n\n\\[E = mc^2\\]\n\nДиаграмма:\n\n```graphviz\ndigraph G { A -> B }\n```"
	got, attachments, err := renderRichArticleMediaBlocks(in)
	if err != nil {
		t.Fatalf("render mixed rich media: %v", err)
	}
	if len(attachments) != 2 {
		t.Fatalf("expected 2 rendered attachments, got %d", len(attachments))
	}
	for _, attachment := range attachments {
		if !isPNGBytes(attachment.PNG) {
			t.Fatalf("attachment %s is not png", attachment.ID)
		}
		if !strings.Contains(got, richArticleRenderedFormulaMarkdown(attachment.ID)) {
			t.Fatalf("placeholder for %s missing: %q", attachment.ID, got)
		}
	}
}

func TestRichArticleFormulaDimensionsNeedDocument(t *testing.T) {
	tests := []struct {
		name   string
		width  int
		height int
		bytes  int
		want   bool
	}{
		{name: "ordinary inline formula", width: 900, height: 180, bytes: 40_000, want: false},
		{name: "wide genealogy tree", width: 8457, height: 1386, bytes: 319_449, want: true},
		{name: "too tall diagram", width: 900, height: 5000, bytes: 120_000, want: true},
		{name: "telegram photo dimension sum", width: 7000, height: 4000, bytes: 300_000, want: true},
		{name: "telegram photo bytes", width: 1200, height: 900, bytes: telegramPhotoMaxBytes + 1, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := richArticleFormulaDimensionsNeedDocument(tt.width, tt.height, tt.bytes)
			if got != tt.want {
				t.Fatalf("richArticleFormulaDimensionsNeedDocument(%d, %d, %d) = %v, want %v", tt.width, tt.height, tt.bytes, got, tt.want)
			}
		})
	}
}

func TestNormalizeRichArticleInlineImagePNG_PadsToArticleWidth(t *testing.T) {
	t.Setenv("TRIGGER_BOT_RICH_ARTICLE_IMAGE_WIDTH", "573")
	in := testPNG(t, 120, 32, color.RGBA{R: 240, G: 240, B: 240, A: 255})

	out, err := normalizeRichArticleInlineImagePNG(in)
	if err != nil {
		t.Fatalf("normalize rich article image: %v", err)
	}

	cfg, err := png.DecodeConfig(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("decode normalized png config: %v", err)
	}
	if cfg.Width != 573 || cfg.Height != 32 {
		t.Fatalf("normalized image size = %dx%d, want 573x32", cfg.Width, cfg.Height)
	}
}

func TestNormalizeRichArticleInlineImagePNG_ScalesWideImageToArticleWidth(t *testing.T) {
	t.Setenv("TRIGGER_BOT_RICH_ARTICLE_IMAGE_WIDTH", "573")
	in := testPNG(t, 1146, 200, color.RGBA{R: 255, G: 255, B: 255, A: 255})

	out, err := normalizeRichArticleInlineImagePNG(in)
	if err != nil {
		t.Fatalf("normalize rich article image: %v", err)
	}

	cfg, err := png.DecodeConfig(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("decode normalized png config: %v", err)
	}
	if cfg.Width != 573 || cfg.Height != 100 {
		t.Fatalf("normalized wide image size = %dx%d, want 573x100", cfg.Width, cfg.Height)
	}
}

func TestRenderUnsupportedRichArticleMath_UsesUniqueMediaIDs(t *testing.T) {
	requireSystemLatexRenderer(t)
	in := "До\n\n\\[E = mc^2\\]\n\nПосле"
	_, first, err := renderUnsupportedRichArticleMath(in)
	if err != nil {
		t.Fatalf("first render unsupported math: %v", err)
	}
	_, second, err := renderUnsupportedRichArticleMath(in)
	if err != nil {
		t.Fatalf("second render unsupported math: %v", err)
	}
	if len(first) != 1 || len(second) != 1 {
		t.Fatalf("expected one attachment for each render, got %d and %d", len(first), len(second))
	}
	if first[0].ID == second[0].ID {
		t.Fatalf("formula media ids must be unique across sends, got %q", first[0].ID)
	}
}

func TestRenderUnsupportedRichArticleMath_KeepsSimpleDollarBlockNative(t *testing.T) {
	in := "До\n\n$$E = mc^2$$\n\nПосле"
	got, attachments, err := renderUnsupportedRichArticleMath(in)
	if err != nil {
		t.Fatalf("render unsupported math: %v", err)
	}
	if len(attachments) != 0 {
		t.Fatalf("simple formula should stay native, got %d attachments", len(attachments))
	}
	if got != in {
		t.Fatalf("simple formula changed: %q", got)
	}
}

func TestRenderUnsupportedRichArticleMath_KeepsRawWhenSystemLatexMissing(t *testing.T) {
	t.Setenv("TRIGGER_BOT_PDFLATEX_BIN", "/definitely/missing/pdflatex")
	t.Setenv("TRIGGER_BOT_PDFTOCAIRO_BIN", "/definitely/missing/pdftocairo")
	in := "До\n\n\\[E = mc^2\\]\n\nПосле"
	got, attachments, err := renderUnsupportedRichArticleMath(in)
	if err != nil {
		t.Fatalf("missing system renderer must not fail rich article rendering: %v", err)
	}
	if len(attachments) != 0 {
		t.Fatalf("missing system renderer must not attach formulas, got %d", len(attachments))
	}
	if got != in {
		t.Fatalf("missing system renderer must keep raw latex: %q", got)
	}
}

func testPNG(t *testing.T, width, height int, fill color.Color) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.Set(x, y, fill)
		}
	}
	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil {
		t.Fatalf("encode test png: %v", err)
	}
	return out.Bytes()
}

func requireSystemLatexRenderer(t *testing.T) {
	t.Helper()
	if !systemLatexRendererAvailable() {
		t.Skip("system LaTeX renderer is not installed")
	}
}

func requireSystemMermaidRenderer(t *testing.T) {
	t.Helper()
	if !systemMermaidRendererAvailable() {
		t.Skip("system Mermaid renderer is not installed")
	}
}

func requireSystemGraphvizRenderer(t *testing.T) {
	t.Helper()
	if !systemGraphvizRendererAvailable() {
		t.Skip("system Graphviz renderer is not installed")
	}
}

func TestMarkdownToTelegramHTMLLite_ItalicDoesNotSpanLines(t *testing.T) {
	in := "* пункт списка\nстрока с *курсивом* и **жирным**\nещё строка"
	got := markdownToTelegramHTMLLite(in)
	if strings.Contains(got, "<i> пункт списка") {
		t.Fatalf("list marker must not become italic: %q", got)
	}
	if !strings.Contains(got, "<i>курсивом</i>") {
		t.Fatalf("italic conversion missing: %q", got)
	}
	if !strings.Contains(got, "<b>жирным</b>") {
		t.Fatalf("bold conversion missing: %q", got)
	}
	if strings.Count(got, "<i>") != strings.Count(got, "</i>") {
		t.Fatalf("unbalanced italic tags: %q", got)
	}
}

func TestMarkdownToTelegramHTMLLite_NestedListHasNoListTags(t *testing.T) {
	in := "1. пункт\n   - подпункт A\n   - подпункт B\n2. второй пункт"
	got := markdownToTelegramHTMLLite(in)
	if strings.Contains(strings.ToLower(got), "<li") || strings.Contains(strings.ToLower(got), "<ul") || strings.Contains(strings.ToLower(got), "<ol") {
		t.Fatalf("telegram html must not contain list tags: %q", got)
	}
	if !strings.Contains(got, "• ") {
		t.Fatalf("expected bullet conversion for list items: %q", got)
	}
}

func TestMarkdownDividerTGEmojiFromEnv(t *testing.T) {
	const key = "GPT_MARKDOWN_DIVIDER_EMOJI"
	old, had := os.LookupEnv(key)
	t.Cleanup(func() {
		if had {
			_ = os.Setenv(key, old)
		} else {
			_ = os.Unsetenv(key)
		}
	})

	_ = os.Unsetenv(key)
	if got := markdownDividerTGEmoji(); got != defaultMarkdownDividerTGEmoji {
		t.Fatalf("unexpected default divider emoji: %q", got)
	}

	custom := `<tg-emoji emoji-id="1">~</tg-emoji>`
	_ = os.Setenv(key, custom)
	if got := markdownDividerTGEmoji(); got != custom {
		t.Fatalf("unexpected env divider emoji: %q", got)
	}
}

func TestExtractLeadingReactionCandidateCustomEmoji(t *testing.T) {
	in := `<tg-emoji emoji-id="123456789">🦌</tg-emoji> привет миру`
	c, next, ok := extractLeadingReactionCandidate(in)
	if !ok {
		t.Fatalf("expected custom emoji reaction candidate")
	}
	if c.CustomEmojiID != "123456789" {
		t.Fatalf("unexpected custom emoji id: %q", c.CustomEmojiID)
	}
	if c.Emoji != "🦌" {
		t.Fatalf("unexpected fallback emoji: %q", c.Emoji)
	}
	if next != "привет миру" {
		t.Fatalf("unexpected next text: %q", next)
	}
}

func TestExtractLeadingReactionCandidate_PrefersUnicodeOutsideCustomTag(t *testing.T) {
	in := `<tg-emoji emoji-id="123456789">🦌</tg-emoji> привет 😎 миру`
	c, next, ok := extractLeadingReactionCandidate(in)
	if !ok {
		t.Fatalf("expected unicode emoji reaction candidate")
	}
	if c.CustomEmojiID != "" {
		t.Fatalf("expected no custom id, got=%q", c.CustomEmojiID)
	}
	if c.Emoji != "😎" {
		t.Fatalf("unexpected emoji: %q", c.Emoji)
	}
	if strings.Contains(next, "😎") {
		t.Fatalf("expected unicode emoji to be consumed, next=%q", next)
	}
}

func TestExtractLeadingReactionCandidateUnicodeEmoji(t *testing.T) {
	in := "🙂 привет"
	c, next, ok := extractLeadingReactionCandidate(in)
	if !ok {
		t.Fatalf("expected unicode emoji reaction candidate")
	}
	if c.CustomEmojiID != "" {
		t.Fatalf("expected no custom id, got=%q", c.CustomEmojiID)
	}
	if c.Emoji != "🙂" {
		t.Fatalf("unexpected emoji: %q", c.Emoji)
	}
	if next != "привет" {
		t.Fatalf("unexpected next text: %q", next)
	}
}

func TestExtractLeadingReactionCandidateFindsFirstEmojiInMessage(t *testing.T) {
	in := "Текст до реакции 😌 и дальше"
	c, next, ok := extractLeadingReactionCandidate(in)
	if !ok {
		t.Fatalf("expected unicode emoji reaction candidate in middle of text")
	}
	if c.Emoji != "😌" {
		t.Fatalf("unexpected emoji: %q", c.Emoji)
	}
	if next != "Текст до реакции и дальше" {
		t.Fatalf("unexpected next text: %q", next)
	}
}

func TestExtractLeadingReactionCandidateNoEmoji(t *testing.T) {
	in := "* пункт списка"
	_, _, ok := extractLeadingReactionCandidate(in)
	if ok {
		t.Fatalf("list marker must not be treated as reaction emoji")
	}
}

func TestHasIgnoredAutoReplyLeadingTokenUnicodeEmoji(t *testing.T) {
	msg := &tgbotapi.Message{Text: "  🙂 привет"}
	if !hasIgnoredAutoReplyLeadingToken(msg) {
		t.Fatalf("leading unicode emoji must be ignored")
	}
}

func TestHasIgnoredAutoReplyLeadingTokenCustomEmojiEntity(t *testing.T) {
	msg := &tgbotapi.Message{
		Text: "  x привет",
		Entities: []tgbotapi.MessageEntity{
			{Type: "custom_emoji", Offset: 2, Length: 1, CustomEmojiID: "123"},
		},
	}
	if !hasIgnoredAutoReplyLeadingToken(msg) {
		t.Fatalf("leading custom emoji entity must be ignored")
	}
}

func TestHasIgnoredAutoReplyLeadingTokenTGEmojiTag(t *testing.T) {
	msg := &tgbotapi.Message{Text: `<tg-emoji emoji-id="123">🦌</tg-emoji> привет`}
	if !hasIgnoredAutoReplyLeadingToken(msg) {
		t.Fatalf("leading tg-emoji tag must be ignored")
	}
}

func TestHasIgnoredAutoReplyLeadingTokenMiddleEmojiAllowed(t *testing.T) {
	msg := &tgbotapi.Message{Text: "привет 🙂"}
	if hasIgnoredAutoReplyLeadingToken(msg) {
		t.Fatalf("middle emoji must not suppress reply trigger")
	}
}

func TestParseModerationCommandBan(t *testing.T) {
	raw := "!ban @user 2h\nспам ссылками"
	req, ok, err := parseModerationCommand(raw)
	if err != nil {
		t.Fatalf("parse err: %v", err)
	}
	if !ok {
		t.Fatalf("expected recognized command")
	}
	if req.Action != "ban" || req.Silent {
		t.Fatalf("unexpected action/silent: %#v", req)
	}
	if len(req.Targets) != 1 || req.Targets[0] != "@user" {
		t.Fatalf("unexpected targets: %#v", req.Targets)
	}
	if req.Duration != 2*time.Hour || req.DurationRaw != "2h" {
		t.Fatalf("unexpected duration: %s raw=%q", req.Duration, req.DurationRaw)
	}
	if req.Reason != "спам ссылками" {
		t.Fatalf("unexpected reason: %q", req.Reason)
	}
}

func TestParseModerationCommandBanSlashAndMention(t *testing.T) {
	raw := "/ban@olenyam_bot @user 6d"
	req, ok, err := parseModerationCommand(raw)
	if err != nil {
		t.Fatalf("parse err: %v", err)
	}
	if !ok {
		t.Fatalf("expected recognized slash command")
	}
	if req.Action != "ban" || req.Silent {
		t.Fatalf("unexpected action/silent: %#v", req)
	}
	if len(req.Targets) != 1 || req.Targets[0] != "@user" {
		t.Fatalf("unexpected targets: %#v", req.Targets)
	}
	if req.Duration != 6*24*time.Hour || req.DurationRaw != "6d" {
		t.Fatalf("unexpected duration: %s raw=%q", req.Duration, req.DurationRaw)
	}
}

func TestParseModerationCommandMuteDurationBeforeTarget(t *testing.T) {
	req, ok, err := parseModerationCommand("!mute 6m @user")
	if err != nil {
		t.Fatalf("parse err: %v", err)
	}
	if !ok || req.Action != "mute" {
		t.Fatalf("unexpected parse: ok=%v req=%#v", ok, req)
	}
	if req.Duration != 6*time.Minute || req.DurationRaw != "6m" {
		t.Fatalf("unexpected duration: %s raw=%q", req.Duration, req.DurationRaw)
	}
	if len(req.Targets) != 1 || req.Targets[0] != "@user" {
		t.Fatalf("unexpected targets: %#v", req.Targets)
	}
}

func TestParseModerationCommandUnmuteTargets(t *testing.T) {
	req, ok, err := parseModerationCommand("!unmute @username")
	if err != nil {
		t.Fatalf("parse err: %v", err)
	}
	if !ok || req.Action != "unmute" {
		t.Fatalf("unexpected parse: ok=%v req=%#v", ok, req)
	}
	if len(req.Targets) != 1 || req.Targets[0] != "@username" {
		t.Fatalf("unexpected targets: %#v", req.Targets)
	}

	req, ok, err = parseModerationCommand("!unmute 79886464684")
	if err != nil {
		t.Fatalf("parse err (id): %v", err)
	}
	if !ok || req.Action != "unmute" {
		t.Fatalf("unexpected parse (id): ok=%v req=%#v", ok, req)
	}
	if len(req.Targets) != 1 || req.Targets[0] != "79886464684" {
		t.Fatalf("unexpected targets (id): %#v", req.Targets)
	}
}

func TestParseModerationCommandReadonlyAlias(t *testing.T) {
	req, ok, err := parseModerationCommand("!ro 30m")
	if err != nil {
		t.Fatalf("parse err: %v", err)
	}
	if !ok || req.Action != "readonly" {
		t.Fatalf("unexpected parse: ok=%v req=%#v", ok, req)
	}
	if req.Duration != 30*time.Minute || req.DurationRaw != "30m" {
		t.Fatalf("unexpected duration: %s raw=%q", req.Duration, req.DurationRaw)
	}
}

func TestParseModerationCommandUnknown(t *testing.T) {
	_, ok, err := parseModerationCommand("!notreal test")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if ok {
		t.Fatalf("unknown command should not be recognized")
	}
}

func TestParseModerationCommandReloadAdmins(t *testing.T) {
	req, ok, err := parseModerationCommand("!reload_admins")
	if err != nil {
		t.Fatalf("parse err: %v", err)
	}
	if !ok {
		t.Fatalf("expected recognized command")
	}
	if req.Action != "reload_admins" {
		t.Fatalf("unexpected action: %#v", req)
	}
	if len(req.Targets) != 0 || req.Duration != 0 || req.Reason != "" {
		t.Fatalf("unexpected fields for reload command: %#v", req)
	}
}

func TestHumanModerationDurationRU(t *testing.T) {
	cases := []struct {
		d    time.Duration
		raw  string
		want string
	}{
		{d: 6 * 24 * time.Hour, raw: "6d", want: "6 дней"},
		{d: 2 * time.Hour, raw: "2h", want: "2 часа"},
		{d: 1 * time.Hour, raw: "1h", want: "1 час"},
		{d: 30 * time.Minute, raw: "30m", want: "30 минут"},
		{d: 14 * 24 * time.Hour, raw: "2w", want: "2 недели"},
	}
	for _, tc := range cases {
		if got := humanModerationDurationRU(tc.d, tc.raw); got != tc.want {
			t.Fatalf("humanModerationDurationRU(%s, %q)=%q want=%q", tc.d, tc.raw, got, tc.want)
		}
	}
}
