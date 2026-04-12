package main

import (
	"math/rand"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

type TriggerEngine struct {
	randIntn func(int) int
}

type triggerSelectInput struct {
	Bot       *tgbotapi.BotAPI
	Msg       *tgbotapi.Message
	Text      string
	Triggers  []Trigger
	IsAdminFn func() bool
}

type triggerSelectNewMemberInput struct {
	Bot       *tgbotapi.BotAPI
	Msg       *tgbotapi.Message
	Triggers  []Trigger
	IsAdminFn func() bool
}

func NewTriggerEngine() *TriggerEngine {
	return &TriggerEngine{
		randIntn: rand.Intn,
	}
}

func (e *TriggerEngine) Select(input triggerSelectInput) *Trigger {
	if input.Msg == nil {
		return nil
	}
	adminChecked := false
	isAdmin := false

	for i := range input.Triggers {
		cand := input.Triggers[i]
		if !cand.Enabled {
			continue
		}
		if normalizeMatchType(cand.MatchType) == "new_member" {
			continue
		}
		matched, capture := TriggerMatchCapture(cand, input.Text)
		if !matched {
			continue
		}
		cand.CapturingText = capture
		if !triggerModeMatches(input.Bot, &cand, input.Msg) {
			continue
		}
		if cand.AdminMode != "anybody" {
			if !adminChecked {
				isAdmin = input.IsAdminFn()
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

func (e *TriggerEngine) SelectNewMember(input triggerSelectNewMemberInput) *Trigger {
	if input.Msg == nil {
		return nil
	}
	adminChecked := false
	isAdmin := false

	for i := range input.Triggers {
		cand := input.Triggers[i]
		if !cand.Enabled {
			continue
		}
		if normalizeMatchType(cand.MatchType) != "new_member" {
			continue
		}
		if !triggerModeMatches(input.Bot, &cand, input.Msg) {
			continue
		}
		if cand.AdminMode != "anybody" {
			if !adminChecked {
				isAdmin = input.IsAdminFn()
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
