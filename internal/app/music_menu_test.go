package app

// Меню «где искать трек» после неудачного скачивания аудио.

import (
	"strings"
	"testing"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"trigger-admin-bot/internal/mediadl"
)

func retryTask(mode mediadl.Mode, text, query string) mediaDownloadTask {
	return mediaDownloadTask{
		SendCtx: sendContext{ChatID: -1002329738424, ReplyTo: 501},
		Mode:    mode,
		Query:   query,
		Msg: &tgbotapi.Message{
			MessageID: 502,
			Text:      text,
			Chat:      &tgbotapi.Chat{ID: -1002329738424},
			From:      &tgbotapi.User{ID: 1519741912},
		},
	}
}

func TestMusicRetryMenuHasProviders(t *testing.T) {
	menu, ok := musicRetryMenu(retryTask(mediadl.ModeAudio, "танец под водой", "танец под водой"))
	if !ok {
		t.Fatal("меню не собралось")
	}
	if !strings.Contains(menu.Text, "Где искать трек") {
		t.Fatalf("неожиданный текст меню: %q", menu.Text)
	}
	kb, isKB := menu.ReplyMarkup.(tgbotapi.InlineKeyboardMarkup)
	if !isKB {
		t.Fatalf("нет клавиатуры: %T", menu.ReplyMarkup)
	}
	var data []string
	for _, row := range kb.InlineKeyboard {
		for _, btn := range row {
			if btn.CallbackData != nil {
				data = append(data, *btn.CallbackData)
			}
		}
	}
	joined := strings.Join(data, " ")
	for _, want := range []string{"musicpick_s:", "musicpick_y:", "musicpick_v:", "musicpick_sc:", "musicpick_c:"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("в меню нет %q: %s", want, joined)
		}
	}
	// Запрос живёт в токене, а не в callback открытым текстом.
	if strings.Contains(joined, "танец под водой") {
		t.Fatalf("query попал в callback открытым текстом: %s", joined)
	}
}

func TestMusicRetryMenuUsesMessageTextFallback(t *testing.T) {
	menu, ok := musicRetryMenu(retryTask(mediadl.ModeAudio, "включи танец под водой", ""))
	if !ok {
		t.Fatal("меню не собралось")
	}
	if menu.BaseChat.ChatID != -1002329738424 {
		t.Fatalf("меню уйдёт не в тот чат: %d", menu.BaseChat.ChatID)
	}
	if menu.ReplyToMessageID != 501 || !menu.AllowSendingWithoutReply {
		t.Fatalf("ответ не привязан к исходному сообщению: %+v", menu)
	}
}

func TestMusicRetryMenuWithoutAuthor(t *testing.T) {
	task := retryTask(mediadl.ModeAudio, "трек", "")
	task.Msg.From = nil
	if _, ok := musicRetryMenu(task); ok {
		t.Fatal("без автора меню собирать нельзя (нужен user_id для токена)")
	}
}
