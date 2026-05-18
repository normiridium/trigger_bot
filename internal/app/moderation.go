package app

import (
	crand "crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"html"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

type moderationRequest struct {
	Action      string
	Silent      bool
	Targets     []string
	Duration    time.Duration
	DurationRaw string
	Reason      string
}

const (
	moderationConfirmQuestionCount = 3
)

type moderationConfirmState struct {
	ID           string
	ChatID       int64
	AdminID      int64
	SenderTag    string
	Req          moderationRequest
	Targets      []int64
	TargetLabels []string
	Answers      [moderationConfirmQuestionCount]bool
	CreatedAt    time.Time
}

type moderationConfirmManager struct {
	mu    sync.Mutex
	ttl   time.Duration
	items map[string]moderationConfirmState
}

func newModerationConfirmManager(ttl time.Duration) *moderationConfirmManager {
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}
	return &moderationConfirmManager{
		ttl:   ttl,
		items: make(map[string]moderationConfirmState),
	}
}

func newModerationConfirmID() string {
	var b [6]byte
	if _, err := crand.Read(b[:]); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return hex.EncodeToString(b[:])
}

func (m *moderationConfirmManager) cleanupLocked(now time.Time) {
	if m == nil {
		return
	}
	for id, st := range m.items {
		if now.Sub(st.CreatedAt) > m.ttl {
			delete(m.items, id)
		}
	}
}

func (m *moderationConfirmManager) Create(st moderationConfirmState) string {
	if m == nil {
		return ""
	}
	now := time.Now()
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cleanupLocked(now)
	st.ID = newModerationConfirmID()
	st.CreatedAt = now
	m.items[st.ID] = st
	return st.ID
}

func (m *moderationConfirmManager) Get(id string) (moderationConfirmState, bool) {
	if m == nil {
		return moderationConfirmState{}, false
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return moderationConfirmState{}, false
	}
	now := time.Now()
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cleanupLocked(now)
	st, ok := m.items[id]
	return st, ok
}

func (m *moderationConfirmManager) Toggle(id string, idx int) (moderationConfirmState, bool) {
	if m == nil || idx < 0 || idx >= moderationConfirmQuestionCount {
		return moderationConfirmState{}, false
	}
	id = strings.TrimSpace(id)
	now := time.Now()
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cleanupLocked(now)
	st, ok := m.items[id]
	if !ok {
		return moderationConfirmState{}, false
	}
	st.Answers[idx] = !st.Answers[idx]
	m.items[id] = st
	return st, true
}

func (m *moderationConfirmManager) Delete(id string) {
	if m == nil {
		return
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return
	}
	m.mu.Lock()
	delete(m.items, id)
	m.mu.Unlock()
}

func genderedModerationVerb(tag, male, female, unknown string) string {
	return resolveGenderVariant(tag, genderVariants{
		Male:    male,
		Female:  female,
		Neuter:  unknown,
		Plural:  unknown,
		Unknown: unknown,
	})
}

func moderationActionVerb(action, senderTag string) string {
	switch strings.TrimSpace(action) {
	case "ban":
		return genderedModerationVerb(senderTag, "забанил", "забанила", "забанил(а)")
	case "unban":
		return genderedModerationVerb(senderTag, "разбанил", "разбанила", "разбанил(а)")
	case "mute":
		return genderedModerationVerb(senderTag, "замьютил", "замьютила", "замьютил(а)")
	case "unmute":
		return genderedModerationVerb(senderTag, "размьютил", "размьютила", "размьютил(а)")
	case "kick":
		return genderedModerationVerb(senderTag, "кикнул", "кикнула", "кикнул(а)")
	default:
		return genderedModerationVerb(senderTag, "изменил", "изменила", "изменил(а)")
	}
}

func moderationReadonlyStateVerb(turnOn bool, senderTag string) string {
	if turnOn {
		return genderedModerationVerb(senderTag, "включил режим только чтения", "включила режим только чтения", "включил(а) режим только чтения")
	}
	return genderedModerationVerb(senderTag, "выключил режим только чтения", "выключила режим только чтения", "выключил(а) режим только чтения")
}

