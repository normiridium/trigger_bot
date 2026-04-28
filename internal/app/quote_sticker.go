package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

const (
	quoteStickerSessionTTL        = 15 * time.Minute
	quoteStickerHistoryMaxPerChat = 1000
	quoteStickerEmojiPerPage      = 48
	quoteStickerEmojiRowSize      = 6
	quoteStickerTotalPages        = 10
)

var quoteStickerEmojiOptions = []string{
	"😀", "😃", "😄", "😁", "😆", "😅", "🤣", "😂", "🙂", "🙃", "😉", "😊",
	"😇", "🥰", "😍", "🤩", "😘", "😗", "😚", "😙", "😋", "😛", "😜", "🤪",
	"😝", "🤑", "🤗", "🤭", "🫢", "🫣", "🤫", "🤔", "🫡", "🤐", "🤨", "😐",
	"😑", "😶", "🫥", "😏", "😒", "🙄", "😬", "😮‍💨", "🤥", "😌", "😔", "😪",
	"🤤", "😴", "😷", "🤒", "🤕", "🤢", "🤮", "🤧", "🥵", "🥶", "🥴", "😵",
	"🤯", "🤠", "🥳", "🥸", "😎", "🤓", "🧐", "😕", "🫤", "😟", "🙁", "☹️",
	"😮", "😯", "😲", "😳", "🥺", "🥹", "😦", "😧", "😨", "😰", "😥", "😢",
	"😭", "😱", "😖", "😣", "😞", "😓", "😩", "😫", "🥱", "😤", "😡", "😠",
	"🤬", "😈", "👿", "💀", "☠️", "💩", "🤡", "👹", "👺", "👻", "👽", "🤖",
	"🎉", "🔥", "✨", "⭐", "🌟", "💫", "💥", "💯", "❤️", "🧡", "💛", "💚",
	"💙", "💜", "🖤", "🤍", "🤎", "💔", "❣️", "💕", "💞", "💓", "💗", "💖",
	"💘", "💝", "💟", "👍", "👎", "👊", "✊", "🤛", "🤜", "👏", "🙌", "🫶",
	"🤝", "🙏", "💪", "🫡", "✍️", "🫰", "👌", "🤌", "🤏", "✌️", "🤞", "🫳",
	"🫴", "👋", "🤙", "🖐️", "✋", "🖖", "☝️", "👆", "👇", "👉", "👈", "🫵",
	"👀", "🧠", "🦾", "🦿", "🦷", "🫦", "👄", "🫀", "🫁", "🧸", "🎁", "🎈",
	"🎊", "🎀", "🎂", "🍰", "🧁", "🍫", "🍬", "🍭", "🍓", "🍒", "🍉", "🍋",
	"🍇", "🍍", "🥝", "🥑", "🍅", "🌶️", "🥕", "🌽", "🍕", "🍔", "🍟", "🌮",
	"🍣", "🍜", "🍩", "☕", "🍵", "🥤", "🍺", "🍷", "🍸", "🏆", "🥇", "🥈",
	"🥉", "⚽", "🏀", "🏐", "🎾", "🎲", "♟️", "🎮", "🎯", "🎻", "🎹", "🥁",
	"🐶", "🐱", "🐭", "🐹", "🐰", "🦊", "🐻", "🐼", "🐨", "🐯", "🦁", "🐮",
	"🐷", "🐸", "🐵", "🐔", "🐧", "🐦", "🐤", "🐣", "🦄", "🦋", "🐝", "🐞",
	"🪲", "🦂", "🐢", "🐍", "🦎", "🐙", "🦑", "🦐", "🦞", "🦀", "🐬", "🐳",
	"🦈", "🐊", "🦒", "🦓", "🦬", "🦌", "🐘", "🦛", "🦏", "🦘", "🦥", "🦦",
	"🌍", "🌎", "🌏", "🌕", "🌖", "🌗", "🌘", "🌑", "🌒", "🌓", "🌔", "🌙",
	"☀️", "⛅", "☁️", "🌧️", "⛈️", "🌩️", "❄️", "☃️", "🌪️", "🌈", "⚡", "🌊",
}

var telegramBotTokenPattern = regexp.MustCompile(`bot[0-9]{6,}:[A-Za-z0-9_-]{20,}`)

func redactTelegramToken(s string) string {
	if s == "" {
		return ""
	}
	// Hide bot tokens that appear inside direct Telegram file URLs.
	return telegramBotTokenPattern.ReplaceAllString(s, "bot<redacted>")
}

func quoteMediaLogf(msg *tgbotapi.Message, format string, args ...interface{}) {
	if !debugTriggerLogEnabled {
		return
	}
	chatID := int64(0)
	msgID := 0
	if msg != nil {
		msgID = msg.MessageID
		if msg.Chat != nil {
			chatID = msg.Chat.ID
		}
	}
	log.Printf("quote media chat=%d msg=%d "+format, append([]interface{}{chatID, msgID}, args...)...)
}

func quoteMediaChoiceLog(msg *tgbotapi.Message, source, url, fileID string, width, height int) {
	quoteMediaLogf(msg, "choice source=%s has_url=%v has_file_id=%v w=%d h=%d",
		source, strings.TrimSpace(url) != "", strings.TrimSpace(fileID) != "", width, height)
}

func effectiveQuoteStickerEmojiOptions() []string {
	max := quoteStickerEmojiPerPage * quoteStickerTotalPages
	if max <= 0 || len(quoteStickerEmojiOptions) <= max {
		return quoteStickerEmojiOptions
	}
	return quoteStickerEmojiOptions[:max]
}

