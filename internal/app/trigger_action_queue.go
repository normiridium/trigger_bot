package app

import (
	"log"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

type triggerActionTask struct {
	deps                triggerActionDeps
	msg                 *tgbotapi.Message
	trigger             Trigger
	recentBefore        string
	userLimitLowTrigger *Trigger
}

type triggerActionQueue struct {
	ch chan triggerActionTask
}

func newTriggerActionQueue(workers, capacity int) *triggerActionQueue {
	if workers <= 0 {
		workers = 1
	}
	if capacity <= 0 {
		capacity = 128
	}
	q := &triggerActionQueue{ch: make(chan triggerActionTask, capacity)}
	for i := 0; i < workers; i++ {
		go func() {
			for task := range q.ch {
				tr := task.trigger
				handleTriggerActionForMessage(task.deps, task.msg, &tr, task.recentBefore, task.userLimitLowTrigger)
			}
		}()
	}
	return q
}

func (q *triggerActionQueue) Enqueue(task triggerActionTask) {
	if q == nil {
		tr := task.trigger
		handleTriggerActionForMessage(task.deps, task.msg, &tr, task.recentBefore, task.userLimitLowTrigger)
		return
	}
	select {
	case q.ch <- task:
		return
	default:
		// Queue is full: avoid dropping trigger execution.
		go func(t triggerActionTask) {
			if debugTriggerLogEnabled {
				log.Printf("trigger action queue overflow, fallback goroutine trigger=%d chat=%d", t.trigger.ID, msgChatID(t.msg))
			}
			tr := t.trigger
			handleTriggerActionForMessage(t.deps, t.msg, &tr, t.recentBefore, t.userLimitLowTrigger)
		}(task)
	}
}

func msgChatID(msg *tgbotapi.Message) int64 {
	if msg == nil || msg.Chat == nil {
		return 0
	}
	return msg.Chat.ID
}

func defaultTriggerActionWorkers() int {
	return envInt("TRIGGER_ACTION_WORKERS", 8)
}

func defaultTriggerActionQueueSize() int {
	return envInt("TRIGGER_ACTION_QUEUE", 256)
}

func enqueueTriggerAction(deps triggerActionDeps, q *triggerActionQueue, msg *tgbotapi.Message, tr *Trigger, recentBefore string, userLimitLowTrigger *Trigger) {
	if msg == nil || tr == nil {
		return
	}
	var lowTriggerCopy *Trigger
	if userLimitLowTrigger != nil {
		cp := *userLimitLowTrigger
		lowTriggerCopy = &cp
	}
	task := triggerActionTask{
		deps:                deps,
		msg:                 msg,
		trigger:             *tr,
		recentBefore:        recentBefore,
		userLimitLowTrigger: lowTriggerCopy,
	}
	q.Enqueue(task)
}
