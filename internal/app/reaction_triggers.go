package app

import (
	"log"
	"strconv"
	"strings"
	"sync"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"trigger-admin-bot/internal/match"
	"trigger-admin-bot/internal/model"
)

type reactionPolarity string

const (
	reactionPolarityPositive reactionPolarity = "positive"
	reactionPolarityNegative reactionPolarity = "negative"
)

var reactionTriggerState = struct {
	mu            sync.Mutex
	counts        map[string]int
	messageCounts map[string]reactionMessageCounts
}{
	counts:        make(map[string]int),
	messageCounts: make(map[string]reactionMessageCounts),
}

type reactionMessageCounts struct {
	Positive int
	Negative int
}

var positiveReactionEmoji = map[string]struct{}{
	"👍": {}, "❤": {}, "🔥": {}, "🥰": {}, "👏": {}, "😁": {}, "🤩": {}, "😍": {},
	"❤‍🔥": {}, "💯": {}, "⚡": {}, "🏆": {}, "🍾": {}, "🤝": {}, "👌": {}, "💋": {}, "🫡": {},
}

var negativeReactionEmoji = map[string]struct{}{
	"👎": {}, "💩": {}, "🤮": {}, "🤬": {}, "😡": {}, "😭": {}, "😢": {}, "😱": {}, "😨": {}, "🤯": {},
}

func isStartupBacklogReactionCount(upd *rawMessageReactionCountUpdate, startedAtUnix int64) bool {
	if upd == nil || upd.Date <= 0 || startedAtUnix <= 0 {
		return false
	}
	return upd.Date < startupBacklogCutoff(startedAtUnix)
}

func isStartupBacklogMessageReaction(upd *rawMessageReactionUpdate, startedAtUnix int64) bool {
	if upd == nil || upd.Date <= 0 || startedAtUnix <= 0 {
		return false
	}
	return upd.Date < startupBacklogCutoff(startedAtUnix)
}

func handleReactionCountUpdate(deps triggerHandlerDeps, upd *rawMessageReactionCountUpdate) {
	if upd == nil || upd.Chat == nil || upd.MessageID == 0 {
		return
	}
	chatID := upd.Chat.ID
	if !deps.Allowed.Allows(chatID) {
		return
	}
	positive, negative := reactionPolarityCounts(upd)
	if positive <= 0 && negative <= 0 {
		return
	}
	setReactionMessageCounts(chatID, upd.MessageID, positive, negative)
	log.Printf("message_reaction_count chat=%d msg=%d positive=%d negative=%d", chatID, upd.MessageID, positive, negative)
	handleReactionCounts(deps, chatID, upd.Chat, upd.MessageID, upd.Date, positive, negative)
}

func handleMessageReactionUpdate(deps triggerHandlerDeps, upd *rawMessageReactionUpdate) {
	if upd == nil || upd.Chat == nil || upd.MessageID == 0 {
		return
	}
	chatID := upd.Chat.ID
	if !deps.Allowed.Allows(chatID) {
		return
	}
	oldPositive, oldNegative := reactionTypesPolarityCounts(upd.OldReaction)
	newPositive, newNegative := reactionTypesPolarityCounts(upd.NewReaction)
	deltaPositive := newPositive - oldPositive
	deltaNegative := newNegative - oldNegative
	if deltaPositive == 0 && deltaNegative == 0 {
		return
	}
	counts := applyReactionMessageDelta(chatID, upd.MessageID, deltaPositive, deltaNegative)
	log.Printf("message_reaction chat=%d msg=%d user=%d actor_chat=%d delta_positive=%d delta_negative=%d positive=%d negative=%d",
		chatID, upd.MessageID, rawReactionUserID(upd), rawReactionActorChatID(upd), deltaPositive, deltaNegative, counts.Positive, counts.Negative)
	handleReactionCounts(deps, chatID, upd.Chat, upd.MessageID, upd.Date, counts.Positive, counts.Negative)
}

func handleReactionCounts(deps triggerHandlerDeps, chatID int64, chat *rawChat, messageID int, date int64, positive, negative int) {
	items, err := deps.Store.ListTriggersCached()
	if err != nil {
		log.Printf("list triggers for reaction count failed: %v", err)
		return
	}
	msg := &tgbotapi.Message{
		MessageID: messageID,
		Date:      int(date),
		Chat: &tgbotapi.Chat{
			ID:    chatID,
			Type:  chat.Type,
			Title: chat.Title,
		},
		From: &tgbotapi.User{
			ID:        0,
			FirstName: "reaction",
			IsBot:     false,
		},
	}
	for i := range items {
		tr := items[i]
		if !tr.Enabled {
			continue
		}
		polarity, ok := triggerReactionPolarity(tr.MatchType)
		if !ok {
			continue
		}
		threshold, ok := parseReactionThreshold(tr.MatchText)
		if !ok {
			if debugTriggerLogEnabled {
				log.Printf("skip reaction trigger=%d invalid threshold=%q", tr.ID, tr.MatchText)
			}
			continue
		}
		count := positive
		if polarity == reactionPolarityNegative {
			count = negative
		}
		if !reactionThresholdCrossed(tr.ID, chatID, messageID, polarity, threshold, count) {
			continue
		}
		if !engineModeMatchesReaction(deps.Bot, &tr, msg) {
			continue
		}
		if tr.AdminMode == model.AdminModeAdmins {
			if debugTriggerLogEnabled {
				log.Printf("skip reaction trigger=%d admin_mode=admins unsupported for aggregate reaction counts", tr.ID)
			}
			continue
		}
		if deps.Engine != nil && !deps.Engine.ChanceAllowed(tr.ID, chatID, tr.Chance) {
			continue
		}
		tr.CapturingText = strconv.Itoa(count)
		enqueueTriggerAction(deps.triggerActionDeps, deps.ActionQueue, msg, &tr, "", nil)
		log.Printf("reaction trigger queued trigger=%d chat=%d msg=%d polarity=%s count=%d threshold=%d", tr.ID, chatID, messageID, polarity, count, threshold)
	}
}