func parseModerationCommand(text string) (moderationRequest, bool, error) {
	raw := strings.TrimSpace(text)
	if raw == "" || (!strings.HasPrefix(raw, "!") && !strings.HasPrefix(raw, "/")) {
		return moderationRequest{}, false, nil
	}
	firstLine := raw
	reason := ""
	if nl := strings.IndexByte(raw, '\n'); nl >= 0 {
		firstLine = strings.TrimSpace(raw[:nl])
		reason = strings.TrimSpace(raw[nl+1:])
	}
	parts := strings.Fields(firstLine)
	if len(parts) == 0 {
		return moderationRequest{}, false, nil
	}
	cmd := normalizeModerationCommandToken(parts[0])
	args := parts[1:]
	out := moderationRequest{Reason: reason}

	switch cmd {
	case "!ban", "/ban":
		out.Action = "ban"
	case "!sban", "/sban":
		out.Action = "ban"
		out.Silent = true
	case "!unban", "/unban":
		out.Action = "unban"
	case "!sunban", "/sunban":
		out.Action = "unban"
		out.Silent = true
	case "!mute", "/mute":
		out.Action = "mute"
	case "!smute", "/smute":
		out.Action = "mute"
		out.Silent = true
	case "!unmute", "/unmute":
		out.Action = "unmute"
	case "!kick", "/kick":
		out.Action = "kick"
	case "!skick", "/skick":
		out.Action = "kick"
		out.Silent = true
	case "!readonly", "!ro", "!channelmode", "/readonly", "/ro", "/channelmode":
		out.Action = cmdReadonly
	case "!reload_admins", "/reload_admins":
		out.Action = cmdReloadAdmins
	default:
		return moderationRequest{}, false, nil
	}

	if out.Action == cmdReloadAdmins {
		return out, true, nil
	}

	if out.Action == cmdReadonly {
		if len(args) > 0 {
			if d, ok := parseModerationDurationToken(args[0]); ok {
				out.Duration = d
				out.DurationRaw = strings.ToLower(strings.TrimSpace(args[0]))
			}
		}
		return out, true, nil
	}

	filtered := make([]string, 0, len(args))
	for _, a := range args {
		v := strings.TrimSpace(strings.Trim(a, ",;"))
		if v != "" {
			if (out.Action == "ban" || out.Action == "mute") && out.Duration == 0 {
				if d, ok := parseModerationDurationToken(v); ok {
					out.Duration = d
					out.DurationRaw = strings.ToLower(strings.TrimSpace(v))
					continue
				}
			}
			filtered = append(filtered, v)
		}
	}
	out.Targets = append(out.Targets, filtered...)
	return out, true, nil
}

func htmlUserLink(label string, userID int64) string {
	name := strings.TrimSpace(label)
	if name == "" {
		name = "Участник без имени"
	}
	return fmt.Sprintf(`<a href="tg://user?id=%d">%s</a>`, userID, html.EscapeString(name))
}

func applyReadonly(bot *tgbotapi.BotAPI, chatID int64, on bool) error {
	if bot == nil || chatID == 0 {
		return errors.New("invalid readonly params")
	}
	perm := &tgbotapi.ChatPermissions{}
	if !on {
		perm = &tgbotapi.ChatPermissions{
			CanSendMessages:       true,
			CanSendMediaMessages:  true,
			CanSendPolls:          true,
			CanSendOtherMessages:  true,
			CanAddWebPagePreviews: true,
			CanChangeInfo:         true,
			CanInviteUsers:        true,
			CanPinMessages:        true,
		}
	}
	_, err := bot.Request(tgbotapi.SetChatPermissionsConfig{
		ChatConfig:  tgbotapi.ChatConfig{ChatID: chatID},
		Permissions: perm,
	})
	return err
}

