package gpt

import (
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"trigger-admin-bot/internal/model"
)

type PromptTask struct {
	Bot                 *tgbotapi.BotAPI
	Trigger             model.Trigger
	QuotaLowTrigger     *model.Trigger
	UserLimitLowTrigger *model.Trigger
	Msg                 *tgbotapi.Message
	TriggeredAt         time.Time
	RecentContext       string
	TemplateLookup      func(string) string
	RecordGPTTokens     func(userID int64, tokens int, now time.Time) (remaining int, crossedLow bool, err error)
	IdleMarkActivity    func(chatID int64, now time.Time)
	ChatID              int64
}
