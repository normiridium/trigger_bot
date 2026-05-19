package app

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"trigger-admin-bot/internal/chatclear"
)

type mtprotoSetupStep int

const (
	mtprotoSetupNone mtprotoSetupStep = iota
	mtprotoSetupWaitPhone
	mtprotoSetupWaitCode
)

type mtprotoSetupState struct {
	UserID      int64
	ChatID      int64
	Step        mtprotoSetupStep
	ChallengeID string
	UpdatedAt   time.Time
}

type mtprotoSetupManager struct {
	mu    sync.Mutex
	ttl   time.Duration
	items map[int64]mtprotoSetupState
}

func newMTProtoSetupManager(ttl time.Duration) *mtprotoSetupManager {
	if ttl <= 0 {
		ttl = 20 * time.Minute
	}
	return &mtprotoSetupManager{ttl: ttl, items: make(map[int64]mtprotoSetupState)}
}

func (m *mtprotoSetupManager) put(st mtprotoSetupState) {
	if m == nil || st.UserID == 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.gcLocked(time.Now())
	st.UpdatedAt = time.Now()
	m.items[st.UserID] = st
}

func (m *mtprotoSetupManager) get(userID int64) (mtprotoSetupState, bool) {
	if m == nil || userID == 0 {
		return mtprotoSetupState{}, false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.gcLocked(time.Now())
	st, ok := m.items[userID]
	return st, ok
}

func (m *mtprotoSetupManager) del(userID int64) {
	if m == nil || userID == 0 {
		return
	}
	m.mu.Lock()
	delete(m.items, userID)
	m.mu.Unlock()
}

func (m *mtprotoSetupManager) gcLocked(now time.Time) {
	for uid, st := range m.items {
		if now.Sub(st.UpdatedAt) > m.ttl {
			delete(m.items, uid)
		}
	}
}

func mtprotoSetupChatKeyboard(bot *tgbotapi.BotAPI) tgbotapi.InlineKeyboardMarkup {
	chatIDs := parseBotCommandChatIDs()
	rows := make([][]tgbotapi.InlineKeyboardButton, 0, len(chatIDs)+1)
	for _, id := range chatIDs {
		label := resolveChatButtonLabel(bot, id)
		rows = append(rows, tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(label, mtprotoSetupCallbackPrefix+"|"+mtprotoSetupCallbackChat+"|"+strconv.FormatInt(id, 10))))
	}
	rows = append(rows, tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("Отмена", mtprotoSetupCallbackPrefix+"|"+mtprotoSetupCallbackCancel)))
	return tgbotapi.NewInlineKeyboardMarkup(rows...)
}

func resolveChatButtonLabel(bot *tgbotapi.BotAPI, chatID int64) string {
	if bot != nil {
		cfg := tgbotapi.ChatInfoConfig{ChatConfig: tgbotapi.ChatConfig{ChatID: chatID}}
		chat, err := bot.GetChat(cfg)
		if err == nil {
			title := strings.TrimSpace(chat.Title)
			if title == "" {
				title = strings.TrimSpace(chat.UserName)
			}
			if title != "" {
				return fmt.Sprintf("%s (%d)", clipText(title, 48), chatID)
			}
		}
	}
	return fmt.Sprintf("Чат %d", chatID)
}

func handleSetMTProtoCommand(bot *tgbotapi.BotAPI, svc chatclear.Service, setup *mtprotoSetupManager, msg *tgbotapi.Message) bool {
	if bot == nil || msg == nil || msg.Chat == nil || msg.From == nil || setup == nil {
		return false
	}
	if !msg.Chat.IsPrivate() {
		reply(sendContext{Bot: bot, ChatID: msg.Chat.ID, ReplyTo: msg.MessageID}, "Команда /set_mtproto доступна только в личке с ботом.", false)
		return true
	}
	if svc == nil || !svc.Available(context.Background()) {
		reply(sendContext{Bot: bot, ChatID: msg.Chat.ID, ReplyTo: msg.MessageID}, "tg_ops_service недоступен, настройка MTProto сейчас отключена.", false)
		return true
	}
	if len(parseBotCommandChatIDs()) == 0 {
		reply(sendContext{Bot: bot, ChatID: msg.Chat.ID, ReplyTo: msg.MessageID}, "Список чатов пуст. Заполните ALLOWED_CHAT_IDS.", false)
		return true
	}
	setup.del(msg.From.ID)
	m := tgbotapi.NewMessage(msg.Chat.ID, "Выберите чат для привязки к MTProto:")
	m.ReplyMarkup = mtprotoSetupChatKeyboard(bot)
	m.ReplyToMessageID = msg.MessageID
	_, _ = bot.Send(m)
	return true
}

