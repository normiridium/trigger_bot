package vkaudio

import (
	"math/big"
	"testing"
)

func TestApplyVKAudioMaskOpIUsesMaskArgumentDirectly(t *testing.T) {
	input := "abcdefghijklmnopqrstuvwxyz0123456789"
	got, ok := applyVKAudioMaskOp(input, "i", []string{"791"}, 15453779, false)
	if !ok {
		t.Fatal("expected i op to apply")
	}
	want := shuffleString(input, big.NewInt(791))
	if got != want {
		t.Fatalf("unexpected i op result: %q, want %q", got, want)
	}
}

func TestApplyVKAudioMaskOpICanUseVKIDXOR(t *testing.T) {
	input := "abcdefghijklmnopqrstuvwxyz0123456789"
	got, ok := applyVKAudioMaskOp(input, "i", []string{"791"}, 15453779, true)
	if !ok {
		t.Fatal("expected i op to apply")
	}
	want := shuffleString(input, big.NewInt(791^15453779))
	if got != want {
		t.Fatalf("unexpected i op result: %q, want %q", got, want)
	}
}
