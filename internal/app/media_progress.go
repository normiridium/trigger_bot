package app

import (
	"html"
	"log"
	"strings"
	"sync"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

func mediaProgressFrames() []string {
	// Custom TG emoji pieces (with env override).
	barLeft := envOr("MEDIA_PROGRESS_BAR_LEFT", `<tg-emoji emoji-id="6190572316343144337">◀️</tg-emoji>`)
	barRun := envOr("MEDIA_PROGRESS_BAR_RUN", `<tg-emoji emoji-id="6192818283591242169">⏯</tg-emoji>`)
	barFill := envOr("MEDIA_PROGRESS_BAR_FILL", `<tg-emoji emoji-id="6190533137651469695">⏹</tg-emoji>`)
	barIdle := envOr("MEDIA_PROGRESS_BAR_IDLE", `<tg-emoji emoji-id="6197267096615784923">⏩</tg-emoji>`)
	barEnd0 := envOr("MEDIA_PROGRESS_BAR_END_IDLE", `<tg-emoji emoji-id="6197315938983874282">#️⃣</tg-emoji>`)
	barEnd1 := envOr("MEDIA_PROGRESS_BAR_END_DONE", `<tg-emoji emoji-id="6192944714543534133">⏭</tg-emoji>`)

	return []string{
		// 10% ◀️⏩⏩⏩⏩#️⃣
		barLeft + barIdle + barIdle + barIdle + barIdle + barEnd0,
		// 20% ◀️⏯⏩⏩⏩#️⃣
		barLeft + barRun + barIdle + barIdle + barIdle + barEnd0,
		// 30% ◀️⏹⏩⏩⏩#️⃣
		barLeft + barFill + barIdle + barIdle + barIdle + barEnd0,
		// 40% ◀️⏹⏯⏩⏩#️⃣
		barLeft + barFill + barRun + barIdle + barIdle + barEnd0,
		// 50% ◀️⏹⏹⏩⏩#️⃣
		barLeft + barFill + barFill + barIdle + barIdle + barEnd0,
		// 60% ◀️⏹⏹⏯⏩#️⃣
		barLeft + barFill + barFill + barRun + barIdle + barEnd0,
		// 70% ◀️⏹⏹⏹⏩#️⃣
		barLeft + barFill + barFill + barFill + barIdle + barEnd0,
		// 80% ◀️⏹⏹⏹⏯#️⃣
		barLeft + barFill + barFill + barFill + barRun + barEnd0,
		// 90% ◀️⏹⏹⏹⏹⏭
		barLeft + barFill + barFill + barFill + barFill + barEnd1,
	}
}

type mediaProgressHandle struct {
	bot       *tgbotapi.BotAPI
	chatID    int64
	messageID int
	mu        sync.Mutex
	lastFrame int
	stage     string
	frames    []string
	lastText  string
	lastEdit  time.Time
}

func (h *mediaProgressHandle) SetFrame(frame int) {
	if h == nil || h.bot == nil || h.chatID == 0 || h.messageID == 0 {
		return
	}
	if frame < 0 {
		frame = 0
	}
	if frame >= len(h.frames) {
		frame = len(h.frames) - 1
	}
	h.mu.Lock()
	if h.lastFrame == frame {
		h.mu.Unlock()
		return
	}
	h.lastFrame = frame
	stage := h.stage
	h.mu.Unlock()

	h.edit(frame, stage)
}

func (h *mediaProgressHandle) SetStage(stage string) {
	if h == nil || h.bot == nil || h.chatID == 0 || h.messageID == 0 {
		return
	}
	stage = strings.TrimSpace(stage)
	h.mu.Lock()
	if h.stage == stage {
		h.mu.Unlock()
		return
	}
	h.stage = stage
	frame := h.lastFrame
	h.mu.Unlock()

	h.edit(frame, stage)
}

func (h *mediaProgressHandle) edit(frame int, stage string) {
	if h == nil || h.bot == nil || h.chatID == 0 || h.messageID == 0 {
		return
	}
	text := renderMediaProgressText(h.frames, frame, stage)
	minInterval := time.Duration(envInt("MEDIA_PROGRESS_MIN_EDIT_INTERVAL_MS", 1000)) * time.Millisecond
	now := time.Now()
	h.mu.Lock()
	if text == h.lastText {
		h.mu.Unlock()
		return
	}
	if minInterval > 0 && !h.lastEdit.IsZero() && now.Sub(h.lastEdit) < minInterval {
		h.mu.Unlock()
		return
	}
	h.lastText = text
	h.lastEdit = now
	h.mu.Unlock()

	edit := tgbotapi.NewEditMessageText(h.chatID, h.messageID, renderMediaProgressText(h.frames, frame, stage))
	edit.ParseMode = "HTML"
	if _, e := h.bot.Request(edit); e != nil && debugTriggerLogEnabled {
		log.Printf("media progress edit failed chat=%d msg=%d err=%v", h.chatID, h.messageID, e)
	}
}

func renderMediaProgressText(frames []string, frame int, stage string) string {
	if len(frames) == 0 {
		return ""
	}
	if frame < 0 {
		frame = 0
	}
	if frame >= len(frames) {
		frame = len(frames) - 1
	}
	bar := frames[frame]
	stage = strings.TrimSpace(stage)
	if stage == "" {
		return bar
	}
	return bar + "\n<i>" + html.EscapeString(stage) + "</i>"
}

func startMediaDownloadProgress(task mediaDownloadTask) (*mediaProgressHandle, func()) {
	bot := task.SendCtx.Bot
	chatID := task.SendCtx.ChatID
	frames := mediaProgressFrames()
	if bot == nil || chatID == 0 || len(frames) == 0 {
		return nil, func() {}
	}

	replyTo := task.SendCtx.ReplyTo
	msg := tgbotapi.NewMessage(chatID, renderMediaProgressText(frames, 0, "Подготовка"))
	msg.ParseMode = "HTML"
	if replyTo > 0 {
		msg.ReplyToMessageID = replyTo
		msg.AllowSendingWithoutReply = true
	}
	sent, err := bot.Send(msg)
	if err != nil {
		return nil, func() {}
	}
	h := &mediaProgressHandle{
		bot:       bot,
		chatID:    chatID,
		messageID: sent.MessageID,
		lastFrame: 0,
		stage:     "Подготовка",
		frames:    frames,
		lastText:  msg.Text,
		lastEdit:  time.Now(),
	}

	var once sync.Once
	return h, func() {
		once.Do(func() {
			_, _ = bot.Request(tgbotapi.DeleteMessageConfig{ChatID: chatID, MessageID: sent.MessageID})
		})
	}
}