type quoteStickerHistory struct {
	mu     sync.RWMutex
	byChat map[int64][]quoteHistoryItem
	maxPer int
}

type quoteHistoryItem struct {
	Msg *tgbotapi.Message
	Raw *rawMessageWithEmoji
}

type quoteMediaPayload struct {
	FileID string `json:"file_id,omitempty"`
	URL    string `json:"url,omitempty"`
	Width  int    `json:"width,omitempty"`
	Height int    `json:"height,omitempty"`
}

func newQuoteStickerHistory(maxPer int) *quoteStickerHistory {
	if maxPer <= 0 {
		maxPer = quoteStickerHistoryMaxPerChat
	}
	return &quoteStickerHistory{
		byChat: make(map[int64][]quoteHistoryItem),
		maxPer: maxPer,
	}
}

func (h *quoteStickerHistory) Add(msg *tgbotapi.Message, raw *rawMessageWithEmoji) {
	if h == nil || msg == nil || msg.Chat == nil || msg.Chat.ID == 0 {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	items := h.byChat[msg.Chat.ID]
	items = append(items, quoteHistoryItem{Msg: msg, Raw: raw})
	if len(items) > h.maxPer {
		items = items[len(items)-h.maxPer:]
	}
	h.byChat[msg.Chat.ID] = items
}

func (h *quoteStickerHistory) Previous(chatID int64, currentMsgID int) *tgbotapi.Message {
	if h == nil || chatID == 0 || currentMsgID <= 0 {
		return nil
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	items := h.byChat[chatID]
	for i := len(items) - 1; i >= 0; i-- {
		if items[i].Msg == nil {
			continue
		}
		if items[i].Msg.MessageID < currentMsgID {
			return items[i].Msg
		}
	}
	return nil
}

func (h *quoteStickerHistory) CollectBefore(chatID int64, targetMsgID int, count int) []quoteHistoryItem {
	if h == nil || chatID == 0 || targetMsgID <= 0 || count <= 0 {
		return nil
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	items := h.byChat[chatID]
	out := make([]quoteHistoryItem, 0, count)
	for i := len(items) - 1; i >= 0; i-- {
		it := items[i]
		m := it.Msg
		if m == nil {
			continue
		}
		if m.MessageID > targetMsgID {
			continue
		}
		out = append(out, it)
		if len(out) >= count {
			break
		}
	}
	// reverse to oldest -> newest
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

type quoteStickerSession struct {
	ID          string
	ChatID      int64
	UserID      int64
	CreatedAt   time.Time
	Page        int
	Emoji       string
	TargetMsgID int
	StickerPNG  []byte
}

type quoteStickerSessionManager struct {
	mu    sync.Mutex
	items map[string]quoteStickerSession
	ttl   time.Duration
}

func newQuoteStickerSessionManager(ttl time.Duration) *quoteStickerSessionManager {
	if ttl <= 0 {
		ttl = quoteStickerSessionTTL
	}
	return &quoteStickerSessionManager{
		items: make(map[string]quoteStickerSession),
		ttl:   ttl,
	}
}

func (m *quoteStickerSessionManager) Put(st quoteStickerSession) {
	if m == nil || strings.TrimSpace(st.ID) == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.items[st.ID] = st
}

func (m *quoteStickerSessionManager) Get(id string) (quoteStickerSession, bool) {
	if m == nil || strings.TrimSpace(id) == "" {
		return quoteStickerSession{}, false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	st, ok := m.items[id]
	if !ok {
		return quoteStickerSession{}, false
	}
	if time.Since(st.CreatedAt) > m.ttl {
		delete(m.items, id)
		return quoteStickerSession{}, false
	}
	return st, true
}

func (m *quoteStickerSessionManager) Delete(id string) {
	if m == nil || strings.TrimSpace(id) == "" {
		return
	}
	m.mu.Lock()
	delete(m.items, id)
	m.mu.Unlock()
}

func parseQuoteStickerCountArg(raw string) int {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 1
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 1
	}
	if n < 1 {
		return 1
	}
	if n > 20 {
		return 20
	}
	return n
}

func quoteStickerPackName(botUserName string, chatID int64) string {
	base := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(botUserName), "@"))
	if base == "" {
		base = "triggeradminbot"
	}
	id := chatID
	if id < 0 {
		id = -id
	}
	return fmt.Sprintf("q_%d_by_%s", id, base)
}

func quoteStickerPackTitle(chatTitle string) string {
	t := strings.TrimSpace(chatTitle)
	if t == "" {
		t = "Chat"
	}
	return clipText("Quotes • "+t, 60)
}

func buildQuoteStickerPickerText(st quoteStickerSession) string {
	lines := []string{
		"Выберите эмодзи для нового стикера:",
	}
	if strings.TrimSpace(st.Emoji) != "" {
		lines = append(lines, "Выбрано: "+st.Emoji)
	} else {
		lines = append(lines, "Выбрано: —")
	}
	return strings.Join(lines, "\n")
}

func buildQuoteStickerPickerKeyboard(st quoteStickerSession) tgbotapi.InlineKeyboardMarkup {
	opts := effectiveQuoteStickerEmojiOptions()
	rows := make([][]tgbotapi.InlineKeyboardButton, 0, 6)
	totalPages := (len(opts) + quoteStickerEmojiPerPage - 1) / quoteStickerEmojiPerPage
	if totalPages < 1 {
		totalPages = 1
	}
	page := st.Page
	if page < 0 {
		page = 0
	}
	if page >= totalPages {
		page = totalPages - 1
	}
	start := page * quoteStickerEmojiPerPage
	end := start + quoteStickerEmojiPerPage
	if end > len(opts) {
		end = len(opts)
	}
	row := make([]tgbotapi.InlineKeyboardButton, 0, quoteStickerEmojiRowSize)
	for i := start; i < end; i++ {
		e := opts[i]
		label := e
		if st.Emoji == e {
			label = "✓ " + e
		}
		row = append(row, tgbotapi.NewInlineKeyboardButtonData(label, "qstk|pick|"+st.ID+"|"+strconv.Itoa(i)))
		if len(row) >= quoteStickerEmojiRowSize {
			rows = append(rows, row)
			row = make([]tgbotapi.InlineKeyboardButton, 0, quoteStickerEmojiRowSize)
		}
	}
	if len(row) > 0 {
		rows = append(rows, row)
	}
	nav := []tgbotapi.InlineKeyboardButton{
		tgbotapi.NewInlineKeyboardButtonData("◀️", "qstk|page|"+st.ID+"|prev"),
		tgbotapi.NewInlineKeyboardButtonData(fmt.Sprintf("%d/%d", page+1, totalPages), "qstk|noop|"+st.ID+"|x"),
		tgbotapi.NewInlineKeyboardButtonData("▶️", "qstk|page|"+st.ID+"|next"),
	}
	rows = append(rows, nav)
	if strings.TrimSpace(st.Emoji) == "" {
		rows = append(rows, tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("Отмена", "qstk|cancel|"+st.ID+"|x"),
		))
	} else {
		rows = append(rows, tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("Сохранить", "qstk|save|"+st.ID+"|x"),
			tgbotapi.NewInlineKeyboardButtonData("Отмена", "qstk|cancel|"+st.ID+"|x"),
		))
	}
	return tgbotapi.NewInlineKeyboardMarkup(rows...)
}

