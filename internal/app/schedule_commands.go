package app

// Команды для управления расписаниями участника: показать список и удалить
// (одно или все), плюс регистрация сохранятеля для GPT-флоу.

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

var scheduleSaverHook = struct {
	mu sync.RWMutex
	fn func(chatID, userID int64, username string, directives []ScheduleDirective) ([]ParticipantSchedule, error)
}{}

// SetScheduleSaver регистрирует сохранение расписаний (вызывается при старте).
func SetScheduleSaver(fn func(chatID, userID int64, username string, directives []ScheduleDirective) ([]ParticipantSchedule, error)) {
	scheduleSaverHook.mu.Lock()
	scheduleSaverHook.fn = fn
	scheduleSaverHook.mu.Unlock()
}

func saveScheduledDirectives(chatID, userID int64, username string, directives []ScheduleDirective) ([]ParticipantSchedule, error) {
	scheduleSaverHook.mu.RLock()
	fn := scheduleSaverHook.fn
	scheduleSaverHook.mu.RUnlock()
	if fn == nil {
		return nil, fmt.Errorf("schedule storage is not configured")
	}
	return fn(chatID, userID, username, directives)
}

// formatScheduleLine renders one schedule as a chat-friendly line.
func formatScheduleLine(s ParticipantSchedule) string {
	action := s.Action
	if len([]rune(action)) > 120 {
		action = string([]rune(action)[:120]) + "…"
	}
	line := fmt.Sprintf("`%s` — %s (id %s)", s.Cron, action, shortScheduleID(s.ID))
	if s.LastRunAt > 0 {
		line += fmt.Sprintf(", последний запуск %s", time.Unix(s.LastRunAt, 0).Format("02.01 15:04"))
	}
	return line
}

func shortScheduleID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// handleMySchedulesCommand показывает расписания участника в этом чате.
func handleMySchedulesCommand(bot *tgbotapi.BotAPI, store *Store, msg *tgbotapi.Message) bool {
	if bot == nil || store == nil || msg == nil || msg.Chat == nil || msg.From == nil {
		return false
	}
	items, err := store.ListSchedules(msg.Chat.ID, msg.From.ID)
	if err != nil {
		reportChatFailure(bot, msg.Chat.ID, "не удалось прочитать расписания", err)
		return true
	}
	if len(items) == 0 {
		reply(sendContext{Bot: bot, ChatID: msg.Chat.ID, ReplyTo: msg.MessageID},
			"У тебя пока нет расписаний. Попроси Оле-ням — например: «напоминай мне каждый день в 10:53 пить воду».", false)
		return true
	}
	sort.Slice(items, func(i, j int) bool { return items[i].CreatedAt < items[j].CreatedAt })
	var b strings.Builder
	fmt.Fprintf(&b, "Твои расписания в этом чате (%d из %d):\n\n", len(items), maxSchedulesPerUser)
	for i, item := range items {
		fmt.Fprintf(&b, "%d. %s\n", i+1, formatScheduleLine(item))
	}
	b.WriteString("\nДневной лимит: до ")
	fmt.Fprintf(&b, "%d", maxScheduleRunsPerDay)
	b.WriteString(" срабатываний в сутки.\nУдалить: `/my_schedules_delete <id>` или `/my_schedules_delete all`")
	sendRichMarkdownArticle(sendContext{Bot: bot, ChatID: msg.Chat.ID}, b.String(), false)
	return true
}

// handleMySchedulesDeleteCommand удаляет одно расписание по id (или все).
func handleMySchedulesDeleteCommand(bot *tgbotapi.BotAPI, store *Store, msg *tgbotapi.Message) bool {
	if bot == nil || store == nil || msg == nil || msg.Chat == nil || msg.From == nil {
		return false
	}
	arg := strings.TrimSpace(commandArgument(msg.Text))
	ctxReply := sendContext{Bot: bot, ChatID: msg.Chat.ID, ReplyTo: msg.MessageID}
	if arg == "" {
		reply(ctxReply, "Укажи id расписания или `all`:\n`/my_schedules_delete 1a2b3c4d`\n`/my_schedules_delete all`", false)
		return true
	}
	if strings.EqualFold(arg, "all") || strings.EqualFold(arg, "все") {
		removed, err := store.DeleteSchedulesForUser(msg.Chat.ID, msg.From.ID)
		if err != nil {
			reportChatFailure(bot, msg.Chat.ID, "не удалось удалить расписания", err)
			return true
		}
		reply(ctxReply, fmt.Sprintf("Удалено расписаний: %d.", removed), false)
		return true
	}
	items, err := store.ListSchedules(msg.Chat.ID, msg.From.ID)
	if err != nil {
		reportChatFailure(bot, msg.Chat.ID, "не удалось прочитать расписания", err)
		return true
	}
	target := ""
	for _, item := range items {
		if strings.HasPrefix(item.ID, arg) || shortScheduleID(item.ID) == arg {
			target = item.ID
			break
		}
	}
	if target == "" {
		reply(ctxReply, "Не нашёл такое расписание. Посмотри список: `/my_schedules`", false)
		return true
	}
	ok, err := store.DeleteSchedule(msg.Chat.ID, msg.From.ID, target)
	if err != nil {
		reportChatFailure(bot, msg.Chat.ID, "не удалось удалить расписание", err)
		return true
	}
	if ok {
		reply(ctxReply, "Расписание удалено.", false)
	} else {
		reply(ctxReply, "Расписание уже удалено.", false)
	}
	return true
}

// commandArgument returns everything after the first space of the message.
func commandArgument(text string) string {
	text = strings.TrimSpace(text)
	if idx := strings.IndexAny(text, " \t\n"); idx >= 0 {
		return text[idx+1:]
	}
	return ""
}
