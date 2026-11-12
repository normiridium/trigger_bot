package app

// Промпты суммаризации, которые должны быть редактируемыми из админки.
//
// chat_summary уже жил шаблоном, а промпты портретов были зашиты в код —
// из-за этого в админке их не было видно. Теперь у каждого свой ключ
// в response_templates и значения по умолчанию из кода.

import (
	"log"
	"strings"
)

const (
	// participantPortraitTemplateKey — промпт суммаризации портрета участника.
	participantPortraitTemplateKey = "participant_portrait"
	// botPortraitTemplateKey — промпт суммаризации портрета бота.
	botPortraitTemplateKey = "bot_portrait"
)

// portraitPromptTemplates — что и как засеять при старте.
func portraitPromptTemplates() []ResponseTemplate {
	return []ResponseTemplate{
		{
			Key:   participantPortraitTemplateKey,
			Title: "Саммаризация: портрет участника",
			Text:  participantPortraitSystemPrompt,
		},
		{
			Key:   botPortraitTemplateKey,
			Title: "Саммаризация: портрет бота",
			Text:  botPortraitSystemPrompt,
		},
	}
}

// ensurePortraitPromptTemplates создаёт недостающие шаблоны промптов, чтобы
// они появились в админке. Существующие не трогает.
func ensurePortraitPromptTemplates(store *Store) {
	if store == nil {
		return
	}
	for _, tpl := range portraitPromptTemplates() {
		existing, err := store.getTemplateByKey(tpl.Key)
		if err != nil {
			log.Printf("portrait prompt template lookup failed key=%s err=%v", tpl.Key, err)
			continue
		}
		if existing != nil {
			continue
		}
		if err := store.SaveTemplate(tpl); err != nil {
			log.Printf("portrait prompt template create failed key=%s err=%v", tpl.Key, err)
			continue
		}
		log.Printf("portrait prompt template created key=%s", tpl.Key)
	}
}

// portraitPromptFor берёт промпт из шаблона, а если шаблона нет или он пуст —
// значение по умолчанию из кода.
func portraitPromptFor(store *Store, key, fallback string) string {
	if store == nil {
		return fallback
	}
	tpl, err := store.getTemplateByKey(key)
	if err != nil {
		log.Printf("portrait prompt template load failed key=%s err=%v", key, err)
		return fallback
	}
	if tpl == nil {
		return fallback
	}
	if text := strings.TrimSpace(tpl.Text); text != "" {
		return text
	}
	return fallback
}
