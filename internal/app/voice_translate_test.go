package app

import (
	"strings"
	"testing"
)

func TestVoiceTranslateMixFiltersUseModerateDefaults(t *testing.T) {
	dynamicFilter, staticFilter := voiceTranslateMixFilters()
	for _, want := range []string{
		"apad,asplit=2",
		"volume=1.2[a1mix]",
		"volume=0.92[a0base]",
		"threshold=0.06:ratio=3",
	} {
		if !strings.Contains(dynamicFilter, want) {
			t.Fatalf("dynamic filter does not contain %q: %s", want, dynamicFilter)
		}
	}
	for _, want := range []string{
		"volume=0.8[a0]",
		"apad,volume=1.2[a1]",
	} {
		if !strings.Contains(staticFilter, want) {
			t.Fatalf("static filter does not contain %q: %s", want, staticFilter)
		}
	}
	if strings.Contains(dynamicFilter, "normalize=") || strings.Contains(staticFilter, "normalize=") {
		t.Fatalf("filters should stay compatible with ffmpeg builds without amix normalize: %s / %s", dynamicFilter, staticFilter)
	}
}

func TestVoiceTranslateMixFiltersUseEnvOverrides(t *testing.T) {
	t.Setenv("VOICE_TRANSLATE_MIX_ORIGINAL_VOLUME", "0.9")
	t.Setenv("VOICE_TRANSLATE_MIX_TRANSLATED_VOLUME", "1.1")
	t.Setenv("VOICE_TRANSLATE_MIX_DUCK_THRESHOLD", "0.04")
	t.Setenv("VOICE_TRANSLATE_MIX_DUCK_RATIO", "3")
	t.Setenv("VOICE_TRANSLATE_MIX_STATIC_ORIGINAL_VOLUME", "0.8")
	t.Setenv("VOICE_TRANSLATE_MIX_STATIC_TRANSLATED_VOLUME", "1.05")
	dynamicFilter, staticFilter := voiceTranslateMixFilters()
	for _, want := range []string{
		"apad,asplit=2",
		"volume=1.1[a1mix]",
		"volume=0.9[a0base]",
		"threshold=0.04:ratio=3",
	} {
		if !strings.Contains(dynamicFilter, want) {
			t.Fatalf("dynamic filter does not contain override %q: %s", want, dynamicFilter)
		}
	}
	for _, want := range []string{
		"volume=0.8[a0]",
		"apad,volume=1.05[a1]",
	} {
		if !strings.Contains(staticFilter, want) {
			t.Fatalf("static filter does not contain override %q: %s", want, staticFilter)
		}
	}
}

func TestVOTCLIOutputFailedDetectsZeroExitFailures(t *testing.T) {
	out := `Request language is set to en
Response language is set to ru
❯ Translating (ID: https://example.test/video.mp4).
Error: Возникла ошибка при переводе, попробуйте позже
✔ Failed to request video translation
✖ Downloading (ID: https://example.test/video.mp4). [FAILED: Downloading failed!]`
	if !votCLIOutputFailed(out) {
		t.Fatalf("expected VOT CLI error output to be treated as failure")
	}
}

func TestVOTCLIOutputFailedAllowsNormalOutput(t *testing.T) {
	out := `Request language is set to en
Response language is set to ru
✔ Forming a link to the video
✔ Translating finished!`
	if votCLIOutputFailed(out) {
		t.Fatalf("expected normal VOT CLI output to be allowed")
	}
}

func TestBuildVOTCLITranslateArgsUsesStableFlags(t *testing.T) {
	args, err := buildVOTCLITranslateArgs("https://example.test/source.mp4", "/tmp/out", "translated", "auto", "ru", votProviderYandex)
	if err != nil {
		t.Fatalf("build args: %v", err)
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{"--output=/tmp/out", "--output-file=translated", "--lang=auto", "--reslang=ru"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("args do not contain %q: %v", want, args)
		}
	}
	if strings.Contains(joined, "--clone") {
		t.Fatalf("unexpected lively flag: %v", args)
	}
}

func TestBuildVOTCLITranslateArgsEnablesLivelyVoice(t *testing.T) {
	t.Setenv("VOT_LIVELY_API_TOKEN", "test-oauth-token")
	t.Setenv("YA_MUSIC_TOKEN", "")
	args, err := buildVOTCLITranslateArgs("https://example.test/source.mp4", "/tmp/out", "translated", "en", "ru", votProviderYandexLively)
	if err != nil {
		t.Fatalf("build lively args: %v", err)
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{"--clone", "--output-file=translated.mp3", "--lang=en", "--reslang=ru"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("lively args do not contain expected flag: %v", args)
		}
	}
	if strings.Contains(joined, "test-oauth-token") || strings.Contains(joined, "--token") {
		t.Fatalf("OAuth token must be passed through the child environment, not argv: %v", args)
	}
}

