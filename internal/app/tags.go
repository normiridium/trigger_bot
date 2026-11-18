package app

// Единый реестр тегов. Из него строятся и выпадающий список в админке
// (/trigger_bot/template_tags), и набор данных для рендера заданий расписаний:
// один источник — расхождению взяться неоткуда.
//
// Разбор тега идёт движком text/template (никаких регулярных выражений).

import (
	texttemplate "text/template"
	"text/template/parse"
)

type tagItem struct {
	Value string
	Label string
}

var responseTagList = []tagItem{
	{Value: "{{ .message }}", Label: "{{ .message }} — текст сообщения"},
	{Value: "{{ .user_text }}", Label: "{{ .user_text }} — текст/подпись сообщения"},
	{Value: "{{ .capturing_text }}", Label: "{{ .capturing_text }} — захват regex"},
	{Value: "{{ index .capturing_groups 2 }}", Label: "{{ index .capturing_groups 2 }} — группа regex по номеру"},
	{Value: "{{ .capturing_count }}", Label: "{{ .capturing_count }} — сколько групп захвата"},
	{Value: "{{ .capturing_choice }}", Label: "{{ .capturing_choice }} — вариант из regex"},
	{Value: "{{ .capturing_option }}", Label: "{{ .capturing_option }} — вариант из regex (алиас)"},
	{Value: "{{ .user_id }}", Label: "{{ .user_id }} — ID автора"},
	{Value: "{{ .user_first_name }}", Label: "{{ .user_first_name }} — имя автора"},
	{Value: "{{ .user_username }}", Label: "{{ .user_username }} — username автора"},
	{Value: "{{ .user_display_name }}", Label: "{{ .user_display_name }} — отображаемое имя автора"},
	{Value: "{{ .user_label }}", Label: "{{ .user_label }} — метка из скобок в имени автора"},
	{Value: "{{ .user_link }}", Label: "{{ .user_link }} — ссылка на автора"},
	{Value: "{{ .sender_tag }}", Label: "{{ .sender_tag }} — тег автора в чате"},
	{Value: "{{ .admins | get \"full_name\" | join \", \" }}", Label: "{{ .admins | get \"full_name\" | join \", \" }} — имена админов чата (без ботов)"},
	{Value: "{{ .user_portrait }}", Label: "{{ .user_portrait }} — портрет участника"},
	{Value: "{{ .user_portrait_remaining }}", Label: "{{ .user_portrait_remaining }} — сколько сообщений осталось до обновления портрета"},
	{Value: "{{ .bot_portrait }}", Label: "{{ .bot_portrait }} — портрет бота в этом чате"},
	{Value: "{{ .random_username }}", Label: "{{ .random_username }} — случайный @username из чата"},
	{Value: "{{ .gpt_4h_remaining }}", Label: "{{ .gpt_4h_remaining }} — остаток GPT-токенов автора в текущем 4-часовом окне"},
	{Value: "{{ .gpt_4h_limit }}", Label: "{{ .gpt_4h_limit }} — лимит GPT-токенов автора на 4 часа"},
	{Value: "{{ .gpt_4h_low_threshold }}", Label: "{{ .gpt_4h_low_threshold }} — порог усталости GPT-токенов автора"},
	{Value: "{{ chat_context 12 }}", Label: "{{ chat_context 12 }} — последние сообщения чата в бюджете ~150×N символов (без обрезки строк)"},
	{Value: "{{ weather \"Рязань\" }}", Label: "{{ weather \"Рязань\" }} — погода сейчас по городу (кэшируется)"},
	{Value: "{{ .message | web_search }}", Label: "{{ .message | web_search }} — веб-поиск по запросу (pipeline-стиль)"},
	{Value: "{{ .message | web_search \"компактно\" }}", Label: "{{ .message | web_search \"компактно\" }} — компактная поисковая выдача"},
	{Value: "{{ .message | regexp_replace \"pattern\" \"replacement\" }}", Label: "{{ .message | regexp_replace ... }} — regex-замена в строке (pipeline-стиль)"},
	{Value: "{{ rune_len .message }}", Label: "{{ rune_len .message }} — длина сообщения в символах"},
	{Value: "{{ time_of_day \"Europe/Moscow\" }}", Label: "{{ time_of_day \"Europe/Moscow\" }} — время суток по часовому поясу"},
	{Value: "{{ weekday \"Europe/Moscow\" }}", Label: "{{ weekday \"Europe/Moscow\" }} — день недели по часовому поясу"},
	{Value: "{{ .chat_id }}", Label: "{{ .chat_id }} — ID чата"},
	{Value: "{{ .chat_title }}", Label: "{{ .chat_title }} — название чата"},
	{Value: "{{ .reply_text }}", Label: "{{ .reply_text }} — текст сообщения, на которое ответили"},
	{Value: "{{ .reply_user_id }}", Label: "{{ .reply_user_id }} — ID адресата реплая"},
	{Value: "{{ .reply_first_name }}", Label: "{{ .reply_first_name }} — имя адресата реплая"},
	{Value: "{{ .reply_username }}", Label: "{{ .reply_username }} — username адресата реплая"},
	{Value: "{{ .reply_display_name }}", Label: "{{ .reply_display_name }} — отображаемое имя адресата реплая"},
	{Value: "{{ .reply_label }}", Label: "{{ .reply_label }} — метка из скобок в имени адресата"},
	{Value: "{{ .reply_user_link }}", Label: "{{ .reply_user_link }} — ссылка на адресата реплая"},
	{Value: "{{ .reply_sender_tag }}", Label: "{{ .reply_sender_tag }} — тег адресата в чате"},
	{Value: "{{ .reply_audio_title }}", Label: "{{ .reply_audio_title }} — название трека в реплае (если это аудио)"},
	{Value: "{{ .reply_audio_artist }}", Label: "{{ .reply_audio_artist }} — исполнитель трека в реплае"},
	{Value: "{{ .reply_audio_album }}", Label: "{{ .reply_audio_album }} — альбом трека в реплае"},
	{Value: "{{ .reply_audio_year }}", Label: "{{ .reply_audio_year }} — год трека в реплае"},
	{Value: "{{ .reply_audio_track }}", Label: "{{ .reply_audio_track }} — номер дорожки в реплае"},
	{Value: "{{ .reply_audio_text }}", Label: "{{ .reply_audio_text }} — текст песни из тега TEXT в реплае"},
}

