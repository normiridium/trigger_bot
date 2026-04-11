package main

import (
	"math/rand"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

type TriggerEngine struct {
	randIntn func(int) int
}

func NewTriggerEngine() *TriggerEngine {
	return &TriggerEngine{
		randIntn: rand.Intn,
	}
}

func (e *TriggerEngine) Select(
	bot *tgbotapi.BotAPI,
	msg *tgbotapi.Message,
	text string,
	triggers []Trigger,
	isAdminFn func() bool,
) *Trigger {
	if msg == nil {
		return nil
	}
	adminChecked := false
	isAdmin := false

	for i := range triggers {
		cand := triggers[i]
		if !cand.Enabled {
			continue
		}
		if normalizeMatchType(cand.MatchType) == "new_member" {
			continue
		}
		matched, capture := TriggerMatchCapture(cand, text)
		if !matched {
			continue
		}
		cand.CapturingText = capture
		if !triggerModeMatches(bot, &cand, msg) {
			continue
		}
		if cand.AdminMode != "anybody" {
			if !adminChecked {
				isAdmin = isAdminFn()
				adminChecked = true
			}
			if cand.AdminMode == "admins" && !isAdmin {
				continue
			}
			if cand.AdminMode == "not_admins" && isAdmin {
				continue
			}
		}
		if cand.Chance < 100 && e.randIntn(100) >= cand.Chance {
			continue
		}
		return &cand
	}
	return nil
}

func (e *TriggerEngine) SelectNewMember(
	bot *tgbotapi.BotAPI,
	msg *tgbotapi.Message,
	triggers []Trigger,
	isAdminFn func() bool,
) *Trigger {
	if msg == nil {
		return nil
	}
	adminChecked := false
	isAdmin := false

	for i := range triggers {
		cand := triggers[i]
		if !cand.Enabled {
			continue
		}
		if normalizeMatchType(cand.MatchType) != "new_member" {
			continue
		}
		if !triggerModeMatches(bot, &cand, msg) {
			continue
		}
		if cand.AdminMode != "anybody" {
			if !adminChecked {
				isAdmin = isAdminFn()
				adminChecked = true
			}
			if cand.AdminMode == "admins" && !isAdmin {
				continue
			}
			if cand.AdminMode == "not_admins" && isAdmin {
				continue
			}
		}
		if cand.Chance < 100 && e.randIntn(100) >= cand.Chance {
			continue
		}
		return &cand
	}
	return nil
}
