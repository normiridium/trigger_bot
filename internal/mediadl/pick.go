package mediadl

import (
	"context"
	crand "crypto/rand"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

const (
	ModeAudio = "audio"
	ModeVideo = "video"
	ModeAuto  = "auto"
)

type ChoiceRequest struct {
	Token        string
	URL          string
	ChatID       int64
	ReplyTo      int
	SourceMsgID  int
	UserID       int64
	DeleteSource bool
	ExpiresAt    time.Time
}

type ChoiceProcessor func(ctx context.Context, req ChoiceRequest, mode string) error

type ChoiceFailureReporter func(chatID int64, title string, err error)

var choiceMu sync.Mutex
var choiceRequests = make(map[string]ChoiceRequest)

func BuildChoiceKeyboard(msg *tgbotapi.Message, req ChoiceRequest) tgbotapi.InlineKeyboardMarkup {
	audioToken := putChoice(req)
	videoToken := putChoice(req)
	cancelToken := putChoice(req)
	rows := [][]tgbotapi.InlineKeyboardButton{
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("Скачать аудио", "mdpick_a:"+audioToken),
			tgbotapi.NewInlineKeyboardButtonData("Скачать видео", "mdpick_v:"+videoToken),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("Отменить", "mdpick_c:"+cancelToken),
		),
	}
	_ = msg
	return tgbotapi.NewInlineKeyboardMarkup(rows...)
}

func HandleChoiceCallback(bot *tgbotapi.BotAPI, cb *tgbotapi.CallbackQuery, report ChoiceFailureReporter, process ChoiceProcessor) bool {
	if cb == nil || bot == nil {
		return false
	}
	mode := ""
	token := ""
	switch {
	case strings.HasPrefix(cb.Data, "mdpick_a:"):
		mode = ModeAudio
		token = strings.TrimPrefix(cb.Data, "mdpick_a:")
	case strings.HasPrefix(cb.Data, "mdpick_v:"):
		mode = ModeVideo
		token = strings.TrimPrefix(cb.Data, "mdpick_v:")
	case strings.HasPrefix(cb.Data, "mdpick_c:"):
		token = strings.TrimPrefix(cb.Data, "mdpick_c:")
		req, ok, msg := takeChoice(token, cb.From.ID)
		if !ok {
			_, _ = bot.Request(tgbotapi.NewCallback(cb.ID, msg))
			return true
		}
		_, _ = bot.Request(tgbotapi.NewCallback(cb.ID, "Отменено"))
		if cb.Message != nil {
			_, _ = bot.Request(tgbotapi.DeleteMessageConfig{ChatID: cb.Message.Chat.ID, MessageID: cb.Message.MessageID})
		}
		if req.DeleteSource && req.SourceMsgID > 0 {
			_, _ = bot.Request(tgbotapi.DeleteMessageConfig{ChatID: req.ChatID, MessageID: req.SourceMsgID})
		}
		return true
	default:
		return false
	}

	req, ok, msg := takeChoice(token, cb.From.ID)
	if !ok {
		_, _ = bot.Request(tgbotapi.NewCallback(cb.ID, msg))
		return true
	}
	_, _ = bot.Request(tgbotapi.NewCallback(cb.ID, "Скачиваю..."))
	if cb.Message != nil {
		status := "🎞 Выбрано: видео"
		if mode == ModeAudio {
			status = "🎵 Выбрано: аудио"
		}
		_, _ = bot.Request(tgbotapi.NewEditMessageText(cb.Message.Chat.ID, cb.Message.MessageID, status))
	}
	if process == nil {
		reportFailure(report, req.ChatID, "ошибка обработки выбора скачивания", errors.New("media pick processor not configured"))
		return true
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	err := process(ctx, req, mode)
	cancel()
	if err != nil {
		reportFailure(report, req.ChatID, "ошибка скачивания файла", err)
		return true
	}
	if cb.Message != nil {
		_, _ = bot.Request(tgbotapi.DeleteMessageConfig{ChatID: cb.Message.Chat.ID, MessageID: cb.Message.MessageID})
	}
	if req.DeleteSource && req.SourceMsgID > 0 {
		_, _ = bot.Request(tgbotapi.DeleteMessageConfig{ChatID: req.ChatID, MessageID: req.SourceMsgID})
	}
	return true
}

func putChoice(req ChoiceRequest) string {
	choiceMu.Lock()
	defer choiceMu.Unlock()
	now := time.Now()
	for k, v := range choiceRequests {
		if v.ExpiresAt.Before(now) {
			delete(choiceRequests, k)
		}
	}
	token := newChoiceToken()
	req.Token = token
	if req.ExpiresAt.IsZero() {
		// Keep media choice alive long enough so users can decide without rushed timeout.
		req.ExpiresAt = now.Add(2 * time.Hour)
	}
	choiceRequests[token] = req
	return token
}

func takeChoice(token string, userID int64) (ChoiceRequest, bool, string) {
	choiceMu.Lock()
	defer choiceMu.Unlock()
	req, ok := choiceRequests[token]
	if !ok {
		return ChoiceRequest{}, false, "выбор устарел"
	}
	if time.Now().After(req.ExpiresAt) {
		delete(choiceRequests, token)
		return ChoiceRequest{}, false, "выбор устарел"
	}
	if req.UserID != 0 && userID != 0 && req.UserID != userID {
		return ChoiceRequest{}, false, "этот выбор доступен только автору запроса"
	}
	delete(choiceRequests, token)
	return req, true, ""
}

func newChoiceToken() string {
	var b [6]byte
	_, _ = crand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func reportFailure(report ChoiceFailureReporter, chatID int64, title string, err error) {
	if report == nil {
		return
	}
	report(chatID, title, err)
}