func requestQuoteStickerPNG(bot *tgbotapi.BotAPI, items []quoteHistoryItem) ([]byte, error) {
	if len(items) == 0 {
		return nil, errors.New("empty message list")
	}
	if debugTriggerLogEnabled {
		firstMsgID := 0
		lastMsgID := 0
		chatID := int64(0)
		if items[0].Msg != nil {
			firstMsgID = items[0].Msg.MessageID
			if items[0].Msg.Chat != nil {
				chatID = items[0].Msg.Chat.ID
			}
		}
		if items[len(items)-1].Msg != nil {
			lastMsgID = items[len(items)-1].Msg.MessageID
		}
		log.Printf("quote build start chat=%d count=%d first_msg=%d last_msg=%d", chatID, len(items), firstMsgID, lastMsgID)
	}
	type fromObj struct {
		ID    int64  `json:"id"`
		Name  string `json:"name"`
		Photo *struct {
			URL string `json:"url"`
		} `json:"photo,omitempty"`
	}
	type mObj struct {
		From     fromObj `json:"from"`
		Text     string  `json:"text"`
		Avatar   bool    `json:"avatar"`
		Entities []struct {
			Type          string `json:"type"`
			Offset        int    `json:"offset"`
			Length        int    `json:"length"`
			CustomEmojiID string `json:"custom_emoji_id,omitempty"`
		} `json:"entities,omitempty"`
		Media any `json:"media,omitempty"`
	}
	payload := struct {
		BotToken string `json:"botToken,omitempty"`
		Messages []mObj `json:"messages"`
	}{
		BotToken: "",
		Messages: make([]mObj, 0, len(items)),
	}
	if bot != nil {
		payload.BotToken = strings.TrimSpace(bot.Token)
	}
	avatarURLByUser := make(map[int64]string, 16)
	mediaURLByFileID := make(map[string]string, 32)
	for _, it := range items {
		m := it.Msg
		if m == nil {
			continue
		}
		name := ""
		uid := int64(0)
		if m.From != nil {
			uid = m.From.ID
			name = strings.TrimSpace(buildUserDisplayName(m.From))
		}
		if name == "" {
			name = "User"
		}
		text := strings.TrimSpace(firstNonEmptyUserText(m))
		hasMedia := false
		if text == "" {
			if m.Sticker != nil {
				text = "стикер"
			} else if len(m.Photo) > 0 {
				text = "фото"
				hasMedia = true
			} else if m.Animation != nil {
				text = "гиф"
				hasMedia = true
			} else if m.Video != nil {
				text = "видео"
				hasMedia = true
			} else if m.Document != nil {
				mime := strings.ToLower(strings.TrimSpace(m.Document.MimeType))
				if strings.HasPrefix(mime, "image/") {
					text = "картинка"
					hasMedia = true
				}
			}
			if text == "" {
				text = "сообщение"
			}
		}
		from := fromObj{ID: uid, Name: clipText(name, 48)}
		if uid != 0 && bot != nil {
			if cached, ok := avatarURLByUser[uid]; ok {
				if cached != "" {
					from.Photo = &struct {
						URL string `json:"url"`
					}{URL: cached}
				}
			} else if url := resolveUserAvatarURL(bot, uid); url != "" {
				avatarURLByUser[uid] = url
				from.Photo = &struct {
					URL string `json:"url"`
				}{URL: url}
			} else {
				avatarURLByUser[uid] = ""
			}
		}
		item := mObj{
			From:   from,
			Text:   clipText(text, 1200),
			Avatar: true,
		}
		if ents := buildQuoteEntitiesPayload(it.Raw, text); len(ents) > 0 {
			item.Entities = ents
		}
		media := buildQuoteMediaPayload(bot, mediaURLByFileID, m)
		if media != nil {
			docMime := ""
			if m.Document != nil {
				docMime = strings.TrimSpace(m.Document.MimeType)
			}
			quoteMediaLogf(m, "selected photo=%v animation=%v video=%v docMime=%q hasURL=%v hasFileID=%v",
				len(m.Photo) > 0, m.Animation != nil, m.Video != nil, docMime,
				strings.TrimSpace(media.URL) != "", strings.TrimSpace(media.FileID) != "")
			item.Media = buildQuoteAPIMediaField(media)
			// Hide synthetic placeholder only when media has a direct URL and will render for sure.
			if hasMedia && item.Text == text && strings.TrimSpace(media.URL) != "" {
				item.Text = ""
			}
		}
		payload.Messages = append(payload.Messages, item)
	}
	if len(payload.Messages) == 0 {
		return nil, errors.New("empty quote payload")
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	endpoint := strings.TrimSpace(os.Getenv("QUOTE_API_URI"))
	if endpoint == "" {
		endpoint = "https://bot.lyo.su/quote/generate.png"
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 25 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	bin, err := io.ReadAll(io.LimitReader(resp.Body, 15*1024*1024))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		bodySample := clipText(strings.TrimSpace(string(bin)), 200)
		if debugTriggerLogEnabled {
			log.Printf("quote api error status=%d body=%q", resp.StatusCode, bodySample)
		}
		return nil, fmt.Errorf("quote api status=%d body=%s", resp.StatusCode, bodySample)
	}
	if len(bin) == 0 {
		return nil, errors.New("empty quote image")
	}
	if debugTriggerLogEnabled {
		log.Printf("quote build done png_bytes=%d", len(bin))
	}
	return normalizeStickerPNG(bin)
}

func buildQuoteAPIMediaField(media *quoteMediaPayload) any {
	if media == nil {
		return nil
	}
	if url := strings.TrimSpace(media.URL); url != "" {
		return map[string]any{"url": url}
	}
	if fileID := strings.TrimSpace(media.FileID); fileID != "" {
		// quote-api instance used by this bot expects file-id media as array items.
		return []string{fileID}
	}
	return nil
}

func buildQuoteEntitiesPayload(raw *rawMessageWithEmoji, chosenText string) []struct {
	Type          string `json:"type"`
	Offset        int    `json:"offset"`
	Length        int    `json:"length"`
	CustomEmojiID string `json:"custom_emoji_id,omitempty"`
} {
	if raw == nil {
		return nil
	}
	src := strings.TrimSpace(chosenText)
	if src == "" {
		return nil
	}
	entities := raw.Entities
	if strings.TrimSpace(raw.Caption) == src {
		entities = raw.CaptionEntities
	}
	out := make([]struct {
		Type          string `json:"type"`
		Offset        int    `json:"offset"`
		Length        int    `json:"length"`
		CustomEmojiID string `json:"custom_emoji_id,omitempty"`
	}, 0, len(entities))
	for _, e := range entities {
		if e.Offset < 0 || e.Length <= 0 {
			continue
		}
		item := struct {
			Type          string `json:"type"`
			Offset        int    `json:"offset"`
			Length        int    `json:"length"`
			CustomEmojiID string `json:"custom_emoji_id,omitempty"`
		}{
			Type:   strings.TrimSpace(e.Type),
			Offset: e.Offset,
			Length: e.Length,
		}
		if item.Type == "" {
			continue
		}
		if item.Type == "custom_emoji" {
			item.CustomEmojiID = strings.TrimSpace(e.CustomEmojiID)
			if item.CustomEmojiID == "" {
				continue
			}
		}
		out = append(out, item)
	}
	return out
}

func buildQuoteMediaPayload(bot *tgbotapi.BotAPI, urlCache map[string]string, msg *tgbotapi.Message) *quoteMediaPayload {
	if msg == nil {
		return nil
	}
	// Prefer plain photos.
	if len(msg.Photo) > 0 {
		p := msg.Photo[len(msg.Photo)-1]
		if id := strings.TrimSpace(p.FileID); id != "" {
			url := resolveTelegramFileURL(bot, urlCache, id)
			fileID := ""
			if url == "" {
				fileID = id
			}
			quoteMediaChoiceLog(msg, "photo", url, fileID, p.Width, p.Height)
			return &quoteMediaPayload{
				FileID: fileID,
				URL:    url,
				Width:  p.Width,
				Height: p.Height,
			}
		}
	}
	// GIF/animation: try static thumbnail first.
	if msg.Animation != nil {
		if msg.Animation.Thumbnail != nil {
			th := msg.Animation.Thumbnail
			if id := strings.TrimSpace(th.FileID); id != "" {
				url := resolveTelegramFileURL(bot, urlCache, id)
				fileID := ""
				if url == "" {
					fileID = id
				}
				quoteMediaChoiceLog(msg, "animation.thumbnail", url, fileID, th.Width, th.Height)
				return &quoteMediaPayload{
					FileID: fileID,
					URL:    url,
					Width:  th.Width,
					Height: th.Height,
				}
			}
		}
		if id := strings.TrimSpace(msg.Animation.FileID); id != "" {
			url := resolveTelegramFileURL(bot, urlCache, id)
			if url != "" {
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
				previewPNG, err := extractFirstFramePNG(ctx, url)
				cancel()
				if err == nil && len(previewPNG) > 0 {
					if upID := uploadPNGAsStickerFileID(bot, msg.From.ID, previewPNG); upID != "" {
						upURL := resolveTelegramFileURL(bot, urlCache, upID)
						quoteMediaChoiceLog(msg, "animation.uploaded_frame", upURL, upID, msg.Animation.Width, msg.Animation.Height)
						if upURL != "" {
							return &quoteMediaPayload{URL: upURL, Width: msg.Animation.Width, Height: msg.Animation.Height}
						}
						return &quoteMediaPayload{FileID: upID, Width: msg.Animation.Width, Height: msg.Animation.Height}
					}
					quoteMediaLogf(msg, "animation frame extracted but upload failed; fallback to original file_id")
				}
			}
			fileID := id
			quoteMediaChoiceLog(msg, "animation.file_id_only", "", fileID, msg.Animation.Width, msg.Animation.Height)
			return &quoteMediaPayload{FileID: fileID, Width: msg.Animation.Width, Height: msg.Animation.Height}
		}
	}
	// Telegram "GIF" may come as regular video in some clients/chats.
	if msg.Video != nil {
		if msg.Video.Thumbnail != nil {
			th := msg.Video.Thumbnail
			if id := strings.TrimSpace(th.FileID); id != "" {
				url := resolveTelegramFileURL(bot, urlCache, id)
				fileID := ""
				if url == "" {
					fileID = id
				}
				quoteMediaChoiceLog(msg, "video.thumbnail", url, fileID, th.Width, th.Height)
				return &quoteMediaPayload{
					FileID: fileID,
					URL:    url,
					Width:  th.Width,
					Height: th.Height,
				}
			}
		}
		if id := strings.TrimSpace(msg.Video.FileID); id != "" {
			url := resolveTelegramFileURL(bot, urlCache, id)
			if url != "" {
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
				previewPNG, err := extractFirstFramePNG(ctx, url)
				cancel()
				if err == nil && len(previewPNG) > 0 {
					if upID := uploadPNGAsStickerFileID(bot, msg.From.ID, previewPNG); upID != "" {
						upURL := resolveTelegramFileURL(bot, urlCache, upID)
						quoteMediaChoiceLog(msg, "video.uploaded_frame", upURL, upID, msg.Video.Width, msg.Video.Height)
						if upURL != "" {
							return &quoteMediaPayload{URL: upURL, Width: msg.Video.Width, Height: msg.Video.Height}
						}
						return &quoteMediaPayload{FileID: upID, Width: msg.Video.Width, Height: msg.Video.Height}
					}
					quoteMediaLogf(msg, "video frame extracted but upload failed; fallback to original file_id")
				}
			}
			fileID := id
			quoteMediaChoiceLog(msg, "video.file_id_only", "", fileID, msg.Video.Width, msg.Video.Height)
			return &quoteMediaPayload{FileID: fileID, Width: msg.Video.Width, Height: msg.Video.Height}
		}
	}
	if msg.Document != nil {
		mime := strings.ToLower(strings.TrimSpace(msg.Document.MimeType))
		if strings.HasPrefix(mime, "image/") {
			if id := strings.TrimSpace(msg.Document.FileID); id != "" {
				url := resolveTelegramFileURL(bot, urlCache, id)
				fileID := ""
				if url == "" {
					fileID = id
				}
				quoteMediaChoiceLog(msg, "document.image", url, fileID, 0, 0)
				return &quoteMediaPayload{FileID: fileID, URL: url}
			}
		}
		if strings.HasPrefix(mime, "video/") {
			if msg.Document.Thumbnail != nil {
				th := msg.Document.Thumbnail
				if id := strings.TrimSpace(th.FileID); id != "" {
					url := resolveTelegramFileURL(bot, urlCache, id)
					fileID := ""
					if url == "" {
						fileID = id
					}
					quoteMediaChoiceLog(msg, "document.video.thumbnail", url, fileID, th.Width, th.Height)
					return &quoteMediaPayload{FileID: fileID, URL: url, Width: th.Width, Height: th.Height}
				}
			}
			if id := strings.TrimSpace(msg.Document.FileID); id != "" {
				url := resolveTelegramFileURL(bot, urlCache, id)
				if url != "" {
					ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
					previewPNG, err := extractFirstFramePNG(ctx, url)
					cancel()
					if err == nil && len(previewPNG) > 0 {
						if upID := uploadPNGAsStickerFileID(bot, msg.From.ID, previewPNG); upID != "" {
							upURL := resolveTelegramFileURL(bot, urlCache, upID)
							quoteMediaChoiceLog(msg, "document.video.uploaded_frame", upURL, upID, 0, 0)
							if upURL != "" {
								return &quoteMediaPayload{URL: upURL}
							}
							return &quoteMediaPayload{FileID: upID}
						}
						quoteMediaLogf(msg, "document.video frame extracted but upload failed; fallback to original file_id")
					}
				}
				fileID := id
				quoteMediaChoiceLog(msg, "document.video.file_id_only", "", fileID, 0, 0)
				return &quoteMediaPayload{FileID: fileID}
			}
		}
	}
	quoteMediaLogf(msg, "no usable media payload")
	return nil
}

func resolveTelegramFileURL(bot *tgbotapi.BotAPI, cache map[string]string, fileID string) string {
	fileID = strings.TrimSpace(fileID)
	if fileID == "" || bot == nil {
		return ""
	}
	if cache != nil {
		if v, ok := cache[fileID]; ok {
			if debugTriggerLogEnabled {
				log.Printf("quote media file url cache hit file_id=%q has_url=%v", clipText(fileID, 24), strings.TrimSpace(v) != "")
			}
			return v
		}
	}
	if debugTriggerLogEnabled {
		log.Printf("quote media file url fetch file_id=%q", clipText(fileID, 24))
	}
	url, err := bot.GetFileDirectURL(fileID)
	if err != nil {
		if debugTriggerLogEnabled {
			log.Printf("quote media file url fetch failed file_id=%q err=%v", clipText(fileID, 24), err)
		}
		if cache != nil {
			cache[fileID] = ""
		}
		return ""
	}
	url = strings.TrimSpace(url)
	if debugTriggerLogEnabled {
		log.Printf("quote media file url ok file_id=%q has_url=%v", clipText(fileID, 24), url != "")
	}
	if cache != nil {
		cache[fileID] = url
	}
	return url
}

func extractFirstFramePNG(ctx context.Context, videoURL string) ([]byte, error) {
	videoURL = strings.TrimSpace(videoURL)
	if videoURL == "" {
		return nil, errors.New("empty video url")
	}
	cmd := exec.CommandContext(ctx,
		"/usr/bin/ffmpeg",
		"-v", "error",
		"-y",
		"-i", videoURL,
		"-frames:v", "1",
		"-f", "image2pipe",
		"-vcodec", "png",
		"pipe:1",
	)
	var out bytes.Buffer
	var serr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &serr
	if debugTriggerLogEnabled {
		log.Printf("quote ffmpeg start src=%q", clipText(redactTelegramToken(videoURL), 180))
	}
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ffmpeg frame extract failed: %w (%s)", err, clipText(strings.TrimSpace(serr.String()), 300))
	}
	if out.Len() == 0 {
		return nil, errors.New("ffmpeg returned empty frame")
	}
	if debugTriggerLogEnabled {
		log.Printf("quote ffmpeg ok frame_png_bytes=%d", out.Len())
	}
	return out.Bytes(), nil
}