func handleSetMTProtoCallback(bot *tgbotapi.BotAPI, setup *mtprotoSetupManager, cb *tgbotapi.CallbackQuery) bool {
	if bot == nil || cb == nil || cb.Message == nil || cb.Message.Chat == nil || cb.From == nil || setup == nil {
		return false
	}
	parts := strings.Split(strings.TrimSpace(cb.Data), "|")
	if len(parts) == 0 || parts[0] != mtprotoSetupCallbackPrefix {
		return false
	}
	if cb.Message.Chat.ID != cb.From.ID {
		_, _ = bot.Request(tgbotapi.NewCallback(cb.ID, "Делайте это в личке с ботом"))
		return true
	}
	if len(parts) >= 2 && parts[1] == mtprotoSetupCallbackCancel {
		setup.del(cb.From.ID)
		_, _ = bot.Request(tgbotapi.NewCallback(cb.ID, "Отменено"))
		_, _ = bot.Send(tgbotapi.NewMessage(cb.Message.Chat.ID, "Настройка MTProto отменена."))
		return true
	}
	if len(parts) < 3 || parts[1] != mtprotoSetupCallbackChat {
		_, _ = bot.Request(tgbotapi.NewCallback(cb.ID, "Неизвестное действие"))
		return true
	}
	chatID, err := strconv.ParseInt(strings.TrimSpace(parts[2]), 10, 64)
	if err != nil || chatID == 0 {
		_, _ = bot.Request(tgbotapi.NewCallback(cb.ID, "Неверный chat_id"))
		return true
	}
	setup.put(mtprotoSetupState{UserID: cb.From.ID, ChatID: chatID, Step: mtprotoSetupWaitPhone})
	_, _ = bot.Request(tgbotapi.NewCallback(cb.ID, "Чат выбран"))
	_, _ = bot.Send(tgbotapi.NewMessage(cb.Message.Chat.ID, "Введите телефон в формате +79991234567"))
	return true
}

const (
	mtprotoSetupCallbackPrefix = "mtpset"
	mtprotoSetupCallbackCancel = "cancel"
	mtprotoSetupCallbackChat   = "chat"
)

func handleSetMTProtoPrivateText(bot *tgbotapi.BotAPI, svc chatclear.Service, setup *mtprotoSetupManager, msg *tgbotapi.Message) bool {
	if bot == nil || msg == nil || msg.Chat == nil || msg.From == nil || svc == nil || setup == nil {
		return false
	}
	if !msg.Chat.IsPrivate() {
		return false
	}
	st, ok := setup.get(msg.From.ID)
	if !ok {
		return false
	}
	text := strings.TrimSpace(msg.Text)
	if text == "" {
		return true
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if st.Step == mtprotoSetupWaitPhone {
		res, err := svc.StartAuth(ctx, chatclear.AuthStartRequest{ChatID: st.ChatID, Phone: text})
		if err != nil {
			reply(sendContext{Bot: bot, ChatID: msg.Chat.ID, ReplyTo: msg.MessageID}, "Не удалось отправить код: "+clipText(err.Error(), 220), false)
			return true
		}
		st.Step = mtprotoSetupWaitCode
		st.ChallengeID = res.ChallengeID
		setup.put(st)
		reply(sendContext{Bot: bot, ChatID: msg.Chat.ID, ReplyTo: msg.MessageID}, "Код отправлен в Telegram. Пришлите код сюда одним сообщением.", false)
		return true
	}
	if st.Step == mtprotoSetupWaitCode {
		res, err := svc.CompleteAuth(ctx, chatclear.AuthCompleteRequest{ChallengeID: st.ChallengeID, Code: text})
		if err != nil {
			reply(sendContext{Bot: bot, ChatID: msg.Chat.ID, ReplyTo: msg.MessageID}, "Не удалось завершить привязку: "+clipText(err.Error(), 220), false)
			return true
		}
		setup.del(msg.From.ID)
		reply(sendContext{Bot: bot, ChatID: msg.Chat.ID, ReplyTo: msg.MessageID}, fmt.Sprintf("Готово: chat_id=%d привязан, access_hash сохранен.", res.ChatID), false)
		return true
	}
	return false
}
