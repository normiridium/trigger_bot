package app

import (
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

func defaultBotCommands() []tgbotapi.BotCommand {
	return []tgbotapi.BotCommand{
		{Command: cmdStart, Description: "О боте и возможностях"},
		{Command: cmdHelp, Description: "Справка по командам"},
		{Command: cmdEmojiID, Description: "ID кастомного эмодзи"},
		{Command: cmdStickerID, Description: "Код стикера из реплая"},
		{Command: cmdGifID, Description: "ID гифки и подпись"},
		{Command: cmdSpotifySearch, Description: "Поиск трека в Spotify"},
		{Command: cmdMyPortrait, Description: "Показать мой портрет"},
		{Command: cmdDeleteMyPortrait, Description: "Удалить мой портрет"},
	}
}

func adminBotCommands() []tgbotapi.BotCommand {
	return []tgbotapi.BotCommand{
		{Command: cmdBan, Description: "Бан пользователя"},
		{Command: cmdUnban, Description: "Снять бан"},
		{Command: cmdMute, Description: "Мут пользователя"},
		{Command: cmdUnmute, Description: "Снять мут"},
		{Command: cmdKick, Description: "Кик пользователя"},
		{Command: cmdReadonly, Description: "Режим readonly в чате"},
		{Command: cmdReloadAdmins, Description: "Обновить кеш админов"},
	}
}

func allVisibleBotCommands() []tgbotapi.BotCommand {
	base := defaultBotCommands()
	extra := adminBotCommands()
	out := make([]tgbotapi.BotCommand, 0, len(base)+len(extra))
	seen := make(map[string]struct{}, len(base)+len(extra))
	for _, c := range base {
		key := strings.TrimSpace(strings.ToLower(c.Command))
		if key == "" {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, c)
	}
	for _, c := range extra {
		key := strings.TrimSpace(strings.ToLower(c.Command))
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, c)
	}
	return out
}

func syncBotCommands(bot *tgbotapi.BotAPI) {
	if bot == nil || !envBool("BOT_COMMANDS_SYNC_ON_START", true) {
		return
	}
	langs := parseBotCommandLangs()
	scopes := []struct {
		scope    *tgbotapi.BotCommandScope
		commands []tgbotapi.BotCommand
	}{
		{
			scope:    &tgbotapi.BotCommandScope{Type: "default"},
			commands: allVisibleBotCommands(),
		},
		{
			scope:    &tgbotapi.BotCommandScope{Type: "all_group_chats"},
			commands: allVisibleBotCommands(),
		},
		{
			scope:    &tgbotapi.BotCommandScope{Type: "all_chat_administrators"},
			commands: allVisibleBotCommands(),
		},
	}
	for _, chatID := range parseBotCommandChatIDs() {
		scopes = append(scopes,
			struct {
				scope    *tgbotapi.BotCommandScope
				commands []tgbotapi.BotCommand
			}{
				scope:    &tgbotapi.BotCommandScope{Type: "chat", ChatID: chatID},
				commands: allVisibleBotCommands(),
			},
			struct {
				scope    *tgbotapi.BotCommandScope
				commands []tgbotapi.BotCommand
			}{
				scope:    &tgbotapi.BotCommandScope{Type: "chat_administrators", ChatID: chatID},
				commands: allVisibleBotCommands(),
			},
		)
	}
	for _, lang := range langs {
		for _, sc := range scopes {
			delCfg := tgbotapi.DeleteMyCommandsConfig{
				Scope:        sc.scope,
				LanguageCode: lang,
			}
			if _, err := bot.Request(delCfg); err != nil {
				log.Printf("deleteMyCommands failed scope=%s lang=%q err=%v", sc.scope.Type, lang, err)
			}
			setCfg := tgbotapi.SetMyCommandsConfig{
				Commands:     sc.commands,
				Scope:        sc.scope,
				LanguageCode: lang,
			}
			if _, err := bot.Request(setCfg); err != nil {
				log.Printf("setMyCommands failed scope=%s lang=%q err=%v", sc.scope.Type, lang, err)
			}
		}
	}
	log.Printf("bot commands synced langs=%q", strings.Join(langs, ","))
}

func startBotCommandsSyncLoop(bot *tgbotapi.BotAPI) {
	if bot == nil {
		return
	}
	syncBotCommands(bot)
	intervalMin := envInt("BOT_COMMANDS_SYNC_INTERVAL_MIN", 0)
	if intervalMin <= 0 {
		return
	}
	interval := time.Duration(intervalMin) * time.Minute
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for range t.C {
			syncBotCommands(bot)
		}
	}()
}

func parseBotCommandLangs() []string {
	// Keep default language ("") plus common UI locales to override stale locale-specific menus.
	raw := strings.TrimSpace(os.Getenv("BOT_COMMANDS_LANGS"))
	if raw == "" {
		raw = ",ru,en"
	} else if !strings.Contains(raw, ",") {
		raw = raw + ",ru,en"
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts)+1)
	seen := map[string]struct{}{"": {}}
	out = append(out, "")
	for _, p := range parts {
		v := strings.ToLower(strings.TrimSpace(p))
		if v == "" {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}

func parseBotCommandChatIDs() []int64 {
	raw := strings.TrimSpace(os.Getenv("ALLOWED_CHAT_IDS"))
	if raw == "" {
		return nil
	}
	split := func(r rune) bool {
		switch r {
		case ',', ';', '\n', '\r', '\t', ' ':
			return true
		default:
			return false
		}
	}
	parts := strings.FieldsFunc(raw, split)
	out := make([]int64, 0, len(parts))
	seen := map[int64]struct{}{}
	for _, p := range parts {
		v, err := strconv.ParseInt(strings.TrimSpace(p), 10, 64)
		if err != nil || v == 0 {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}