func uploadPNGAsStickerFileID(bot *tgbotapi.BotAPI, userID int64, pngBytes []byte) string {
	if bot == nil || userID == 0 || len(pngBytes) == 0 {
		return ""
	}
	cfg := tgbotapi.UploadStickerConfig{
		UserID:     userID,
		PNGSticker: tgbotapi.FileBytes{Name: "quote_frame.png", Bytes: pngBytes},
	}
	resp, err := bot.Request(cfg)
	if err != nil {
		if debugTriggerLogEnabled {
			log.Printf("quote sticker upload failed user=%d err=%v", userID, err)
		}
		return ""
	}
	if !resp.Ok {
		if debugTriggerLogEnabled {
			log.Printf("quote sticker upload not ok user=%d desc=%q", userID, strings.TrimSpace(resp.Description))
		}
		return ""
	}
	var f tgbotapi.File
	if err := json.Unmarshal(resp.Result, &f); err != nil {
		if debugTriggerLogEnabled {
			log.Printf("quote sticker upload parse failed user=%d err=%v", userID, err)
		}
		return ""
	}
	fileID := strings.TrimSpace(f.FileID)
	if debugTriggerLogEnabled {
		log.Printf("quote sticker upload ok user=%d has_file_id=%v", userID, fileID != "")
	}
	return fileID
}

