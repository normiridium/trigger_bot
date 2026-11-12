package app

// Первичный портрет по запросу: если участник просит свой портрет, а его ещё
// нет, берём его сообщения за последние сутки из истории чата и строим портрет
// с нуля — вместо ответа «нужно ещё N сообщений».

import (
	"context"
	"strconv"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"trigger-admin-bot/internal/chatclear"
)

// bootstrapParticipantPortrait kicks off async portrait creation from the last
// 24 hours of the user's messages. Returns false when it cannot even start (the
// caller then falls back to the usual hint).
func bootstrapParticipantPortrait(bot *tgbotapi.BotAPI, svc chatclear.Service, mgr *participantPortraitManager, msg *tgbotapi.Message) bool {
	if bot == nil || svc == nil || mgr == nil || mgr.store == nil || msg == nil || msg.Chat == nil || msg.From == nil {
		return false
	}
	chatID := msg.Chat.ID
	userID := msg.From.ID
	username := strings.TrimSpace(msg.Chat.UserName)

	reply(sendContext{Bot: bot, ChatID: chatID, ReplyTo: msg.MessageID},
		"Портрет пока пуст — собираю его по твоим сообщениям за последние сутки, это займёт до минуты.", false)

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
		defer cancel()

		now := time.Now()
		history, err := svc.GetHistory(ctx, chatclear.HistoryRequest{
			ChatID:    chatID,
			Username:  username,
			SinceUnix: now.Add(-24 * time.Hour).Unix(),
			UntilUnix: now.Unix(),
			FromID:    userID,
		})
		if err != nil {
			reportChatFailure(bot, chatID, "не удалось получить историю для портрета", err)
			return
		}
		texts := make([]string, 0, len(history.Messages))
		for _, item := range history.Messages {
			text := strings.TrimSpace(item.Text)
			if text == "" {
				continue
			}
			texts = append(texts, text)
		}
		// Older messages first, and keep the batch bounded for the model.
		if len(texts) > participantPortraitBootstrapMaxMessages {
			texts = texts[len(texts)-participantPortraitBootstrapMaxMessages:]
		}
		if len(texts) == 0 {
			reply(sendContext{Bot: bot, ChatID: chatID},
				"За последние сутки твоих сообщений не нашлось — портрет пока не из чего собрать.", false)
			return
		}

		portrait, err := generateParticipantPortrait("", texts)
		if err != nil {
			reportChatFailure(bot, chatID, "не удалось собрать первичный портрет", err)
			return
		}
		portrait = strings.TrimSpace(portrait)
		if portrait == "" {
			reply(sendContext{Bot: bot, ChatID: chatID}, "Не получилось собрать портрет — попробуй позже.", false)
			return
		}
		if err := mgr.store.SaveParticipantPortrait(chatID, userID, portrait); err != nil {
			reportChatFailure(bot, chatID, "не удалось сохранить первичный портрет", err)
			return
		}
		mgr.InvalidatePortrait(userID)
		out := "Твой портрет по сообщениям за последние сутки (" + strconv.Itoa(len(texts)) + " сообщ.):\n" + portrait
		sendRichMarkdownArticle(sendContext{Bot: bot, ChatID: chatID}, out, false)
	}()
	return true
}
