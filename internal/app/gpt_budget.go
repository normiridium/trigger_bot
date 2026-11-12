package app

// Жёсткий пре-чек бюджета GPT-токенов.
//
// Раньше лимит срабатывал уже после списания: запрос уходил в модель, токены
// списывались, и счётчик перелетал потолок в разы (видели 38k при лимите 20k).
// Здесь остаток проверяется ДО дорогой операции, а отказ объясняет, сколько
// осталось и когда окно сбросится.

import (
	"fmt"
	"strings"
	"sync"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// gptMinRequestTokens — минимальный остаток, при котором обычный ответ имеет
// смысл: иначе он обрывается на середине.
const gptMinRequestTokens = 800

// Сколько примерно стоит типовая дорогая операция (для пре-чека).
const (
	gptCostSummary        = 4000
	gptCostPortrait       = 3000
	gptCostVoiceTranslate = 2500
)

type gptBudgetStoreState struct {
	mu            sync.RWMutex
	store         *Store
	openaiLimit   int
	deepseekLimit int
}

var gptBudgetState = gptBudgetStoreState{}

// setGPTBudgetStore регистрирует хранилище и активные лимиты при старте.
func setGPTBudgetStore(store *Store, openaiLimit, deepseekLimit int) {
	gptBudgetState.mu.Lock()
	gptBudgetState.store = store
	gptBudgetState.openaiLimit = openaiLimit
	gptBudgetState.deepseekLimit = deepseekLimit
	gptBudgetState.mu.Unlock()
}

// gptBudgetSnapshot — остаток квоты автора в текущем 4-часовом окне.
type gptBudgetSnapshot struct {
	Limited   bool
	Remaining int
	Limit     int
	ResetIn   time.Duration
}

// currentGPTBudget берёт самый щедрый из включённых бакетов (openai/deepseek):
// они расходуются независимо, и человек блокируется только когда исчерпаны оба.
func currentGPTBudget(userID int64, now time.Time) gptBudgetSnapshot {
	if userID == 0 || !userGPTTokenLimitApplies(userID) {
		return gptBudgetSnapshot{}
	}
	gptBudgetState.mu.RLock()
	store := gptBudgetState.store
	openaiLimit := gptBudgetState.openaiLimit
	deepseekLimit := gptBudgetState.deepseekLimit
	gptBudgetState.mu.RUnlock()
	if store == nil {
		return gptBudgetSnapshot{}
	}

	best := -1
	bestLimit := 0
	checked := false
	for _, item := range []struct {
		limit  int
		bucket string
	}{
		{openaiLimit, "openai"},
		{deepseekLimit, "deepseek"},
	} {
		if item.limit <= 0 {
			continue
		}
		remaining, err := store.UserGPTTokensRemaining(userID, now, item.limit, item.bucket)
		if err != nil {
			continue
		}
		checked = true
		if best < 0 || remaining > best {
			best = remaining
			bestLimit = item.limit
		}
	}
	if !checked {
		return gptBudgetSnapshot{}
	}
	if best < 0 {
		best = 0
	}
	return gptBudgetSnapshot{
		Limited:   true,
		Remaining: best,
		Limit:     bestLimit,
		ResetIn:   timeUntilGPTWindowReset(now),
	}
}

// timeUntilGPTWindowReset — сколько осталось до конца текущего окна квоты.
func timeUntilGPTWindowReset(now time.Time) time.Duration {
	window := int64(gptUserQuotaWindow / time.Second)
	if window <= 0 {
		window = 4 * 3600
	}
	cur := now.Unix()
	next := (cur/window + 1) * window
	return time.Duration(next-cur) * time.Second
}

// humanDuration — «1 ч 20 мин», «12 мин».
func humanDuration(d time.Duration) string {
	if d <= 0 {
		return "меньше минуты"
	}
	minutes := int(d.Minutes())
	if minutes < 1 {
		return "меньше минуты"
	}
	hours := minutes / 60
	rest := minutes % 60
	switch {
	case hours > 0 && rest > 0:
		return fmt.Sprintf("%d ч %d мин", hours, rest)
	case hours > 0:
		return fmt.Sprintf("%d ч", hours)
	default:
		return fmt.Sprintf("%d мин", rest+hours*60)
	}
}

// gptBudgetRefusal — понятный отказ вместо молчания.
func gptBudgetRefusal(snap gptBudgetSnapshot, need int, what string) string {
	what = strings.TrimSpace(what)
	if what == "" {
		what = "это"
	}
	return fmt.Sprintf(
		"Сейчас не возьмусь за %s: на текущий 4-часовой отрезок осталось %d токенов из %d, а нужно примерно %d. Окно сбросится через %s.",
		what, snap.Remaining, snap.Limit, need, humanDuration(snap.ResetIn))
}

// ensureGPTBudget возвращает false и отвечает в чат, если на операцию не хватает
// запаса. Для операций без автора (или при выключенных лимитах) всегда true.
func ensureGPTBudget(bot *tgbotapi.BotAPI, msg *tgbotapi.Message, need int, what string) bool {
	if msg == nil || msg.From == nil || msg.From.ID == 0 {
		return true
	}
	if _, banned := isTokenBanned(msg.From.ID); banned {
		if bot != nil && msg.Chat != nil {
			reply(sendContext{Bot: bot, ChatID: msg.Chat.ID, ReplyTo: msg.MessageID}, tokenBanNotice(), false)
		}
		return false
	}
	snap := currentGPTBudget(msg.From.ID, time.Now())
	if !snap.Limited {
		return true
	}
	if snap.Remaining >= need {
		return true
	}
	if bot != nil && msg.Chat != nil {
		reply(sendContext{Bot: bot, ChatID: msg.Chat.ID, ReplyTo: msg.MessageID},
			gptBudgetRefusal(snap, need, what), false)
	}
	return false
}
