package app

// Промпты суммаризации должны быть шаблонами: их видно и правится в админке.

import (
	"strings"
	"testing"
)

func TestPortraitPromptTemplatesCovered(t *testing.T) {
	list := portraitPromptTemplates()
	if len(list) != 2 {
		t.Fatalf("ожидали два шаблона, получили %d", len(list))
	}
	want := map[string]string{
		participantPortraitTemplateKey: "Саммаризация: портрет участника",
		botPortraitTemplateKey:         "Саммаризация: портрет бота",
	}
	seen := map[string]bool{}
	for _, tpl := range list {
		title, ok := want[tpl.Key]
		if !ok {
			t.Fatalf("неожиданный ключ шаблона %q", tpl.Key)
		}
		if tpl.Title != title {
			t.Fatalf("заголовок %q, ждали %q", tpl.Title, title)
		}
		if strings.TrimSpace(tpl.Text) == "" {
			t.Fatalf("пустой текст шаблона %q", tpl.Key)
		}
		seen[tpl.Key] = true
	}
	if len(seen) != len(want) {
		t.Fatalf("шаблоны не покрыты: %+v", seen)
	}
}

func TestBotPortraitTemplateCarriesImmunity(t *testing.T) {
	for _, tpl := range portraitPromptTemplates() {
		if tpl.Key != botPortraitTemplateKey {
			continue
		}
		if !strings.Contains(tpl.Text, "Иммунитет") || !strings.Contains(tpl.Text, "не перенимай") {
			t.Fatalf("в шаблоне портрета бота нет иммунитета")
		}
		return
	}
	t.Fatal("шаблон портрета бота не найден")
}

func TestPortraitPromptFallbackWithoutStore(t *testing.T) {
	if got := portraitPromptFor(nil, botPortraitTemplateKey, "fallback"); got != "fallback" {
		t.Fatalf("без store ждали fallback, получили %q", got)
	}
}

func TestPortraitTemplateKeysDistinct(t *testing.T) {
	if participantPortraitTemplateKey == botPortraitTemplateKey {
		t.Fatal("ключи шаблонов совпадают")
	}
	if participantPortraitTemplateKey == chatSummaryTemplateKey {
		t.Fatal("ключ портрета участника совпал с chat_summary")
	}
}
