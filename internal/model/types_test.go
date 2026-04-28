package model

import "testing"

func TestTriggerModeString(t *testing.T) {
	if got := TriggerModeAll.String(); got == "" {
		t.Fatalf("empty string for TriggerModeAll")
	}
	if got := TriggerModeOnlyRepliesToSelfNoMedia.String(); got == "" {
		t.Fatalf("empty string for TriggerModeOnlyRepliesToSelfNoMedia")
	}
	if got := TriggerMode("custom").String(); got != "custom" {
		t.Fatalf("unexpected fallback string: %q", got)
	}
}

func TestAdminModeString(t *testing.T) {
	if got := AdminModeAdmins.String(); got == "" {
		t.Fatalf("empty string for AdminModeAdmins")
	}
	if got := AdminMode("x").String(); got != "x" {
		t.Fatalf("unexpected fallback string: %q", got)
	}
}

func TestMatchTypeString(t *testing.T) {
	if got := MatchTypeRegex.String(); got == "" {
		t.Fatalf("empty string for MatchTypeRegex")
	}
	if got := MatchType("x").String(); got != "x" {
		t.Fatalf("unexpected fallback string: %q", got)
	}
}

func TestActionTypeString(t *testing.T) {
	if got := ActionTypeSendSticker.String(); got == "" {
		t.Fatalf("empty string for ActionTypeSendSticker")
	}
	if got := ActionTypeSendFile.String(); got == "" {
		t.Fatalf("empty string for ActionTypeSendFile")
	}
	if got := ActionTypeSendGIF.String(); got == "" {
		t.Fatalf("empty string for ActionTypeSendGIF")
	}
	if got := ActionTypeMediaAudio.String(); got == "" {
		t.Fatalf("empty string for ActionTypeMediaAudio")
	}
	if got := ActionTypeMediaTikTok.String(); got == "" {
		t.Fatalf("empty string for ActionTypeMediaTikTok")
	}
	if got := ActionTypeMediaX.String(); got == "" {
		t.Fatalf("empty string for ActionTypeMediaX")
	}
	if got := ActionTypeMusic.String(); got == "" {
		t.Fatalf("empty string for ActionTypeMusic")
	}
	if got := ActionTypeYandexMusic.String(); got == "" {
		t.Fatalf("empty string for ActionTypeYandexMusic")
	}
	if got := ActionTypeUserLimitLow.String(); got == "" {
		t.Fatalf("empty string for ActionTypeUserLimitLow")
	}
	if got := ActionType("x").String(); got != "x" {
		t.Fatalf("unexpected fallback string: %q", got)
	}
}
