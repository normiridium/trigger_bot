package engine

import (
	"math/rand"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"trigger-admin-bot/internal/match"
	"trigger-admin-bot/internal/model"
)

type TriggerEngine struct {
	randIntn func(int) int
}

type SelectInput struct {
	Bot       *tgbotapi.BotAPI
	Msg       *tgbotapi.Message
	Text      string
	Triggers  []model.Trigger
	IsAdminFn func() bool
}

type SelectNewMemberInput struct {
	Bot       *tgbotapi.BotAPI
	Msg       *tgbotapi.Message
	Triggers  []model.Trigger
	IsAdminFn func() bool
}

func NewTriggerEngine() *TriggerEngine {
	return &TriggerEngine{
		randIntn: rand.Intn,
	}
}

func (e *TriggerEngine) Select(input SelectInput) *model.Trigger {
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
		if match.NormalizeMatchType(string(cand.MatchType)) == "new_member" {
			continue
		}
		matched, capture := match.TriggerMatchCapture(cand, input.Text)
		if !matched {
			continue
		}
		cand.CapturingText = capture
		if !TriggerModeMatches(input.Bot, &cand, input.Msg) {
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

func (e *TriggerEngine) SelectNewMember(input SelectNewMemberInput) *model.Trigger {
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
		if match.NormalizeMatchType(string(cand.MatchType)) != "new_member" {
			continue
		}
		if !TriggerModeMatches(input.Bot, &cand, input.Msg) {
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

func TriggerModeMatches(bot *tgbotapi.BotAPI, tr *model.Trigger, msg *tgbotapi.Message) bool {
	if tr == nil || msg == nil {
		return false
	}
	mode := tr.TriggerMode
	switch mode {
	case "only_replies":
		return msg.ReplyToMessage != nil
	case "only_replies_to_any_bot":
		return msg.ReplyToMessage != nil && msg.ReplyToMessage.From != nil && msg.ReplyToMessage.From.IsBot
	case "only_replies_to_combot":
		// Legacy storage key kept for compatibility, actual behavior:
		// trigger only on replies to this bot's own messages.
		if msg.ReplyToMessage == nil || msg.ReplyToMessage.From == nil {
			return false
		}
		if bot == nil {
			return false
		}
		return msg.ReplyToMessage.From.IsBot && msg.ReplyToMessage.From.ID == bot.Self.ID
	case "only_replies_to_combot_no_media":
		// Same as reply-to-self mode but ignores media on both sides:
		// the incoming message and the message being replied to.
		// Needed for text-only conversations with the bot.
		if msg.ReplyToMessage == nil || msg.ReplyToMessage.From == nil {
			return false
		}
		if bot == nil {
			return false
		}
		if hasMessageMedia(msg) || hasMessageMedia(msg.ReplyToMessage) {
			return false
		}
		return msg.ReplyToMessage.From.IsBot && msg.ReplyToMessage.From.ID == bot.Self.ID
	case "never_on_replies":
		return msg.ReplyToMessage == nil
	case "command_reply":
		return msg.IsCommand()
	default:
		return true
	}
}

func hasMessageMedia(msg *tgbotapi.Message) bool {
	if msg == nil {
		return false
	}
	if len(msg.Photo) > 0 || msg.Audio != nil || msg.Video != nil || msg.Animation != nil || msg.Voice != nil || msg.VideoNote != nil {
		return true
	}
	if msg.Document != nil {
		mime := msg.Document.MimeType
		if len(mime) >= 6 && mime[:6] == "image/" {
			return true
		}
		if len(mime) >= 6 && mime[:6] == "audio/" {
			return true
		}
		if len(mime) >= 6 && mime[:6] == "video/" {
			return true
		}
	}
	return false
}