func resolveUserAvatarURL(bot *tgbotapi.BotAPI, userID int64) string {
	if bot == nil || userID == 0 {
		return ""
	}
	photos, err := bot.GetUserProfilePhotos(tgbotapi.UserProfilePhotosConfig{
		UserID: userID,
		Offset: 0,
		Limit:  1,
	})
	if err != nil || len(photos.Photos) == 0 || len(photos.Photos[0]) == 0 {
		return ""
	}
	best := photos.Photos[0][len(photos.Photos[0])-1]
	fileID := strings.TrimSpace(best.FileID)
	if fileID == "" {
		return ""
	}
	url, err := bot.GetFileDirectURL(fileID)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(url)
}

func normalizeStickerPNG(raw []byte) ([]byte, error) {
	if len(raw) == 0 {
		return nil, errors.New("empty image")
	}
	src, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	srcB := src.Bounds()
	srcW := srcB.Dx()
	srcH := srcB.Dy()
	if srcW <= 0 || srcH <= 0 {
		return nil, errors.New("invalid image bounds")
	}
	const side = 512
	scaleX := float64(side) / float64(srcW)
	scaleY := float64(side) / float64(srcH)
	scale := scaleX
	if scaleY < scale {
		scale = scaleY
	}
	dstW := int(float64(srcW) * scale)
	dstH := int(float64(srcH) * scale)
	if dstW < 1 {
		dstW = 1
	}
	if dstH < 1 {
		dstH = 1
	}

	scaled := image.NewRGBA(image.Rect(0, 0, dstW, dstH))
	nearestScale(scaled, src)

	canvas := image.NewRGBA(image.Rect(0, 0, side, side))
	draw.Draw(canvas, canvas.Bounds(), &image.Uniform{C: color.Alpha{A: 0}}, image.Point{}, draw.Src)
	offX := (side - dstW) / 2
	offY := (side - dstH) / 2
	draw.Draw(canvas, image.Rect(offX, offY, offX+dstW, offY+dstH), scaled, image.Point{}, draw.Over)

	var out bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.BestCompression}
	if err := enc.Encode(&out, canvas); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func nearestScale(dst draw.Image, src image.Image) {
	db := dst.Bounds()
	sb := src.Bounds()
	dw, dh := db.Dx(), db.Dy()
	sw, sh := sb.Dx(), sb.Dy()
	if dw <= 0 || dh <= 0 || sw <= 0 || sh <= 0 {
		return
	}
	for y := 0; y < dh; y++ {
		sy := sb.Min.Y + (y*sh)/dh
		for x := 0; x < dw; x++ {
			sx := sb.Min.X + (x*sw)/dw
			dst.Set(db.Min.X+x, db.Min.Y+y, src.At(sx, sy))
		}
	}
}