func unmuteChatMember(bot *tgbotapi.BotAPI, chatID, userID int64) error {
	if bot == nil || chatID == 0 || userID == 0 {
		return errors.New("invalid unmute params")
	}
	_, err := bot.Request(tgbotapi.RestrictChatMemberConfig{
		ChatMemberConfig: tgbotapi.ChatMemberConfig{ChatID: chatID, UserID: userID},
		Permissions: &tgbotapi.ChatPermissions{
			CanSendMessages:       true,
			CanSendMediaMessages:  true,
			CanSendPolls:          true,
			CanSendOtherMessages:  true,
			CanAddWebPagePreviews: true,
		},
		UntilDate: 0,
	})
	return err
}

func runScheduledUnmutes(bot *tgbotapi.BotAPI, store *Store) {
	if bot == nil || store == nil {
		return
	}
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		nowUnix := time.Now().Unix()
		items, err := store.ListDueScheduledUnmutes(nowUnix, 64)
		if err != nil {
			log.Printf("scheduled unmute list failed: %v", err)
			continue
		}
		for _, it := range items {
			if it.ChatID == 0 || it.UserID == 0 {
				continue
			}
			if err := unmuteChatMember(bot, it.ChatID, it.UserID); err != nil {
				if debugTriggerLogEnabled {
					log.Printf("scheduled unmute failed chat=%d user=%d: %v", it.ChatID, it.UserID, err)
				}
				continue
			}
			if err := store.DeleteScheduledUnmute(it.ChatID, it.UserID); err != nil {
				log.Printf("scheduled unmute cleanup failed chat=%d user=%d: %v", it.ChatID, it.UserID, err)
			}
		}
	}
}

type moderationContext struct {
	Bot        *tgbotapi.BotAPI
	AdminCache *adminStatusCache
	UserIndex  *chatUserIndex
	Readonly   *readonlyManager
	Store      *Store
	Confirms   *moderationConfirmManager
}

func syncAdminCacheForUser(bot *tgbotapi.BotAPI, cache *adminStatusCache, chatID, userID int64) bool {
	if bot == nil || cache == nil || chatID == 0 || userID == 0 {
		return false
	}
	member, err := bot.GetChatMember(tgbotapi.GetChatMemberConfig{
		ChatConfigWithUser: tgbotapi.ChatConfigWithUser{
			ChatID: chatID,
			UserID: userID,
		},
	})
	if err != nil {
		return false
	}
	isAdmin := member.Status == "administrator" || member.Status == "creator"
	now := time.Now()
	cache.setCached(chatID, userID, isAdmin, now)
	if cache.store != nil {
		_ = cache.store.UpsertChatAdminCache(chatID, userID, isAdmin, now.Unix())
	}
	return isAdmin
}

func moderationRequiresConfirm(action string) bool {
	switch action {
	case "ban", "mute", "kick":
		return true
	default:
		return false
	}
}

func moderationConfirmQuestions(action string) [moderationConfirmQuestionCount]string {
	questions := [moderationConfirmQuestionCount]string{
		"Нарушение правил действительно есть",
		"Это не первое нарушение",
		"Наказание соразмерно ситуации",
	}
	if action == cmdMute {
		questions[1] = "Пользователь получал предупреждение"
	}
	return questions
}

func moderationActionLabel(req moderationRequest) string {
	switch req.Action {
	case "ban":
		if req.Duration > 0 {
			return "Бан на " + humanModerationDurationRU(req.Duration, req.DurationRaw)
		}
		return "Бан"
	case "mute":
		if req.Duration > 0 {
			return "Мут на " + humanModerationDurationRU(req.Duration, req.DurationRaw)
		}
		return "Мут"
	case "kick":
		return "Кик"
	case "unban":
		return "Разбан"
	case "unmute":
		return "Размут"
	default:
		return strings.ToUpper(req.Action)
	}
}

func moderationConfirmAllChecked(st moderationConfirmState) bool {
	for _, v := range st.Answers {
		if !v {
			return false
		}
	}
	return true
}

func formatModerationTargets(labels []string) string {
	if len(labels) == 0 {
		return "не указаны"
	}
	return strings.Join(labels, ", ")
}

