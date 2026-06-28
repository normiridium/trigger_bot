package app

import (
	"strings"
	"testing"
)

func TestVoiceTranslateMixFiltersUseModerateDefaults(t *testing.T) {
	dynamicFilter, staticFilter := voiceTranslateMixFilters()
	for _, want := range []string{
		"volume=1[a1mix]",
		"volume=0.92[a0base]",
		"threshold=0.06:ratio=3",
	} {
		if !strings.Contains(dynamicFilter, want) {
			t.Fatalf("dynamic filter does not contain %q: %s", want, dynamicFilter)
		}
	}
	for _, want := range []string{
		"volume=0.8[a0]",
		"volume=1[a1]",
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
		"volume=1.05[a1]",
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

func TestVoiceTranslateUserErrorMessageForVOTFailure(t *testing.T) {
	msg := voiceTranslateUserErrorMessage(assertErr("vot-cli reported failure (Error: Возникла ошибка при переводе)"))
	if !strings.Contains(msg, "VOT не смог обработать") {
		t.Fatalf("unexpected user message: %q", msg)
	}
}

type assertErr string

func (e assertErr) Error() string { return string(e) }
