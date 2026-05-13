package gpt

import (
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"trigger-admin-bot/internal/model"
)

type PromptTask struct {
	Bot              *tgbotapi.BotAPI
	Trigger          model.Trigger
	QuotaLowTrigger  *model.Trigger
	Msg              *tgbotapi.Message
	TriggeredAt      time.Time
	RecentContext    string
	TemplateLookup   func(string) string
	IdleMarkActivity func(chatID int64, now time.Time)
	ChatID           int64
}