func TestBuildVOTCLITranslateArgsRejectsInvalidLivelyConfiguration(t *testing.T) {
	t.Setenv("VOT_LIVELY_API_TOKEN", "")
	t.Setenv("YA_MUSIC_TOKEN", "")
	if _, err := buildVOTCLITranslateArgs("source", "/tmp/out", "translated", "en", "ru", votProviderYandexLively); err == nil || !strings.Contains(err.Error(), "requires") {
		t.Fatalf("expected missing token error, got %v", err)
	}
	t.Setenv("VOT_LIVELY_API_TOKEN", "test-oauth-token")
	if _, err := buildVOTCLITranslateArgs("source", "/tmp/out", "translated", "nl", "ru", votProviderYandexLively); err == nil || !strings.Contains(err.Error(), "en -> ru") {
		t.Fatalf("expected unsupported language error, got %v", err)
	}
}

func TestRunVOTCLITranslateLocalDoesNotFallbackWhenLivelyCLIMissing(t *testing.T) {
	t.Setenv("VOT_LIVELY_API_TOKEN", "test-oauth-token")
	t.Setenv("VOT_LIVELY_CLI_BIN", "/definitely/missing/vot-cli-go")
	_, err := runVOTCLITranslateLocal("source", t.TempDir(), "translated", "en", "ru", votProviderYandexLively)
	if err == nil || !strings.Contains(err.Error(), "lively voice CLI not found") {
		t.Fatalf("expected explicit missing lively CLI error, got %v", err)
	}
}

func TestVoiceTranslateLivelyKeyboardOnlyOffersEnglish(t *testing.T) {
	keyboard := renderVoiceTranslateLangKeyboard("token", voiceTranslateActionMix, voiceTranslateEngineVOT, votProviderYandexLively)
	callbacks := make([]string, 0)
	for _, row := range keyboard.InlineKeyboard {
		for _, button := range row {
			if button.CallbackData != nil {
				callbacks = append(callbacks, *button.CallbackData)
			}
		}
	}
	joined := strings.Join(callbacks, " ")
	if !strings.Contains(joined, "vtr|lang|mix|token|en") {
		t.Fatalf("English option missing: %v", callbacks)
	}
	if strings.Contains(joined, "|auto") || strings.Contains(joined, "|nl") || strings.Contains(joined, "|ru") {
		t.Fatalf("lively keyboard contains unsupported source language: %v", callbacks)
	}
}

func TestVoiceTranslateCacheSeparatesVoiceProviders(t *testing.T) {
	normal := buildVoiceTranslateCacheKeyWithProvider("file", "en", "ru", string(votProviderYandex))
	lively := buildVoiceTranslateCacheKeyWithProvider("file", "en", "ru", string(votProviderYandexLively))
	if normal == lively {
		t.Fatalf("normal and lively cache keys must differ: %q", normal)
	}
}

func TestVoiceTranslateUserErrorMessageForVOTFailure(t *testing.T) {
	msg := voiceTranslateUserErrorMessage(assertErr("vot-cli reported failure (Error: Возникла ошибка при переводе)"))
	if !strings.Contains(msg, "VOT не смог обработать этот файл") {
		t.Fatalf("unexpected user message: %q", msg)
	}
}

func TestVoiceTranslateUserErrorMessageForNoSpeech(t *testing.T) {
	msg := voiceTranslateUserErrorMessage(assertErr("vot-cli reported failure (Error: Не удалось перевести видео — похоже, в нём нет речи)"))
	if !strings.Contains(msg, "не нашёл распознаваемую речь") {
		t.Fatalf("unexpected user message: %q", msg)
	}
}

func TestOpenAIGPTTranslateCacheProviderIncludesModelsAndVoice(t *testing.T) {
	t.Setenv("GPT_TRANSLATE_TRANSCRIBE_MODEL", "whisper-test")
	t.Setenv("GPT_TRANSLATE_MODEL", "gpt-test")
	t.Setenv("GPT_TRANSLATE_TTS_MODEL", "tts-test")
	t.Setenv("GPT_TRANSLATE_TTS_VOICE", "voice-test")
	got := openAIGPTTranslateCacheProvider()
	want := "openai-gpt:timed-dubbing-v4:whisper-test:gpt-test:tts-test:voice-test"
	if got != want {
		t.Fatalf("cache provider = %q, want %q", got, want)
	}
}

