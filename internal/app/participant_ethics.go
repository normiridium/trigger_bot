package app

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"trigger-admin-bot/internal/chatclear"
)

const participantEthicsPeriod = 24 * time.Hour

const participantEthicsPrompt = `Ты анализируешь только наблюдаемое поведение участника Telegram-чата по предоставленным сообщениям за последние 24 часа. Не ставь диагнозы, не делай выводов о защищённых характеристиках человека и не объявляй человека «хорошим» или «плохим».

Верни только готовый отчёт на русском в rich Markdown. Укажи:
1. Общую оценку этичности общения от 0 до 10 и уровень уверенности.
2. Уважение к собеседникам и их границам.
3. Наличие или отсутствие расизма, сексизма, эйджизма, эйблизма, ксенофобии, гомофобии, трансфобии и иных дискриминационных паттернов.
4. Возможные травля, унижение, угрозы, манипуляции или разжигание конфликтов.
5. Позитивные паттерны: поддержка, эмпатия, признание ошибок, конструктивность.
6. Краткий вывод и ограничения оценки из-за объёма или контекста данных.

Каждое существенное утверждение подтверждай ссылками message_link из входных данных. Отличай цитирование, шутку, пересказ и собственную позицию автора. Не додумывай отсутствующий контекст и прямо отмечай неоднозначность.`

type ethicsTarget struct {
	ID       int64
	Username string
	Label    string
}

func handleEthicsCommand(bot *tgbotapi.BotAPI, service chatclear.Service, adminCache *adminStatusCache, msg *tgbotapi.Message) {
	if bot == nil || msg == nil || msg.Chat == nil || msg.From == nil {
		return
	}
	ctx := sendContext{Bot: bot, ChatID: msg.Chat.ID, ReplyTo: msg.MessageID}
	if msg.Chat.IsPrivate() {
		reply(ctx, "Команда /ethics работает только в группах и супергруппах.", false)
		return
	}
	if !canUseSensitiveBotAdminCommand(bot, adminCache, msg) {
		reply(ctx, "Команда /ethics доступна только администраторам чата.", false)
		return
	}
	target, err := parseEthicsTarget(msg)
	if err != nil {
		reply(ctx, err.Error(), false)
		return
	}

	go func() {
		_, _ = bot.Request(tgbotapi.NewChatAction(msg.Chat.ID, tgbotapi.ChatTyping))
		now := time.Now()
		requestCtx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		result, err := service.GetHistory(requestCtx, chatclear.HistoryRequest{
			ChatID: msg.Chat.ID, Username: strings.TrimSpace(msg.Chat.UserName),
			SinceUnix: now.Add(-participantEthicsPeriod).Unix(), UntilUnix: now.Unix(),
			FromID: target.ID, FromUsername: target.Username,
		})
		cancel()
		if err != nil {
			reportChatFailure(bot, msg.Chat.ID, "ошибка получения истории участника", err)
			return
		}
		messages := filterSummaryMessages(result.Messages)
		if len(messages) == 0 {
			reply(ctx, fmt.Sprintf("За последние 24 часа не найдено текстовых сообщений участника %s.", target.Label), false)
			return
		}
		promptMessages := buildSummaryPromptMessages(msg.Chat.ID, strings.TrimSpace(msg.Chat.UserName), messages)
		report, err := generateChatSummary(participantEthicsPrompt, promptMessages)
		if err != nil {
			reportChatFailure(bot, msg.Chat.ID, "ошибка оценки участника", err)
			return
		}
		report = fmt.Sprintf("Оценка участника **%s** по %d сообщениям за последние 24 часа.\n\n%s", target.Label, len(messages), report)
		if !sendRichMarkdownArticle(ctx, report, false) {
			log.Printf("participant ethics delivery failed chat=%d target_id=%d target_username=%q", msg.Chat.ID, target.ID, target.Username)
		}
	}()
}

func parseEthicsTarget(msg *tgbotapi.Message) (ethicsTarget, error) {
	if msg != nil && msg.ReplyToMessage != nil && msg.ReplyToMessage.From != nil {
		user := msg.ReplyToMessage.From
		label := strings.TrimSpace(strings.Join([]string{user.FirstName, user.LastName}, " "))
		if user.UserName != "" {
			label = "@" + user.UserName
		}
		if label == "" {
			label = fmt.Sprintf("ID %d", user.ID)
		}
		return ethicsTarget{ID: user.ID, Username: user.UserName, Label: label}, nil
	}
	username := strings.TrimPrefix(strings.TrimSpace(msg.CommandArguments()), "@")
	if username == "" || strings.ContainsAny(username, " \t\r\n") {
		return ethicsTarget{}, fmt.Errorf("Использование: ответьте /ethics на сообщение участника или укажите /ethics @username.")
	}
	return ethicsTarget{Username: username, Label: "@" + username}, nil
}
