package vk

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
	vkmusic "github.com/normiridium/vk-music-bot-api/vkmusic"
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

type AudioSender func(chatID int64, url, artist, title string) error

type FailureReporter func(chatID int64, title string, err error)

var pickMu sync.Mutex
var pickRequests = make(map[string]PickRequest)

func BuildPickKeyboard(msg *tgbotapi.Message, deleteSource bool, tracks []vkmusic.Track) tgbotapi.InlineKeyboardMarkup {
	rows := make([][]tgbotapi.InlineKeyboardButton, 0, len(tracks))
	for _, track := range tracks {
		label := strings.TrimSpace(track.Title)
		artist := strings.TrimSpace(track.Artist)
		if artist != "" {
			label = artist + " — " + label
		}
		if track.Duration > 0 {
			label = fmt.Sprintf("%s (%s)", label, FormatDuration(float64(track.Duration)))
		}
		if label == "" {
			label = track.ID
		}
		token := putPick(PickRequest{
			TrackID:      track.ID,
			Artist:       artist,
			Title:        strings.TrimSpace(track.Title),
			ChatID:       msg.Chat.ID,
			ReplyTo:      msg.MessageID,
			SourceMsgID:  msg.MessageID,
			UserID:       msg.From.ID,
			DeleteSource: deleteSource,
		})
		btn := tgbotapi.NewInlineKeyboardButtonData(label, "vkpick:"+token)
		rows = append(rows, tgbotapi.NewInlineKeyboardRow(btn))
	}
	return tgbotapi.NewInlineKeyboardMarkup(rows...)
}

func HandlePickCallback(bot *tgbotapi.BotAPI, cb *tgbotapi.CallbackQuery, vkClient *vkmusic.Client, report FailureReporter, sendAudio AudioSender) bool {
	if cb == nil || bot == nil {
		return false
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

	if vkClient == nil {
		reportFailure(report, req.ChatID, "ошибка VK-музыки", errors.New("VK_TOKEN не настроен"))
		return true
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	song, err := vkClient.GetAudioURL(ctx, req.TrackID)
	cancel()
	if err != nil || song == nil || strings.TrimSpace(song.URL) == "" {
		if err == nil {
			err = errors.New("empty audio URL")
		}
		reportFailure(report, req.ChatID, "ошибка отправки аудио VK", err)
		return true
	}
	if sendAudio == nil {
		reportFailure(report, req.ChatID, "ошибка отправки аудио VK", errors.New("audio sender not configured"))
		return true
	}
	if err := sendAudio(req.ChatID, song.URL, song.Artist, song.Title); err != nil {
		reportFailure(report, req.ChatID, "ошибка отправки аудио VK", err)
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
