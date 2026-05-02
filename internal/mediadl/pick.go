package mediadl

import (
	"context"
	crand "crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
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
	_, service, _ := NormalizeSupportedURL(req.URL)
	if service == "coub" {
		audioToken := putChoice(req)
		videoToken := putChoice(req)
		cancelToken := putChoice(req)
		rows := [][]tgbotapi.InlineKeyboardButton{
			tgbotapi.NewInlineKeyboardRow(
				tgbotapi.NewInlineKeyboardButtonData("Скачать аудио", "mdpick_a:"+audioToken),
				tgbotapi.NewInlineKeyboardButtonData("Скачать видео", "mdpick_cv:"+videoToken),
			),
			tgbotapi.NewInlineKeyboardRow(
				tgbotapi.NewInlineKeyboardButtonData("Отменить", "mdpick_c:"+cancelToken),
			),
		}
		_ = msg
		return tgbotapi.NewInlineKeyboardMarkup(rows...)
	}

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
		// On cancel we intentionally keep source message even if delete flag is enabled.
		_ = req
		return true
	case strings.HasPrefix(cb.Data, "mdpick_cv:"):
		token = strings.TrimPrefix(cb.Data, "mdpick_cv:")
		req, ok, msg := takeChoice(token, cb.From.ID)
		if !ok {
			_, _ = bot.Request(tgbotapi.NewCallback(cb.ID, msg))
			return true
		}
		token1 := putChoice(req)
		token2 := putChoice(req)
		token5 := putChoice(req)
		tokenAll := putChoice(req)
		backToken := putChoice(req)
		cancelToken := putChoice(req)
		kb := tgbotapi.NewInlineKeyboardMarkup(
			tgbotapi.NewInlineKeyboardRow(
				tgbotapi.NewInlineKeyboardButtonData("1", "mdpick_cl1:"+token1),
				tgbotapi.NewInlineKeyboardButtonData("2", "mdpick_cl2:"+token2),
				tgbotapi.NewInlineKeyboardButtonData("5", "mdpick_cl5:"+token5),
				tgbotapi.NewInlineKeyboardButtonData("Все", "mdpick_cla:"+tokenAll),
			),
			tgbotapi.NewInlineKeyboardRow(
				tgbotapi.NewInlineKeyboardButtonData("Назад", "mdpick_cb:"+backToken),
				tgbotapi.NewInlineKeyboardButtonData("Отменить", "mdpick_c:"+cancelToken),
			),
		)
		if cb.Message != nil {
			edit := tgbotapi.NewEditMessageTextAndMarkup(cb.Message.Chat.ID, cb.Message.MessageID, "Сколько кусочков Coub оставить?", kb)
			_, _ = bot.Request(edit)
		}
		_, _ = bot.Request(tgbotapi.NewCallback(cb.ID, "Выбери количество"))
		return true
	case strings.HasPrefix(cb.Data, "mdpick_cb:"):
		token = strings.TrimPrefix(cb.Data, "mdpick_cb:")
		req, ok, msg := takeChoice(token, cb.From.ID)
		if !ok {
			_, _ = bot.Request(tgbotapi.NewCallback(cb.ID, msg))
			return true
		}
		audioToken := putChoice(req)
		videoToken := putChoice(req)
		cancelToken := putChoice(req)
		kb := tgbotapi.NewInlineKeyboardMarkup(
			tgbotapi.NewInlineKeyboardRow(
				tgbotapi.NewInlineKeyboardButtonData("Скачать аудио", "mdpick_a:"+audioToken),
				tgbotapi.NewInlineKeyboardButtonData("Скачать видео", "mdpick_cv:"+videoToken),
			),
			tgbotapi.NewInlineKeyboardRow(
				tgbotapi.NewInlineKeyboardButtonData("Отменить", "mdpick_c:"+cancelToken),
			),
		)
		if cb.Message != nil {
			edit := tgbotapi.NewEditMessageTextAndMarkup(cb.Message.Chat.ID, cb.Message.MessageID, "Выбери формат скачивания:", kb)
			_, _ = bot.Request(edit)
		}
		_, _ = bot.Request(tgbotapi.NewCallback(cb.ID, "Назад"))
		return true
	case strings.HasPrefix(cb.Data, "mdpick_cl1:"):
		mode = "coub_loop:1"
		token = strings.TrimPrefix(cb.Data, "mdpick_cl1:")
	case strings.HasPrefix(cb.Data, "mdpick_cl2:"):
		mode = "coub_loop:2"
		token = strings.TrimPrefix(cb.Data, "mdpick_cl2:")
	case strings.HasPrefix(cb.Data, "mdpick_cl5:"):
		mode = "coub_loop:5"
		token = strings.TrimPrefix(cb.Data, "mdpick_cl5:")
	case strings.HasPrefix(cb.Data, "mdpick_cla:"):
		mode = "coub_loop:all"
		token = strings.TrimPrefix(cb.Data, "mdpick_cla:")
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
		} else if strings.HasPrefix(mode, "coub_loop:") {
			label := strings.TrimPrefix(mode, "coub_loop:")
			if label == "all" {
				label = "все"
			}
			status = fmt.Sprintf("🎞 Coub: %s кусочков", label)
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
