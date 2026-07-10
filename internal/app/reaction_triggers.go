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

type reactionKind string

const (
	reactionKindSupport reactionKind = "support"
	reactionKindHype    reactionKind = "hype"
	reactionKindFunny   reactionKind = "funny"
	reactionKindSad     reactionKind = "sad"
	reactionKindAngry   reactionKind = "angry"
)

var reactionWinnerPriority = []reactionKind{
	reactionKindAngry,
	reactionKindSad,
	reactionKindSupport,
	reactionKindHype,
	reactionKindFunny,
}

var reactionTriggerState = struct {
	mu            sync.Mutex
	counts        map[string]int
	messageCounts map[string]reactionKindCounts
}{
	counts:        make(map[string]int),
	messageCounts: make(map[string]reactionKindCounts),
}

type reactionKindCounts struct {
	Support int
	Hype    int
	Funny   int
	Sad     int
	Angry   int
}

func (c reactionKindCounts) value(kind reactionKind) int {
	switch kind {
	case reactionKindSupport:
		return c.Support
	case reactionKindHype:
		return c.Hype
	case reactionKindFunny:
		return c.Funny
	case reactionKindSad:
		return c.Sad
	case reactionKindAngry:
		return c.Angry
	default:
		return 0
	}
}

func (c *reactionKindCounts) add(kind reactionKind, delta int) {
	switch kind {
	case reactionKindSupport:
		c.Support += delta
		if c.Support < 0 {
			c.Support = 0
		}
	case reactionKindHype:
		c.Hype += delta
		if c.Hype < 0 {
			c.Hype = 0
		}
	case reactionKindFunny:
		c.Funny += delta
		if c.Funny < 0 {
			c.Funny = 0
		}
	case reactionKindSad:
		c.Sad += delta
		if c.Sad < 0 {
			c.Sad = 0
		}
	case reactionKindAngry:
		c.Angry += delta
		if c.Angry < 0 {
			c.Angry = 0
		}
	}
}

func (c reactionKindCounts) winner() (reactionKind, int, bool) {
	best := 0
	winner := reactionKind("")
	for _, kind := range reactionWinnerPriority {
		count := c.value(kind)
		if count > best {
			best = count
			winner = kind
		}
	}
	if best <= 0 {
		return "", 0, false
	}
	return winner, best, true
}

var supportReactionEmoji = map[string]struct{}{
	"👍": {}, "❤": {}, "🥰": {}, "👏": {}, "🤝": {}, "👌": {}, "🫡": {}, "💯": {},
}

var hypeReactionEmoji = map[string]struct{}{
	"🔥": {}, "❤‍🔥": {}, "🤩": {}, "😍": {}, "⚡": {}, "🏆": {},
}

var funnyReactionEmoji = map[string]struct{}{
	"😁": {}, "😂": {}, "🤡": {}, "🤯": {}, "🥴": {}, "🍾": {},
}

var sadReactionEmoji = map[string]struct{}{
	"😭": {}, "😢": {}, "😱": {}, "😨": {},
}

var angryReactionEmoji = map[string]struct{}{
	"👎": {}, "💩": {}, "🤮": {}, "🤬": {}, "😡": {},
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
	counts := reactionCountUpdateKinds(upd)
	winner, winnerCount, ok := counts.winner()
	if !ok {
		return
	}
	setReactionMessageCounts(chatID, upd.MessageID, counts)
	log.Printf("message_reaction_count chat=%d msg=%d support=%d hype=%d funny=%d sad=%d angry=%d winner=%s winner_count=%d",
		chatID, upd.MessageID, counts.Support, counts.Hype, counts.Funny, counts.Sad, counts.Angry, winner, winnerCount)
	handleReactionCounts(deps, chatID, upd.Chat, upd.MessageID, upd.Date, counts)
}

func handleMessageReactionUpdate(deps triggerHandlerDeps, upd *rawMessageReactionUpdate) {
	if upd == nil || upd.Chat == nil || upd.MessageID == 0 {
		return
	}
	chatID := upd.Chat.ID
	if !deps.Allowed.Allows(chatID) {
		return
	}
	oldCounts := reactionTypesKindCounts(upd.OldReaction)
	newCounts := reactionTypesKindCounts(upd.NewReaction)
	delta := reactionKindCounts{
		Support: newCounts.Support - oldCounts.Support,
		Hype:    newCounts.Hype - oldCounts.Hype,
		Funny:   newCounts.Funny - oldCounts.Funny,
		Sad:     newCounts.Sad - oldCounts.Sad,
		Angry:   newCounts.Angry - oldCounts.Angry,
	}
	if delta == (reactionKindCounts{}) {
		return
	}
	counts := applyReactionMessageDelta(chatID, upd.MessageID, delta)
	winner, winnerCount, ok := counts.winner()
	if !ok {
		log.Printf("message_reaction chat=%d msg=%d user=%d actor_chat=%d delta_support=%d delta_hype=%d delta_funny=%d delta_sad=%d delta_angry=%d support=%d hype=%d funny=%d sad=%d angry=%d winner=none winner_count=0",
			chatID, upd.MessageID, rawReactionUserID(upd), rawReactionActorChatID(upd), delta.Support, delta.Hype, delta.Funny, delta.Sad, delta.Angry, counts.Support, counts.Hype, counts.Funny, counts.Sad, counts.Angry)
		return
	}
	log.Printf("message_reaction chat=%d msg=%d user=%d actor_chat=%d delta_support=%d delta_hype=%d delta_funny=%d delta_sad=%d delta_angry=%d support=%d hype=%d funny=%d sad=%d angry=%d winner=%s winner_count=%d",
		chatID, upd.MessageID, rawReactionUserID(upd), rawReactionActorChatID(upd), delta.Support, delta.Hype, delta.Funny, delta.Sad, delta.Angry, counts.Support, counts.Hype, counts.Funny, counts.Sad, counts.Angry, winner, winnerCount)
	handleReactionCounts(deps, chatID, upd.Chat, upd.MessageID, upd.Date, counts)
}

