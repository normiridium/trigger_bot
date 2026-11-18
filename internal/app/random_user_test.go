package app

import (
	"html/template"
	"net/http/httptest"
	"strings"
	"testing"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// Тег {{ .random_username }}: подстановка @ника из индекса чата.
// Разбор делает text/template — проверяем поведение движка шаблонов, не строки.

func TestRenderRandomUsernameTag(t *testing.T) {
	defer setChatRandomUsernameResolver(nil)
	setChatRandomUsernameResolver(func(chatID int64) string {
		if chatID != -1001 {
			return ""
		}
		return "sew0m"
	})

	cases := []struct {
		name     string
		text     string
		want     string
		replaced bool
	}{
		{"обычный тег", "напомни {{ .random_username }}, что пора пить воду", "напомни @sew0m, что пора пить воду", true},
		{"без пробелов", "привет {{.random_username}}!", "привет @sew0m!", true},
		{"несколько тегов", "{{ .random_username }} и снова {{ .random_username }}", "@sew0m и снова @sew0m", true},
		{"без тегов", "просто текст", "просто текст", false},
		{"текст с фигурными скобками", "json {\"a\":1} и всё", "json {\"a\":1} и всё", false},
		{"незакрытый тег", "напомни {{ .random_username", "напомни {{ .random_username", false},
		{"html не экранируется", "{{ .random_username }} <b>жирный</b>", "@sew0m <b>жирный</b>", true},
	}
	for _, tc := range cases {
		got, replaced := renderRandomUsernameTag(tc.text, -1001)
		if got != tc.want || replaced != tc.replaced {
			t.Fatalf("%s: получили %q replaced=%v, ждали %q replaced=%v", tc.name, got, replaced, tc.want, tc.replaced)
		}
	}
}

// В задании расписания работают не только переменные, но и теги-функции:
// {{ weekday "Europe/Moscow" }} должен превратиться в день недели, а не уйти
// модели как текст.
func TestRenderScheduleActionFunctions(t *testing.T) {
	defer setChatRandomUsernameResolver(nil)
	setChatRandomUsernameResolver(func(chatID int64) string { return "sew0m" })

	got, replaced := renderRandomUsernameTag("Сегодня {{ weekday \"Europe/Moscow\" }}, привет {{ .random_username }}", -1001)
	if !replaced {
		t.Fatal("задание с тегами не раскрылось")
	}
	if strings.Contains(got, "{{") || strings.Contains(got, "}}") {
		t.Fatalf("в результате остались теги: %q", got)
	}
	if !strings.Contains(got, "@sew0m") {
		t.Fatalf("переменная не подставилась: %q", got)
	}
	days := []string{"понедельник", "вторник", "среда", "четверг", "пятница", "суббота", "воскресенье"}
	found := false
	for _, d := range days {
		if strings.Contains(got, d) {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("день недели не подставился: %q", got)
	}

	// Неизвестная функция — текст не трогаем, расписание не падает.
	if raw, changed := renderRandomUsernameTag("{{ no_such_func 1 }}", -1001); changed || raw != "{{ no_such_func 1 }}" {
		t.Fatalf("неизвестная функция: %q replaced=%v", raw, changed)
	}
}

// Пустой индекс: тег раскрывается в пустую строку, replaced=true — раннер
// пишет в лог предупреждение.
func TestRenderRandomUsernameTagEmptyIndex(t *testing.T) {
	defer setChatRandomUsernameResolver(nil)
	setChatRandomUsernameResolver(func(chatID int64) string { return "" })

	got, replaced := renderRandomUsernameTag("напомни {{ .random_username }}", -2002)
	if !replaced {
		t.Fatal("тег должен считаться раскрытым")
	}
	if strings.TrimSpace(got) != "напомни" {
		t.Fatalf("получили %q", got)
	}
}

func TestRandomChatUsernameCleansAt(t *testing.T) {
	defer setChatRandomUsernameResolver(nil)
	setChatRandomUsernameResolver(func(chatID int64) string { return "  @nick  " })
	if got := randomChatUsername(-1); got != "@nick" {
		t.Fatalf("получили %q", got)
	}
	setChatRandomUsernameResolver(func(chatID int64) string { return "   " })
	if got := randomChatUsername(-1); got != "" {
		t.Fatalf("пустой ник дал %q", got)
	}
	setChatRandomUsernameResolver(nil)
	if got := randomChatUsername(-1); got != "" {
		t.Fatalf("без резолвера получили %q", got)
	}
}

func TestChatUserIndexRandomUsername(t *testing.T) {
	index := newChatUserIndex(10)
	if got := index.RandomUsername(-1001); got != "" {
		t.Fatalf("пустой индекс дал %q", got)
	}
	index.remember(-1001, &tgbotapi.User{ID: 1, UserName: "alice"})
	index.remember(-1001, &tgbotapi.User{ID: 2, UserName: "bob"})
	seen := map[string]bool{}
	for i := 0; i < 40; i++ {
		name := index.RandomUsername(-1001)
		if name == "" {
			t.Fatal("пустое имя при заполненном индексе")
		}
		seen[name] = true
	}
	if !seen["alice"] || !seen["bob"] {
		t.Fatalf("случайный выбор не покрыл оба ника: %v", seen)
	}
	if other := index.RandomUsername(-2002); other != "" {
		t.Fatalf("чужой чат дал %q", other)
	}
}

// В шаблонах ответов тег — обычная переменная Go-шаблона: {{ .random_username }}.
func TestRandomUsernameTagInTemplates(t *testing.T) {
	defer setChatRandomUsernameResolver(nil)
	setChatRandomUsernameResolver(func(chatID int64) string {
		if chatID == -1001 {
			return "sew0m"
		}
		return ""
	})
	msg := &tgbotapi.Message{
		Chat: &tgbotapi.Chat{ID: -1001, Type: "supergroup", Title: "Чат"},
		From: &tgbotapi.User{ID: 7, FirstName: "Аня", UserName: "anya"},
		Text: "привет",
	}
	ctx := templateContext{Msg: msg}
	if got := renderTemplateWithMessage(ctx, "привет {{ .random_username }}"); !strings.Contains(got, "@sew0m") {
		t.Fatalf("переменная-тег не подставилась: %q", got)
	}
}

// Реестр тегов — единый источник: каждый тег разбирается, каждая функция
// существует в общем наборе функций шаблонов, значения уникальны.
func TestTagRegistryIntegrity(t *testing.T) {
	funcs := mergedResponseTemplateFuncs(nil)
	builtins := map[string]bool{"index": true, "len": true, "printf": true, "print": true, "println": true, "slice": true, "and": true, "or": true, "not": true, "eq": true, "ne": true, "lt": true, "le": true, "gt": true, "ge": true, "call": true, "html": true, "js": true, "urlquery": true}
	seen := map[string]bool{}
	for _, tag := range responseTagList {
		if tag.Value == "" || tag.Label == "" {
			t.Fatalf("пустой тег в реестре: %+v", tag)
		}
		if seen[tag.Value] {
			t.Fatalf("дубль тега в реестре: %q", tag.Value)
		}
		seen[tag.Value] = true
		kind, name := tagKind(tag.Value)
		if kind == "" || name == "" {
			t.Fatalf("тег не разбирается движком шаблонов: %q", tag.Value)
		}
		if kind == "func" && !builtins[name] {
			if _, ok := funcs[name]; !ok {
				t.Fatalf("тег %q зовёт функцию %q, которой нет в наборе шаблонов", tag.Value, name)
			}
		}
	}
	if !seen["{{ .random_username }}"] {
		t.Fatal("в реестре нет тега случайного участника")
	}
}

// Данные для задания расписания собираются из того же реестра.
func TestScheduleActionValuesFromRegistry(t *testing.T) {
	defer setChatRandomUsernameResolver(nil)
	setChatRandomUsernameResolver(func(chatID int64) string {
		if chatID == -1001 {
			return "sew0m"
		}
		return ""
	})
	data := scheduleActionValues(-1001)
	for _, name := range tagVariableNames() {
		if _, ok := data[name]; !ok {
			t.Fatalf("в данных расписания нет переменной %q из реестра", name)
		}
	}
	if data["random_username"] != "@sew0m" {
		t.Fatalf("random_username = %v", data["random_username"])
	}
	if data["message"] != "" {
		t.Fatalf("переменная без сообщения должна быть пустой, получили %v", data["message"])
	}
}

// Регрессия: список тегов в админке должен содержать новый тег в каноническом
// виде, иначе его нельзя вставить из выпадашки ни в шаблон, ни в расписание.
func TestTemplateTagsContainRandomUsername(t *testing.T) {
	w := &WebAdmin{}
	rec := httptest.NewRecorder()
	w.templateTagsJSON(rec, httptest.NewRequest("GET", "/trigger_bot/template_tags", nil))
	body := rec.Body.String()
	if !strings.Contains(body, "{{ .random_username }}") {
		t.Fatalf("в списке тегов нет {{ .random_username }}: %s", body)
	}
}

// Регрессия: литеральный тег Go-шаблона в HTML админки ломает страницу
// (function "random_username" not defined). Разбор шаблона должен проходить.
func TestAdminTemplateParses(t *testing.T) {
	if _, err := template.ParseFiles("../../templates/trigger_list.html"); err != nil {
		t.Fatalf("админский шаблон не парсится: %v", err)
	}
}
