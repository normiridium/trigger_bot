package app

import (
	_ "embed"
	"fmt"
	"hash/fnv"
	"math/rand"
	"strings"
	"unicode"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

//go:embed data/anon_adjectives_ru.txt
var anonAdjectivesRaw string

//go:embed data/anon_nouns_ru.txt
var anonNounsRaw string

var anonAdjectivesDefault = []string{
	"Красная", "Тихая", "Лесная", "Пушистая", "Смелая", "Лунная", "Шустрая", "Снежная",
	"Яркая", "Хитрая", "Медовая", "Морская", "Ловкая", "Ночная", "Добрая", "Тёплая",
}

var anonAdjectives = loadAnonAdjectives()

var anonNounsDefault = []string{
	"панда", "коала", "лиса", "выдра", "сова", "рысь", "енот", "лама",
	"белка", "дельфин", "волчица", "черепаха", "ирбис", "куница", "пума", "цапля",
}

var anonNouns = loadAnonNouns()

func loadAnonAdjectives() []string {
	lines := strings.Split(anonAdjectivesRaw, "\n")
	out := make([]string, 0, len(lines))
	seen := make(map[string]struct{}, len(lines))
	for _, line := range lines {
		w := strings.TrimSpace(strings.ToLower(line))
		if w == "" {
			continue
		}
		if _, ok := seen[w]; ok {
			continue
		}
		seen[w] = struct{}{}
		r := []rune(w)
		if len(r) > 0 {
			r[0] = unicode.ToUpper(r[0])
		}
		out = append(out, string(r))
	}
	if len(out) == 0 {
		return anonAdjectivesDefault
	}
	return out
}

func loadAnonNouns() []string {
	lines := strings.Split(anonNounsRaw, "\n")
	out := make([]string, 0, len(lines))
	seen := make(map[string]struct{}, len(lines))
	for _, line := range lines {
		w := strings.TrimSpace(strings.ToLower(line))
		if w == "" {
			continue
		}
		if _, ok := seen[w]; ok {
			continue
		}
		seen[w] = struct{}{}
		out = append(out, w)
	}
	if len(out) == 0 {
		return anonNounsDefault
	}
	return out
}

func buildAnonAlias() string {
	adj := anonAdjectives[rand.Intn(len(anonAdjectives))]
	noun := anonNouns[rand.Intn(len(anonNouns))]
	return adj + " " + noun
}

func inflectAdjectiveByTag(adj, tag string) string {
	form := resolveGenderVariant(tag, genderVariants{
		Male:    "male",
		Female:  "female",
		Neuter:  "neutral",
		Plural:  "plural",
		Unknown: "female",
	})
	adj = strings.TrimSpace(adj)
	if adj == "" {
		return adj
	}
	switch form {
	case "male":
		if strings.HasSuffix(adj, "ая") {
			return strings.TrimSuffix(adj, "ая") + "ый"
		}
		if strings.HasSuffix(adj, "яя") {
			return strings.TrimSuffix(adj, "яя") + "ий"
		}
	case "neutral":
		if strings.HasSuffix(adj, "ая") {
			return strings.TrimSuffix(adj, "ая") + "ое"
		}
		if strings.HasSuffix(adj, "яя") {
			return strings.TrimSuffix(adj, "яя") + "ее"
		}
	case "plural":
		if strings.HasSuffix(adj, "ая") {
			return strings.TrimSuffix(adj, "ая") + "ые"
		}
		if strings.HasSuffix(adj, "яя") {
			return strings.TrimSuffix(adj, "яя") + "ие"
		}
	}
	return adj
}

func buildAnonAliasForAuthor(authorID int64, senderTag string) string {
	if authorID == 0 {
		return buildAnonAlias()
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(fmt.Sprintf("%d", authorID)))
	sum := h.Sum64()
	adj := inflectAdjectiveByTag(anonAdjectives[int(sum%uint64(len(anonAdjectives)))], senderTag)
	noun := anonNouns[int((sum/uint64(len(anonAdjectives)))%uint64(len(anonNouns)))]
	return adj + " " + noun
}

func handleAnonCommand(bot *tgbotapi.BotAPI, msg *tgbotapi.Message) bool {
	if bot == nil || msg == nil {
		return false
	}
	text := strings.TrimSpace(msg.CommandArguments())
	if text == "" {
		reply(sendContext{Bot: bot, ChatID: msg.Chat.ID, ReplyTo: msg.MessageID}, "Использование: /anon ваш текст", false)
		return true
	}

	authorID := int64(0)
	senderTag := ""
	if msg.From != nil {
		authorID = msg.From.ID
		senderTag = getChatMemberTagRaw(bot.Token, msg.Chat.ID, msg.From.ID)
	}
	alias := buildAnonAliasForAuthor(authorID, senderTag)
	out := fmt.Sprintf("<tg-emoji emoji-id=\"5974347006779329639\">🎭</tg-emoji> <b>%s</b>\n%s\n\n<i>Анонимка</i>", alias, text)
	sendHTML(sendContext{Bot: bot, ChatID: msg.Chat.ID}, out, false)

	if msg.MessageID != 0 {
		_, _ = bot.Request(tgbotapi.DeleteMessageConfig{
			ChatID:    msg.Chat.ID,
			MessageID: msg.MessageID,
		})
	}
	return true
}