func TestGroupTranscriptSegmentsForDubbingKeepsTimingGaps(t *testing.T) {
	t.Setenv("GPT_TRANSLATE_DUB_GROUP_MAX_SEC", "4")
	t.Setenv("GPT_TRANSLATE_DUB_GROUP_MAX_GAP_SEC", "0.5")
	t.Setenv("GPT_TRANSLATE_DUB_GROUP_MIN_SEC", "1.5")
	t.Setenv("GPT_TRANSLATE_DUB_GROUP_MIN_CHARS", "6")
	t.Setenv("GPT_TRANSLATE_DUB_GROUP_MAX_CHARS", "120")
	groups := groupTranscriptSegmentsForDubbing([]openAITranscriptSegment{
		{Start: 0.0, End: 1.0, Text: "one"},
		{Start: 1.2, End: 2.0, Text: "two"},
		{Start: 3.0, End: 4.0, Text: "three"},
	})
	if len(groups) != 2 {
		t.Fatalf("groups len = %d, want 2: %#v", len(groups), groups)
	}
	if groups[0].Index != 0 || groups[0].Start != 0 || groups[0].End != 2 || groups[0].Text != "one two" {
		t.Fatalf("unexpected first group: %#v", groups[0])
	}
	if groups[1].Index != 1 || groups[1].Start != 3 || groups[1].End != 4 || groups[1].Text != "three" {
		t.Fatalf("unexpected second group: %#v", groups[1])
	}
}

func TestGroupTranscriptSegmentsForDubbingMergesTinyFragments(t *testing.T) {
	t.Setenv("GPT_TRANSLATE_DUB_GROUP_MAX_SEC", "6")
	t.Setenv("GPT_TRANSLATE_DUB_GROUP_MAX_GAP_SEC", "0.3")
	t.Setenv("GPT_TRANSLATE_DUB_GROUP_STRONG_GAP_SEC", "2")
	t.Setenv("GPT_TRANSLATE_DUB_GROUP_MIN_SEC", "2")
	t.Setenv("GPT_TRANSLATE_DUB_GROUP_MIN_CHARS", "20")
	t.Setenv("GPT_TRANSLATE_DUB_GROUP_MAX_CHARS", "200")
	groups := groupTranscriptSegmentsForDubbing([]openAITranscriptSegment{
		{Start: 0.0, End: 0.15, Text: "u"},
		{Start: 0.75, End: 0.90, Text: "u"},
		{Start: 1.35, End: 1.55, Text: "u"},
		{Start: 1.80, End: 2.60, Text: "okay"},
	})
	if len(groups) != 1 {
		t.Fatalf("groups len = %d, want 1: %#v", len(groups), groups)
	}
	if groups[0].Text != "u u u okay" {
		t.Fatalf("unexpected merged text: %#v", groups[0])
	}
}

func TestOpenAITTSWindowForGroupUsesGapBeforeNextPhrase(t *testing.T) {
	groups := []openAITimedDubbingGroup{
		{Start: 1, End: 2, Text: "one"},
		{Start: 4, End: 5, Text: "two"},
	}
	if got := openAITTSWindowForGroup(groups, 0, 8); got != 3 {
		t.Fatalf("window = %v, want 3", got)
	}
	if got := openAITTSWindowForGroup(groups, 1, 8); got != 4 {
		t.Fatalf("last window = %v, want 4", got)
	}
}

func TestVoiceTranslateTargetLangOpenAIUsesGPTEnv(t *testing.T) {
	t.Setenv("GPT_TRANSLATE_RESLANG", "nl")
	t.Setenv("VOICE_TRANSLATE_RESLANG", "ru")
	if got := voiceTranslateTargetLang(voiceTranslateEngineOpenAI); got != "nl" {
		t.Fatalf("openai target lang = %q, want nl", got)
	}
	if got := voiceTranslateTargetLang(voiceTranslateEngineVOT); got != "ru" {
		t.Fatalf("vot target lang = %q, want ru", got)
	}
}

func TestAtempoFilterForRatioChainsLargeRatio(t *testing.T) {
	got := atempoFilterForRatio(5)
	if !strings.Contains(got, "atempo=2") || !strings.Contains(got, "atempo=1.2500") {
		t.Fatalf("unexpected atempo chain: %q", got)
	}
}

type assertErr string

func (e assertErr) Error() string { return string(e) }
