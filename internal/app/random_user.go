package app

// Тег случайного участника: в шаблонах и заданиях расписаний используется
// типовая переменная Go-шаблона {{ .random_username }} → @nick.
//
// Источник — коллекция chat_users (по чату): выбор идёт через $sample по
// chat_id, поэтому чаты не смешиваются и рестарт ничего не обнуляет.
// chatUserIndex остаётся кэшем в памяти (USER_INDEX_MAX) для resolve и как
// запас, если база недоступна.

import (
	"bytes"
	"strings"
	texttemplate "text/template"
)

// scheduleActionFuncs отдаёт заданиям расписаний тот же набор тегов-функций, что
// и шаблонам ответов (weekday, time_of_day, now, date …): иначе {{ weekday … }}
// в задании уходил модели как текст. Регулярки не используются — разбор и вызов
// делает сам text/template.
func scheduleActionFuncs() texttemplate.FuncMap {
	merged := mergedResponseTemplateFuncs(nil)
	out := make(texttemplate.FuncMap, len(merged))
	for name, fn := range merged {
		out[name] = fn
	}
	return out
}

// chatRandomUsernameResolver задаётся из Run() поверх chatUserIndex.
var chatRandomUsernameResolver func(chatID int64) string

func setChatRandomUsernameResolver(fn func(chatID int64) string) {
	chatRandomUsernameResolver = fn
}

// randomChatUsername возвращает "@nick" случайного участника чата или "".
func randomChatUsername(chatID int64) string {
	fn := chatRandomUsernameResolver
	if fn == nil || chatID == 0 {
		return ""
	}
	nick := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(fn(chatID)), "@"))
	if nick == "" {
		return ""
	}
	return "@" + nick
}

// scheduleActionValues отдаёт данные для рендера задания расписания: имена
// переменных берутся из общего реестра тегов (tags.go), значения по умолчанию
// пустые, random_username — живой ник участника этого чата.
func scheduleActionValues(chatID int64) map[string]interface{} {
	names := tagVariableNames()
	data := make(map[string]interface{}, len(names)+1)
	for _, name := range names {
		data[name] = ""
	}
	data["random_username"] = randomChatUsername(chatID)
	return data
}

// renderRandomUsernameTag разворачивает шаблонные переменные в тексте задания
// штатным движком text/template (никаких регулярных выражений: разбор и
// подстановку делает сам Go). Если в тексте случайные фигурные скобки и шаблон
// не парсится, текст возвращается как есть — расписание не ломается.
// Первая возвращаемая строка — результат, вторая — изменился ли текст.
func renderRandomUsernameTag(text string, chatID int64) (expanded string, replaced bool) {
	if !strings.Contains(text, "{{") {
		return text, false
	}
	tmpl, err := texttemplate.New("schedule_action").
		Funcs(scheduleActionFuncs()).
		Option("missingkey=zero").
		Parse(text)
	if err != nil {
		return text, false
	}
	var buf bytes.Buffer
	data := scheduleActionValues(chatID)
	if err := tmpl.Execute(&buf, data); err != nil {
		return text, false
	}
	out := buf.String()
	return out, out != text
}