func ensureQuoteStickerSetAndAdd(bot *tgbotapi.BotAPI, chat *tgbotapi.Chat, ownerUserID int64, emoji string, stickerPNG []byte) error {
	if bot == nil || chat == nil || chat.ID == 0 || ownerUserID == 0 || len(stickerPNG) == 0 {
		return errors.New("invalid quote sticker params")
	}
	if strings.TrimSpace(emoji) == "" {
		emoji = "🙂"
	}
	setName := quoteStickerPackName(bot.Self.UserName, chat.ID)
	title := quoteStickerPackTitle(chat.Title)
	_, err := bot.GetStickerSet(tgbotapi.GetStickerSetConfig{Name: setName})
	if err != nil {
		_, createErr := bot.Request(tgbotapi.NewStickerSetConfig{
			UserID:     ownerUserID,
			Name:       setName,
			Title:      title,
			PNGSticker: tgbotapi.FileBytes{Name: "quote.png", Bytes: stickerPNG},
			Emojis:     emoji,
		})
		if createErr != nil {
			return createErr
		}
		ensureDefaultChatStickerSet(bot, chat, setName)
		return nil
	}
	_, addErr := bot.Request(tgbotapi.AddStickerConfig{
		UserID:     ownerUserID,
		Name:       setName,
		PNGSticker: tgbotapi.FileBytes{Name: "quote.png", Bytes: stickerPNG},
		Emojis:     emoji,
	})
	if addErr != nil {
		return addErr
	}
	ensureDefaultChatStickerSet(bot, chat, setName)
	return nil
}