// tagKind говорит, как устроен тег: "var" — переменная шаблона ({{ .name }}),
// "func" — вызов функции ({{ weekday "Europe/Moscow" }}, {{ .message | web_search }}).
// Пустая строка — тег разобрать не удалось (тогда его нельзя использовать).
func tagKind(value string) (kind, name string) {
	tmpl, err := texttemplate.New("tag").Funcs(scheduleActionFuncs()).Parse(value)
	if err != nil || tmpl.Tree == nil || tmpl.Tree.Root == nil {
		return "", ""
	}
	for _, node := range tmpl.Tree.Root.Nodes {
		action, ok := node.(*parse.ActionNode)
		if !ok || action.Pipe == nil {
			continue
		}
		for _, cmd := range action.Pipe.Cmds {
			if len(cmd.Args) == 0 {
				continue
			}
			switch first := cmd.Args[0].(type) {
			case *parse.FieldNode:
				if len(first.Ident) > 0 {
					return "var", first.Ident[0]
				}
			case *parse.VariableNode:
				if len(first.Ident) > 1 {
					return "var", first.Ident[1]
				}
			case *parse.IdentifierNode:
				return "func", first.Ident
			}
		}
	}
	return "", ""
}

// tagVariableNames собирает имена переменных из всех тегов реестра: ровно этот
// набор получает рендер задания расписания (со пустыми значениями), а
// random_username подставляется живым ником.
func tagVariableNames() []string {
	seen := make(map[string]bool, len(responseTagList))
	var names []string
	for _, tag := range responseTagList {
		if kind, name := tagKind(tag.Value); kind == "var" && !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}
	return names
}
