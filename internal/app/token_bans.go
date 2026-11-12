package app

// Токенный бан: запрет участнику пользоваться GPT-ответами бота.
// Это не бан в чате — человек продолжает писать, но бот ему не отвечает и
// не тратит на него токены.

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// tokenBanCacheTTL — как часто перечитываем список из базы, если не было
// локальных изменений.
const tokenBanCacheTTL = 60 * time.Second

// TokenBan описывает запрет на использование токенов.
type TokenBan struct {
	UserID    int64  `bson:"user_id"`
	Username  string `bson:"username"`
	ChatID    int64  `bson:"chat_id"`
	BannedBy  int64  `bson:"banned_by"`
	Reason    string `bson:"reason"`
	CreatedAt int64  `bson:"created_at"`
}

type tokenBanCacheState struct {
	mu       sync.RWMutex
	store    *Store
	items    map[int64]TokenBan
	loadedAt time.Time
}

var tokenBanCache = tokenBanCacheState{items: map[int64]TokenBan{}}

// setTokenBanStore регистрирует хранилище и сразу прогревает кэш.
func setTokenBanStore(store *Store) {
	tokenBanCache.mu.Lock()
	tokenBanCache.store = store
	tokenBanCache.mu.Unlock()
	refreshTokenBans(true)
}

// refreshTokenBans перечитывает список банов (не чаще TTL, если не force).
func refreshTokenBans(force bool) {
	tokenBanCache.mu.RLock()
	store := tokenBanCache.store
	loadedAt := tokenBanCache.loadedAt
	tokenBanCache.mu.RUnlock()
	if store == nil {
		return
	}
	if !force && time.Since(loadedAt) < tokenBanCacheTTL {
		return
	}
	items, err := store.TokenBanList()
	if err != nil {
		return
	}
	next := make(map[int64]TokenBan, len(items))
	for _, item := range items {
		next[item.UserID] = item
	}
	tokenBanCache.mu.Lock()
	tokenBanCache.items = next
	tokenBanCache.loadedAt = time.Now()
	tokenBanCache.mu.Unlock()
}

// isTokenBanned сообщает, запрещено ли участнику пользоваться токенами.
func isTokenBanned(userID int64) (TokenBan, bool) {
	if userID == 0 {
		return TokenBan{}, false
	}
	tokenBanCache.mu.RLock()
	item, ok := tokenBanCache.items[userID]
	tokenBanCache.mu.RUnlock()
	if ok {
		return item, true
	}
	refreshTokenBans(false)
	tokenBanCache.mu.RLock()
	item, ok = tokenBanCache.items[userID]
	tokenBanCache.mu.RUnlock()
	return item, ok
}

// --- хранилище -------------------------------------------------------------

func (m *mongoBackend) tokenBanSet(ban TokenBan) error {
	ctx, cancel := mongoCtx()
	defer cancel()
	_, err := m.tokenBans.UpdateOne(ctx,
		bson.M{"user_id": ban.UserID},
		bson.M{"$set": ban},
		options.Update().SetUpsert(true))
	return err
}

func (m *mongoBackend) tokenBanRemove(userID int64) (bool, error) {
	ctx, cancel := mongoCtx()
	defer cancel()
	res, err := m.tokenBans.DeleteOne(ctx, bson.M{"user_id": userID})
	if err != nil {
		return false, err
	}
	return res.DeletedCount > 0, nil
}

