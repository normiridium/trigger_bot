package app

// Маскирование PII перед отправкой в модель.
//
// Сама логика (регулярки и алгоритм дословно из PerryLink/dsh-mask) живёт в
// отдельном модуле github.com/normiridium/pii-mask; здесь — тонкая обвязка бота:
// ключ сессии по сообщению и настройки из окружения.

import (
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
	"unicode"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	piimask "github.com/normiridium/pii-mask"
)

var (
	piiSessionsOnce sync.Once
	piiSessions     *piimask.Sessions
)

// piiEnabled читает переключатель из окружения (по умолчанию включено).
func piiEnabled() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("PII_MASK_ENABLED")))
	switch v {
	case "0", "false", "no", "off":
		return false
	}
	return true
}

// piiSessionsStore лениво поднимает хранилище сессий.
func piiSessionsStore() *piimask.Sessions {
	if !piiEnabled() {
		return nil
	}
	piiSessionsOnce.Do(func() {
		var entities []string
		for _, part := range strings.Split(os.Getenv("PII_MASK_ENTITIES"), ",") {
			if name := strings.ToLower(strings.TrimSpace(part)); name != "" {
				entities = append(entities, name)
			}
		}
		sessions, err := piimask.NewSessions(piimask.Options{Entities: entities}, 0)
		if err != nil {
			log.Printf("pii mask disabled: %v", err)
			return
		}
		piiSessions = sessions
	})
	return piiSessions
}

// piiResetSessionsStore сбрасывает лениво созданное хранилище сессий.
// Нужен тестам, которые меняют окружение между кейсами.
func piiResetSessionsStore() {
	piiSessionsOnce = sync.Once{}
	piiSessions = nil
}

// piiSessionKey — сессия маскирования: чат + автор.
func piiSessionKey(chatID, userID int64) string {
	return strconv.FormatInt(chatID, 10) + ":" + strconv.FormatInt(userID, 10)
}

// piiKeyForMessage — ключ сессии по сообщению (пусто, если автора нет).
func piiKeyForMessage(msg *tgbotapi.Message) string {
	if msg == nil || msg.Chat == nil || msg.From == nil {
		return ""
	}
	return piiSessionKey(msg.Chat.ID, msg.From.ID)
}

// piiNormalizeForMask убирает невидимые символы форматирования (категория Cf:
// U+200B, U+2060, U+200E/F и подобные). Telegram и копипаст приносят их внутрь
// номера, и тогда регулярки не видят цифры подряд.
func piiNormalizeForMask(text string) (string, int) {
	// Шаблоны рендерятся через html/template: плюс телефона приезжает как
	// &#43;, и шаблон \+7 его не видит. Возвращаем плюс до детекта.
	text = piiRestorePlusEntities(text)
	removed := 0
	clean := strings.Map(func(r rune) rune {
		if unicode.Is(unicode.Cf, r) {
			removed++
			return -1
		}
		return r
	}, text)
	return clean, removed
}

// countFormatRunes считает невидимые символы форматирования (категория Cf).
func countFormatRunes(text string) int {
	count := 0
	for _, r := range text {
		if unicode.Is(unicode.Cf, r) {
			count++
		}
	}
	return count
}

// piiRestorePlusEntities разворачивает HTML-сущности плюса в обычный "+".
func piiRestorePlusEntities(text string) string {
	if !strings.Contains(text, "&#") {
		return text
	}
	for _, entity := range []string{"&#43;", "&#x2b;", "&#x2B;"} {
		text = strings.ReplaceAll(text, entity, "+")
	}
	return text
}

// maskPIIForModel маскирует текст перед отправкой в модель.
func maskPIIForModel(sessionKey, text string) string {
	if sessionKey == "" || strings.TrimSpace(text) == "" {
		return text
	}
	sessions := piiSessionsStore()
	if sessions == nil {
		return text
	}
	clean, removed := piiNormalizeForMask(text)
	if removed > 0 && debugGPTLogEnabled {
		log.Printf("pii mask: убрано невидимых символов=%d", removed)
	}
	return sessions.Mask(sessionKey, clean)
}

// restorePIIFromModel возвращает плейсхолдеры обратно в исходный вид.
func restorePIIFromModel(sessionKey, text string) string {
	if sessionKey == "" || text == "" {
		return text
	}
	sessions := piiSessionsStore()
	if sessions == nil {
		return text
	}
	return sessions.Restore(sessionKey, text)
}

// piiSessionStats — для аудита: только счётчики, без значений.
func piiSessionStats(sessionKey string) (int, map[string]int) {
	sessions := piiSessionsStore()
	if sessions == nil {
		return 0, nil
	}
	return sessions.Stats(sessionKey)
}
