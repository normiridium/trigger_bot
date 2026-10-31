package app

import (
	"errors"
	"log"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

type triggerActionTask struct {
	deps                triggerActionDeps
	msg                 *tgbotapi.Message
	trigger             Trigger
	userLimitLowTrigger *Trigger
}

type triggerActionQueue struct {
	ch chan triggerActionTask
}

var errTriggerActionQueueFull = errors.New("trigger action queue is full")

// triggerActionOverflow caps how many tasks may run outside the queue when the
// channel is full, so a burst cannot spawn an unbounded number of goroutines.
var triggerActionOverflow = make(chan struct{}, defaultTriggerActionWorkers()*4)

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
				runWorkerTask("trigger_action", func() {
					tr := task.trigger
					handleTriggerActionForMessage(task.deps, task.msg, &tr, task.userLimitLowTrigger)
				})
			}
		}()
	}
	return q
}

func (q *triggerActionQueue) Enqueue(task triggerActionTask) {
	if q == nil {
		tr := task.trigger
		runWorkerTask("trigger_action_direct", func() {
			handleTriggerActionForMessage(task.deps, task.msg, &tr, task.userLimitLowTrigger)
		})
		return
	}
	select {
	case q.ch <- task:
		return
	default:
	}
	select {
	case triggerActionOverflow <- struct{}{}:
		if debugTriggerLogEnabled {
			log.Printf("trigger action queue overflow, running outside queue trigger=%d chat=%d", task.trigger.ID, msgChatID(task.msg))
		}
		go func(t triggerActionTask) {
			defer func() { <-triggerActionOverflow }()
			runWorkerTask("trigger_action_overflow", func() {
				tr := t.trigger
				handleTriggerActionForMessage(t.deps, t.msg, &tr, t.userLimitLowTrigger)
			})
		}(task)
	default:
		log.Printf("trigger action queue full and overflow exhausted, dropping trigger=%d chat=%d", task.trigger.ID, msgChatID(task.msg))
		if task.deps.Bot != nil && task.msg != nil && task.msg.Chat != nil {
			reportChatFailure(task.deps.Bot, task.msg.Chat.ID, "бот перегружен", errTriggerActionQueueFull)
		}
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

func enqueueTriggerAction(deps triggerActionDeps, q *triggerActionQueue, msg *tgbotapi.Message, tr *Trigger, userLimitLowTrigger *Trigger) {
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
		userLimitLowTrigger: lowTriggerCopy,
	}
	q.Enqueue(task)
}
