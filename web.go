package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"trigger-admin-bot/internal/match"
	"trigger-admin-bot/internal/model"
)

type WebAdmin struct {
	store      *Store
	adminToken string
}

func NewWebAdmin(store *Store, adminToken string) *WebAdmin {
	return &WebAdmin{store: store, adminToken: strings.TrimSpace(adminToken)}
}

func (w *WebAdmin) authOK(r *http.Request) bool {
	if w.adminToken == "" {
		return true
	}
	if strings.TrimSpace(r.URL.Query().Get("token")) == w.adminToken {
		return true
	}
	if strings.TrimSpace(r.Header.Get("X-Admin-Token")) == w.adminToken {
		return true
	}
	return false
}

func (w *WebAdmin) withAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(rw http.ResponseWriter, r *http.Request) {
		if !w.authOK(r) {
			rw.WriteHeader(http.StatusUnauthorized)
			_, _ = rw.Write([]byte("доступ запрещен"))
			return
		}
		next(rw, r)
	}
}

func (w *WebAdmin) routes() http.Handler {
	mux := http.NewServeMux()
	staticDir := envOr("WEB_STATIC_DIR", "./static")
	mux.Handle("/trigger_bot/static/", http.StripPrefix("/trigger_bot/static/", http.FileServer(http.Dir(staticDir))))
	mux.HandleFunc("/", func(rw http.ResponseWriter, r *http.Request) {
		http.Redirect(rw, r, "/trigger_bot", http.StatusFound)
	})
	mux.HandleFunc("/trigger_bot", w.withAuth(w.listPage))
	mux.HandleFunc("/trigger_bot/list", w.withAuth(w.listJSON))
	mux.HandleFunc("/trigger_bot/get", w.withAuth(w.getJSON))
	mux.HandleFunc("/trigger_bot/enums", w.withAuth(w.enumsJSON))
	mux.HandleFunc("/trigger_bot/template_tags", w.withAuth(w.templateTagsJSON))
	mux.HandleFunc("/trigger_bot/templates", w.withAuth(w.templatesJSON))
	mux.HandleFunc("/trigger_bot/template_get", w.withAuth(w.templateGetJSON))
	mux.HandleFunc("/trigger_bot/template_save", w.withAuth(w.templateSavePost))
	mux.HandleFunc("/trigger_bot/template_delete", w.withAuth(w.templateDeletePost))
	mux.HandleFunc("/trigger_bot/save", w.withAuth(w.savePost))
	mux.HandleFunc("/trigger_bot/reorder", w.withAuth(w.reorderPost))
	mux.HandleFunc("/trigger_bot/toggle", w.withAuth(w.togglePost))
	mux.HandleFunc("/trigger_bot/delete", w.withAuth(w.deletePost))
	mux.HandleFunc("/trigger_bot/export", w.withAuth(w.exportGet))
	mux.HandleFunc("/trigger_bot/import", w.withAuth(w.importPost))
	// legacy route compatibility
	mux.HandleFunc("/trigger_bot/edit", w.withAuth(func(rw http.ResponseWriter, r *http.Request) {
		http.Redirect(rw, r, "/trigger_bot", http.StatusFound)
	}))
	return mux
}

func (w *WebAdmin) listPage(rw http.ResponseWriter, r *http.Request) {
	body, err := w.renderTemplate("trigger_list.html", map[string]interface{}{})
	if err != nil {
		http.Error(rw, err.Error(), http.StatusInternalServerError)
		return
	}
	rw.Header().Set("Content-Type", "text/html; charset=utf-8")
	rw.Header().Set("Content-Length", strconv.Itoa(len(body)))
	rw.WriteHeader(http.StatusOK)
	_, _ = rw.Write(body)
}

func (w *WebAdmin) listJSON(rw http.ResponseWriter, r *http.Request) {
	items, err := w.store.ListTriggers()
	if err != nil {
		http.Error(rw, err.Error(), http.StatusInternalServerError)
		return
	}
	rw.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(rw).Encode(struct {
		Items []Trigger `json:"items"`
	}{
		Items: items,
	})
}