func handleReactionCounts(deps triggerHandlerDeps, chatID int64, chat *rawChat, messageID int, date int64, counts reactionKindCounts) {
	winner, winnerCount, ok := counts.winner()
	if !ok {
		return
	}
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
		kind, ok := triggerReactionKind(tr.MatchType)
		if !ok || kind != winner {
			continue
		}
		threshold, ok := parseReactionThreshold(tr.MatchText)
		if !ok {
			if debugTriggerLogEnabled {
				log.Printf("skip reaction trigger=%d invalid threshold=%q", tr.ID, tr.MatchText)
			}
			continue
		}
		count := counts.value(kind)
		if !reactionThresholdCrossed(tr.ID, chatID, messageID, kind, threshold, count) {
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
		tr.CapturingText = strconv.Itoa(winnerCount)
		enqueueTriggerAction(deps.triggerActionDeps, deps.ActionQueue, msg, &tr, "", nil)
		log.Printf("reaction trigger queued trigger=%d chat=%d msg=%d kind=%s count=%d threshold=%d support=%d hype=%d funny=%d sad=%d angry=%d",
			tr.ID, chatID, messageID, kind, count, threshold, counts.Support, counts.Hype, counts.Funny, counts.Sad, counts.Angry)
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

func triggerReactionKind(mt model.MatchType) (reactionKind, bool) {
	switch match.NormalizeMatchType(string(mt)) {
	case model.MatchTypeSupportReactions:
		return reactionKindSupport, true
	case model.MatchTypeHypeReactions:
		return reactionKindHype, true
	case model.MatchTypeFunnyReactions:
		return reactionKindFunny, true
	case model.MatchTypeSadReactions:
		return reactionKindSad, true
	case model.MatchTypeAngryReactions:
		return reactionKindAngry, true
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

func reactionThresholdCrossed(triggerID, chatID int64, messageID int, kind reactionKind, threshold, current int) bool {
	key := strconv.FormatInt(chatID, 10) + ":" + strconv.Itoa(messageID) + ":" + strconv.FormatInt(triggerID, 10) + ":" + string(kind)
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

func setReactionMessageCounts(chatID int64, messageID int, counts reactionKindCounts) {
	reactionTriggerState.mu.Lock()
	defer reactionTriggerState.mu.Unlock()
	reactionTriggerState.messageCounts[reactionMessageKey(chatID, messageID)] = counts
}

func applyReactionMessageDelta(chatID int64, messageID int, delta reactionKindCounts) reactionKindCounts {
	reactionTriggerState.mu.Lock()
	defer reactionTriggerState.mu.Unlock()
	key := reactionMessageKey(chatID, messageID)
	counts := reactionTriggerState.messageCounts[key]
	counts.add(reactionKindSupport, delta.Support)
	counts.add(reactionKindHype, delta.Hype)
	counts.add(reactionKindFunny, delta.Funny)
	counts.add(reactionKindSad, delta.Sad)
	counts.add(reactionKindAngry, delta.Angry)
	reactionTriggerState.messageCounts[key] = counts
	return counts
}

func reactionCountUpdateKinds(upd *rawMessageReactionCountUpdate) reactionKindCounts {
	if upd == nil {
		return reactionKindCounts{}
	}
	var counts reactionKindCounts
	for _, r := range upd.Reactions {
		kind, ok := reactionEmojiKind(r.Type.Emoji)
		if !ok || r.TotalCount <= 0 {
			continue
		}
		counts.add(kind, r.TotalCount)
	}
	return counts
}

func reactionTypesKindCounts(reactions []rawReactionType) reactionKindCounts {
	var counts reactionKindCounts
	for _, r := range reactions {
		kind, ok := reactionEmojiKind(r.Emoji)
		if !ok {
			continue
		}
		counts.add(kind, 1)
	}
	return counts
}

func reactionEmojiKind(raw string) (reactionKind, bool) {
	emoji := strings.TrimSpace(raw)
	if emoji == "" {
		return "", false
	}
	if _, ok := supportReactionEmoji[emoji]; ok {
		return reactionKindSupport, true
	}
	if _, ok := hypeReactionEmoji[emoji]; ok {
		return reactionKindHype, true
	}
	if _, ok := funnyReactionEmoji[emoji]; ok {
		return reactionKindFunny, true
	}
	if _, ok := sadReactionEmoji[emoji]; ok {
		return reactionKindSad, true
	}
	if _, ok := angryReactionEmoji[emoji]; ok {
		return reactionKindAngry, true
	}
	return "", false
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
