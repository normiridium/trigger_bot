package musicpick

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
	ProviderSpotify = "spotify"
	ProviderYandex  = "yandex"
)

type ChoiceRequest struct {
	Token        string
	Query        string
	ChatID       int64
	ReplyTo      int
	SourceMsgID  int
	UserID       int64
	DeleteSource bool
	ExpiresAt    time.Time
}

type FailureReporter func(chatID int64, title string, err error)
type ChoiceProcessor func(ctx context.Context, req ChoiceRequest, provider string) error

var choiceMu sync.Mutex
var choiceRequests = make(map[string]ChoiceRequest)

func BuildChoiceKeyboard(msg *tgbotapi.Message, replyTo, sourceMsgID int, deleteSource bool, query string) tgbotapi.InlineKeyboardMarkup {
	q := strings.TrimSpace(query)
	spotifyToken := putChoice(ChoiceRequest{
		Query:        q,
		ChatID:       msg.Chat.ID,
		ReplyTo:      replyTo,
		SourceMsgID:  sourceMsgID,
		UserID:       msg.From.ID,
		DeleteSource: deleteSource,
	})
	yandexToken := putChoice(ChoiceRequest{
		Query:        q,
		ChatID:       msg.Chat.ID,
		ReplyTo:      replyTo,
		SourceMsgID:  sourceMsgID,
		UserID:       msg.From.ID,
		DeleteSource: deleteSource,
	})
	cancelToken := putChoice(ChoiceRequest{
		Query:        q,
		ChatID:       msg.Chat.ID,
		ReplyTo:      replyTo,
		SourceMsgID:  sourceMsgID,
		UserID:       msg.From.ID,
		DeleteSource: deleteSource,
	})
	return tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("Spotify", "musicpick_s:"+spotifyToken),
			tgbotapi.NewInlineKeyboardButtonData("Yandex Music", "musicpick_y:"+yandexToken),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("Отменить", "musicpick_c:"+cancelToken),
		),
	)
}

func HandleChoiceCallback(bot *tgbotapi.BotAPI, cb *tgbotapi.CallbackQuery, report FailureReporter, process ChoiceProcessor) bool {
	if cb == nil || bot == nil {
		return false
	}
	if strings.HasPrefix(cb.Data, "musicpick_c:") {
		token := strings.TrimPrefix(cb.Data, "musicpick_c:")
		req, ok, msg := takeChoice(token, cb.From.ID)
		if !ok {
			_, _ = bot.Request(tgbotapi.NewCallback(cb.ID, msg))
			return true
		}
		_, _ = bot.Request(tgbotapi.NewCallback(cb.ID, "Отменено"))
		if cb.Message != nil {
			_, _ = bot.Request(tgbotapi.DeleteMessageConfig{
				ChatID:    cb.Message.Chat.ID,
				MessageID: cb.Message.MessageID,
			})
		}
		if req.DeleteSource && req.SourceMsgID > 0 {
			_, _ = bot.Request(tgbotapi.DeleteMessageConfig{
				ChatID:    req.ChatID,
				MessageID: req.SourceMsgID,
			})
		}
		return true
	}
	if !strings.HasPrefix(cb.Data, "musicpick_s:") && !strings.HasPrefix(cb.Data, "musicpick_y:") {
		return false
	}
	provider := ProviderSpotify
	token := ""
	if strings.HasPrefix(cb.Data, "musicpick_s:") {
		token = strings.TrimPrefix(cb.Data, "musicpick_s:")
		provider = ProviderSpotify
	} else {
		token = strings.TrimPrefix(cb.Data, "musicpick_y:")
		provider = ProviderYandex
	}
	req, ok, msg := takeChoice(token, cb.From.ID)
	if !ok {
		_, _ = bot.Request(tgbotapi.NewCallback(cb.ID, msg))
		return true
	}
	_, _ = bot.Request(tgbotapi.NewCallback(cb.ID, "Ищу..."))
	if process == nil {
		reportFailure(report, req.ChatID, "ошибка обработки выбора музыкального сервиса", errors.New("music pick processor not configured"))
		return true
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	err := process(ctx, req, provider)
	cancel()
	if err != nil {
		reportFailure(report, req.ChatID, "ошибка обработки выбора музыкального сервиса", err)
		return true
	}
	if cb.Message != nil {
		_, _ = bot.Request(tgbotapi.DeleteMessageConfig{
			ChatID:    cb.Message.Chat.ID,
			MessageID: cb.Message.MessageID,
		})
	}
	if req.DeleteSource && req.SourceMsgID > 0 {
		_, _ = bot.Request(tgbotapi.DeleteMessageConfig{
			ChatID:    req.ChatID,
			MessageID: req.SourceMsgID,
		})
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

func reportFailure(report FailureReporter, chatID int64, title string, err error) {
	if report == nil {
		return
	}
	report(chatID, title, err)
}