func ensureDefaultChatStickerSet(bot *tgbotapi.BotAPI, chat *tgbotapi.Chat, setName string) {
	if bot == nil || chat == nil || chat.ID == 0 || strings.TrimSpace(setName) == "" {
		return
	}
	// Chat sticker set is supported for supergroups.
	if !chat.IsSuperGroup() {
		return
	}
	currentName := strings.TrimSpace(chat.StickerSetName)
	canSet := chat.CanSetStickerSet
	if currentName == "" || !canSet {
		if fresh, err := bot.GetChat(tgbotapi.ChatInfoConfig{ChatConfig: tgbotapi.ChatConfig{ChatID: chat.ID}}); err == nil {
			currentName = strings.TrimSpace(fresh.StickerSetName)
			canSet = fresh.CanSetStickerSet
		}
	}
	if currentName != "" || !canSet {
		return
	}
	_, err := bot.Request(tgbotapi.SetChatStickerSetConfig{
		ChatID:         chat.ID,
		StickerSetName: setName,
	})
	if err != nil && debugTriggerLogEnabled {
		log.Printf("set chat sticker set failed chat=%d set=%q err=%v", chat.ID, setName, err)
	}
}

func stickerPackURL(setName string) string {
	setName = strings.TrimSpace(setName)
	if setName == "" {
		return ""
	}
	return "https://t.me/addstickers/" + setName
}

func handleQuoteStickerDelete(bot *tgbotapi.BotAPI, history *quoteStickerHistory, msg *tgbotapi.Message) (bool, string) {
	if bot == nil || history == nil || msg == nil || msg.Chat == nil {
		return false, ""
	}
	target := msg.ReplyToMessage
	if target == nil {
		target = history.Previous(msg.Chat.ID, msg.MessageID)
	}
	if target == nil || target.Sticker == nil || strings.TrimSpace(target.Sticker.FileID) == "" {
		return true, "Стикер не найден. Ответьте /qd на стикер или отправьте /qd после стикера."
	}
	_, err := bot.Request(tgbotapi.DeleteStickerConfig{Sticker: strings.TrimSpace(target.Sticker.FileID)})
	if err != nil {
		return true, "Не удалось удалить стикер: " + clipText(err.Error(), 160)
	}
	return true, "Стикер удалён из стикерпака."
}

func handleQuoteStickerCommand(bot *tgbotapi.BotAPI, sessions *quoteStickerSessionManager, history *quoteStickerHistory, msg *tgbotapi.Message) (bool, string, *quoteStickerSession) {
	if bot == nil || sessions == nil || history == nil || msg == nil || msg.Chat == nil || msg.From == nil {
		return false, "", nil
	}
	count := parseQuoteStickerCountArg(msg.CommandArguments())
	target := msg.ReplyToMessage
	if target == nil {
		target = history.Previous(msg.Chat.ID, msg.MessageID)
	}
	if target == nil {
		return true, "Не нашла сообщение. Ответьте /q на сообщение или отправьте /q сразу после него.", nil
	}
	items := history.CollectBefore(msg.Chat.ID, target.MessageID, count)
	if len(items) == 0 {
		return true, "Не удалось собрать сообщения для цитаты.", nil
	}
	img, err := requestQuoteStickerPNG(bot, items)
	if err != nil {
		return true, "Не удалось собрать quote-стикер: " + clipText(err.Error(), 160), nil
	}
	sid, _ := newUUID4()
	st := quoteStickerSession{
		ID:          sid,
		ChatID:      msg.Chat.ID,
		UserID:      msg.From.ID,
		CreatedAt:   time.Now(),
		Page:        0,
		Emoji:       "",
		TargetMsgID: target.MessageID,
		StickerPNG:  img,
	}
	sessions.Put(st)
	return true, "", &st
}

