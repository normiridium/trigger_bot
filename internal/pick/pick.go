package pick

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

type PickRequest struct {
	Token        string
	TrackID      string
	Artist       string
	Title        string
	ChatID       int64
	ReplyTo      int
	SourceMsgID  int
	UserID       int64
	DeleteSource bool
	ExpiresAt    time.Time
}

type PickTrack struct {
	ID          string
	Artist      string
	Title       string
	DurationSec float64
}

type FailureReporter func(chatID int64, title string, err error)
type PickProcessor func(ctx context.Context, req PickRequest) error

var pickMu sync.Mutex
var pickRequests = make(map[string]PickRequest)

func BuildPickKeyboard(msg *tgbotapi.Message, replyTo int, sourceMsgID int, deleteSource bool, tracks []PickTrack) tgbotapi.InlineKeyboardMarkup {
	rows := make([][]tgbotapi.InlineKeyboardButton, 0, len(tracks))
	for _, track := range tracks {
		label := strings.TrimSpace(track.Title)
		artist := strings.TrimSpace(track.Artist)
		if artist != "" {
			label = artist + " — " + label
		}
		if track.DurationSec > 0 {
			label = fmt.Sprintf("%s (%s)", label, FormatDuration(track.DurationSec))
		}
		if label == "" {
			label = track.ID
		}
		token := putPick(PickRequest{
			TrackID:      track.ID,
			Artist:       artist,
			Title:        strings.TrimSpace(track.Title),
			ChatID:       msg.Chat.ID,
			ReplyTo:      replyTo,
			SourceMsgID:  sourceMsgID,
			UserID:       msg.From.ID,
			DeleteSource: deleteSource,
		})
		btn := tgbotapi.NewInlineKeyboardButtonData(label, "vkpick:"+token)
		rows = append(rows, tgbotapi.NewInlineKeyboardRow(btn))
	}
	cancelToken := putPick(PickRequest{
		TrackID:      "",
		Artist:       "",
		Title:        "",
		ChatID:       msg.Chat.ID,
		ReplyTo:      replyTo,
		SourceMsgID:  sourceMsgID,
		UserID:       msg.From.ID,
		DeleteSource: deleteSource,
	})
	cancelBtn := tgbotapi.NewInlineKeyboardButtonData("Отменить", "vkpick_cancel:"+cancelToken)
	rows = append(rows, tgbotapi.NewInlineKeyboardRow(cancelBtn))
	return tgbotapi.NewInlineKeyboardMarkup(rows...)
}

func HandlePickCallback(bot *tgbotapi.BotAPI, cb *tgbotapi.CallbackQuery, report FailureReporter, process PickProcessor) bool {
	if cb == nil || bot == nil {
		return false
	}
	if strings.HasPrefix(cb.Data, "vkpick_cancel:") {
		token := strings.TrimPrefix(cb.Data, "vkpick_cancel:")
		req, ok, msg := takePick(token, cb.From.ID)
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
	if !strings.HasPrefix(cb.Data, "vkpick:") {
		return false
	}
	token := strings.TrimPrefix(cb.Data, "vkpick:")
	req, ok, msg := takePick(token, cb.From.ID)
	if !ok {
		_, _ = bot.Request(tgbotapi.NewCallback(cb.ID, msg))
		return true
	}
	_, _ = bot.Request(tgbotapi.NewCallback(cb.ID, "Скачиваю..."))

	var pickMsgID int
	if cb.Message != nil {
		pickMsgID = cb.Message.MessageID
		edit := tgbotapi.NewEditMessageText(cb.Message.Chat.ID, cb.Message.MessageID, "🎵 Выбрано: "+strings.TrimSpace(req.Artist+" — "+req.Title))
		_, _ = bot.Request(edit)
	}

	if process == nil {
		reportFailure(report, req.ChatID, "ошибка обработки выбора трека", errors.New("pick processor not configured"))
		return true
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	err := process(ctx, req)
	cancel()
	if err != nil {
		reportFailure(report, req.ChatID, "ошибка отправки аудио", err)
		return true
	}
	if pickMsgID > 0 {
		_, _ = bot.Request(tgbotapi.DeleteMessageConfig{
			ChatID:    req.ChatID,
			MessageID: pickMsgID,
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

func FormatDuration(sec float64) string {
	if sec <= 0 {
		return ""
	}
	total := int64(sec + 0.5)
	h := total / 3600
	m := (total % 3600) / 60
	s := total % 60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%02d:%02d", m, s)
}

func putPick(req PickRequest) string {
	pickMu.Lock()
	defer pickMu.Unlock()
	now := time.Now()
	for k, v := range pickRequests {
		if v.ExpiresAt.Before(now) {
			delete(pickRequests, k)
		}
	}
	token := newPickToken()
	req.Token = token
	if req.ExpiresAt.IsZero() {
		req.ExpiresAt = now.Add(5 * time.Minute)
	}
	pickRequests[token] = req
	return token
}

func takePick(token string, userID int64) (PickRequest, bool, string) {
	pickMu.Lock()
	defer pickMu.Unlock()
	req, ok := pickRequests[token]
	if !ok {
		return PickRequest{}, false, "выбор устарел"
	}
	if time.Now().After(req.ExpiresAt) {
		delete(pickRequests, token)
		return PickRequest{}, false, "выбор устарел"
	}
	if req.UserID != 0 && userID != 0 && req.UserID != userID {
		return PickRequest{}, false, "этот выбор доступен только автору запроса"
	}
	delete(pickRequests, token)
	return req, true, ""
}

func newPickToken() string {
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