func buildModerationConfirmMessage(st moderationConfirmState) (string, tgbotapi.InlineKeyboardMarkup) {
	questions := moderationConfirmQuestions(st.Req.Action)
	lines := make([]string, 0, 10)
	lines = append(lines, "Подтверди действие перед применением.")
	lines = append(lines, "Действие: "+moderationActionLabel(st.Req))
	lines = append(lines, "Цель: "+formatModerationTargets(st.TargetLabels))
	lines = append(lines, "")
	for i, q := range questions {
		prefix := "⬜"
		if st.Answers[i] {
			prefix = "✅"
		}
		lines = append(lines, fmt.Sprintf("%s %s", prefix, q))
	}
	if st.Req.Reason != "" {
		lines = append(lines, "")
		lines = append(lines, "Причина: "+st.Req.Reason)
	}

	rows := make([][]tgbotapi.InlineKeyboardButton, 0, moderationConfirmQuestionCount+1)
	for i, q := range questions {
		data := fmt.Sprintf("modq|t|%s|%d", st.ID, i)
		rows = append(rows, tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(q, data),
		))
	}
	applyText := "Применить"
	if moderationConfirmAllChecked(st) {
		applyText = "✅ Применить"
	}
	rows = append(rows, tgbotapi.NewInlineKeyboardRow(
		tgbotapi.NewInlineKeyboardButtonData(applyText, "modq|a|"+st.ID),
		tgbotapi.NewInlineKeyboardButtonData("Отмена", "modq|c|"+st.ID),
	))
	return strings.Join(lines, "\n"), tgbotapi.NewInlineKeyboardMarkup(rows...)
}