func engineModeMatchesReaction(bot *tgbotapi.BotAPI, tr *model.Trigger, msg *tgbotapi.Message) bool {
	_ = bot
	_ = msg
	switch tr.TriggerMode {
	case model.TriggerModeOnlyReplies, model.TriggerModeOnlyRepliesToBot, model.TriggerModeOnlyRepliesToSelf, model.TriggerModeOnlyRepliesToSelfNoMedia, model.TriggerModeCommandReply:
		return false
	default:
		return true
	}
}

func triggerReactionPolarity(mt model.MatchType) (reactionPolarity, bool) {
	switch match.NormalizeMatchType(string(mt)) {
	case model.MatchTypePositiveReactions:
		return reactionPolarityPositive, true
	case model.MatchTypeNegativeReactions:
		return reactionPolarityNegative, true
	default:
		return "", false
	}
}

func parseReactionThreshold(raw string) (int, bool) {
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

func reactionThresholdCrossed(triggerID, chatID int64, messageID int, polarity reactionPolarity, threshold, current int) bool {
	key := strconv.FormatInt(chatID, 10) + ":" + strconv.Itoa(messageID) + ":" + strconv.FormatInt(triggerID, 10) + ":" + string(polarity)
	reactionTriggerState.mu.Lock()
	defer reactionTriggerState.mu.Unlock()
	prev, seen := reactionTriggerState.counts[key]
	reactionTriggerState.counts[key] = current
	if current <= threshold {
		return false
	}
	return !seen || prev <= threshold
}

func reactionMessageKey(chatID int64, messageID int) string {
	return strconv.FormatInt(chatID, 10) + ":" + strconv.Itoa(messageID)
}

func setReactionMessageCounts(chatID int64, messageID int, positive, negative int) {
	reactionTriggerState.mu.Lock()
	defer reactionTriggerState.mu.Unlock()
	reactionTriggerState.messageCounts[reactionMessageKey(chatID, messageID)] = reactionMessageCounts{
		Positive: positive,
		Negative: negative,
	}
}

func applyReactionMessageDelta(chatID int64, messageID int, deltaPositive, deltaNegative int) reactionMessageCounts {
	reactionTriggerState.mu.Lock()
	defer reactionTriggerState.mu.Unlock()
	key := reactionMessageKey(chatID, messageID)
	counts := reactionTriggerState.messageCounts[key]
	counts.Positive += deltaPositive
	counts.Negative += deltaNegative
	if counts.Positive < 0 {
		counts.Positive = 0
	}
	if counts.Negative < 0 {
		counts.Negative = 0
	}
	reactionTriggerState.messageCounts[key] = counts
	return counts
}

func reactionPolarityCounts(upd *rawMessageReactionCountUpdate) (positive int, negative int) {
	if upd == nil {
		return 0, 0
	}
	for _, r := range upd.Reactions {
		emoji := strings.TrimSpace(r.Type.Emoji)
		if emoji == "" || r.TotalCount <= 0 {
			continue
		}
		if _, ok := positiveReactionEmoji[emoji]; ok {
			positive += r.TotalCount
			continue
		}
		if _, ok := negativeReactionEmoji[emoji]; ok {
			negative += r.TotalCount
		}
	}
	return positive, negative
}

func reactionTypesPolarityCounts(reactions []rawReactionType) (positive int, negative int) {
	for _, r := range reactions {
		emoji := strings.TrimSpace(r.Emoji)
		if emoji == "" {
			continue
		}
		if _, ok := positiveReactionEmoji[emoji]; ok {
			positive++
			continue
		}
		if _, ok := negativeReactionEmoji[emoji]; ok {
			negative++
		}
	}
	return positive, negative
}

func rawReactionUserID(upd *rawMessageReactionUpdate) int64 {
	if upd == nil || upd.User == nil {
		return 0
	}
	return upd.User.ID
}

func rawReactionActorChatID(upd *rawMessageReactionUpdate) int64 {
	if upd == nil || upd.ActorChat == nil {
		return 0
	}
	return upd.ActorChat.ID
}
