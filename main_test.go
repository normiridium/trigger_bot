package main

import (
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"trigger-admin-bot/internal/gpt"
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
		{in: "линк https://instagram.com/reel/ABCDEF/?igsh=123", want: "https://instagram.com/reel/ABCDEF/?igsh=123"},
		{in: "https://soundcloud.com/artist/track", want: "https://soundcloud.com/artist/track"},
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
	got := buildMediaAudioTitle("Track Name", "https://youtu.be/abc")
	if got != "Track Name" {
		t.Fatalf("unexpected media audio title: %q", got)
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

func TestMediaServiceEmoji(t *testing.T) {
	if got := mediaServiceEmoji("youtube", "video"); !strings.Contains(got, "5463206079913533096") {
		t.Fatalf("unexpected youtube video emoji: %q", got)
	}
	if got := mediaServiceEmoji("instagram", "video"); !strings.Contains(got, "5463238270693416950") {
		t.Fatalf("unexpected instagram video emoji: %q", got)
	}
	if got := mediaServiceEmoji("soundcloud", "audio"); !strings.Contains(got, "5359614685664523140") {
		t.Fatalf("unexpected soundcloud emoji: %q", got)
	}
}

func TestExtractImageFileID(t *testing.T) {
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
	mode, interactive := mediaModeAndInteractivity("soundcloud", true)
	if mode != "audio" || interactive {
		t.Fatalf("soundcloud must force audio/no interactive, got mode=%q interactive=%v", mode, interactive)
	}
	mode, interactive = mediaModeAndInteractivity("instagram", true)
	if mode != "auto" || interactive {
		t.Fatalf("instagram must force auto/no interactive, got mode=%q interactive=%v", mode, interactive)
	}
	mode, interactive = mediaModeAndInteractivity("youtube", true)
	if mode != "audio" || !interactive {
		t.Fatalf("youtube should keep interactive when enabled, got mode=%q interactive=%v", mode, interactive)
	}
	mode, interactive = mediaModeAndInteractivity("youtube", false)
	if mode != "audio" || interactive {
		t.Fatalf("youtube should keep interactive=false, got mode=%q interactive=%v", mode, interactive)
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
	if got := resolveGenderVariant("she", variants); got != "она" {
		t.Fatalf("female mismatch: %q", got)
	}
	if got := resolveGenderVariant("it", variants); got != "оно" {
		t.Fatalf("neuter mismatch: %q", got)
	}
	if got := resolveGenderVariant("они", variants); got != "они" {
		t.Fatalf("plural mismatch: %q", got)
	}
	if got := resolveGenderVariant("unknown", variants); got != "кто-то" {
		t.Fatalf("unknown mismatch: %q", got)
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

func TestReplaceTGEmojiTagsWithFallback(t *testing.T) {
	in := `Привет <tg-emoji emoji-id="1">🦌</tg-emoji> мир`
	got := replaceTGEmojiTagsWithFallback(in)
	if got != "Привет 🦌 мир" {
		t.Fatalf("unexpected fallback replace: %q", got)
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

func TestMarkdownToTelegramHTMLLite(t *testing.T) {
	in := "Код:\n```python\nprint('hi')\n```\nИ `x=1` и [сайт](https://example.com)"
	got := markdownToTelegramHTMLLite(in)
	if !strings.Contains(got, `<pre><code class="language-python">`) {
		t.Fatalf("fenced code not converted: %q", got)
	}
	if !strings.Contains(got, `<code>x=1</code>`) {
		t.Fatalf("inline code not converted: %q", got)
	}
	if !strings.Contains(got, `<a href="https://example.com">сайт</a>`) {
		t.Fatalf("link not converted: %q", got)
	}
}

func TestGPTDebouncerLeadingImmediate(t *testing.T) {
	var mu sync.Mutex
	calls := []int{}
	ch := make(chan struct{}, 4)
	d := gpt.NewDebouncer(120*time.Millisecond, func(task gpt.PromptTask) {
		mu.Lock()
		calls = append(calls, task.Msg.MessageID)
		mu.Unlock()
		ch <- struct{}{}
	})
	if d == nil {
		t.Fatalf("debouncer is nil")
	}

	d.Schedule(1, gpt.PromptTask{Msg: &tgbotapi.Message{MessageID: 101}})
	select {
	case <-ch:
	case <-time.After(40 * time.Millisecond):
		t.Fatalf("expected immediate leading call")
	}
	select {
	case <-ch:
		t.Fatalf("unexpected extra call")
	case <-time.After(150 * time.Millisecond):
	}
	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 1 || calls[0] != 101 {
		t.Fatalf("unexpected calls: %#v", calls)
	}
}

func TestGPTDebouncerTrailingLatest(t *testing.T) {
	var mu sync.Mutex
	calls := []int{}
	ch := make(chan struct{}, 8)
	d := gpt.NewDebouncer(140*time.Millisecond, func(task gpt.PromptTask) {
		mu.Lock()
		calls = append(calls, task.Msg.MessageID)
		mu.Unlock()
		ch <- struct{}{}
	})
	if d == nil {
		t.Fatalf("debouncer is nil")
	}

	d.Schedule(1, gpt.PromptTask{Msg: &tgbotapi.Message{MessageID: 201}}) // immediate
	<-ch
	time.Sleep(30 * time.Millisecond)
	d.Schedule(1, gpt.PromptTask{Msg: &tgbotapi.Message{MessageID: 202}})
	time.Sleep(30 * time.Millisecond)
	d.Schedule(1, gpt.PromptTask{Msg: &tgbotapi.Message{MessageID: 203}}) // latest in window

	select {
	case <-ch:
	case <-time.After(220 * time.Millisecond):
		t.Fatalf("expected trailing call")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 2 {
		t.Fatalf("expected 2 calls, got %#v", calls)
	}
	if calls[0] != 201 || calls[1] != 203 {
		t.Fatalf("expected [201 203], got %#v", calls)
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
