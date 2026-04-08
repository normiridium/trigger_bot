package main

import (
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
	mux.HandleFunc("/", func(rw http.ResponseWriter, r *http.Request) {
		http.Redirect(rw, r, "/trigger_bot", http.StatusFound)
	})
	mux.HandleFunc("/trigger_bot", w.withAuth(w.listPage))
	mux.HandleFunc("/trigger_bot/get", w.withAuth(w.getJSON))
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
	items, err := w.store.ListTriggers()
	if err != nil {
		http.Error(rw, err.Error(), http.StatusInternalServerError)
		return
	}
	rw.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := w.renderTemplate(rw, "trigger_list.html", map[string]interface{}{
		"Items": items,
	}); err != nil {
		http.Error(rw, err.Error(), http.StatusInternalServerError)
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
		item = &Trigger{Enabled: true, TriggerMode: "all", AdminMode: "anybody", MatchType: "full", ActionType: "send", Chance: 100, Reply: true}
	}
	rw.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(rw).Encode(item)
}

func (w *WebAdmin) savePost(rw http.ResponseWriter, r *http.Request) {
	started := time.Now()
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
	t := Trigger{
		ID:            id,
		UID:           strings.TrimSpace(r.FormValue("uid")),
		Title:         r.FormValue("title"),
		Enabled:       enabled,
		TriggerMode:   r.FormValue("trigger_mode"),
		AdminMode:     r.FormValue("admin_mode"),
		MatchText:     r.FormValue("match_text"),
		MatchType:     r.FormValue("match_type"),
		CaseSensitive: r.FormValue("case_sensitive") == "1",
		ActionType:    r.FormValue("action_type"),
		ResponseText:  r.FormValue("response_text"),
		Reply:         reply,
		Preview:       r.FormValue("preview") == "1",
		Chance:        chance,
	}
	if err := w.store.SaveTrigger(t); err != nil {
		http.Error(rw, err.Error(), http.StatusInternalServerError)
		return
	}
	log.Printf("web save trigger id=%d title=%q match_type=%s action=%s took=%s", t.ID, clipText(t.Title, 80), t.MatchType, t.ActionType, time.Since(started))
	redirectToListWithToken(rw, r)
}

func (w *WebAdmin) togglePost(rw http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(rw, err.Error(), http.StatusBadRequest)
		return
	}
	id, _ := strconv.ParseInt(strings.TrimSpace(r.FormValue("id")), 10, 64)
	if id > 0 {
		_ = w.store.ToggleTrigger(id)
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

func (w *WebAdmin) renderTemplate(rw http.ResponseWriter, name string, data interface{}) error {
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
			if normalizeMatchType(t.MatchType) != "regex" || t.RegexBenchUS <= 0 {
				return "—"
			}
			return fmt.Sprintf("%.2f ms", float64(t.RegexBenchUS)/1000.0)
		},
	}).ParseFiles(tplPath)
	if err != nil {
		return err
	}
	return tpl.Execute(rw, data)
}
