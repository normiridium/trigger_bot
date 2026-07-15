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
	_ "image/gif"
	_ "image/jpeg"
	"image/png"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf16"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

const (
	quoteStickerSessionTTL        = 60 * time.Minute
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
var tgEmojiSnippetPattern = regexp.MustCompile(`(?is)<tg-emoji\b[^>]*emoji-id=["']?([0-9]+)["']?[^>]*>(.*?)</tg-emoji>`)

type quoteAPIFrom struct {
	ID          int64       `json:"id"`
	Name        string      `json:"name"`
	AvatarImage image.Image `json:"-"`
	Photo       *struct {
		URL string `json:"url"`
	} `json:"photo,omitempty"`
}

type quoteAPIEntity struct {
	Type          string      `json:"type"`
	Offset        int         `json:"offset"`
	Length        int         `json:"length"`
	CustomEmojiID string      `json:"custom_emoji_id,omitempty"`
	Fallback      string      `json:"-"`
	Image         image.Image `json:"-"`
}

type quoteAPIMessage struct {
	From     quoteAPIFrom      `json:"from"`
	Text     string            `json:"text"`
	Avatar   bool              `json:"avatar"`
	Entities []quoteAPIEntity  `json:"entities,omitempty"`
	Media    any               `json:"media,omitempty"`
	Local    *quoteLocalMedia  `json:"-"`
	Reacts   []reactionDisplay `json:"-"`
}

type quoteAPIPayload struct {
	BotToken string            `json:"botToken,omitempty"`
	Messages []quoteAPIMessage `json:"messages"`
}

type quoteLocalMedia struct {
	Kind    string
	Preview image.Image
}

type quoteCustomEmojiAsset struct {
	Fallback string
	Image    image.Image
	Known    bool
}

type quoteTextSegment struct {
	Text  string
	Image image.Image
}

type quoteTextLine []quoteTextSegment

var (
	quoteFontOnce    sync.Once
	quoteRegularFont *opentype.Font
	quoteBoldFont    *opentype.Font
	quoteFontErr     error
)

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
	if max <= 0 || len(quoteStickerEmojiOptions) == 0 {
		return quoteStickerEmojiOptions
	}
	if len(quoteStickerEmojiOptions) >= max {
		return quoteStickerEmojiOptions[:max]
	}
	out := make([]string, 0, max)
	out = append(out, quoteStickerEmojiOptions...)
	for len(out) < max {
		out = append(out, quoteStickerEmojiOptions[len(out)%len(quoteStickerEmojiOptions)])
	}
	return out
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
	Count       int
	TargetMsgID int
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

func sendQuoteStickerPickerMessage(bot *tgbotapi.BotAPI, chatID int64, st quoteStickerSession) error {
	if bot == nil || chatID == 0 || strings.TrimSpace(st.ID) == "" {
		return errors.New("invalid quote sticker picker params")
	}
	out := tgbotapi.NewMessage(chatID, buildQuoteStickerPickerText(st))
	kb := buildQuoteStickerPickerKeyboard(st)
	out.ReplyMarkup = kb
	_, err := bot.Send(out)
	return err
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

func renderLocalQuoteStickerPNG(messages []quoteAPIMessage) ([]byte, error) {
	if len(messages) == 0 {
		return nil, errors.New("empty quote payload")
	}
	if err := loadQuoteFonts(); err != nil {
		return nil, err
	}
	nameFace, err := quoteFontFace(true, 19)
	if err != nil {
		return nil, err
	}
	defer closeQuoteFace(nameFace)
	textFace, err := quoteFontFace(false, 24)
	if err != nil {
		return nil, err
	}
	defer closeQuoteFace(textFace)
	smallFace, err := quoteFontFace(false, 18)
	if err != nil {
		return nil, err
	}
	defer closeQuoteFace(smallFace)

	const side = 512
	const pad = 18
	const gap = 10
	const bubblePadX = 14
	const bubblePadY = 12
	const avatarSize = 48
	const avatarGap = 10
	canvas := image.NewRGBA(image.Rect(0, 0, side, side))
	draw.Draw(canvas, canvas.Bounds(), &image.Uniform{C: color.Alpha{A: 0}}, image.Point{}, draw.Src)
	fillRoundedRect(canvas, canvas.Bounds(), 44, color.RGBA{R: 17, G: 24, B: 39, A: 245})

	y := pad
	drawn := 0
	for _, msg := range messages {
		name := strings.TrimSpace(msg.From.Name)
		if name == "" {
			name = "User"
		}
		name = clipText(name, 42)
		bubbleX := pad
		if msg.Avatar {
			bubbleX += avatarSize + avatarGap
		}
		maxTextW := side - bubbleX - pad - bubblePadX*2
		if maxTextW < 120 {
			maxTextW = 120
		}
		text := strings.TrimSpace(msg.Text)
		if text == "" && msg.Media != nil {
			text = "медиа"
		}
		hasLocalMedia := msg.Local != nil && msg.Local.Preview != nil && !msg.Local.Preview.Bounds().Empty()
		if text == "" && !hasLocalMedia {
			text = "сообщение"
		}
		var lines []quoteTextLine
		if text != "" {
			lines = wrapQuoteTextSegments(textFace, quoteTextSegmentsFromEntities(text, msg.Entities), maxTextW)
		}
		if len(lines) == 0 && !hasLocalMedia {
			lines = []quoteTextLine{{quoteTextSegment{Text: "сообщение"}}}
		}
		if len(lines) > 5 {
			lines = append(lines[:5], quoteTextLine{quoteTextSegment{Text: "…"}})
		}
		lineH := textFace.Metrics().Height.Ceil()
		nameH := nameFace.Metrics().Height.Ceil()
		mediaH := 0
		if hasLocalMedia {
			mediaH = quoteMediaPreviewHeight(msg.Local.Preview, maxTextW, 190)
		}
		reactionsH := quoteReactionsHeight(msg.Reacts)
		bubbleH := bubblePadY*2 + nameH
		if mediaH > 0 {
			bubbleH += 8 + mediaH
		}
		if len(lines) > 0 {
			bubbleH += 4 + len(lines)*lineH
		}
		if reactionsH > 0 {
			bubbleH += 8 + reactionsH
		}
		rowH := bubbleH
		if msg.Avatar && rowH < avatarSize {
			rowH = avatarSize
		}
		if drawn > 0 && y+rowH+pad > side {
			break
		}
		if y+rowH+pad > side {
			// Keep at least one compact message visible for very long text.
			lines = trimQuoteSegmentLinesToFit(lines, textFace, maxTextW, side-y-pad-bubblePadY*2-nameH-4)
			if len(lines) == 0 {
				lines = []quoteTextLine{{quoteTextSegment{Text: "…"}}}
			}
			bubbleH = bubblePadY*2 + nameH
			if mediaH > 0 {
				bubbleH += 8 + mediaH
			}
			if len(lines) > 0 {
				bubbleH += 4 + len(lines)*lineH
			}
			if reactionsH > 0 {
				bubbleH += 8 + reactionsH
			}
			rowH = bubbleH
			if msg.Avatar && rowH < avatarSize {
				rowH = avatarSize
			}
		}
		if msg.Avatar {
			avatarY := y + (rowH-avatarSize)/2
			drawQuoteAvatar(canvas, msg.From.AvatarImage, name, image.Rect(pad, avatarY, pad+avatarSize, avatarY+avatarSize), smallFace)
		}
		bubbleY := y + (rowH-bubbleH)/2
		bubble := image.Rect(bubbleX, bubbleY, side-pad, bubbleY+bubbleH)
		fillRoundedRect(canvas, bubble, 28, color.RGBA{R: 31, G: 41, B: 55, A: 250})
		quoteDrawString(canvas, nameFace, name, bubble.Min.X+bubblePadX, bubble.Min.Y+bubblePadY+nameFace.Metrics().Ascent.Ceil(), color.RGBA{R: 251, G: 113, B: 133, A: 255})
		contentY := bubble.Min.Y + bubblePadY + nameH
		if hasLocalMedia {
			contentY += 8
			mediaRect := image.Rect(bubble.Min.X+bubblePadX, contentY, bubble.Min.X+bubblePadX+maxTextW, contentY+mediaH)
			drawQuoteMediaPreview(canvas, msg.Local.Preview, mediaRect, msg.Local.Kind, smallFace)
			contentY += mediaH
		}
		textY := contentY + 4 + textFace.Metrics().Ascent.Ceil()
		for _, line := range lines {
			drawQuoteTextLine(canvas, textFace, line, bubble.Min.X+bubblePadX, textY, color.RGBA{R: 243, G: 244, B: 246, A: 255})
			textY += lineH
		}
		if reactionsH > 0 {
			drawQuoteReactions(canvas, msg.Reacts, bubble.Min.X+bubblePadX, bubble.Max.Y-bubblePadY-reactionsH, maxTextW, smallFace)
		}
		y += rowH + gap
		drawn++
	}
	if drawn == 0 {
		msg := "Не удалось собрать цитату"
		quoteDrawString(canvas, smallFace, msg, (side-quoteStringWidth(smallFace, msg))/2, side/2, color.RGBA{R: 243, G: 244, B: 246, A: 255})
	}
	var out bytes.Buffer
	if err := png.Encode(&out, canvas); err != nil {
		return nil, err
	}
	return normalizeStickerPNG(out.Bytes())
}

func loadQuoteFonts() error {
	quoteFontOnce.Do(func() {
		regular, err := os.ReadFile("/usr/share/fonts/truetype/dejavu/DejaVuSans.ttf")
		if err != nil {
			quoteFontErr = err
			return
		}
		bold, err := os.ReadFile("/usr/share/fonts/truetype/dejavu/DejaVuSans-Bold.ttf")
		if err != nil {
			quoteFontErr = err
			return
		}
		quoteRegularFont, err = opentype.Parse(regular)
		if err != nil {
			quoteFontErr = err
			return
		}
		quoteBoldFont, err = opentype.Parse(bold)
		if err != nil {
			quoteFontErr = err
			return
		}
	})
	return quoteFontErr
}

func quoteFontFace(bold bool, size float64) (font.Face, error) {
	f := quoteRegularFont
	if bold {
		f = quoteBoldFont
	}
	if f == nil {
		return nil, errors.New("quote font is not loaded")
	}
	return opentype.NewFace(f, &opentype.FaceOptions{
		Size:    size,
		DPI:     72,
		Hinting: font.HintingFull,
	})
}

func closeQuoteFace(face font.Face) {
	if face == nil {
		return
	}
	if closer, ok := face.(interface{ Close() error }); ok {
		_ = closer.Close()
	}
}

func quoteDrawString(dst draw.Image, face font.Face, s string, x, y int, c color.Color) {
	d := font.Drawer{
		Dst:  dst,
		Src:  image.NewUniform(c),
		Face: face,
		Dot:  fixed.P(x, y),
	}
	d.DrawString(s)
}

func drawQuoteAvatar(dst draw.Image, avatar image.Image, name string, r image.Rectangle, face font.Face) {
	if r.Empty() {
		return
	}
	if avatar != nil && !avatar.Bounds().Empty() {
		drawCircularQuoteImage(dst, avatar, r)
		return
	}
	fillCircle(dst, r.Min.X+r.Dx()/2, r.Min.Y+r.Dy()/2, r.Dx()/2, quoteAvatarColor(name))
	initial := quoteAvatarInitial(name)
	if initial == "" || face == nil {
		return
	}
	metrics := face.Metrics()
	x := r.Min.X + (r.Dx()-quoteStringWidth(face, initial))/2
	y := r.Min.Y + (r.Dy()-metrics.Height.Ceil())/2 + metrics.Ascent.Ceil()
	quoteDrawString(dst, face, initial, x, y, color.RGBA{R: 255, G: 255, B: 255, A: 255})
}

func drawCircularQuoteImage(dst draw.Image, src image.Image, r image.Rectangle) {
	sb := src.Bounds()
	if sb.Empty() || r.Empty() {
		return
	}
	side := sb.Dx()
	if sb.Dy() < side {
		side = sb.Dy()
	}
	crop := image.Rect(
		sb.Min.X+(sb.Dx()-side)/2,
		sb.Min.Y+(sb.Dy()-side)/2,
		sb.Min.X+(sb.Dx()+side)/2,
		sb.Min.Y+(sb.Dy()+side)/2,
	)
	cx := r.Min.X + r.Dx()/2
	cy := r.Min.Y + r.Dy()/2
	radius := r.Dx() / 2
	if r.Dy()/2 < radius {
		radius = r.Dy() / 2
	}
	radiusSq := radius * radius
	for y := r.Min.Y; y < r.Max.Y; y++ {
		dy := y - cy
		for x := r.Min.X; x < r.Max.X; x++ {
			dx := x - cx
			if dx*dx+dy*dy > radiusSq {
				continue
			}
			sx := crop.Min.X + (x-r.Min.X)*crop.Dx()/r.Dx()
			sy := crop.Min.Y + (y-r.Min.Y)*crop.Dy()/r.Dy()
			dst.Set(x, y, src.At(sx, sy))
		}
	}
}

func fillCircle(dst draw.Image, cx, cy, radius int, c color.Color) {
	if radius <= 0 {
		return
	}
	radiusSq := radius * radius
	b := dst.Bounds()
	for y := cy - radius; y <= cy+radius; y++ {
		if y < b.Min.Y || y >= b.Max.Y {
			continue
		}
		dy := y - cy
		for x := cx - radius; x <= cx+radius; x++ {
			if x < b.Min.X || x >= b.Max.X {
				continue
			}
			dx := x - cx
			if dx*dx+dy*dy <= radiusSq {
				dst.Set(x, y, c)
			}
		}
	}
}

func quoteAvatarInitial(name string) string {
	for _, field := range strings.Fields(strings.TrimSpace(name)) {
		for _, r := range field {
			return strings.ToUpper(string(r))
		}
	}
	return ""
}

func quoteAvatarColor(name string) color.RGBA {
	palette := []color.RGBA{
		{R: 244, G: 63, B: 94, A: 255},
		{R: 249, G: 115, B: 22, A: 255},
		{R: 14, G: 165, B: 233, A: 255},
		{R: 139, G: 92, B: 246, A: 255},
		{R: 16, G: 185, B: 129, A: 255},
		{R: 236, G: 72, B: 153, A: 255},
	}
	sum := 0
	for _, r := range name {
		sum += int(r)
	}
	return palette[sum%len(palette)]
}

func quoteMediaPreviewHeight(img image.Image, maxWidth, maxHeight int) int {
	if img == nil || img.Bounds().Empty() || maxWidth <= 0 || maxHeight <= 0 {
		return 0
	}
	b := img.Bounds()
	w := b.Dx()
	h := b.Dy()
	if w <= 0 || h <= 0 {
		return 0
	}
	out := h * maxWidth / w
	if out > maxHeight {
		out = maxHeight
	}
	if out < 72 {
		out = 72
	}
	return out
}

func drawQuoteMediaPreview(dst draw.Image, img image.Image, r image.Rectangle, kind string, face font.Face) {
	if img == nil || img.Bounds().Empty() || r.Empty() {
		return
	}
	fillRoundedRect(dst, r, 18, color.RGBA{R: 10, G: 13, B: 22, A: 255})
	inner := r.Inset(2)
	drawImageCoverRounded(dst, img, inner, 16)
	kind = strings.ToUpper(strings.TrimSpace(kind))
	if kind == "" || face == nil {
		return
	}
	label := kind
	w := quoteStringWidth(face, label) + 14
	h := 23
	badge := image.Rect(r.Min.X+8, r.Min.Y+8, r.Min.X+8+w, r.Min.Y+8+h)
	fillRoundedRect(dst, badge, 12, color.RGBA{R: 15, G: 23, B: 42, A: 210})
	quoteDrawString(dst, face, label, badge.Min.X+7, badge.Min.Y+(h-face.Metrics().Height.Ceil())/2+face.Metrics().Ascent.Ceil(), color.RGBA{R: 226, G: 232, B: 240, A: 255})
}

func drawImageCoverRounded(dst draw.Image, src image.Image, r image.Rectangle, radius int) {
	sb := src.Bounds()
	if sb.Empty() || r.Empty() {
		return
	}
	sw := sb.Dx()
	sh := sb.Dy()
	dw := r.Dx()
	dh := r.Dy()
	if sw <= 0 || sh <= 0 || dw <= 0 || dh <= 0 {
		return
	}
	crop := sb
	if sw*dh > sh*dw {
		newW := sh * dw / dh
		if newW < 1 {
			newW = 1
		}
		crop.Min.X = sb.Min.X + (sw-newW)/2
		crop.Max.X = crop.Min.X + newW
	} else if sw*dh < sh*dw {
		newH := sw * dh / dw
		if newH < 1 {
			newH = 1
		}
		crop.Min.Y = sb.Min.Y + (sh-newH)/2
		crop.Max.Y = crop.Min.Y + newH
	}
	rr := radius * radius
	for y := r.Min.Y; y < r.Max.Y; y++ {
		dy := 0
		if y < r.Min.Y+radius {
			dy = r.Min.Y + radius - y
		} else if y >= r.Max.Y-radius {
			dy = y - (r.Max.Y - radius - 1)
		}
		for x := r.Min.X; x < r.Max.X; x++ {
			dx := 0
			if x < r.Min.X+radius {
				dx = r.Min.X + radius - x
			} else if x >= r.Max.X-radius {
				dx = x - (r.Max.X - radius - 1)
			}
			if dx*dx+dy*dy > rr {
				continue
			}
			sx := crop.Min.X + (x-r.Min.X)*crop.Dx()/r.Dx()
			sy := crop.Min.Y + (y-r.Min.Y)*crop.Dy()/r.Dy()
			dst.Set(x, y, src.At(sx, sy))
		}
	}
}

func quoteReactionsHeight(items []reactionDisplay) int {
	for _, it := range items {
		if strings.TrimSpace(it.Emoji) != "" && it.Count > 0 {
			return 25
		}
	}
	return 0
}

func drawQuoteReactions(dst draw.Image, items []reactionDisplay, x, y, maxWidth int, face font.Face) {
	if face == nil || len(items) == 0 || maxWidth <= 0 {
		return
	}
	curX := x
	for _, it := range items {
		emoji := reactionDisplayPlainEmoji(it.Emoji)
		hasImage := it.Image != nil && !it.Image.Bounds().Empty()
		if emoji == "" && !hasImage || it.Count <= 0 {
			continue
		}
		label := emoji
		if hasImage {
			label = ""
		}
		if it.Count > 1 {
			if label == "" {
				label = strconv.Itoa(it.Count)
			} else {
				label += " " + strconv.Itoa(it.Count)
			}
		}
		imgSize := 0
		if hasImage {
			imgSize = 18
		}
		w := quoteStringWidth(face, label) + 14 + imgSize
		if hasImage && label != "" {
			w += 4
		}
		if curX > x && curX+w > x+maxWidth {
			break
		}
		chip := image.Rect(curX, y, curX+w, y+25)
		fillRoundedRect(dst, chip, 13, color.RGBA{R: 15, G: 23, B: 42, A: 230})
		textX := chip.Min.X + 7
		if hasImage {
			imgRect := image.Rect(textX, chip.Min.Y+(chip.Dy()-imgSize)/2, textX+imgSize, chip.Min.Y+(chip.Dy()-imgSize)/2+imgSize)
			drawImageFitRounded(dst, it.Image, imgRect, 4)
			textX += imgSize + 4
		}
		if label != "" {
			quoteDrawString(dst, face, label, textX, chip.Min.Y+(chip.Dy()-face.Metrics().Height.Ceil())/2+face.Metrics().Ascent.Ceil(), color.RGBA{R: 226, G: 232, B: 240, A: 255})
		}
		curX += w + 5
	}
}

func reactionDisplayPlainEmoji(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if strings.Contains(raw, "<tg-emoji") {
		if _, fallback := parseTGEmojiSnippet(raw); fallback != "" {
			return fallback
		}
	}
	return raw
}

func parseTGEmojiSnippet(raw string) (string, string) {
	raw = strings.TrimSpace(raw)
	if raw == "" || !strings.Contains(raw, "<tg-emoji") {
		return "", ""
	}
	m := tgEmojiSnippetPattern.FindStringSubmatch(raw)
	if len(m) < 3 {
		return "", ""
	}
	return strings.TrimSpace(m[1]), strings.TrimSpace(m[2])
}

func drawQuoteTextLine(dst draw.Image, face font.Face, line quoteTextLine, x, baseline int, c color.Color) {
	if face == nil {
		return
	}
	curX := x
	metrics := face.Metrics()
	lineH := metrics.Height.Ceil()
	emojiSize := quoteInlineEmojiSize(face)
	emojiY := baseline - metrics.Ascent.Ceil() + (lineH-emojiSize)/2
	if emojiY < 0 {
		emojiY = 0
	}
	for _, seg := range line {
		if seg.Image != nil && !seg.Image.Bounds().Empty() {
			r := image.Rect(curX, emojiY, curX+emojiSize, emojiY+emojiSize)
			drawImageFitRounded(dst, seg.Image, r, 5)
			curX += emojiSize + 2
			continue
		}
		if seg.Text == "" {
			continue
		}
		quoteDrawString(dst, face, seg.Text, curX, baseline, c)
		curX += quoteStringWidth(face, seg.Text)
	}
}

func quoteTextSegmentsFromEntities(text string, entities []quoteAPIEntity) []quoteTextSegment {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	if text == "" {
		return nil
	}
	custom := make([]quoteAPIEntity, 0, len(entities))
	for _, e := range entities {
		if e.Type != "custom_emoji" || e.Offset < 0 || e.Length <= 0 || e.Image == nil || e.Image.Bounds().Empty() {
			continue
		}
		custom = append(custom, e)
	}
	if len(custom) == 0 {
		return []quoteTextSegment{{Text: text}}
	}
	sort.SliceStable(custom, func(i, j int) bool {
		if custom[i].Offset == custom[j].Offset {
			return custom[i].Length < custom[j].Length
		}
		return custom[i].Offset < custom[j].Offset
	})
	out := make([]quoteTextSegment, 0, len(custom)*2+1)
	curByte := 0
	for _, e := range custom {
		startByte := quoteTextUTF16ByteIndex(text, e.Offset)
		endByte := quoteTextUTF16ByteIndex(text, e.Offset+e.Length)
		if startByte < curByte || endByte <= startByte || startByte > len(text) {
			continue
		}
		if startByte > curByte {
			out = append(out, quoteTextSegment{Text: text[curByte:startByte]})
		}
		out = append(out, quoteTextSegment{
			Text:  quoteFirstNonEmptyString(e.Fallback, text[startByte:endByte], "✨"),
			Image: e.Image,
		})
		curByte = endByte
	}
	if curByte < len(text) {
		out = append(out, quoteTextSegment{Text: text[curByte:]})
	}
	return out
}

func quoteFirstNonEmptyString(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func quoteTextUTF16ByteIndex(text string, targetCU int) int {
	if targetCU <= 0 {
		return 0
	}
	cu := 0
	for idx, r := range text {
		n := utf16.RuneLen(r)
		if n < 1 {
			n = 1
		}
		if cu+n > targetCU {
			return idx
		}
		cu += n
	}
	return len(text)
}

func wrapQuoteTextSegments(face font.Face, segments []quoteTextSegment, maxWidth int) []quoteTextLine {
	if face == nil || maxWidth <= 0 || len(segments) == 0 {
		return nil
	}
	var out []quoteTextLine
	var line quoteTextLine
	lineW := 0
	flush := func() {
		if len(line) == 0 {
			return
		}
		out = append(out, line)
		line = nil
		lineW = 0
	}
	addToken := func(tok quoteTextSegment) {
		if tok.Image == nil && tok.Text == "" {
			return
		}
		if tok.Image == nil && tok.Text == " " && len(line) == 0 {
			return
		}
		w := quoteSegmentWidth(face, tok)
		if w > maxWidth && tok.Image == nil {
			if len(line) > 0 {
				flush()
			}
			for _, chunk := range splitLongQuoteWord(face, tok.Text, maxWidth) {
				if chunk == "" {
					continue
				}
				out = append(out, quoteTextLine{{Text: chunk}})
			}
			return
		}
		if len(line) > 0 && lineW+w > maxWidth {
			flush()
			if tok.Image == nil {
				tok.Text = strings.TrimLeftFunc(tok.Text, unicode.IsSpace)
				if tok.Text == "" {
					return
				}
				w = quoteSegmentWidth(face, tok)
			}
		}
		line = append(line, tok)
		lineW += w
	}
	for _, seg := range segments {
		if seg.Image != nil && !seg.Image.Bounds().Empty() {
			addToken(seg)
			continue
		}
		for _, token := range quoteSplitTextTokens(seg.Text) {
			if token == "\n" {
				flush()
				continue
			}
			addToken(quoteTextSegment{Text: token})
		}
	}
	flush()
	return out
}

func quoteSplitTextTokens(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	var word strings.Builder
	spacePending := false
	flushWord := func() {
		if word.Len() == 0 {
			return
		}
		out = append(out, word.String())
		word.Reset()
	}
	for _, r := range s {
		if r == '\n' {
			flushWord()
			out = append(out, "\n")
			spacePending = false
			continue
		}
		if unicode.IsSpace(r) {
			flushWord()
			if !spacePending {
				out = append(out, " ")
				spacePending = true
			}
			continue
		}
		word.WriteRune(r)
		spacePending = false
	}
	flushWord()
	return out
}

func quoteSegmentWidth(face font.Face, seg quoteTextSegment) int {
	if seg.Image != nil && !seg.Image.Bounds().Empty() {
		return quoteInlineEmojiSize(face) + 2
	}
	return quoteStringWidth(face, seg.Text)
}

func quoteInlineEmojiSize(face font.Face) int {
	if face == nil {
		return 24
	}
	size := face.Metrics().Height.Ceil()
	if size < 20 {
		size = 20
	}
	if size > 32 {
		size = 32
	}
	return size
}

func trimQuoteSegmentLinesToFit(lines []quoteTextLine, face font.Face, maxWidth, maxHeight int) []quoteTextLine {
	if maxHeight <= 0 || face == nil {
		return nil
	}
	lineH := face.Metrics().Height.Ceil()
	if lineH <= 0 {
		return lines
	}
	maxLines := maxHeight / lineH
	if maxLines <= 0 {
		return nil
	}
	if len(lines) <= maxLines {
		return lines
	}
	out := append([]quoteTextLine(nil), lines[:maxLines]...)
	out[len(out)-1] = quoteTextLine{{Text: "…"}}
	return out
}

func drawImageFitRounded(dst draw.Image, src image.Image, r image.Rectangle, radius int) {
	sb := src.Bounds()
	if sb.Empty() || r.Empty() {
		return
	}
	sw := sb.Dx()
	sh := sb.Dy()
	dw := r.Dx()
	dh := r.Dy()
	if sw <= 0 || sh <= 0 || dw <= 0 || dh <= 0 {
		return
	}
	scaleNum := dw
	scaleDen := sw
	if sh*dw > sw*dh {
		scaleNum = dh
		scaleDen = sh
	}
	outW := sw * scaleNum / scaleDen
	outH := sh * scaleNum / scaleDen
	if outW < 1 {
		outW = 1
	}
	if outH < 1 {
		outH = 1
	}
	target := image.Rect(
		r.Min.X+(dw-outW)/2,
		r.Min.Y+(dh-outH)/2,
		r.Min.X+(dw-outW)/2+outW,
		r.Min.Y+(dh-outH)/2+outH,
	)
	drawImageCoverRounded(dst, src, target, radius)
}

func quoteStringWidth(face font.Face, s string) int {
	return font.MeasureString(face, s).Ceil()
}

func wrapQuoteText(face font.Face, text string, maxWidth int) []string {
	text = strings.TrimSpace(strings.ReplaceAll(text, "\r\n", "\n"))
	if text == "" {
		return nil
	}
	var out []string
	for _, paragraph := range strings.Split(text, "\n") {
		words := strings.Fields(paragraph)
		if len(words) == 0 {
			out = append(out, "")
			continue
		}
		line := ""
		for _, word := range words {
			candidate := word
			if line != "" {
				candidate = line + " " + word
			}
			if quoteStringWidth(face, candidate) <= maxWidth {
				line = candidate
				continue
			}
			if line != "" {
				out = append(out, line)
			}
			if quoteStringWidth(face, word) <= maxWidth {
				line = word
				continue
			}
			chunks := splitLongQuoteWord(face, word, maxWidth)
			if len(chunks) > 0 {
				out = append(out, chunks[:len(chunks)-1]...)
				line = chunks[len(chunks)-1]
			} else {
				line = word
			}
		}
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

func splitLongQuoteWord(face font.Face, word string, maxWidth int) []string {
	var chunks []string
	var cur []rune
	for _, r := range []rune(word) {
		next := string(append(cur, r))
		if len(cur) > 0 && quoteStringWidth(face, next) > maxWidth {
			chunks = append(chunks, string(cur))
			cur = []rune{r}
			continue
		}
		cur = append(cur, r)
	}
	if len(cur) > 0 {
		chunks = append(chunks, string(cur))
	}
	return chunks
}

func trimQuoteLinesToFit(lines []string, face font.Face, maxWidth, maxHeight int) []string {
	if maxHeight <= 0 {
		return nil
	}
	lineH := face.Metrics().Height.Ceil()
	if lineH <= 0 {
		return lines
	}
	maxLines := maxHeight / lineH
	if maxLines <= 0 {
		return nil
	}
	if len(lines) <= maxLines {
		return lines
	}
	out := append([]string(nil), lines[:maxLines]...)
	last := strings.TrimSpace(out[len(out)-1])
	for last != "" && quoteStringWidth(face, last+"…") > maxWidth {
		rs := []rune(last)
		if len(rs) <= 1 {
			break
		}
		last = string(rs[:len(rs)-1])
	}
	if last == "" {
		last = "…"
	} else {
		last += "…"
	}
	out[len(out)-1] = last
	return out
}

func fillRoundedRect(dst draw.Image, r image.Rectangle, radius int, c color.Color) {
	if radius <= 0 {
		draw.Draw(dst, r, image.NewUniform(c), image.Point{}, draw.Over)
		return
	}
	rr := radius * radius
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			dx := 0
			if x < r.Min.X+radius {
				dx = r.Min.X + radius - x
			} else if x >= r.Max.X-radius {
				dx = x - (r.Max.X - radius - 1)
			}
			dy := 0
			if y < r.Min.Y+radius {
				dy = r.Min.Y + radius - y
			} else if y >= r.Max.Y-radius {
				dy = y - (r.Max.Y - radius - 1)
			}
			if dx*dx+dy*dy <= rr {
				dst.Set(x, y, c)
			}
		}
	}
}

func requestQuoteStickerPNG(bot *tgbotapi.BotAPI, items []quoteHistoryItem) ([]byte, error) {
	if len(items) == 0 {
		return nil, errors.New("empty message list")
	}
	endpoint := strings.TrimSpace(os.Getenv("QUOTE_API_URI"))
	useExternalQuoteAPI := endpoint != ""
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
	payload := quoteAPIPayload{
		BotToken: "",
		Messages: make([]quoteAPIMessage, 0, len(items)),
	}
	if useExternalQuoteAPI && bot != nil {
		payload.BotToken = strings.TrimSpace(bot.Token)
	}
	avatarImageByUser := make(map[int64]image.Image, 16)
	avatarURLByUser := make(map[int64]string, 16)
	mediaURLByFileID := make(map[string]string, 32)
	customEmojiByID := make(map[string]quoteCustomEmojiAsset, 32)
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
		realText := strings.TrimSpace(firstNonEmptyUserText(m))
		text := realText
		hasMedia := false
		if text == "" {
			if m.Sticker != nil {
				text = "стикер"
				hasMedia = true
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
		from := quoteAPIFrom{ID: uid, Name: clipText(name, 48)}
		if !useExternalQuoteAPI && uid != 0 && bot != nil {
			if cached, ok := avatarImageByUser[uid]; ok {
				from.AvatarImage = cached
			} else {
				img := resolveUserAvatarImage(bot, uid)
				avatarImageByUser[uid] = img
				from.AvatarImage = img
			}
		} else if useExternalQuoteAPI && uid != 0 && bot != nil {
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
		item := quoteAPIMessage{
			From:   from,
			Text:   clipText(text, 1200),
			Avatar: true,
		}
		if !useExternalQuoteAPI {
			item.Local = buildLocalQuoteMediaPreview(bot, mediaURLByFileID, m)
			if item.Local != nil && hasMedia && realText == "" {
				item.Text = ""
			}
			if m.Chat != nil && m.MessageID != 0 {
				item.Reacts = hydrateQuoteReactionCustomEmoji(bot, mediaURLByFileID, getReactionMessageDisplay(m.Chat.ID, m.MessageID), customEmojiByID)
			}
		}
		if ents := buildQuoteEntitiesPayload(it.Raw, item.Text); len(ents) > 0 {
			if !useExternalQuoteAPI {
				ents = hydrateQuoteCustomEmojiEntities(bot, mediaURLByFileID, item.Text, ents, customEmojiByID)
			}
			item.Entities = ents
		}
		if useExternalQuoteAPI {
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
		}
		payload.Messages = append(payload.Messages, item)
	}
	if len(payload.Messages) == 0 {
		return nil, errors.New("empty quote payload")
	}
	if !useExternalQuoteAPI {
		return renderLocalQuoteStickerPNG(payload.Messages)
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
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

func buildQuoteEntitiesPayload(raw *rawMessageWithEmoji, chosenText string) []quoteAPIEntity {
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
	out := make([]quoteAPIEntity, 0, len(entities))
	for _, e := range entities {
		if e.Offset < 0 || e.Length <= 0 {
			continue
		}
		item := quoteAPIEntity{
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

type quoteTelegramSticker struct {
	CustomEmojiID string `json:"custom_emoji_id"`
	Emoji         string `json:"emoji"`
	FileID        string `json:"file_id"`
	Thumb         *struct {
		FileID string `json:"file_id"`
	} `json:"thumb"`
	Thumbnail *struct {
		FileID string `json:"file_id"`
	} `json:"thumbnail"`
}

func hydrateQuoteCustomEmojiEntities(bot *tgbotapi.BotAPI, urlCache map[string]string, text string, entities []quoteAPIEntity, cache map[string]quoteCustomEmojiAsset) []quoteAPIEntity {
	if len(entities) == 0 {
		return entities
	}
	missingSet := make(map[string]struct{})
	for i := range entities {
		if entities[i].Type != "custom_emoji" {
			continue
		}
		id := strings.TrimSpace(entities[i].CustomEmojiID)
		if id == "" {
			continue
		}
		entities[i].Fallback = strings.TrimSpace(sliceUTF16ByEntity(text, entities[i].Offset, entities[i].Length))
		if cache != nil {
			if asset, ok := cache[id]; ok {
				entities[i].Image = asset.Image
				if entities[i].Fallback == "" {
					entities[i].Fallback = asset.Fallback
				}
				continue
			}
		}
		missingSet[id] = struct{}{}
	}
	if bot != nil && len(missingSet) > 0 {
		missing := make([]string, 0, len(missingSet))
		for id := range missingSet {
			missing = append(missing, id)
		}
		sort.Strings(missing)
		stickers, err := quoteGetCustomEmojiStickers(bot, missing)
		if err != nil {
			if debugTriggerLogEnabled {
				log.Printf("quote custom emoji fetch failed count=%d err=%v", len(missing), err)
			}
		} else {
			for _, st := range stickers {
				id := strings.TrimSpace(st.CustomEmojiID)
				if id == "" {
					continue
				}
				asset := quoteCustomEmojiAsset{
					Fallback: strings.TrimSpace(st.Emoji),
					Known:    true,
				}
				for _, fileID := range quoteCustomEmojiPreviewFileIDs(st) {
					if media := telegramFileImagePreview(bot, urlCache, fileID, "custom_emoji"); media != nil {
						asset.Image = media.Preview
						break
					}
					if media := telegramFileFramePreview(bot, urlCache, fileID, "custom_emoji"); media != nil {
						asset.Image = media.Preview
						break
					}
				}
				if cache != nil {
					cache[id] = asset
				}
			}
		}
		if cache != nil {
			for _, id := range missing {
				if _, ok := cache[id]; !ok {
					cache[id] = quoteCustomEmojiAsset{Known: true}
				}
			}
		}
	}
	for i := range entities {
		if entities[i].Type != "custom_emoji" {
			continue
		}
		id := strings.TrimSpace(entities[i].CustomEmojiID)
		if id == "" || cache == nil {
			continue
		}
		asset := cache[id]
		entities[i].Image = asset.Image
		if entities[i].Fallback == "" {
			entities[i].Fallback = asset.Fallback
		}
	}
	return entities
}

func hydrateQuoteReactionCustomEmoji(bot *tgbotapi.BotAPI, urlCache map[string]string, items []reactionDisplay, cache map[string]quoteCustomEmojiAsset) []reactionDisplay {
	if len(items) == 0 {
		return nil
	}
	out := append([]reactionDisplay(nil), items...)
	for i := range out {
		id, fallback := parseTGEmojiSnippet(out[i].Emoji)
		if id == "" {
			continue
		}
		asset := quoteResolveCustomEmojiAsset(bot, urlCache, id, fallback, cache)
		out[i].Image = asset.Image
		if fallback == "" && asset.Fallback != "" {
			out[i].Emoji = buildTGEmojiSnippet(id, asset.Fallback)
		}
	}
	return out
}

func quoteResolveCustomEmojiAsset(bot *tgbotapi.BotAPI, urlCache map[string]string, id, fallback string, cache map[string]quoteCustomEmojiAsset) quoteCustomEmojiAsset {
	id = strings.TrimSpace(id)
	fallback = strings.TrimSpace(fallback)
	if id == "" {
		return quoteCustomEmojiAsset{Fallback: fallback, Known: true}
	}
	if cache != nil {
		if asset, ok := cache[id]; ok && asset.Known {
			if asset.Fallback == "" {
				asset.Fallback = fallback
			}
			return asset
		}
	}
	asset := quoteCustomEmojiAsset{Fallback: fallback, Known: true}
	if bot != nil {
		stickers, err := quoteGetCustomEmojiStickers(bot, []string{id})
		if err != nil {
			if debugTriggerLogEnabled {
				log.Printf("quote custom emoji fetch failed id=%s err=%v", id, err)
			}
		} else if len(stickers) > 0 {
			st := stickers[0]
			if strings.TrimSpace(st.Emoji) != "" {
				asset.Fallback = strings.TrimSpace(st.Emoji)
			}
			for _, fileID := range quoteCustomEmojiPreviewFileIDs(st) {
				if media := telegramFileImagePreview(bot, urlCache, fileID, "custom_emoji"); media != nil {
					asset.Image = media.Preview
					break
				}
				if media := telegramFileFramePreview(bot, urlCache, fileID, "custom_emoji"); media != nil {
					asset.Image = media.Preview
					break
				}
			}
		}
	}
	if cache != nil {
		cache[id] = asset
	}
	return asset
}

func quoteGetCustomEmojiStickers(bot *tgbotapi.BotAPI, ids []string) ([]quoteTelegramSticker, error) {
	if bot == nil {
		return nil, errors.New("telegram bot is nil")
	}
	clean := make([]string, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		clean = append(clean, id)
	}
	if len(clean) == 0 {
		return nil, nil
	}
	rawIDs, err := json.Marshal(clean)
	if err != nil {
		return nil, err
	}
	resp, err := bot.MakeRequest("getCustomEmojiStickers", tgbotapi.Params{
		"custom_emoji_ids": string(rawIDs),
	})
	if err != nil {
		return nil, err
	}
	var out []quoteTelegramSticker
	if err := json.Unmarshal(resp.Result, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func quoteCustomEmojiPreviewFileIDs(st quoteTelegramSticker) []string {
	out := make([]string, 0, 2)
	if st.Thumbnail != nil {
		if id := strings.TrimSpace(st.Thumbnail.FileID); id != "" {
			out = append(out, id)
		}
	}
	if st.Thumb != nil {
		if id := strings.TrimSpace(st.Thumb.FileID); id != "" {
			out = append(out, id)
		}
	}
	if id := strings.TrimSpace(st.FileID); id != "" {
		seen := false
		for _, existing := range out {
			if existing == id {
				seen = true
				break
			}
		}
		if !seen {
			out = append(out, id)
		}
	}
	return out
}

func buildQuoteMediaPayload(bot *tgbotapi.BotAPI, urlCache map[string]string, msg *tgbotapi.Message) *quoteMediaPayload {
	if msg == nil {
		return nil
	}
	// Stickers: prefer thumbnail; for static stickers fallback to sticker file itself.
	if msg.Sticker != nil {
		st := msg.Sticker
		if st.Thumbnail != nil {
			th := st.Thumbnail
			if id := strings.TrimSpace(th.FileID); id != "" {
				url := resolveTelegramFileURL(bot, urlCache, id)
				fileID := ""
				if url == "" {
					fileID = id
				}
				quoteMediaChoiceLog(msg, "sticker.thumbnail", url, fileID, th.Width, th.Height)
				return &quoteMediaPayload{FileID: fileID, URL: url}
			}
		}
		if !st.IsAnimated {
			if id := strings.TrimSpace(st.FileID); id != "" {
				url := resolveTelegramFileURL(bot, urlCache, id)
				fileID := ""
				if url == "" {
					fileID = id
				}
				quoteMediaChoiceLog(msg, "sticker.file", url, fileID, st.Width, st.Height)
				return &quoteMediaPayload{FileID: fileID, URL: url}
			}
		}
		if id := strings.TrimSpace(st.FileID); id != "" {
			quoteMediaChoiceLog(msg, "sticker.file_id_only", "", id, st.Width, st.Height)
			return &quoteMediaPayload{FileID: id}
		}
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
							return &quoteMediaPayload{URL: upURL}
						}
						quoteMediaLogf(msg, "animation frame uploaded but URL unavailable; fallback to original file_id")
					}
					quoteMediaLogf(msg, "animation frame extracted but upload failed; fallback to original file_id")
				}
			}
			fileID := id
			quoteMediaChoiceLog(msg, "animation.file_id_only", "", fileID, msg.Animation.Width, msg.Animation.Height)
			return &quoteMediaPayload{FileID: fileID}
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
							return &quoteMediaPayload{URL: upURL}
						}
						quoteMediaLogf(msg, "video frame uploaded but URL unavailable; fallback to original file_id")
					}
					quoteMediaLogf(msg, "video frame extracted but upload failed; fallback to original file_id")
				}
			}
			fileID := id
			quoteMediaChoiceLog(msg, "video.file_id_only", "", fileID, msg.Video.Width, msg.Video.Height)
			return &quoteMediaPayload{FileID: fileID}
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
					return &quoteMediaPayload{FileID: fileID, URL: url}
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
							quoteMediaLogf(msg, "document.video frame uploaded but URL unavailable; fallback to original file_id")
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
			return v
		}
	}
	url, err := getTelegramFileDirectURL(bot, fileID)
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
	if cache != nil {
		cache[fileID] = url
	}
	return url
}

func resolveUserAvatarImage(bot *tgbotapi.BotAPI, userID int64) image.Image {
	if bot == nil || userID == 0 {
		return nil
	}
	photos, err := bot.GetUserProfilePhotos(tgbotapi.UserProfilePhotosConfig{
		UserID: userID,
		Offset: 0,
		Limit:  1,
	})
	if err != nil || len(photos.Photos) == 0 || len(photos.Photos[0]) == 0 {
		if err != nil && debugTriggerLogEnabled {
			log.Printf("quote avatar profile fetch failed user=%d err=%v", userID, err)
		}
		return nil
	}
	best := photos.Photos[0][len(photos.Photos[0])-1]
	fileID := strings.TrimSpace(best.FileID)
	if fileID == "" {
		return nil
	}
	url, err := getTelegramFileDirectURL(bot, fileID)
	if err != nil {
		if debugTriggerLogEnabled {
			log.Printf("quote avatar file url fetch failed user=%d file_id=%q err=%v", userID, clipText(fileID, 24), err)
		}
		return nil
	}
	raw, err := fetchTrustedTelegramImageBytes(url)
	if err != nil {
		if debugTriggerLogEnabled {
			log.Printf("quote avatar download failed user=%d url=%q err=%v", userID, clipText(redactTelegramToken(url), 160), err)
		}
		return nil
	}
	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		if debugTriggerLogEnabled {
			log.Printf("quote avatar decode failed user=%d err=%v", userID, err)
		}
		return nil
	}
	return img
}

func buildLocalQuoteMediaPreview(bot *tgbotapi.BotAPI, urlCache map[string]string, msg *tgbotapi.Message) *quoteLocalMedia {
	if bot == nil || msg == nil {
		return nil
	}
	if msg.Sticker != nil {
		st := msg.Sticker
		if st.Thumbnail != nil {
			if media := telegramFileImagePreview(bot, urlCache, st.Thumbnail.FileID, "sticker"); media != nil {
				return media
			}
		}
		if media := telegramFileImagePreview(bot, urlCache, st.FileID, "sticker"); media != nil {
			return media
		}
		return telegramFileFramePreview(bot, urlCache, st.FileID, "sticker")
	}
	if len(msg.Photo) > 0 {
		p := msg.Photo[len(msg.Photo)-1]
		return telegramFileImagePreview(bot, urlCache, p.FileID, "photo")
	}
	if msg.Animation != nil {
		if msg.Animation.Thumbnail != nil {
			if media := telegramFileImagePreview(bot, urlCache, msg.Animation.Thumbnail.FileID, "gif"); media != nil {
				return media
			}
		}
		return telegramFileFramePreview(bot, urlCache, msg.Animation.FileID, "gif")
	}
	if msg.Video != nil {
		if msg.Video.Thumbnail != nil {
			if media := telegramFileImagePreview(bot, urlCache, msg.Video.Thumbnail.FileID, "video"); media != nil {
				return media
			}
		}
		return telegramFileFramePreview(bot, urlCache, msg.Video.FileID, "video")
	}
	if msg.VideoNote != nil {
		if msg.VideoNote.Thumbnail != nil {
			if media := telegramFileImagePreview(bot, urlCache, msg.VideoNote.Thumbnail.FileID, "video"); media != nil {
				return media
			}
		}
		return telegramFileFramePreview(bot, urlCache, msg.VideoNote.FileID, "video")
	}
	if msg.Document != nil {
		mime := strings.ToLower(strings.TrimSpace(msg.Document.MimeType))
		if strings.HasPrefix(mime, "image/") {
			return telegramFileImagePreview(bot, urlCache, msg.Document.FileID, "image")
		}
		if strings.HasPrefix(mime, "video/") {
			if msg.Document.Thumbnail != nil {
				if media := telegramFileImagePreview(bot, urlCache, msg.Document.Thumbnail.FileID, "video"); media != nil {
					return media
				}
			}
			return telegramFileFramePreview(bot, urlCache, msg.Document.FileID, "video")
		}
	}
	return nil
}

func telegramFileImagePreview(bot *tgbotapi.BotAPI, urlCache map[string]string, fileID, kind string) *quoteLocalMedia {
	url := resolveTelegramFileURL(bot, urlCache, fileID)
	if url == "" {
		return nil
	}
	raw, err := fetchTrustedTelegramImageBytes(url)
	if err != nil {
		if debugTriggerLogEnabled {
			log.Printf("quote local preview image download failed kind=%s file_id=%q url=%q err=%v", kind, clipText(fileID, 24), clipText(redactTelegramToken(url), 160), err)
		}
		return nil
	}
	img, err := decodeQuotePreviewImage(raw)
	if err != nil {
		if debugTriggerLogEnabled {
			log.Printf("quote local preview image decode failed kind=%s file_id=%q err=%v", kind, clipText(fileID, 24), err)
		}
		return nil
	}
	return &quoteLocalMedia{Kind: kind, Preview: img}
}

func telegramFileFramePreview(bot *tgbotapi.BotAPI, urlCache map[string]string, fileID, kind string) *quoteLocalMedia {
	url := resolveTelegramFileURL(bot, urlCache, fileID)
	if url == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	raw, err := extractFirstFramePNG(ctx, url)
	cancel()
	if err != nil {
		if debugTriggerLogEnabled {
			log.Printf("quote local preview frame extract failed kind=%s file_id=%q err=%v", kind, clipText(fileID, 24), err)
		}
		return nil
	}
	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		if debugTriggerLogEnabled {
			log.Printf("quote local preview frame decode failed kind=%s file_id=%q err=%v", kind, clipText(fileID, 24), err)
		}
		return nil
	}
	return &quoteLocalMedia{Kind: kind, Preview: img}
}

func decodeQuotePreviewImage(raw []byte) (image.Image, error) {
	if len(raw) == 0 {
		return nil, errors.New("empty preview image")
	}
	if img, _, err := image.Decode(bytes.NewReader(raw)); err == nil {
		return img, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx,
		"/usr/bin/ffmpeg",
		"-v", "error",
		"-i", "pipe:0",
		"-frames:v", "1",
		"-f", "image2pipe",
		"-vcodec", "png",
		"pipe:1",
	)
	cmd.Stdin = bytes.NewReader(raw)
	var out bytes.Buffer
	var serr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &serr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("preview image ffmpeg decode failed: %w (%s)", err, clipText(strings.TrimSpace(serr.String()), 240))
	}
	if out.Len() == 0 {
		return nil, errors.New("preview image ffmpeg returned empty frame")
	}
	img, _, err := image.Decode(bytes.NewReader(out.Bytes()))
	if err != nil {
		return nil, err
	}
	return img, nil
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
	url, err := getTelegramFileDirectURL(bot, fileID)
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
	if fresh, err := bot.GetChat(tgbotapi.ChatInfoConfig{ChatConfig: tgbotapi.ChatConfig{ChatID: chat.ID}}); err == nil {
		currentName = strings.TrimSpace(fresh.StickerSetName)
		canSet = fresh.CanSetStickerSet
	} else if debugTriggerLogEnabled {
		log.Printf("set chat sticker set getChat failed chat=%d err=%v", chat.ID, err)
	}
	if !canSet {
		if debugTriggerLogEnabled {
			log.Printf("set chat sticker set skipped chat=%d set=%q can_set=false current=%q", chat.ID, setName, currentName)
		}
		return
	}
	if currentName != "" {
		// Keep existing live sticker set as-is.
		if _, err := bot.GetStickerSet(tgbotapi.GetStickerSetConfig{Name: currentName}); err == nil {
			return
		}
		// Existing chat sticker set reference is stale/missing; re-bind below.
		if debugTriggerLogEnabled {
			log.Printf("set chat sticker set stale current chat=%d current=%q target=%q", chat.ID, currentName, setName)
		}
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
	sid, _ := newUUID4()
	st := quoteStickerSession{
		ID:          sid,
		ChatID:      msg.Chat.ID,
		UserID:      msg.From.ID,
		CreatedAt:   time.Now(),
		Page:        0,
		Emoji:       "",
		Count:       count,
		TargetMsgID: target.MessageID,
	}
	sessions.Put(st)
	return true, "", &st
}

func handleQuoteStickerCallback(bot *tgbotapi.BotAPI, sessions *quoteStickerSessionManager, history *quoteStickerHistory, cb *tgbotapi.CallbackQuery) bool {
	if bot == nil || sessions == nil || history == nil || cb == nil {
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
	case quoteStickerActionNoop:
		_, _ = bot.Request(tgbotapi.NewCallback(cb.ID, ""))
		return true
	case quoteStickerActionCancel:
		sessions.Delete(sid)
		_, _ = bot.Request(tgbotapi.NewCallback(cb.ID, "Отменено"))
		if cb.Message != nil {
			_, _ = bot.Request(tgbotapi.NewEditMessageText(cb.Message.Chat.ID, cb.Message.MessageID, "Сохранение quote-стикера отменено."))
		}
		return true
	case quoteStickerActionPage:
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
		if cb.Message != nil {
			kb := buildQuoteStickerPickerKeyboard(st)
			edit := tgbotapi.NewEditMessageReplyMarkup(cb.Message.Chat.ID, cb.Message.MessageID, kb)
			if _, err := bot.Request(edit); err != nil && debugTriggerLogEnabled {
				log.Printf("quote sticker page edit markup failed sid=%s chat=%d user=%d err=%v", st.ID, st.ChatID, st.UserID, err)
			}
		}
		return true
	case quoteStickerActionPick:
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
	case quoteStickerActionSave:
		if strings.TrimSpace(st.Emoji) == "" {
			_, _ = bot.Request(tgbotapi.NewCallback(cb.ID, "Сначала выберите эмодзи"))
			return true
		}
		count := st.Count
		if count < 1 {
			count = 1
		}
		items := history.CollectBefore(st.ChatID, st.TargetMsgID, count)
		if len(items) == 0 {
			_, _ = bot.Request(tgbotapi.NewCallback(cb.ID, "Сообщения для цитаты не найдены"))
			return true
		}
		img, err := requestQuoteStickerPNG(bot, items)
		if err != nil {
			_, _ = bot.Request(tgbotapi.NewCallback(cb.ID, clipText("Не удалось собрать quote-стикер: "+err.Error(), 180)))
			return true
		}
		err = ensureQuoteStickerSetAndAdd(bot, cb.Message.Chat, cb.From.ID, st.Emoji, img)
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

const (
	quoteStickerActionNoop   = "noop"
	quoteStickerActionCancel = "cancel"
	quoteStickerActionPage   = "page"
	quoteStickerActionPick   = "pick"
	quoteStickerActionSave   = "save"
)
