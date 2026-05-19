package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"trigger-admin-bot/internal/chatclear"
)

type clearChatState struct {
	ID        string
	ChatID    int64
	Username  string
	Requester int64
	CreatedAt time.Time
	SourceMsg int
}

type clearChatConfirmManager struct {
	mu    sync.Mutex
	ttl   time.Duration
	items map[string]clearChatState
}

func newClearChatConfirmManager(ttl time.Duration) *clearChatConfirmManager {
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}
	return &clearChatConfirmManager{ttl: ttl, items: make(map[string]clearChatState)}
}

func (m *clearChatConfirmManager) put(st clearChatState) string {
	if m == nil {
		return ""
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.gcLocked(time.Now())
	if strings.TrimSpace(st.ID) == "" {
		st.ID = randomClearChatToken()
	}
	m.items[st.ID] = st
	return st.ID
}

func (m *clearChatConfirmManager) get(id string) (clearChatState, bool) {
	if m == nil {
		return clearChatState{}, false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.gcLocked(time.Now())
	st, ok := m.items[strings.TrimSpace(id)]
	return st, ok
}

func (m *clearChatConfirmManager) del(id string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	delete(m.items, strings.TrimSpace(id))
	m.mu.Unlock()
}

func (m *clearChatConfirmManager) gcLocked(now time.Time) {
	for k, st := range m.items {
		if now.Sub(st.CreatedAt) >= m.ttl {
			delete(m.items, k)
		}
	}
}

func randomClearChatToken() string {
	buf := make([]byte, 10)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf)
}

func clearChatConfirmKeyboard(id string) tgbotapi.InlineKeyboardMarkup {
	return tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("Очистить чат", "clrchat|a|"+id),
			tgbotapi.NewInlineKeyboardButtonData("Отмена", "clrchat|c|"+id),
		),
	)
}

func handleClearChatCommand(bot *tgbotapi.BotAPI, adminCache *adminStatusCache, confirms *clearChatConfirmManager, msg *tgbotapi.Message) bool {
	if bot == nil || msg == nil || msg.Chat == nil || msg.From == nil || confirms == nil {
		return false
	}
	if msg.Chat.IsPrivate() {
		reply(sendContext{Bot: bot, ChatID: msg.Chat.ID, ReplyTo: msg.MessageID}, "Команда /clear_chat работает только в группах и супергруппах.", false)
		return true
	}
	_ = syncAdminCacheForUser(bot, adminCache, msg.Chat.ID, msg.From.ID)
	if adminCache == nil || !adminCache.IsChatAdmin(bot, msg.Chat.ID, msg.From.ID) {
		reply(sendContext{Bot: bot, ChatID: msg.Chat.ID, ReplyTo: msg.MessageID}, "Только администраторы могут использовать /clear_chat.", false)
		return true
	}
	st := clearChatState{
		ChatID:    msg.Chat.ID,
		Username:  strings.TrimSpace(msg.Chat.UserName),
		Requester: msg.From.ID,
		CreatedAt: time.Now(),
		SourceMsg: msg.MessageID,
	}
	id := confirms.put(st)
	warn := "⚠️ Подтвердите очистку истории чата.\n\n" +
		"Команда будет отправлена в tg_ops_service. " +
		"Действие необратимо и может занять время."
	m := tgbotapi.NewMessage(msg.Chat.ID, warn)
	m.ReplyToMessageID = msg.MessageID
	m.AllowSendingWithoutReply = true
	m.ReplyMarkup = clearChatConfirmKeyboard(id)
	if _, err := bot.Send(m); err != nil {
		reportChatFailure(bot, msg.Chat.ID, "ошибка отправки подтверждения clear_chat", err)
	}
	return true
}

func handleClearChatCallback(bot *tgbotapi.BotAPI, adminCache *adminStatusCache, confirms *clearChatConfirmManager, cleaner chatclear.Service, cb *tgbotapi.CallbackQuery) bool {
	if bot == nil || cb == nil || cb.From == nil || cb.Message == nil || cb.Message.Chat == nil || confirms == nil {
		return false
	}
	parts := strings.Split(strings.TrimSpace(cb.Data), "|")
	if len(parts) < 3 || parts[0] != "clrchat" {
		return false
	}
	action := strings.TrimSpace(parts[1])
	id := strings.TrimSpace(parts[2])
	st, ok := confirms.get(id)
	if !ok {
		_, _ = bot.Request(tgbotapi.NewCallback(cb.ID, "Подтверждение устарело"))
		return true
	}
	if cb.Message.Chat.ID != st.ChatID {
		_, _ = bot.Request(tgbotapi.NewCallback(cb.ID, "Чат подтверждения не совпадает"))
		return true
	}
	if cb.From.ID != st.Requester {
		_, _ = bot.Request(tgbotapi.NewCallback(cb.ID, "Подтверждать может только автор команды"))
		return true
	}
	_ = syncAdminCacheForUser(bot, adminCache, st.ChatID, cb.From.ID)
	if adminCache == nil || !adminCache.IsChatAdmin(bot, st.ChatID, cb.From.ID) {
		_, _ = bot.Request(tgbotapi.NewCallback(cb.ID, "Права администратора не подтверждены"))
		return true
	}

	switch action {
	case clearChatConfirmActionCancel:
		confirms.del(id)
		_, _ = bot.Request(tgbotapi.NewCallback(cb.ID, "Отменено"))
		_, _ = bot.Request(tgbotapi.DeleteMessageConfig{ChatID: st.ChatID, MessageID: cb.Message.MessageID})
		return true
	case clearChatConfirmActionApply:
		confirms.del(id)
		_, _ = bot.Request(tgbotapi.NewCallback(cb.ID, "Запускаю очистку..."))
		if cleaner == nil {
			reply(sendContext{Bot: bot, ChatID: st.ChatID, ReplyTo: st.SourceMsg}, "clear backend не подключен.", false)
			return true
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Duration(envInt("CLEAR_CHAT_TIMEOUT_SEC", 120))*time.Second)
		defer cancel()
		err := cleaner.Clear(ctx, chatclear.Request{ChatID: st.ChatID, Username: st.Username})
		if err != nil {
			if errors.Is(err, chatclear.ErrNotConfigured) {
				reply(sendContext{Bot: bot, ChatID: st.ChatID, ReplyTo: st.SourceMsg}, "Сервис очистки недоступен или не настроен.", false)
			} else {
				reportChatFailure(bot, st.ChatID, "ошибка clear_chat", err)
				reply(sendContext{Bot: bot, ChatID: st.ChatID, ReplyTo: st.SourceMsg}, "Не удалось очистить чат: "+clipText(err.Error(), 220), false)
			}
			return true
		}
		reply(sendContext{Bot: bot, ChatID: st.ChatID, ReplyTo: st.SourceMsg}, "Команда на очистку отправлена в tg_ops_service ✅", false)
		_, _ = bot.Request(tgbotapi.DeleteMessageConfig{ChatID: st.ChatID, MessageID: cb.Message.MessageID})
		return true
	default:
		_, _ = bot.Request(tgbotapi.NewCallback(cb.ID, "Неизвестное действие"))
		return true
	}
}

const (
	clearChatConfirmActionCancel = "c"
	clearChatConfirmActionApply  = "a"
)