func (w *WebAdmin) enumsJSON(rw http.ResponseWriter, r *http.Request) {
	type enumItem struct {
		Value string `json:"value"`
		Label string `json:"label"`
		Icon  string `json:"icon"`
	}
	out := struct {
		TriggerModes []enumItem `json:"trigger_modes"`
		AdminModes   []enumItem `json:"admin_modes"`
		MatchTypes   []enumItem `json:"match_types"`
		ActionTypes  []enumItem `json:"action_types"`
	}{
		TriggerModes: make([]enumItem, 0, len(model.TriggerModeValues)),
		AdminModes:   make([]enumItem, 0, len(model.AdminModeValues)),
		MatchTypes:   make([]enumItem, 0, len(model.MatchTypeValues)),
		ActionTypes:  make([]enumItem, 0, len(model.ActionTypeValues)),
	}
	for _, v := range model.TriggerModeValues {
		out.TriggerModes = append(out.TriggerModes, enumItem{Value: string(v), Label: v.String(), Icon: iconForTriggerMode(v)})
	}
	for _, v := range model.AdminModeValues {
		out.AdminModes = append(out.AdminModes, enumItem{Value: string(v), Label: v.String(), Icon: iconForAdminMode(v)})
	}
	for _, v := range model.MatchTypeValues {
		out.MatchTypes = append(out.MatchTypes, enumItem{Value: string(v), Label: v.String(), Icon: iconForMatchType(v)})
	}
	for _, v := range model.ActionTypeValues {
		out.ActionTypes = append(out.ActionTypes, enumItem{Value: string(v), Label: v.String(), Icon: iconForActionType(v)})
	}
	rw.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(rw).Encode(out)
}

func (w *WebAdmin) templateTagsJSON(rw http.ResponseWriter, r *http.Request) {
	type item struct {
		Value string `json:"value"`
		Label string `json:"label"`
	}
	items := []item{
		{Value: "{{message}}", Label: "{{message}} — текст сообщения"},
		{Value: "{{user_text}}", Label: "{{user_text}} — текст/подпись сообщения"},
		{Value: "{{capturing_text}}", Label: "{{capturing_text}} — захват regex"},
		{Value: "{{capturing_choice}}", Label: "{{capturing_choice}} — вариант из regex"},
		{Value: "{{capturing_option}}", Label: "{{capturing_option}} — вариант из regex (алиас)"},
		{Value: "{{user_id}}", Label: "{{user_id}} — ID автора"},
		{Value: "{{user_first_name}}", Label: "{{user_first_name}} — имя автора"},
		{Value: "{{user_username}}", Label: "{{user_username}} — username автора"},
		{Value: "{{user_display_name}}", Label: "{{user_display_name}} — отображаемое имя автора"},
		{Value: "{{user_label}}", Label: "{{user_label}} — метка из скобок в имени автора"},
		{Value: "{{user_link}}", Label: "{{user_link}} — ссылка на автора"},
		{Value: "{{sender_tag}}", Label: "{{sender_tag}} — тег автора в чате"},
		{Value: "{{chat_id}}", Label: "{{chat_id}} — ID чата"},
		{Value: "{{chat_title}}", Label: "{{chat_title}} — название чата"},
		{Value: "{{reply_text}}", Label: "{{reply_text}} — текст сообщения, на которое ответили"},
		{Value: "{{reply_user_id}}", Label: "{{reply_user_id}} — ID адресата реплая"},
		{Value: "{{reply_first_name}}", Label: "{{reply_first_name}} — имя адресата реплая"},
		{Value: "{{reply_username}}", Label: "{{reply_username}} — username адресата реплая"},
		{Value: "{{reply_display_name}}", Label: "{{reply_display_name}} — отображаемое имя адресата реплая"},
		{Value: "{{reply_label}}", Label: "{{reply_label}} — метка из скобок в имени адресата"},
		{Value: "{{reply_user_link}}", Label: "{{reply_user_link}} — ссылка на адресата реплая"},
		{Value: "{{reply_sender_tag}}", Label: "{{reply_sender_tag}} — тег адресата в чате"},
	}
	rw.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(rw).Encode(struct {
		Items []item `json:"items"`
	}{
		Items: items,
	})
}

func (w *WebAdmin) templatesJSON(rw http.ResponseWriter, r *http.Request) {
	items, err := w.store.ListTemplates()
	if err != nil {
		http.Error(rw, err.Error(), http.StatusInternalServerError)
		return
	}
	rw.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(rw).Encode(struct {
		Items []ResponseTemplate `json:"items"`
	}{
		Items: items,
	})
}

