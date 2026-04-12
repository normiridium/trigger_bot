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
	isJSON := strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json")
	var t Trigger
	if isJSON {
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
			http.Error(rw, err.Error(), http.StatusBadRequest)
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
	} else {
		// Save is submitted from JS as multipart/form-data (FormData),
		// and sometimes as regular form-urlencoded. Support both robustly.
		if err := r.ParseMultipartForm(10 << 20); err != nil {
			if err := r.ParseForm(); err != nil {
				http.Error(rw, err.Error(), http.StatusBadRequest)
				return
			}
		}
		id, _ := strconv.ParseInt(strings.TrimSpace(r.FormValue("id")), 10, 64)
		chance, _ := strconv.Atoi(strings.TrimSpace(r.FormValue("chance")))
		enabledRaw := strings.TrimSpace(r.FormValue("enabled"))
		enabled := enabledRaw == "1"
		if id <= 0 && enabledRaw == "" {
			enabled = true
		}
		replyRaw := strings.TrimSpace(r.FormValue("reply"))
		reply := replyRaw == "1"
		if id <= 0 && replyRaw == "" {
			reply = true
		}
		deleteSourceRaw := strings.TrimSpace(r.FormValue("delete_source"))
		deleteSource := deleteSourceRaw == "1"
		var responseItems []ResponseTextItem
		responseRaw := strings.TrimSpace(r.FormValue("response_text"))
		if responseRaw != "" {
			if strings.HasPrefix(responseRaw, "[") {
				_ = json.Unmarshal([]byte(responseRaw), &responseItems)
			}
			if len(responseItems) == 0 {
				responseItems = []ResponseTextItem{{Text: responseRaw}}
			}
		}
		t = Trigger{
			ID:            id,
			UID:           strings.TrimSpace(r.FormValue("uid")),
			Title:         r.FormValue("title"),
			Enabled:       enabled,
			TriggerMode:   normalizeTriggerMode(r.FormValue("trigger_mode")),
			AdminMode:     normalizeAdminMode(r.FormValue("admin_mode")),
			MatchText:     r.FormValue("match_text"),
			MatchType:     match.NormalizeMatchType(r.FormValue("match_type")),
			CaseSensitive: r.FormValue("case_sensitive") == "1",
			ActionType:    normalizeActionType(r.FormValue("action_type")),
			ResponseText:  responseItems,
			Reply:         reply,
			Preview:       r.FormValue("preview") == "1",
			DeleteSource:  deleteSource,
			Chance:        chance,
		}
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
	if isJSON {
		rw.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(rw).Encode(struct {
			OK bool `json:"ok"`
		}{OK: true})
		return
	}
	redirectToListWithToken(rw, r)
}

func (w *WebAdmin) togglePost(rw http.ResponseWriter, r *http.Request) {
	idStr := ""
	if strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
		var payload struct {
			ID int64 `json:"id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(rw, err.Error(), http.StatusBadRequest)
			return
		}
		if payload.ID > 0 {
			idStr = strconv.FormatInt(payload.ID, 10)
		}
	}
	if idStr == "" {
		_ = r.ParseForm()
		idStr = strings.TrimSpace(r.FormValue("id"))
	}
	if idStr == "" {
		idStr = strings.TrimSpace(r.URL.Query().Get("id"))
	}
	id, _ := strconv.ParseInt(idStr, 10, 64)
	if id <= 0 {
		http.Error(rw, "id required", http.StatusBadRequest)
		return
	}
	enabled, err := w.store.ToggleTrigger(id)
	if err != nil {
		http.Error(rw, err.Error(), http.StatusInternalServerError)
		return
	}
	accept := r.Header.Get("Accept")
	if strings.Contains(accept, "application/json") || r.Header.Get("X-Requested-With") == "XMLHttpRequest" {
		rw.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = rw.Write([]byte(fmt.Sprintf(`{"ok":true,"id":%d,"enabled":%v}`, id, enabled)))
		return
	}
	redirectToListWithToken(rw, r)
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
	if err := r.ParseForm(); err != nil {
		http.Error(rw, err.Error(), http.StatusBadRequest)
		return
	}
	id, _ := strconv.ParseInt(strings.TrimSpace(r.FormValue("id")), 10, 64)
	if id > 0 {
		_ = w.store.DeleteTrigger(id)
	}
	redirectToListWithToken(rw, r)
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
	rw.Header().Set("Content-Disposition", `attachment; filename="triggers.json"`)
	_, _ = rw.Write(body)
}

func (w *WebAdmin) importPost(rw http.ResponseWriter, r *http.Request) {
	started := time.Now()
	if err := r.ParseMultipartForm(10 << 20); err != nil {
		_ = r.ParseForm()
	}
	var raw string
	if file, _, err := r.FormFile("file"); err == nil {
		defer file.Close()
		body, _ := io.ReadAll(file)
		raw = string(body)
	}
	if strings.TrimSpace(raw) == "" {
		raw = r.FormValue("raw")
	}
	added, err := w.store.ImportJSON([]byte(raw))
	if err != nil {
		http.Error(rw, err.Error(), http.StatusBadRequest)
		return
	}
	log.Printf("web import triggers added=%d took=%s", added, time.Since(started))
	redirectToListWithToken(rw, r)
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
