package app

// Иммунитет должен жить в промпте портрета бота, а не в шаблоне ответов.

import (
	"strings"
	"testing"
)

func TestBotPortraitPromptHasImmunity(t *testing.T) {
	prompt := botPortraitSystemPrompt
	for _, want := range []string{
		"Иммунитет",
		"не перенимай",
		"Травлю",
		"Чужую злость",
		"Лозунги",
		"Вредные советы",
		"приоритет у этого блока",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("в промпте портрета бота нет %q", want)
		}
	}
}

func TestBotPortraitPromptKeepsPersonaRules(t *testing.T) {
	prompt := botPortraitSystemPrompt
	for _, want := range []string{"портрет ЛИЧНОСТИ", "речевые тики", "2-3 абзаца"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("потерялось правило портрета: %q", want)
		}
	}
}

func TestParticipantPortraitPromptUnchanged(t *testing.T) {
	prompt := participantPortraitSystemPrompt
	if strings.Contains(prompt, "Иммунитет") {
		t.Fatal("иммунитет не должен попадать в промпт портрета участника")
	}
	for _, want := range []string{"мягко и нейтрально", "3-5 связных абзацев"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("промпт портрета участника изменился: %q", want)
		}
	}
}