func (m *mongoBackend) tokenBanList() ([]TokenBan, error) {
	ctx, cancel := mongoCtx()
	defer cancel()
	cur, err := m.tokenBans.Find(ctx, bson.M{}, options.Find().SetSort(bson.D{{Key: "created_at", Value: 1}}).SetLimit(500))
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	out := make([]TokenBan, 0, 16)
	for cur.Next(ctx) {
		var item TokenBan
		if err := cur.Decode(&item); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, cur.Err()
}

// TokenBan ставит запрет участнику (upsert по user_id).
func (s *Store) TokenBan(ban TokenBan) error {
	if s == nil || s.mg == nil {
		return fmt.Errorf("mongo backend not initialized")
	}
	if ban.UserID == 0 {
		return fmt.Errorf("user id is required")
	}
	if ban.CreatedAt == 0 {
		ban.CreatedAt = time.Now().Unix()
	}
	if err := s.mg.tokenBanSet(ban); err != nil {
		return err
	}
	refreshTokenBans(true)
	return nil
}

// TokenUnban снимает запрет.
func (s *Store) TokenUnban(userID int64) (bool, error) {
	if s == nil || s.mg == nil {
		return false, fmt.Errorf("mongo backend not initialized")
	}
	removed, err := s.mg.tokenBanRemove(userID)
	if err != nil {
		return false, err
	}
	refreshTokenBans(true)
	return removed, nil
}

// TokenBanList возвращает все запреты.
func (s *Store) TokenBanList() ([]TokenBan, error) {
	if s == nil || s.mg == nil {
		return nil, fmt.Errorf("mongo backend not initialized")
	}
	return s.mg.tokenBanList()
}

// --- команды ---------------------------------------------------------------

// tokenBanTargets разбирает цели команды: реплай, id или @username.
func tokenBanTargets(msg *tgbotapi.Message, userIndex *chatUserIndex, args string) ([]int64, map[int64]string, string) {
	labels := map[int64]string{}
	ids := make([]int64, 0, 2)
	if msg != nil && msg.ReplyToMessage != nil && msg.ReplyToMessage.From != nil && msg.ReplyToMessage.From.ID != 0 {
		from := msg.ReplyToMessage.From
		ids = append(ids, from.ID)
		label := strings.TrimSpace(from.FirstName)
		if u := strings.TrimSpace(from.UserName); u != "" {
			label = strings.TrimSpace(label + " @" + u)
		}
		labels[from.ID] = label
		return ids, labels, strings.TrimSpace(args)
	}
	fields := strings.Fields(args)
	reason := ""
	for _, tok := range fields {
		if id, label, ok := userIndex.Resolve(chatIDOf(msg), tok); ok {
			ids = append(ids, id)
			labels[id] = label
			continue
		}
		// первое нераспознанное слово и всё после — причина
		idx := strings.Index(args, tok)
		if idx >= 0 {
			reason = strings.TrimSpace(args[idx:])
		}
		break
	}
	return ids, labels, reason
}

func chatIDOf(msg *tgbotapi.Message) int64 {
	if msg == nil || msg.Chat == nil {
		return 0
	}
	return msg.Chat.ID
}

// handleTokenBanCommand — /token_ban <id|@username> [причина] или реплаем.
func handleTokenBanCommand(bot *tgbotapi.BotAPI, store *Store, userIndex *chatUserIndex, msg *tgbotapi.Message) bool {
	if bot == nil || store == nil || msg == nil || msg.Chat == nil {
		return false
	}
	ctxReply := sendContext{Bot: bot, ChatID: msg.Chat.ID, ReplyTo: msg.MessageID}
	args := strings.TrimSpace(commandArgument(msg.Text))
	ids, labels, reason := tokenBanTargets(msg, userIndex, args)
	if len(ids) == 0 {
		reply(ctxReply, "Кого банить? Ответь на его сообщение или укажи id/@username:\n`/token_ban @username причина`", true)
		return true
	}
	bannedBy := int64(0)
	if msg.From != nil {
		bannedBy = msg.From.ID
	}
	okCount := 0
	var lines []string
	for _, id := range ids {
		ban := TokenBan{
			UserID:   id,
			Username: strings.TrimPrefix(labels[id], ""),
			ChatID:   msg.Chat.ID,
			BannedBy: bannedBy,
			Reason:   reason,
		}
		if err := store.TokenBan(ban); err != nil {
			reportChatFailure(bot, msg.Chat.ID, "не удалось поставить токенный бан", err)
			continue
		}
		okCount++
		label := strings.TrimSpace(labels[id])
		if label == "" {
			label = fmt.Sprintf("id %d", id)
		}
		lines = append(lines, fmt.Sprintf("• %s (id %d)", label, id))
	}
	if okCount == 0 {
		return true
	}
	out := fmt.Sprintf("Запретила тратить токены: %d.\n%s", okCount, strings.Join(lines, "\n"))
	if reason != "" {
		out += "\nПричина: " + reason
	}
	out += "\n\nЧеловек остаётся в чате, просто бот ему не отвечает. Снять: `/token_unban <id|@username>`"
	reply(ctxReply, out, true)
	return true
}

// handleTokenUnbanCommand — /token_unban <id|@username> или реплаем.
func handleTokenUnbanCommand(bot *tgbotapi.BotAPI, store *Store, userIndex *chatUserIndex, msg *tgbotapi.Message) bool {
	if bot == nil || store == nil || msg == nil || msg.Chat == nil {
		return false
	}
	ctxReply := sendContext{Bot: bot, ChatID: msg.Chat.ID, ReplyTo: msg.MessageID}
	args := strings.TrimSpace(commandArgument(msg.Text))
	ids, labels, _ := tokenBanTargets(msg, userIndex, args)
	if len(ids) == 0 {
		reply(ctxReply, "Кого разбанить? Ответь на его сообщение или укажи id/@username:\n`/token_unban @username`", true)
		return true
	}
	var out []string
	for _, id := range ids {
		removed, err := store.TokenUnban(id)
		if err != nil {
			reportChatFailure(bot, msg.Chat.ID, "не удалось снять токенный бан", err)
			continue
		}
		label := strings.TrimSpace(labels[id])
		if label == "" {
			label = fmt.Sprintf("id %d", id)
		}
		if removed {
			out = append(out, fmt.Sprintf("• %s (id %d) — разбанен", label, id))
		} else {
			out = append(out, fmt.Sprintf("• %s (id %d) — не был забанен", label, id))
		}
	}
	if len(out) > 0 {
		reply(ctxReply, strings.Join(out, "\n"), true)
	}
	return true
}

// handleTokenBansCommand — /token_bans, список запретов.
func handleTokenBansCommand(bot *tgbotapi.BotAPI, store *Store, msg *tgbotapi.Message) bool {
	if bot == nil || store == nil || msg == nil || msg.Chat == nil {
		return false
	}
	ctxReply := sendContext{Bot: bot, ChatID: msg.Chat.ID, ReplyTo: msg.MessageID}
	items, err := store.TokenBanList()
	if err != nil {
		reportChatFailure(bot, msg.Chat.ID, "не удалось прочитать список токенных банов", err)
		return true
	}
	if len(items) == 0 {
		reply(ctxReply, "Токенных банов нет.", false)
		return true
	}
	sort.Slice(items, func(i, j int) bool { return items[i].CreatedAt < items[j].CreatedAt })
	var b strings.Builder
	fmt.Fprintf(&b, "Токенные баны (%d):\n", len(items))
	for _, item := range items {
		name := strings.TrimSpace(item.Username)
		if name == "" {
			name = "—"
		}
		fmt.Fprintf(&b, "• %s | id %d", name, item.UserID)
		if item.Reason != "" {
			fmt.Fprintf(&b, " | %s", item.Reason)
		}
		fmt.Fprintf(&b, " | %s\n", time.Unix(item.CreatedAt, 0).Format("02.01.2006"))
	}
	b.WriteString("\nСнять: `/token_unban <id|@username>`")
	reply(ctxReply, b.String(), true)
	return true
}

// tokenBanNotice — текст отказа забаненному участнику.
func tokenBanNotice() string {
	return "Тебе сейчас недоступны мои ответы — администратор отключил доступ. Это не бан в чате, писать можешь."
}
