package app

// Планировщик расписаний участников: раз в 30 секунд смотрит, что пора
// выполнить, и «самоиндуцирует» Оле-ням — просит модель написать сообщение по
// заданию расписания и отправляет его в чат.

import (
	"context"
	"log"
	"os"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// scheduleRunnerDeps — всё, что нужно планировщику для отправки сообщений.
type scheduleRunnerDeps struct {
	Bot            *tgbotapi.BotAPI
	Store          *Store
	TemplateLookup func(string) string
}

// scheduleSelfPrompt — системная инструкция для самоиндуцированного сообщения.
const scheduleSelfPrompt = "Ты — Оле-ням, участница этого чата. Сейчас сработало расписание, " +
	"которое попросил участник, и ты пишешь сообщение сама, по своей инициативе. " +
	"Задание расписания: «%s». Напиши одно короткое живое сообщение в чат, как обычно пишешь: " +
	"если в задании есть @username — упомяни его, если просили «приятности» — будь в своём стиле. " +
	"Не объясняй, что это расписание, не цитируй задание дословно, не добавляй служебных пометок."

// scheduleLocation — часовой пояс, в котором трактуются cron-выражения.
// Участники пишут время по Москве, а сервер живёт в EET: без явного перевода
// напоминание приходит на час раньше/позже.
func scheduleLocation() *time.Location {
	name := strings.TrimSpace(os.Getenv("SCHEDULE_TIMEZONE"))
	if name == "" {
		name = "Europe/Moscow"
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		log.Printf("schedule runner: bad SCHEDULE_TIMEZONE %q: %v (fallback Europe/Moscow)", name, err)
		if loc, err = time.LoadLocation("Europe/Moscow"); err != nil {
			return time.FixedZone("MSK", 3*3600)
		}
	}
	return loc
}

// StartScheduleRunner запускает цикл проверки расписаний.
func StartScheduleRunner(deps scheduleRunnerDeps) {
	if deps.Bot == nil || deps.Store == nil {
		return
	}
	go func() {
		// Первая проверка — сразу, дальше каждые 30 секунд.
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			runDueSchedules(deps, time.Now().In(scheduleLocation()))
			<-ticker.C
		}
	}()
}

// runDueSchedules fires every schedule whose cron matches the current minute.
func runDueSchedules(deps scheduleRunnerDeps, now time.Time) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("schedule runner panic: %v", r)
		}
	}()
	items, err := deps.Store.ListEnabledSchedules()
	if err != nil {
		log.Printf("schedule runner: list failed: %v", err)
		return
	}
	if len(items) == 0 {
		return
	}
	today := now.Format("2006-01-02")
	for _, item := range items {
		spec, err := parseCronSpec(item.Cron)
		if err != nil {
			log.Printf("schedule %s: bad cron %q: %v", item.ID, item.Cron, err)
			continue
		}
		if !spec.Matches(now) {
			continue
		}
		// Не больше одного срабатывания в минуту на расписание. Сравниваем
		// Unix-минуты: раньше здесь форматировались времена в разных поясах
		// (МСК и локальный EET), сравнение не срабатывало и летело дважды.
		if item.LastRunAt > 0 && item.LastRunAt/60 == now.Unix()/60 {
			continue
		}
		usedToday, err := deps.Store.ScheduleRunsOnDate(item.ChatID, item.UserID, today)
		if err != nil {
			log.Printf("schedule %s: runs count failed: %v", item.ID, err)
			continue
		}
		if usedToday >= maxScheduleRunsPerDay {
			log.Printf("schedule %s: дневной лимит %d срабатываний для user=%d достигнут", item.ID, maxScheduleRunsPerDay, item.UserID)
			continue
		}
		if err := deps.Store.MarkScheduleRun(item.ID, item.RunDate, today, now); err != nil {
			log.Printf("schedule %s: mark run failed: %v", item.ID, err)
			continue
		}
		log.Printf("schedule fire id=%s chat=%d user=%d cron=%q action_len=%d", item.ID, item.ChatID, item.UserID, item.Cron, len([]rune(item.Action)))
		go executeSchedule(deps, item)
	}
}

// executeSchedule asks the model to produce the scheduled message and sends it.
func executeSchedule(deps scheduleRunnerDeps, item ParticipantSchedule) {
	defer recoverWorkerPanic("schedule_run")

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	synthetic := &tgbotapi.Message{
		MessageID: 0,
		Date:      int(time.Now().Unix()),
		Chat: &tgbotapi.Chat{
			ID:   item.ChatID,
			Type: "supergroup",
		},
		From: &tgbotapi.User{
			ID:       item.UserID,
			UserName: item.Username,
		},
	}
	prompt := strings.Replace(scheduleSelfPrompt, "%s", item.Action, 1)
	reply, err := generateChatGPTReply(templateContext{
		Bot:            deps.Bot,
		Msg:            synthetic,
		TemplateLookup: deps.TemplateLookup,
	}, prompt)
	if err != nil {
		log.Printf("schedule %s: generation failed: %v", item.ID, err)
		return
	}
	text := strings.TrimSpace(reply.Text)
	if text == "" {
		log.Printf("schedule %s: empty answer", item.ID)
		return
	}
	// Само расписание в ответе не нужно: директивы уже сохранены.
	text, _ = ExtractScheduleDirectives(text)
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	_ = ctx
	if ok := sendRichMarkdownArticle(sendContext{Bot: deps.Bot, ChatID: item.ChatID}, text, false); !ok {
		log.Printf("schedule %s: send failed chat=%d", item.ID, item.ChatID)
	}
}