func (w *WebAdmin) templateGetJSON(rw http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("id")), 10, 64)
	var item *ResponseTemplate
	if id > 0 {
		var err error
		item, err = w.store.GetTemplate(id)
		if err != nil {
			http.Error(rw, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	if item == nil {
		item = &ResponseTemplate{}
	}
	rw.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(rw).Encode(item)
}

func (w *WebAdmin) templateSavePost(rw http.ResponseWriter, r *http.Request) {
	var payload struct {
		ID    int64  `json:"id"`
		Key   string `json:"key"`
		Title string `json:"title"`
		Text  string `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(rw, "bad json", http.StatusBadRequest)
		return
	}
	id := payload.ID
	key := strings.TrimSpace(payload.Key)
	title := strings.TrimSpace(payload.Title)
	text := strings.TrimSpace(payload.Text)
	if key == "" && id > 0 {
		if existing, err := w.store.GetTemplate(id); err == nil && existing != nil {
			key = strings.TrimSpace(existing.Key)
		}
	}
	t := ResponseTemplate{
		ID:    id,
		Key:   key,
		Title: title,
		Text:  text,
	}
	if err := w.store.SaveTemplate(t); err != nil {
		http.Error(rw, err.Error(), http.StatusInternalServerError)
		return
	}
	rw.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(rw).Encode(struct {
		OK bool `json:"ok"`
	}{OK: true})
}

func (w *WebAdmin) templateDeletePost(rw http.ResponseWriter, r *http.Request) {
	var payload struct {
		ID  int64  `json:"id"`
		Key string `json:"key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(rw, "bad json", http.StatusBadRequest)
		return
	}
	id := payload.ID
	key := strings.TrimSpace(payload.Key)
	var tpl *ResponseTemplate
	var err error
	if id > 0 {
		tpl, err = w.store.GetTemplate(id)
	} else if key != "" {
		tpl, err = w.store.getTemplateByKey(key)
		if tpl != nil {
			id = tpl.ID
		}
	}
	if err != nil {
		http.Error(rw, err.Error(), http.StatusInternalServerError)
		return
	}
	if id <= 0 {
		http.Error(rw, "id required", http.StatusBadRequest)
		return
	}
	if tpl == nil {
		http.Error(rw, "template not found", http.StatusNotFound)
		return
	}
	if tpl != nil && strings.TrimSpace(tpl.Key) != "" {
		usedBy, err := w.store.findTemplateUsageByKey(tpl.Key)
		if err != nil {
			http.Error(rw, err.Error(), http.StatusInternalServerError)
			return
		}
		if len(usedBy) > 0 {
			type ref struct {
				ID    int64  `json:"id"`
				Title string `json:"title"`
			}
			refs := make([]ref, 0, len(usedBy))
			for _, tr := range usedBy {
				refs = append(refs, ref{ID: tr.ID, Title: tr.Title})
			}
			rw.Header().Set("Content-Type", "application/json; charset=utf-8")
			rw.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(rw).Encode(struct {
				OK       bool  `json:"ok"`
				Message  string `json:"message"`
				Triggers []ref  `json:"triggers"`
			}{
				OK:       false,
				Message:  "Шаблон используется в триггерах",
				Triggers: refs,
			})
			return
		}
	}
	if err := w.store.DeleteTemplate(id); err != nil {
		http.Error(rw, err.Error(), http.StatusInternalServerError)
		return
	}
	rw.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(rw).Encode(struct {
		OK bool `json:"ok"`
	}{OK: true})
}

func iconForTriggerMode(v model.TriggerMode) string {
	switch v {
	case model.TriggerModeAll:
		return "bi-chat-dots"
	case model.TriggerModeOnlyReplies:
		return "bi-reply"
	case model.TriggerModeOnlyRepliesToBot:
		return "bi-robot"
	case model.TriggerModeOnlyRepliesToSelf:
		return "bi-reply-fill"
	case model.TriggerModeNeverOnReplies:
		return "bi-chat"
	case model.TriggerModeCommandReply:
		return "bi-terminal"
	default:
		return ""
	}
}

func iconForAdminMode(v model.AdminMode) string {
	switch v {
	case model.AdminModeAnybody:
		return "bi-people"
	case model.AdminModeAdmins:
		return "bi-shield-lock"
	case model.AdminModeNotAdmin:
		return "bi-person"
	default:
		return ""
	}
}

func iconForMatchType(v model.MatchType) string {
	switch v {
	case model.MatchTypeIdle:
		return "bi-clock"
	case model.MatchTypeFull:
		return "bi-check2-square"
	case model.MatchTypePartial:
		return "bi-intersect"
	case model.MatchTypeRegex:
		return "bi-braces"
	case model.MatchTypeStarts:
		return "bi-arrow-right"
	case model.MatchTypeEnds:
		return "bi-arrow-left"
	case model.MatchTypeNewMember:
		return "bi-person-plus"
	default:
		return ""
	}
}

func iconForActionType(v model.ActionType) string {
	switch v {
	case model.ActionTypeSend:
		return "bi-send"
	case model.ActionTypeDelete:
		return "bi-trash"
	case model.ActionTypeGPTPrompt:
		return "bi-cpu"
	case model.ActionTypeGPTImage:
		return "bi-image"
	case model.ActionTypeSearchImage:
		return "bi-search"
	case model.ActionTypeVKMusic:
		return "bi-music-note-beamed"
	default:
		return ""
	}
}

func (w *WebAdmin) getJSON(rw http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("id")), 10, 64)
	var item *Trigger
	if id > 0 {
		var err error
		item, err = w.store.GetTrigger(id)
		if err != nil {
			http.Error(rw, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	if item == nil {
		item = &Trigger{
			Enabled:     true,
			TriggerMode: TriggerModeAll,
			AdminMode:   AdminModeAnybody,
			MatchType:   MatchTypeFull,
			ActionType:  ActionTypeSend,
			Chance:      100,
			Reply:       true,
		}
	}
	rw.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(rw).Encode(item)
}

func (w *WebAdmin) savePost(rw http.ResponseWriter, r *http.Request) {
	started := time.Now()
	var t Trigger
	var payload struct {
		ID            int64              `json:"id"`
		UID           string             `json:"uid"`
		Title         string             `json:"title"`
		Enabled       bool               `json:"enabled"`
		TriggerMode   string             `json:"trigger_mode"`
		AdminMode     string             `json:"admin_mode"`
		MatchText     string             `json:"match_text"`
		MatchType     string             `json:"match_type"`
		CaseSensitive bool               `json:"case_sensitive"`
		ActionType    string             `json:"action_type"`
		ResponseText  []ResponseTextItem `json:"response_text"`
		Reply         bool               `json:"reply"`
		Preview       bool               `json:"preview"`
		DeleteSource  bool               `json:"delete_source"`
		Chance        int                `json:"chance"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(rw, "bad json", http.StatusBadRequest)
		return
	}
	t = Trigger{
		ID:            payload.ID,
		UID:           strings.TrimSpace(payload.UID),
		Title:         payload.Title,
		Enabled:       payload.Enabled,
		TriggerMode:   normalizeTriggerMode(payload.TriggerMode),
		AdminMode:     normalizeAdminMode(payload.AdminMode),
		MatchText:     payload.MatchText,
		MatchType:     match.NormalizeMatchType(payload.MatchType),
		CaseSensitive: payload.CaseSensitive,
		ActionType:    normalizeActionType(payload.ActionType),
		ResponseText:  payload.ResponseText,
		Reply:         payload.Reply,
		Preview:       payload.Preview,
		DeleteSource:  payload.DeleteSource,
		Chance:        payload.Chance,
	}
	if err := w.store.SaveTrigger(t); err != nil {
		http.Error(rw, err.Error(), http.StatusInternalServerError)
		return
	}
	variantCount := len(t.ResponseText)
	primaryLen := 0
	if variantCount > 0 {
		primaryLen = len(t.ResponseText[0].Text)
	}
	log.Printf(
		"web save trigger id=%d title=%q match_type=%s action=%s variants=%d first_len=%d took=%s",
		t.ID,
		clipText(t.Title, 80),
		t.MatchType,
		t.ActionType,
		variantCount,
		primaryLen,
		time.Since(started),
	)
	rw.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(rw).Encode(struct {
		OK bool `json:"ok"`
	}{OK: true})
}

func (w *WebAdmin) togglePost(rw http.ResponseWriter, r *http.Request) {
	var payload struct {
		ID int64 `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(rw, "bad json", http.StatusBadRequest)
		return
	}
	id := payload.ID
	if id <= 0 {
		http.Error(rw, "id required", http.StatusBadRequest)
		return
	}
	enabled, err := w.store.ToggleTrigger(id)
	if err != nil {
		http.Error(rw, err.Error(), http.StatusInternalServerError)
		return
	}
	rw.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = rw.Write([]byte(fmt.Sprintf(`{"ok":true,"id":%d,"enabled":%v}`, id, enabled)))
}

func (w *WebAdmin) reorderPost(rw http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(rw, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var payload struct {
		IDs []int64 `json:"ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(rw, "bad json", http.StatusBadRequest)
		return
	}
	if err := w.store.ReorderTriggersByIDs(payload.IDs); err != nil {
		http.Error(rw, err.Error(), http.StatusInternalServerError)
		return
	}
	rw.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = rw.Write([]byte(`{"ok":true}`))
}

func (w *WebAdmin) deletePost(rw http.ResponseWriter, r *http.Request) {
	var payload struct {
		ID int64 `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(rw, "bad json", http.StatusBadRequest)
		return
	}
	if payload.ID <= 0 {
		http.Error(rw, "id required", http.StatusBadRequest)
		return
	}
	if err := w.store.DeleteTrigger(payload.ID); err != nil {
		http.Error(rw, err.Error(), http.StatusInternalServerError)
		return
	}
	rw.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(rw).Encode(struct {
		OK bool `json:"ok"`
	}{OK: true})
}

func redirectToListWithToken(rw http.ResponseWriter, r *http.Request) {
	path := "/trigger_bot"
	token := strings.TrimSpace(r.URL.Query().Get("token"))
	if token == "" {
		token = strings.TrimSpace(r.FormValue("token"))
	}
	if token != "" {
		path = path + "?token=" + url.QueryEscape(token)
	}
	http.Redirect(rw, r, path, http.StatusFound)
}

func (w *WebAdmin) exportGet(rw http.ResponseWriter, r *http.Request) {
	body, err := w.store.ExportJSON()
	if err != nil {
		http.Error(rw, err.Error(), http.StatusInternalServerError)
		return
	}
	rw.Header().Set("Content-Type", "application/json; charset=utf-8")
	rw.Header().Set("Content-Disposition", `attachment; filename="trigger_bot_export.json"`)
	_, _ = rw.Write(body)
}

func (w *WebAdmin) importPost(rw http.ResponseWriter, r *http.Request) {
	started := time.Now()
	var raw string
	var payload struct {
		Raw string `json:"raw"`
	}
	body, _ := io.ReadAll(r.Body)
	if err := json.Unmarshal(body, &payload); err == nil && strings.TrimSpace(payload.Raw) != "" {
		raw = payload.Raw
	} else {
		raw = string(body)
	}
	added, err := w.store.ImportJSON([]byte(raw))
	if err != nil {
		http.Error(rw, err.Error(), http.StatusBadRequest)
		return
	}
	log.Printf("web import added=%d took=%s", added, time.Since(started))
	rw.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(rw).Encode(struct {
		OK    bool  `json:"ok"`
		Added int   `json:"added"`
	}{OK: true, Added: added})
}

func (w *WebAdmin) renderTemplate(name string, data interface{}) ([]byte, error) {
	tplPath := filepath.Join(envOr("WEB_TEMPLATE_DIR", "./templates"), name)
	tpl, err := template.New(name).Funcs(template.FuncMap{
		"statusClass": func(enabled bool) string {
			if enabled {
				return "text-bg-success"
			}
			return "text-bg-secondary"
		},
		"statusText": func(enabled bool) string {
			if enabled {
				return "ВКЛ"
			}
			return "ВЫКЛ"
		},
		"statusTitle": func(enabled bool) string {
			if enabled {
				return "Включен"
			}
			return "Выключен"
		},
		"statusIcon": func(enabled bool) string {
			if enabled {
				return "bi-eye-fill"
			}
			return "bi-eye-slash-fill"
		},
		"regexBenchText": func(t Trigger) string {
			if match.NormalizeMatchType(string(t.MatchType)) != "regex" || t.RegexBenchUS <= 0 {
				return "—"
			}
			return fmt.Sprintf("%.2f ms", float64(t.RegexBenchUS)/1000.0)
		},
	}).ParseFiles(tplPath)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := tpl.Execute(&buf, data); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
