package app

import (
	"html"
	"log"
	"strings"
	"sync"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

const (
	// Custom TG emoji pieces (keep ids).
	barLeft = `<tg-emoji emoji-id="6190572316343144337">◀️</tg-emoji>`
	barRun  = `<tg-emoji emoji-id="6192818283591242169">⏯</tg-emoji>`
	barFill = `<tg-emoji emoji-id="6190533137651469695">⏹</tg-emoji>`
	barIdle = `<tg-emoji emoji-id="6197267096615784923">⏩</tg-emoji>`
	barEnd0 = `<tg-emoji emoji-id="6197315938983874282">#️⃣</tg-emoji>`
	barEnd1 = `<tg-emoji emoji-id="6192944714543534133">⏭</tg-emoji>`
)

var mediaProgressFrames = []string{
	// 20% ◀️⏩⏩⏩#️⃣
	barLeft + barIdle + barIdle + barIdle + barEnd0,
	// 30% ◀️⏯⏩⏩#️⃣
	barLeft + barRun + barIdle + barIdle + barEnd0,
	// 40% ◀️⏹⏩⏩#️⃣
	barLeft + barFill + barIdle + barIdle + barEnd0,
	// 50% ◀️⏹⏯⏩#️⃣
	barLeft + barFill + barRun + barIdle + barEnd0,
	// 60% ◀️⏹⏹⏩#️⃣
	barLeft + barFill + barFill + barIdle + barEnd0,
	// 70% ◀️⏹⏹⏯#️⃣
	barLeft + barFill + barFill + barRun + barEnd0,
	// 80% ◀️⏹⏹⏹#️⃣
	barLeft + barFill + barFill + barFill + barEnd0,
	// 90% ◀️⏹⏹⏹⏭
	barLeft + barFill + barFill + barFill + barEnd1,
}

type mediaProgressHandle struct {
	bot       *tgbotapi.BotAPI
	chatID    int64
	messageID int
	mu        sync.Mutex
	lastFrame int
	stage     string
}

func (h *mediaProgressHandle) SetFrame(frame int) {
	if h == nil || h.bot == nil || h.chatID == 0 || h.messageID == 0 {
		return
	}
	if frame < 0 {
		frame = 0
	}
	if frame >= len(mediaProgressFrames) {
		frame = len(mediaProgressFrames) - 1
	}
	h.mu.Lock()
	if h.lastFrame == frame {
		h.mu.Unlock()
		return
	}
	h.lastFrame = frame
	stage := h.stage
	h.mu.Unlock()

	edit := tgbotapi.NewEditMessageText(h.chatID, h.messageID, renderMediaProgressText(frame, stage))
	edit.ParseMode = "HTML"
	if _, e := h.bot.Request(edit); e != nil && debugTriggerLogEnabled {
		log.Printf("media progress edit failed chat=%d msg=%d err=%v", h.chatID, h.messageID, e)
	}
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

	edit := tgbotapi.NewEditMessageText(h.chatID, h.messageID, renderMediaProgressText(frame, stage))
	edit.ParseMode = "HTML"
	if _, e := h.bot.Request(edit); e != nil && debugTriggerLogEnabled {
		log.Printf("media progress stage edit failed chat=%d msg=%d err=%v", h.chatID, h.messageID, e)
	}
}

func renderMediaProgressText(frame int, stage string) string {
	if frame < 0 {
		frame = 0
	}
	if frame >= len(mediaProgressFrames) {
		frame = len(mediaProgressFrames) - 1
	}
	bar := mediaProgressFrames[frame]
	stage = strings.TrimSpace(stage)
	if stage == "" {
		return bar
	}
	return bar + "\n<i>" + html.EscapeString(stage) + "</i>"
}

func startMediaDownloadProgress(task mediaDownloadTask) (*mediaProgressHandle, func()) {
	bot := task.SendCtx.Bot
	chatID := task.SendCtx.ChatID
	if bot == nil || chatID == 0 || len(mediaProgressFrames) == 0 {
		return nil, func() {}
	}

	replyTo := task.SendCtx.ReplyTo
	msg := tgbotapi.NewMessage(chatID, mediaProgressFrames[0])
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
	}

	var once sync.Once
	return h, func() {
		once.Do(func() {
			_, _ = bot.Request(tgbotapi.DeleteMessageConfig{ChatID: chatID, MessageID: sent.MessageID})
		})
	}
}