func applyModerationToTargets(bot *tgbotapi.BotAPI, store *Store, chatID int64, req moderationRequest, targets []int64) error {
	if bot == nil || chatID == 0 {
		return errors.New("invalid moderation apply params")
	}
	var firstErr error
	for _, uid := range targets {
		if uid == 0 {
			continue
		}
		cfgMember := tgbotapi.ChatMemberConfig{ChatID: chatID, UserID: uid}
		var err error
		switch req.Action {
		case "ban":
			cfg := tgbotapi.BanChatMemberConfig{
				ChatMemberConfig: cfgMember,
				RevokeMessages:   true,
			}
			if req.Duration > 0 {
				cfg.UntilDate = time.Now().Add(req.Duration).Unix()
			}
			_, err = bot.Request(cfg)
		case "unban":
			_, err = bot.Request(tgbotapi.UnbanChatMemberConfig{
				ChatMemberConfig: cfgMember,
				OnlyIfBanned:     false,
			})
		case "mute":
			cfg := tgbotapi.RestrictChatMemberConfig{
				ChatMemberConfig: cfgMember,
				Permissions:      &tgbotapi.ChatPermissions{},
			}
			if req.Duration > 0 {
				cfg.UntilDate = time.Now().Add(req.Duration).Unix()
			}
			_, err = bot.Request(cfg)
			if err == nil {
				if store != nil && req.Duration > 0 {
					unmuteAt := time.Now().Add(req.Duration).Unix()
					if e := store.UpsertScheduledUnmute(chatID, uid, unmuteAt); e != nil {
						log.Printf("scheduled unmute save failed chat=%d user=%d: %v", chatID, uid, e)
					}
				} else if store != nil {
					_ = store.DeleteScheduledUnmute(chatID, uid)
				}
			}
		case "unmute":
			err = unmuteChatMember(bot, chatID, uid)
			if err == nil && store != nil {
				_ = store.DeleteScheduledUnmute(chatID, uid)
			}
		case "kick":
			_, err = bot.Request(tgbotapi.BanChatMemberConfig{
				ChatMemberConfig: cfgMember,
				UntilDate:        time.Now().Add(45 * time.Second).Unix(),
				RevokeMessages:   false,
			})
			if err == nil {
				_, err = bot.Request(tgbotapi.UnbanChatMemberConfig{
					ChatMemberConfig: cfgMember,
					OnlyIfBanned:     true,
				})
			}
		default:
			err = fmt.Errorf("unsupported moderation action: %s", req.Action)
		}
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func buildModerationResultText(userIndex *chatUserIndex, chatID int64, moderatorID int64, senderTag string, req moderationRequest, targets []int64, targetLabels []string) string {
	if userIndex == nil {
		return ""
	}
	modLabel := userIndex.Display(chatID, moderatorID)
	modLink := htmlUserLink(modLabel, moderatorID)
	verb := moderationActionVerb(req.Action, senderTag)
	targetLinks := make([]string, 0, len(targets))
	for i, uid := range targets {
		lbl := userIndex.Display(chatID, uid)
		if i < len(targetLabels) && strings.TrimSpace(targetLabels[i]) != "" {
			lbl = targetLabels[i]
		}
		targetLinks = append(targetLinks, htmlUserLink(lbl, uid))
	}
	var b strings.Builder
	b.WriteString(modLink)
	b.WriteByte(' ')
	b.WriteString(verb)
	b.WriteByte(' ')
	b.WriteString(strings.Join(targetLinks, ", "))
	if req.DurationRaw != "" && (req.Action == "ban" || req.Action == "mute") {
		b.WriteString(" на ")
		b.WriteString(html.EscapeString(humanModerationDurationRU(req.Duration, req.DurationRaw)))
	}
	if req.Reason != "" {
		b.WriteString(" — ")
		b.WriteString(html.EscapeString(req.Reason))
	}
	return strings.TrimSpace(b.String())
}

func handleModerationConfirmCallback(bot *tgbotapi.BotAPI, adminCache *adminStatusCache, userIndex *chatUserIndex, store *Store, confirms *moderationConfirmManager, cb *tgbotapi.CallbackQuery) bool {
	if bot == nil || confirms == nil || cb == nil || cb.From == nil {
		return false
	}
	parts := strings.Split(strings.TrimSpace(cb.Data), "|")
	if len(parts) < 3 || parts[0] != "modq" {
		return false
	}
	action := strings.TrimSpace(parts[1])
	id := strings.TrimSpace(parts[2])
	st, ok := confirms.Get(id)
	if !ok {
		_, _ = bot.Request(tgbotapi.NewCallback(cb.ID, "Подтверждение устарело"))
		return true
	}
	if cb.Message == nil || cb.Message.Chat == nil || cb.Message.Chat.ID != st.ChatID {
		_, _ = bot.Request(tgbotapi.NewCallback(cb.ID, "Чат не совпадает с запросом"))
		return true
	}
	_ = syncAdminCacheForUser(bot, adminCache, st.ChatID, cb.From.ID)
	if adminCache != nil && !adminCache.IsChatAdmin(bot, st.ChatID, cb.From.ID) {
		_, _ = bot.Request(tgbotapi.NewCallback(cb.ID, "Права администратора больше не подтверждены"))
		return true
	}
	actorTag := getChatMemberTagRaw(bot.Token, st.ChatID, cb.From.ID)

	switch action {
	case "t":
		if len(parts) < 4 {
			_, _ = bot.Request(tgbotapi.NewCallback(cb.ID, "Некорректный выбор"))
			return true
		}
		idx, err := strconv.Atoi(strings.TrimSpace(parts[3]))
		if err != nil {
			_, _ = bot.Request(tgbotapi.NewCallback(cb.ID, "Некорректный выбор"))
			return true
		}
		updated, ok := confirms.Toggle(id, idx)
		if !ok {
			_, _ = bot.Request(tgbotapi.NewCallback(cb.ID, "Подтверждение устарело"))
			return true
		}
		text, kb := buildModerationConfirmMessage(updated)
		edit := tgbotapi.NewEditMessageTextAndMarkup(st.ChatID, cb.Message.MessageID, text, kb)
		if _, err := bot.Request(edit); err != nil {
			log.Printf("moderation confirm edit failed chat=%d msg=%d: %v", st.ChatID, cb.Message.MessageID, err)
		}
		_, _ = bot.Request(tgbotapi.NewCallback(cb.ID, "Обновлено"))
		return true
	case "c":
		confirms.Delete(id)
		_, _ = bot.Request(tgbotapi.NewCallback(cb.ID, "Отменено"))
		_, _ = bot.Request(tgbotapi.DeleteMessageConfig{ChatID: st.ChatID, MessageID: cb.Message.MessageID})
		return true
	case "a":
		if !moderationConfirmAllChecked(st) {
			_, _ = bot.Request(tgbotapi.NewCallback(cb.ID, "Отметь все пункты перед применением"))
			return true
		}
		if err := applyModerationToTargets(bot, store, st.ChatID, st.Req, st.Targets); err != nil {
			reportChatFailure(bot, st.ChatID, "ошибка модерации", err)
			_, _ = bot.Request(tgbotapi.NewCallback(cb.ID, "Не удалось применить"))
			return true
		}
		confirms.Delete(id)
		_, _ = bot.Request(tgbotapi.NewCallback(cb.ID, "Применено"))
		_, _ = bot.Request(tgbotapi.DeleteMessageConfig{ChatID: st.ChatID, MessageID: cb.Message.MessageID})
		if !st.Req.Silent {
			sendHTML(sendContext{Bot: bot, ChatID: st.ChatID}, buildModerationResultText(userIndex, st.ChatID, cb.From.ID, actorTag, st.Req, st.Targets, st.TargetLabels), false)
		}
		return true
	default:
		return false
	}
}

func handleModerationCommand(ctx moderationContext, msg *tgbotapi.Message, text string) bool {
	req, ok, err := parseModerationCommand(text)
	if !ok {
		return false
	}
	if err != nil {
		return true
	}
	if ctx.Bot == nil || msg == nil || msg.Chat == nil || msg.From == nil {
		return true
	}
	sendCtx := sendContext{Bot: ctx.Bot, ChatID: msg.Chat.ID}
	if msg.Chat.IsPrivate() {
		reply(sendCtx.WithReply(msg.MessageID), "Мод-команды работают только в группах.", false)
		return true
	}
	_ = syncAdminCacheForUser(ctx.Bot, ctx.AdminCache, msg.Chat.ID, msg.From.ID)

	deleteCmd := func() {
		_, _ = ctx.Bot.Request(tgbotapi.DeleteMessageConfig{
			ChatID:    msg.Chat.ID,
			MessageID: msg.MessageID,
		})
	}

	senderTag := ""
	if ctx.Bot != nil && msg.Chat != nil && msg.From != nil {
		senderTag = getChatMemberTagRaw(ctx.Bot.Token, msg.Chat.ID, msg.From.ID)
	}

	if req.Action == cmdReloadAdmins {
		if ctx.AdminCache == nil {
			reply(sendCtx.WithReply(msg.MessageID), "Кэш админов недоступен.", false)
			return true
		}
		if !fetchChatAdminStatus(ctx.Bot, msg.Chat.ID, msg.From.ID) && !ctx.AdminCache.IsChatAdmin(ctx.Bot, msg.Chat.ID, msg.From.ID) {
			reply(sendCtx.WithReply(msg.MessageID), "Только администраторы могут использовать эту команду.", false)
			return true
		}
		count, err := ctx.AdminCache.ReloadChatAdmins(ctx.Bot, msg.Chat.ID)
		if err != nil {
			reportChatFailure(ctx.Bot, msg.Chat.ID, "ошибка обновления кэша админов", err)
			return true
		}
		deleteCmd()
		reply(sendCtx, fmt.Sprintf("Кэш админов обновлён: %d.", count), false)
		return true
	}

	if ctx.AdminCache == nil || !ctx.AdminCache.IsChatAdmin(ctx.Bot, msg.Chat.ID, msg.From.ID) {
		reply(sendCtx.WithReply(msg.MessageID), "Только администраторы могут использовать эту команду.", false)
		return true
	}

	if req.Action == cmdReadonly {
		turnOn := true
		if ctx.Readonly != nil && ctx.Readonly.IsOn(msg.Chat.ID) {
			turnOn = false
		}
		if err := applyReadonly(ctx.Bot, msg.Chat.ID, turnOn); err != nil {
			reportChatFailure(ctx.Bot, msg.Chat.ID, "ошибка переключения readonly", err)
			return true
		}
		if ctx.Readonly != nil {
			ctx.Readonly.Set(msg.Chat.ID, turnOn)
		}
		deleteCmd()
		if turnOn && req.Duration > 0 && ctx.Readonly != nil {
			chatID := msg.Chat.ID
			ctx.Readonly.ScheduleOff(chatID, req.Duration, func() {
				_ = applyReadonly(ctx.Bot, chatID, false)
				ctx.Readonly.Set(chatID, false)
				reply(sendCtx.WithReply(0), "Режим только чтения автоматически выключен.", false)
			})
		}
		if !req.Silent {
			modLabel := ctx.UserIndex.Display(msg.Chat.ID, msg.From.ID)
			modLink := htmlUserLink(modLabel, msg.From.ID)
			state := moderationReadonlyStateVerb(turnOn, senderTag)
			var b strings.Builder
			b.WriteString(modLink)
			b.WriteByte(' ')
			b.WriteString(state)
			if req.DurationRaw != "" && turnOn {
				b.WriteString(" на ")
				b.WriteString(html.EscapeString(humanModerationDurationRU(req.Duration, req.DurationRaw)))
			}
			if req.Reason != "" {
				b.WriteString(" — ")
				b.WriteString(html.EscapeString(req.Reason))
			}
			sendHTML(sendCtx, b.String(), false)
		}
		return true
	}

	seen := make(map[int64]struct{})
	targets := make([]int64, 0, 4)
	targetLabels := make([]string, 0, 4)
	addTarget := func(id int64, label string) {
		if id == 0 {
			return
		}
		if _, ok := seen[id]; ok {
			return
		}
		seen[id] = struct{}{}
		targets = append(targets, id)
		targetLabels = append(targetLabels, label)
	}

	if msg.ReplyToMessage != nil && msg.ReplyToMessage.From != nil {
		u := msg.ReplyToMessage.From
		addTarget(u.ID, ctx.UserIndex.Display(msg.Chat.ID, u.ID))
	}
	for _, tok := range req.Targets {
		id, label, ok := ctx.UserIndex.Resolve(msg.Chat.ID, tok)
		if !ok {
			reply(sendCtx.WithReply(msg.MessageID), "Не удалось распознать участника: "+tok, false)
			return true
		}
		if label == "" {
			label = ctx.UserIndex.Display(msg.Chat.ID, id)
		}
		addTarget(id, label)
	}
	if len(targets) == 0 {
		reply(sendCtx.WithReply(msg.MessageID), "Нужен reply на сообщение участника или список @username/ID.", false)
		return true
	}

	if moderationRequiresConfirm(req.Action) && ctx.Confirms != nil {
		id := ctx.Confirms.Create(moderationConfirmState{
			ChatID:       msg.Chat.ID,
			AdminID:      msg.From.ID,
			SenderTag:    senderTag,
			Req:          req,
			Targets:      append([]int64(nil), targets...),
			TargetLabels: append([]string(nil), targetLabels...),
		})
		if id == "" {
			reply(sendCtx.WithReply(msg.MessageID), "Не удалось создать подтверждение. Повтори команду.", false)
			return true
		}
		st, _ := ctx.Confirms.Get(id)
		text, kb := buildModerationConfirmMessage(st)
		deleteCmd()
		prompt := tgbotapi.NewMessage(msg.Chat.ID, text)
		prompt.ReplyMarkup = kb
		_, _ = ctx.Bot.Send(prompt)
		return true
	}

	if err := applyModerationToTargets(ctx.Bot, ctx.Store, msg.Chat.ID, req, targets); err != nil {
		reportChatFailure(ctx.Bot, msg.Chat.ID, "ошибка модерации", err)
		return true
	}
	deleteCmd()
	if req.Silent {
		return true
	}
	sendHTML(sendCtx, buildModerationResultText(ctx.UserIndex, msg.Chat.ID, msg.From.ID, senderTag, req, targets, targetLabels), false)
	return true
}
