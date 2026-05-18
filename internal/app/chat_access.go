package app

import (
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"trigger-admin-bot/internal/chataccess"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

var openAIQuotaErrorState = struct {
	mu   sync.Mutex
	last map[int64]time.Time
}{
	last: make(map[int64]time.Time),
}

func isOpenAIInsufficientQuotaError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(strings.TrimSpace(err.Error()))
	if msg == "" {
		return false
	}
	return strings.Contains(msg, "openai status=429") && strings.Contains(msg, "insufficient_quota")
}

func allowOpenAIQuotaWarning(chatID int64, now time.Time, cooldown time.Duration) bool {
	if chatID == 0 {
		return false
	}
	if cooldown <= 0 {
		cooldown = 30 * time.Minute
	}
	openAIQuotaErrorState.mu.Lock()
	defer openAIQuotaErrorState.mu.Unlock()
	last, ok := openAIQuotaErrorState.last[chatID]
	if ok && now.Sub(last) < cooldown {
		return false
	}
	openAIQuotaErrorState.last[chatID] = now
	return true
}

func pickOlenyamHungryTrigger(items []Trigger, isAdmin bool) *Trigger {
	for i := range items {
		it := items[i]
		if !it.Enabled || it.ActionType != ActionTypeOpenAIQuotaLow {
			continue
		}
		if !adminModeAllowsTrigger(&it, isAdmin) {
			continue
		}
		title := strings.ToLower(strings.TrimSpace(it.Title))
		if strings.Contains(title, "голод") {
			return &it
		}
	}
	return nil
}

func notifyDisallowedChat(bot *tgbotapi.BotAPI, chatID int64) error {
	if bot == nil || chatID == 0 {
		return errors.New("invalid notifyDisallowedChat args")
	}
	text := fmt.Sprintf(
		"⚠️ Этот чат не входит в список разрешённых.\nchat_id: <code>%d</code>\nДобавьте его в админке в поле «Разрешённые чаты (ALLOWED_CHAT_IDS)».",
		chatID,
	)
	m := tgbotapi.NewMessage(chatID, text)
	m.ParseMode = "HTML"
	m.DisableWebPagePreview = true
	if _, err := bot.Send(m); err != nil {
		log.Printf("send disallowed-chat notice failed chat=%d err=%v", chatID, err)
		return err
	}
	log.Printf("disallowed-chat notice sent chat=%d", chatID)
	return nil
}

func handleDisallowedMyChatMemberNotice(bot *tgbotapi.BotAPI, allowed chataccess.AllowList, notifier *chataccess.DisallowedChatNotifier, upd *rawChatMemberUpdated) {
	if bot == nil || upd == nil || upd.Chat == nil || upd.NewChatMember == nil || upd.NewChatMember.User == nil {
		return
	}
	// Only for updates about this bot account.
	if !upd.NewChatMember.User.IsBot {
		return
	}
	newStatus := strings.TrimSpace(upd.NewChatMember.Status)
	oldStatus := ""
	if upd.OldChatMember != nil {
		oldStatus = strings.TrimSpace(upd.OldChatMember.Status)
	}
	// Notify only when bot becomes active in chat (added/unbanned), not on every status change.
	if !chataccess.IsActiveChatMemberStatus(newStatus) || chataccess.IsActiveChatMemberStatus(oldStatus) {
		return
	}
	chatID := upd.Chat.ID
	if chatID == 0 || allowed.Allows(chatID) {
		return
	}
	now := time.Now()
	if !notifier.ShouldNotify(chatID, now) {
		return
	}
	if err := notifyDisallowedChat(bot, chatID); err == nil {
		notifier.MarkNotified(chatID, now)
	}
}