func handleQuoteStickerCallback(bot *tgbotapi.BotAPI, sessions *quoteStickerSessionManager, cb *tgbotapi.CallbackQuery) bool {
	if bot == nil || sessions == nil || cb == nil {
		return false
	}
	raw := strings.TrimSpace(cb.Data)
	if !strings.HasPrefix(raw, "qstk|") {
		return false
	}
	parts := strings.Split(raw, "|")
	if len(parts) < 4 {
		_, _ = bot.Request(tgbotapi.NewCallback(cb.ID, "Некорректные данные"))
		return true
	}
	action, sid, arg := parts[1], parts[2], parts[3]
	st, ok := sessions.Get(sid)
	if !ok {
		_, _ = bot.Request(tgbotapi.NewCallback(cb.ID, "Сессия устарела"))
		return true
	}
	if cb.From == nil || cb.From.ID != st.UserID {
		_, _ = bot.Request(tgbotapi.NewCallback(cb.ID, "Это меню не для вас"))
		return true
	}
	if cb.Message == nil || cb.Message.Chat == nil || cb.Message.Chat.ID != st.ChatID {
		_, _ = bot.Request(tgbotapi.NewCallback(cb.ID, "Чат не совпадает"))
		return true
	}
	switch action {
	case "noop":
		_, _ = bot.Request(tgbotapi.NewCallback(cb.ID, ""))
		return true
	case "cancel":
		sessions.Delete(sid)
		_, _ = bot.Request(tgbotapi.NewCallback(cb.ID, "Отменено"))
		if cb.Message != nil {
			_, _ = bot.Request(tgbotapi.NewEditMessageText(cb.Message.Chat.ID, cb.Message.MessageID, "Сохранение quote-стикера отменено."))
		}
		return true
	case "page":
		opts := effectiveQuoteStickerEmojiOptions()
		totalPages := (len(opts) + quoteStickerEmojiPerPage - 1) / quoteStickerEmojiPerPage
		if totalPages < 1 {
			totalPages = 1
		}
		if arg == "prev" {
			st.Page--
			if st.Page < 0 {
				st.Page = totalPages - 1
			}
		} else {
			st.Page++
			if st.Page >= totalPages {
				st.Page = 0
			}
		}
		sessions.Put(st)
		_, _ = bot.Request(tgbotapi.NewCallback(cb.ID, ""))
	case "pick":
		idx, err := strconv.Atoi(strings.TrimSpace(arg))
		opts := effectiveQuoteStickerEmojiOptions()
		if err != nil || idx < 0 || idx >= len(opts) {
			_, _ = bot.Request(tgbotapi.NewCallback(cb.ID, "Некорректный выбор"))
			return true
		}
		st.Emoji = opts[idx]
		st.Page = idx / quoteStickerEmojiPerPage
		sessions.Put(st)
		_, _ = bot.Request(tgbotapi.NewCallback(cb.ID, "Выбрано "+st.Emoji))
	case "save":
		if strings.TrimSpace(st.Emoji) == "" {
			_, _ = bot.Request(tgbotapi.NewCallback(cb.ID, "Сначала выберите эмодзи"))
			return true
		}
		err := ensureQuoteStickerSetAndAdd(bot, cb.Message.Chat, cb.From.ID, st.Emoji, st.StickerPNG)
		if err != nil {
			_, _ = bot.Request(tgbotapi.NewCallback(cb.ID, clipText("Не удалось сохранить: "+err.Error(), 180)))
			if debugTriggerLogEnabled {
				log.Printf("quote sticker save failed sid=%s chat=%d user=%d err=%v", st.ID, st.ChatID, st.UserID, err)
			}
			return true
		}
		sessions.Delete(sid)
		_, _ = bot.Request(tgbotapi.NewCallback(cb.ID, "Сохранено"))
		if cb.Message != nil {
			setName := quoteStickerPackName(bot.Self.UserName, cb.Message.Chat.ID)
			link := stickerPackURL(setName)
			text := "Quote-стикер сохранён в стикерпак."
			edit := tgbotapi.NewEditMessageText(cb.Message.Chat.ID, cb.Message.MessageID, text)
			if link != "" {
				edit.Text = `Quote-стикер сохранён в <a href="` + html.EscapeString(link) + `">стикерпак</a>.`
				edit.ParseMode = "HTML"
				edit.DisableWebPagePreview = true
			}
			_, _ = bot.Request(edit)
		}
		return true
	default:
		_, _ = bot.Request(tgbotapi.NewCallback(cb.ID, "Неизвестная команда"))
		return true
	}
	if cb.Message != nil {
		edit := tgbotapi.NewEditMessageText(cb.Message.Chat.ID, cb.Message.MessageID, buildQuoteStickerPickerText(st))
		kb := buildQuoteStickerPickerKeyboard(st)
		edit.ReplyMarkup = &kb
		_, _ = bot.Request(edit)
	}
	return true
}
