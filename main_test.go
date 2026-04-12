package main

import (
	"strings"
	"sync"
	"testing"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
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

func TestBuildVKMusicQueryFromMessage(t *testing.T) {
	msg := &tgbotapi.Message{
		Chat: &tgbotapi.Chat{ID: -1001, Title: "Чат"},
		From: &tgbotapi.User{ID: 7, FirstName: "Аня", UserName: "anya"},
		Text: "найди песню",
	}

	gotDefault := buildVKMusicQueryFromMessage(templateContext{
		Msg: msg,
	}, "")
	if gotDefault != "найди песню" {
		t.Fatalf("default query mismatch: %q", gotDefault)
	}

	got := buildVKMusicQueryFromMessage(templateContext{
		Msg:           msg,
		CapturingText: "летов",
	}, "играй {{capturing_text}}")
	if got != "играй летов" {
		t.Fatalf("query mismatch: %q", got)
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

func TestParseIdleMinutes(t *testing.T) {
	if v, ok := parseIdleMinutes("120"); !ok || v != 120 {
		t.Fatalf("expected parsed 120, got v=%d ok=%v", v, ok)
	}
	if _, ok := parseIdleMinutes("0"); ok {
		t.Fatalf("zero must be invalid")
	}
	if _, ok := parseIdleMinutes("abc"); ok {
		t.Fatalf("non-number must be invalid")
	}
}

func TestChatIdleTrackerFlow(t *testing.T) {
	tr := newChatIdleTracker()
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

	got, after := selectIdleAutoReplyTrigger(nil, msg, items, func() bool { return false })
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
	d := newGPTPromptDebouncer(120 * time.Millisecond)
	if d == nil {
		t.Fatalf("debouncer is nil")
	}
	orig := runGPTPromptTask
	defer func() { runGPTPromptTask = orig }()

	var mu sync.Mutex
	calls := []int{}
	ch := make(chan struct{}, 4)
	runGPTPromptTask = func(task gptPromptTask) {
		mu.Lock()
		calls = append(calls, task.Msg.MessageID)
		mu.Unlock()
		ch <- struct{}{}
	}

	d.Schedule(1, gptPromptTask{Msg: &tgbotapi.Message{MessageID: 101}})
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
	d := newGPTPromptDebouncer(140 * time.Millisecond)
	if d == nil {
		t.Fatalf("debouncer is nil")
	}
	orig := runGPTPromptTask
	defer func() { runGPTPromptTask = orig }()

	var mu sync.Mutex
	calls := []int{}
	ch := make(chan struct{}, 8)
	runGPTPromptTask = func(task gptPromptTask) {
		mu.Lock()
		calls = append(calls, task.Msg.MessageID)
		mu.Unlock()
		ch <- struct{}{}
	}

	d.Schedule(1, gptPromptTask{Msg: &tgbotapi.Message{MessageID: 201}}) // immediate
	<-ch
	time.Sleep(30 * time.Millisecond)
	d.Schedule(1, gptPromptTask{Msg: &tgbotapi.Message{MessageID: 202}})
	time.Sleep(30 * time.Millisecond)
	d.Schedule(1, gptPromptTask{Msg: &tgbotapi.Message{MessageID: 203}}) // latest in window

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
