package app

import (
	"bytes"
	"context"
	crand "crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"trigger-admin-bot/internal/match"
	"trigger-admin-bot/internal/model"

	"golang.org/x/crypto/bcrypt"
)

type WebAdmin struct {
	store      *Store
	emojiProxy emojiProxyService
}

const adminSessionCookieName = "trigger_admin_session"
const adminCSRFCookieName = "trigger_admin_csrf"
const adminSessionTTL = 30 * 24 * time.Hour
const webStaticCacheTTL = 7 * 24 * time.Hour
const webProxyCacheTTL = 7 * 24 * time.Hour
const adminAuthStateSetupRequired = "setup_required"
const adminAuthStateLoginRequired = "login_required"
const adminAuthStateAuthenticated = "authenticated"

type settingField struct {
	Key         string   `json:"key"`
	Label       string   `json:"label"`
	Type        string   `json:"type"` // bool|string|int
	Description string   `json:"description,omitempty"`
	Options     []string `json:"options,omitempty"`
}

func NewWebAdmin(store *Store, adminToken string) *WebAdmin {
	_ = adminToken
	return &WebAdmin{
		store: store,
		emojiProxy: emojiProxyService{
			Token: strings.TrimSpace(envOr("TELEGRAM_BOT_TOKEN", "")),
		},
	}
}

func (w *WebAdmin) currentAdminAuth() (*AdminAuth, error) {
	if w == nil || w.store == nil {
		return nil, errors.New("store is not initialized")
	}
	auth, err := w.store.GetAdminAuth()
	if err != nil {
		return nil, err
	}
	if auth == nil {
		return nil, nil
	}
	if strings.TrimSpace(auth.PasswordHash) == "" {
		return nil, nil
	}
	return auth, nil
}

func (w *WebAdmin) authStateForRequest(r *http.Request) (string, error) {
	auth, err := w.currentAdminAuth()
	if err != nil {
		return "", err
	}
	if auth == nil {
		return adminAuthStateSetupRequired, nil
	}
	ok, err := w.sessionAuthorized(r)
	if err != nil {
		return "", err
	}
	if ok {
		return adminAuthStateAuthenticated, nil
	}
	return adminAuthStateLoginRequired, nil
}

func tokenHashHex(v string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(v)))
	return hex.EncodeToString(sum[:])
}

func newSessionToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := crand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func (w *WebAdmin) sessionAuthorized(r *http.Request) (bool, error) {
	if w == nil || w.store == nil {
		return false, errors.New("store is not initialized")
	}
	if r == nil {
		return false, nil
	}
	c, err := r.Cookie(adminSessionCookieName)
	if err != nil || strings.TrimSpace(c.Value) == "" {
		return false, nil
	}
	tokenHash := tokenHashHex(c.Value)
	now := time.Now().Unix()
	_ = w.store.DeleteExpiredAdminSessions(now)
	sess, err := w.store.GetAdminSession(tokenHash)
	if err != nil || sess == nil {
		return false, err
	}
	if sess.ExpiresAt <= now {
		_ = w.store.DeleteAdminSession(tokenHash)
		return false, nil
	}
	return true, nil
}

func setAdminSessionCookie(rw http.ResponseWriter, r *http.Request, token string) {
	if rw == nil {
		return
	}
	http.SetCookie(rw, &http.Cookie{
		Name:     adminSessionCookieName,
		Value:    strings.TrimSpace(token),
		Path:     "/trigger_bot",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   r != nil && r.TLS != nil,
		MaxAge:   int(adminSessionTTL / time.Second),
	})
}

func setCSRFCookie(rw http.ResponseWriter, r *http.Request, token string) {
	if rw == nil {
		return
	}
	http.SetCookie(rw, &http.Cookie{
		Name:     adminCSRFCookieName,
		Value:    strings.TrimSpace(token),
		Path:     "/trigger_bot",
		HttpOnly: false,
		SameSite: http.SameSiteLaxMode,
		Secure:   r != nil && r.TLS != nil,
		MaxAge:   int(adminSessionTTL / time.Second),
	})
}

func ensureCSRFCookie(rw http.ResponseWriter, r *http.Request) (string, error) {
	if r != nil {
		if c, err := r.Cookie(adminCSRFCookieName); err == nil {
			v := strings.TrimSpace(c.Value)
			if len(v) >= 32 {
				return v, nil
			}
		}
	}
	token, err := newSessionToken()
	if err != nil {
		return "", err
	}
	setCSRFCookie(rw, r, token)
	return token, nil
}

func clearAdminSessionCookie(rw http.ResponseWriter, r *http.Request) {
	if rw == nil {
		return
	}
	http.SetCookie(rw, &http.Cookie{
		Name:     adminSessionCookieName,
		Value:    "",
		Path:     "/trigger_bot",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   r != nil && r.TLS != nil,
		MaxAge:   -1,
	})
}

func clearCSRFCookie(rw http.ResponseWriter, r *http.Request) {
	if rw == nil {
		return
	}
	http.SetCookie(rw, &http.Cookie{
		Name:     adminCSRFCookieName,
		Value:    "",
		Path:     "/trigger_bot",
		HttpOnly: false,
		SameSite: http.SameSiteLaxMode,
		Secure:   r != nil && r.TLS != nil,
		MaxAge:   -1,
	})
}

func isStateChangingMethod(method string) bool {
	switch strings.ToUpper(strings.TrimSpace(method)) {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return false
	default:
		return true
	}
}

func csrfTokenValid(r *http.Request) bool {
	if r == nil {
		return false
	}
	c, err := r.Cookie(adminCSRFCookieName)
	if err != nil {
		return false
	}
	cookieVal := strings.TrimSpace(c.Value)
	headerVal := strings.TrimSpace(r.Header.Get("X-CSRF-Token"))
	if cookieVal == "" || headerVal == "" {
		return false
	}
	if len(cookieVal) != len(headerVal) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(cookieVal), []byte(headerVal)) == 1
}

func writeJSON(rw http.ResponseWriter, code int, payload interface{}) {
	if rw == nil {
		return
	}
	rw.Header().Set("Content-Type", "application/json; charset=utf-8")
	rw.WriteHeader(code)
	_ = json.NewEncoder(rw).Encode(payload)
}

func (w *WebAdmin) writeAuthRequired(rw http.ResponseWriter, r *http.Request) {
	state, err := w.authStateForRequest(r)
	if err != nil {
		http.Error(rw, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(rw, http.StatusUnauthorized, map[string]interface{}{
		"ok":         false,
		"auth_state": state,
		"error":      "unauthorized",
	})
}

func (w *WebAdmin) withAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(rw http.ResponseWriter, r *http.Request) {
		state, err := w.authStateForRequest(r)
		if err != nil {
			http.Error(rw, err.Error(), http.StatusInternalServerError)
			return
		}
		if state != adminAuthStateAuthenticated {
			w.writeAuthRequired(rw, r)
			return
		}
		if isStateChangingMethod(r.Method) && !csrfTokenValid(r) {
			writeJSON(rw, http.StatusForbidden, map[string]interface{}{
				"ok":         false,
				"auth_state": adminAuthStateAuthenticated,
				"error":      "csrf_invalid",
			})
			return
		}
		next(rw, r)
	}
}

func (w *WebAdmin) routes() http.Handler {
	mux := http.NewServeMux()
	staticDir := envOr("WEB_STATIC_DIR", "./static")
	mux.Handle("/trigger_bot/static/", http.StripPrefix("/trigger_bot/static/", cacheControlMiddleware(http.FileServer(http.Dir(staticDir)), webStaticCacheTTL)))
	mux.HandleFunc("/", func(rw http.ResponseWriter, r *http.Request) {
		http.Redirect(rw, r, "/trigger_bot", http.StatusFound)
	})
	mux.HandleFunc("/trigger_bot", w.listPage)
	mux.HandleFunc("/trigger_bot/auth_state", w.authStateJSON)
	mux.HandleFunc("/trigger_bot/auth_setup", w.authSetupPost)
	mux.HandleFunc("/trigger_bot/auth_login", w.authLoginPost)
	mux.HandleFunc("/trigger_bot/auth_logout", w.withAuth(w.authLogoutPost))
	mux.HandleFunc("/trigger_bot/change_password", w.withAuth(w.changePasswordPost))
	mux.HandleFunc("/trigger_bot/list", w.withAuth(w.listJSON))
	mux.HandleFunc("/trigger_bot/get", w.withAuth(w.getJSON))
	mux.HandleFunc("/trigger_bot/enums", w.withAuth(w.enumsJSON))
	mux.HandleFunc("/trigger_bot/template_tags", w.withAuth(w.templateTagsJSON))
	mux.HandleFunc("/trigger_bot/emoji_set", w.withAuth(w.emojiSetJSON))
	mux.HandleFunc("/trigger_bot/sticker_set", w.withAuth(w.stickerSetJSON))
	mux.HandleFunc("/trigger_bot/recent_sets_get", w.withAuth(w.recentSetsGetJSON))
	mux.HandleFunc("/trigger_bot/recent_sets_save", w.withAuth(w.recentSetsSavePost))
	mux.HandleFunc("/trigger_bot/emoji_proxy/file", w.withAuth(w.emojiFileProxy))
	mux.HandleFunc("/trigger_bot/emoji_proxy/preview", w.withAuth(w.emojiPreviewProxy))
	mux.HandleFunc("/trigger_bot/templates", w.withAuth(w.templatesJSON))
	mux.HandleFunc("/trigger_bot/template_get", w.withAuth(w.templateGetJSON))
	mux.HandleFunc("/trigger_bot/template_save", w.withAuth(w.templateSavePost))
	mux.HandleFunc("/trigger_bot/template_delete", w.withAuth(w.templateDeletePost))
	mux.HandleFunc("/trigger_bot/settings_get", w.withAuth(w.settingsGet))
	mux.HandleFunc("/trigger_bot/settings_save", w.withAuth(w.settingsSave))
	mux.HandleFunc("/trigger_bot/restart", w.withAuth(w.restartPost))
	mux.HandleFunc("/trigger_bot/save", w.withAuth(w.savePost))
	mux.HandleFunc("/trigger_bot/reorder", w.withAuth(w.reorderPost))
	mux.HandleFunc("/trigger_bot/toggle", w.withAuth(w.togglePost))
	mux.HandleFunc("/trigger_bot/delete", w.withAuth(w.deletePost))
	mux.HandleFunc("/trigger_bot/export", w.withAuth(w.exportGet))
	mux.HandleFunc("/trigger_bot/import", w.withAuth(w.importPost))
	// legacy route compatibility
	mux.HandleFunc("/trigger_bot/edit", func(rw http.ResponseWriter, r *http.Request) {
		http.Redirect(rw, r, "/trigger_bot", http.StatusFound)
	})
	return mux
}

func (w *WebAdmin) listPage(rw http.ResponseWriter, r *http.Request) {
	if state, err := w.authStateForRequest(r); err == nil && state == adminAuthStateAuthenticated {
		_, _ = ensureCSRFCookie(rw, r)
	}
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

func parseJSONBody(r *http.Request, out interface{}) error {
	if r == nil {
		return errors.New("request is nil")
	}
	if r.Method != http.MethodPost {
		return fmt.Errorf("method not allowed")
	}
	ctype := strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Type")))
	if !strings.HasPrefix(ctype, "application/json") {
		return fmt.Errorf("content-type must be application/json")
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	return dec.Decode(out)
}

func (w *WebAdmin) authStateJSON(rw http.ResponseWriter, r *http.Request) {
	state, err := w.authStateForRequest(r)
	if err != nil {
		http.Error(rw, err.Error(), http.StatusInternalServerError)
		return
	}
	resp := map[string]interface{}{
		"ok":            true,
		"auth_state":    state,
		"authenticated": state == adminAuthStateAuthenticated,
	}
	if state == adminAuthStateAuthenticated {
		token, err := ensureCSRFCookie(rw, r)
		if err != nil {
			http.Error(rw, err.Error(), http.StatusInternalServerError)
			return
		}
		resp["csrf_token"] = token
	}
	writeJSON(rw, http.StatusOK, resp)
}

func (w *WebAdmin) authSetupPost(rw http.ResponseWriter, r *http.Request) {
	var payload struct {
		Password string `json:"password"`
	}
	if err := parseJSONBody(r, &payload); err != nil {
		http.Error(rw, err.Error(), http.StatusBadRequest)
		return
	}
	state, err := w.authStateForRequest(r)
	if err != nil {
		http.Error(rw, err.Error(), http.StatusInternalServerError)
		return
	}
	if state != adminAuthStateSetupRequired {
		writeJSON(rw, http.StatusConflict, map[string]interface{}{
			"ok":         false,
			"auth_state": state,
			"error":      "password_already_set",
		})
		return
	}
	pass := strings.TrimSpace(payload.Password)
	if len([]rune(pass)) < 8 {
		http.Error(rw, "password must be at least 8 characters", http.StatusBadRequest)
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(pass), bcrypt.DefaultCost)
	if err != nil {
		http.Error(rw, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := w.store.SetAdminPasswordHash(string(hash)); err != nil {
		http.Error(rw, err.Error(), http.StatusInternalServerError)
		return
	}
	_ = w.store.DeleteAllAdminSessions()
	token, err := newSessionToken()
	if err != nil {
		http.Error(rw, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := w.store.CreateAdminSession(tokenHashHex(token), time.Now().Add(adminSessionTTL).Unix()); err != nil {
		http.Error(rw, err.Error(), http.StatusInternalServerError)
		return
	}
	setAdminSessionCookie(rw, r, token)
	csrfToken, err := ensureCSRFCookie(rw, r)
	if err != nil {
		http.Error(rw, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(rw, http.StatusOK, map[string]interface{}{
		"ok":            true,
		"auth_state":    adminAuthStateAuthenticated,
		"authenticated": true,
		"csrf_token":    csrfToken,
	})
}

func (w *WebAdmin) authLoginPost(rw http.ResponseWriter, r *http.Request) {
	var payload struct {
		Password string `json:"password"`
	}
	if err := parseJSONBody(r, &payload); err != nil {
		http.Error(rw, err.Error(), http.StatusBadRequest)
		return
	}
	auth, err := w.currentAdminAuth()
	if err != nil {
		http.Error(rw, err.Error(), http.StatusInternalServerError)
		return
	}
	if auth == nil {
		writeJSON(rw, http.StatusConflict, map[string]interface{}{
			"ok":         false,
			"auth_state": adminAuthStateSetupRequired,
			"error":      "setup_required",
		})
		return
	}
	pass := strings.TrimSpace(payload.Password)
	if pass == "" {
		http.Error(rw, "password is required", http.StatusBadRequest)
		return
	}
	if err := bcrypt.CompareHashAndPassword([]byte(auth.PasswordHash), []byte(pass)); err != nil {
		writeJSON(rw, http.StatusUnauthorized, map[string]interface{}{
			"ok":         false,
			"auth_state": adminAuthStateLoginRequired,
			"error":      "invalid_credentials",
		})
		return
	}
	token, err := newSessionToken()
	if err != nil {
		http.Error(rw, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := w.store.CreateAdminSession(tokenHashHex(token), time.Now().Add(adminSessionTTL).Unix()); err != nil {
		http.Error(rw, err.Error(), http.StatusInternalServerError)
		return
	}
	setAdminSessionCookie(rw, r, token)
	csrfToken, err := ensureCSRFCookie(rw, r)
	if err != nil {
		http.Error(rw, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(rw, http.StatusOK, map[string]interface{}{
		"ok":            true,
		"auth_state":    adminAuthStateAuthenticated,
		"authenticated": true,
		"csrf_token":    csrfToken,
	})
}

func (w *WebAdmin) authLogoutPost(rw http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(rw, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if c, err := r.Cookie(adminSessionCookieName); err == nil && strings.TrimSpace(c.Value) != "" {
		_ = w.store.DeleteAdminSession(tokenHashHex(c.Value))
	}
	clearAdminSessionCookie(rw, r)
	clearCSRFCookie(rw, r)
	writeJSON(rw, http.StatusOK, map[string]interface{}{
		"ok":            true,
		"auth_state":    adminAuthStateLoginRequired,
		"authenticated": false,
	})
}

func (w *WebAdmin) changePasswordPost(rw http.ResponseWriter, r *http.Request) {
	var payload struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if err := parseJSONBody(r, &payload); err != nil {
		http.Error(rw, err.Error(), http.StatusBadRequest)
		return
	}
	auth, err := w.currentAdminAuth()
	if err != nil {
		http.Error(rw, err.Error(), http.StatusInternalServerError)
		return
	}
	if auth == nil {
		writeJSON(rw, http.StatusConflict, map[string]interface{}{
			"ok":         false,
			"auth_state": adminAuthStateSetupRequired,
			"error":      "setup_required",
		})
		return
	}
	cur := strings.TrimSpace(payload.CurrentPassword)
	if cur == "" {
		http.Error(rw, "current_password is required", http.StatusBadRequest)
		return
	}
	if err := bcrypt.CompareHashAndPassword([]byte(auth.PasswordHash), []byte(cur)); err != nil {
		writeJSON(rw, http.StatusUnauthorized, map[string]interface{}{
			"ok":    false,
			"error": "invalid_current_password",
		})
		return
	}
	newPass := strings.TrimSpace(payload.NewPassword)
	if len([]rune(newPass)) < 8 {
		http.Error(rw, "new_password must be at least 8 characters", http.StatusBadRequest)
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(newPass), bcrypt.DefaultCost)
	if err != nil {
		http.Error(rw, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := w.store.SetAdminPasswordHash(string(hash)); err != nil {
		http.Error(rw, err.Error(), http.StatusInternalServerError)
		return
	}
	_ = w.store.DeleteAllAdminSessions()
	clearAdminSessionCookie(rw, r)
	clearCSRFCookie(rw, r)
	writeJSON(rw, http.StatusOK, map[string]interface{}{
		"ok":               true,
		"restart_required": false,
		"message":          "Пароль изменён. Войдите снова.",
		"auth_state":       adminAuthStateLoginRequired,
		"authenticated":    false,
	})
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
		{Value: "{{ .message }}", Label: "{{ .message }} — текст сообщения"},
		{Value: "{{ .user_text }}", Label: "{{ .user_text }} — текст/подпись сообщения"},
		{Value: "{{ .capturing_text }}", Label: "{{ .capturing_text }} — захват regex"},
		{Value: "{{ .capturing_choice }}", Label: "{{ .capturing_choice }} — вариант из regex"},
		{Value: "{{ .capturing_option }}", Label: "{{ .capturing_option }} — вариант из regex (алиас)"},
		{Value: "{{ .user_id }}", Label: "{{ .user_id }} — ID автора"},
		{Value: "{{ .user_first_name }}", Label: "{{ .user_first_name }} — имя автора"},
		{Value: "{{ .user_username }}", Label: "{{ .user_username }} — username автора"},
		{Value: "{{ .user_display_name }}", Label: "{{ .user_display_name }} — отображаемое имя автора"},
		{Value: "{{ .user_label }}", Label: "{{ .user_label }} — метка из скобок в имени автора"},
		{Value: "{{ .user_link }}", Label: "{{ .user_link }} — ссылка на автора"},
		{Value: "{{ .sender_tag }}", Label: "{{ .sender_tag }} — тег автора в чате"},
		{Value: "{{ .user_portrait }}", Label: "{{ .user_portrait }} — портрет участника"},
		{Value: "{{ .user_portrait_remaining }}", Label: "{{ .user_portrait_remaining }} — сколько сообщений осталось до обновления портрета"},
		{Value: "{{ chat_context 12 }}", Label: "{{ chat_context 12 }} — последние 12 сообщений чата (можно менять N)"},
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
	rw.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(rw).Encode(struct {
		Items []item `json:"items"`
	}{
		Items: items,
	})
}

func (w *WebAdmin) emojiSetJSON(rw http.ResponseWriter, r *http.Request) {
	emojiID := strings.TrimSpace(r.URL.Query().Get("emoji_id"))
	if emojiID == "" {
		http.Error(rw, "emoji_id required", http.StatusBadRequest)
		return
	}
	if !w.emojiProxy.Enabled() {
		http.Error(rw, "TELEGRAM_BOT_TOKEN is not configured", http.StatusFailedDependency)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	set, err := w.emojiProxy.ResolveSetByEmojiID(ctx, emojiID)
	if err != nil {
		http.Error(rw, err.Error(), http.StatusBadGateway)
		return
	}
	type item struct {
		CustomEmojiID string `json:"custom_emoji_id"`
		Emoji         string `json:"emoji"`
		SetName       string `json:"set_name"`
		PreviewURL    string `json:"preview_url"`
		ThumbURL      string `json:"thumb_url"`
	}
	outItems := make([]item, 0, len(set.Items))
	thumbURL := ""
	if strings.TrimSpace(set.ThumbFileID) != "" {
		thumbURL = "/trigger_bot/emoji_proxy/file?file_id=" + url.QueryEscape(strings.TrimSpace(set.ThumbFileID))
	}
	for _, it := range set.Items {
		previewURL := ""
		thumbURL := ""
		if strings.TrimSpace(it.FileID) != "" {
			previewURL = "/trigger_bot/emoji_proxy/preview?file_id=" + url.QueryEscape(strings.TrimSpace(it.FileID))
		}
		if strings.TrimSpace(it.ThumbFileID) != "" {
			thumbURL = "/trigger_bot/emoji_proxy/file?file_id=" + url.QueryEscape(strings.TrimSpace(it.ThumbFileID))
		}
		if previewURL == "" && strings.TrimSpace(it.FileID) != "" {
			previewURL = "/trigger_bot/emoji_proxy/file?file_id=" + url.QueryEscape(strings.TrimSpace(it.FileID))
		}
		if thumbURL == "" {
			thumbURL = previewURL
		}
		outItems = append(outItems, item{
			CustomEmojiID: strings.TrimSpace(it.CustomEmojiID),
			Emoji:         strings.TrimSpace(it.Emoji),
			SetName:       strings.TrimSpace(it.SetName),
			PreviewURL:    previewURL,
			ThumbURL:      thumbURL,
		})
	}
	rw.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(rw).Encode(struct {
		OK           bool   `json:"ok"`
		SetName      string `json:"set_name"`
		Title        string `json:"title"`
		ThumbnailURL string `json:"thumbnail_url"`
		Items        []item `json:"items"`
	}{
		OK:           true,
		SetName:      strings.TrimSpace(set.SetName),
		Title:        strings.TrimSpace(set.Title),
		ThumbnailURL: thumbURL,
		Items:        outItems,
	})
}

func (w *WebAdmin) stickerSetJSON(rw http.ResponseWriter, r *http.Request) {
	setName := strings.TrimSpace(r.URL.Query().Get("set_name"))
	if setName == "" {
		http.Error(rw, "set_name required", http.StatusBadRequest)
		return
	}
	if !w.emojiProxy.Enabled() {
		http.Error(rw, "TELEGRAM_BOT_TOKEN is not configured", http.StatusFailedDependency)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	set, err := w.emojiProxy.ResolveStickerSetByName(ctx, setName)
	if err != nil {
		http.Error(rw, err.Error(), http.StatusBadGateway)
		return
	}
	type item struct {
		SetName    string `json:"set_name"`
		Emoji      string `json:"emoji"`
		Code       string `json:"code"`
		PreviewURL string `json:"preview_url"`
		ThumbURL   string `json:"thumb_url"`
	}
	outItems := make([]item, 0, len(set.Items))
	thumbURL := ""
	if strings.TrimSpace(set.ThumbFileID) != "" {
		thumbURL = "/trigger_bot/emoji_proxy/file?file_id=" + url.QueryEscape(strings.TrimSpace(set.ThumbFileID))
	}
	for _, it := range set.Items {
		previewURL := "/trigger_bot/emoji_proxy/preview?file_id=" + url.QueryEscape(strings.TrimSpace(it.FileID))
		thumbURL := previewURL
		if strings.TrimSpace(it.ThumbFileID) != "" {
			thumbURL = "/trigger_bot/emoji_proxy/file?file_id=" + url.QueryEscape(strings.TrimSpace(it.ThumbFileID))
		}
		code := strings.TrimSpace(it.FileID) + ":" + strings.TrimSpace(set.SetName)
		outItems = append(outItems, item{
			SetName:    strings.TrimSpace(it.SetName),
			Emoji:      strings.TrimSpace(it.Emoji),
			Code:       code,
			PreviewURL: previewURL,
			ThumbURL:   thumbURL,
		})
	}
	rw.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(rw).Encode(struct {
		OK           bool   `json:"ok"`
		SetName      string `json:"set_name"`
		Title        string `json:"title"`
		ThumbnailURL string `json:"thumbnail_url"`
		Items        []item `json:"items"`
	}{
		OK:           true,
		SetName:      strings.TrimSpace(set.SetName),
		Title:        strings.TrimSpace(set.Title),
		ThumbnailURL: thumbURL,
		Items:        outItems,
	})
}

func (w *WebAdmin) recentSetsGetJSON(rw http.ResponseWriter, r *http.Request) {
	if w == nil || w.store == nil {
		http.Error(rw, "store is not initialized", http.StatusInternalServerError)
		return
	}
	v, err := w.store.GetUIPickerRecentSets()
	if err != nil {
		http.Error(rw, err.Error(), http.StatusInternalServerError)
		return
	}
	if v == nil {
		v = &UIPickerRecentSets{}
	}
	if v.EmojiSets == nil {
		v.EmojiSets = []UIPickerRecentEmojiSet{}
	}
	if v.StickerSets == nil {
		v.StickerSets = []UIPickerRecentStickerSet{}
	}
	writeJSON(rw, http.StatusOK, map[string]interface{}{
		"ok":           true,
		"emoji_sets":   v.EmojiSets,
		"sticker_sets": v.StickerSets,
	})
}

func sanitizeUIPickerRecentSets(v UIPickerRecentSets) UIPickerRecentSets {
	const maxEmojiSets = 300
	const maxStickerSets = 300
	out := UIPickerRecentSets{
		EmojiSets:   make([]UIPickerRecentEmojiSet, 0, min(len(v.EmojiSets), maxEmojiSets)),
		StickerSets: make([]UIPickerRecentStickerSet, 0, min(len(v.StickerSets), maxStickerSets)),
	}
	seenEmoji := make(map[string]struct{}, maxEmojiSets)
	seenSticker := make(map[string]struct{}, maxStickerSets)
	for _, it := range v.EmojiSets {
		id := strings.TrimSpace(it.EmojiID)
		setName := strings.TrimSpace(it.SetName)
		if setName == "" {
			continue
		}
		key := setName
		if _, ok := seenEmoji[key]; ok {
			continue
		}
		seenEmoji[key] = struct{}{}
		out.EmojiSets = append(out.EmojiSets, UIPickerRecentEmojiSet{
			EmojiID:   id,
			SetName:   clipText(setName, 128),
			Title:     clipText(strings.TrimSpace(it.Title), 256),
			Emoji:     clipText(strings.TrimSpace(it.Emoji), 16),
			Preview:   clipText(strings.TrimSpace(it.Preview), 1024),
			Thumb:     clipText(strings.TrimSpace(it.Thumb), 1024),
			UpdatedAt: it.UpdatedAt,
		})
		if len(out.EmojiSets) >= maxEmojiSets {
			break
		}
	}
	for _, it := range v.StickerSets {
		setName := strings.TrimSpace(it.SetName)
		if setName == "" {
			continue
		}
		if _, ok := seenSticker[setName]; ok {
			continue
		}
		seenSticker[setName] = struct{}{}
		out.StickerSets = append(out.StickerSets, UIPickerRecentStickerSet{
			SetName:   clipText(setName, 128),
			Title:     clipText(strings.TrimSpace(it.Title), 256),
			Preview:   clipText(strings.TrimSpace(it.Preview), 1024),
			Thumb:     clipText(strings.TrimSpace(it.Thumb), 1024),
			UpdatedAt: it.UpdatedAt,
		})
		if len(out.StickerSets) >= maxStickerSets {
			break
		}
	}
	return out
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func (w *WebAdmin) recentSetsSavePost(rw http.ResponseWriter, r *http.Request) {
	if r == nil || !strings.EqualFold(r.Method, http.MethodPost) {
		writeJSON(rw, http.StatusMethodNotAllowed, map[string]interface{}{"ok": false, "error": "method_not_allowed"})
		return
	}
	if w == nil || w.store == nil {
		http.Error(rw, "store is not initialized", http.StatusInternalServerError)
		return
	}
	var in UIPickerRecentSets
	if err := parseJSONBody(r, &in); err != nil {
		http.Error(rw, err.Error(), http.StatusBadRequest)
		return
	}
	in = sanitizeUIPickerRecentSets(in)
	if err := w.store.SaveUIPickerRecentSets(in); err != nil {
		http.Error(rw, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(rw, http.StatusOK, map[string]interface{}{"ok": true})
}

func (w *WebAdmin) emojiFileProxy(rw http.ResponseWriter, r *http.Request) {
	fileID := strings.TrimSpace(r.URL.Query().Get("file_id"))
	if fileID == "" {
		http.Error(rw, "file_id required", http.StatusBadRequest)
		return
	}
	if !w.emojiProxy.Enabled() {
		http.Error(rw, "TELEGRAM_BOT_TOKEN is not configured", http.StatusFailedDependency)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	body, ctype, err := w.emojiProxy.FetchFile(ctx, fileID)
	if err != nil {
		http.Error(rw, err.Error(), http.StatusBadGateway)
		return
	}
	ctype = detectContentTypeOrDefault(body, ctype, "application/octet-stream")
	rw.Header().Set("Content-Type", ctype)
	rw.Header().Set("Cache-Control", cacheControlValue(webProxyCacheTTL))
	rw.WriteHeader(http.StatusOK)
	_, _ = rw.Write(body)
}

func (w *WebAdmin) emojiPreviewProxy(rw http.ResponseWriter, r *http.Request) {
	fileID := strings.TrimSpace(r.URL.Query().Get("file_id"))
	if fileID == "" {
		http.Error(rw, "file_id required", http.StatusBadRequest)
		return
	}
	if !w.emojiProxy.Enabled() {
		http.Error(rw, "TELEGRAM_BOT_TOKEN is not configured", http.StatusFailedDependency)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	body, ctype, err := w.emojiProxy.FetchPreviewImage(ctx, fileID)
	if err != nil {
		http.Error(rw, err.Error(), http.StatusBadGateway)
		return
	}
	ctype = detectContentTypeOrDefault(body, ctype, "image/webp")
	rw.Header().Set("Content-Type", ctype)
	rw.Header().Set("Cache-Control", cacheControlValue(webProxyCacheTTL))
	rw.WriteHeader(http.StatusOK)
	_, _ = rw.Write(body)
}

func cacheControlValue(ttl time.Duration) string {
	secs := int(ttl / time.Second)
	if secs < 0 {
		secs = 0
	}
	return fmt.Sprintf("public, max-age=%d, immutable", secs)
}

func cacheControlMiddleware(next http.Handler, ttl time.Duration) http.Handler {
	if next == nil {
		return http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) {
			http.Error(rw, "handler is not configured", http.StatusInternalServerError)
		})
	}
	value := cacheControlValue(ttl)
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("Cache-Control", value)
		next.ServeHTTP(rw, r)
	})
}

func detectContentTypeOrDefault(body []byte, current, fallback string) string {
	ctype := strings.TrimSpace(current)
	if ctype != "" && ctype != "application/octet-stream" {
		return ctype
	}
	if len(body) > 0 {
		if sniffed := strings.TrimSpace(http.DetectContentType(body)); sniffed != "" && sniffed != "application/octet-stream" {
			return sniffed
		}
	}
	if strings.TrimSpace(ctype) != "" {
		return ctype
	}
	if strings.TrimSpace(fallback) == "" {
		return "application/octet-stream"
	}
	return fallback
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
	if err := parseJSONBody(r, &payload); err != nil {
		http.Error(rw, err.Error(), http.StatusBadRequest)
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
	if err := parseJSONBody(r, &payload); err != nil {
		http.Error(rw, err.Error(), http.StatusBadRequest)
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
	if strings.TrimSpace(tpl.Key) != "" {
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
				OK       bool   `json:"ok"`
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

func (w *WebAdmin) settingsGet(rw http.ResponseWriter, r *http.Request) {
	fields := settingsSchema()
	values := loadEnvSettings(fields)
	rw.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(rw).Encode(struct {
		Fields []settingField    `json:"fields"`
		Values map[string]string `json:"values"`
	}{
		Fields: fields,
		Values: values,
	})
}

func (w *WebAdmin) settingsSave(rw http.ResponseWriter, r *http.Request) {
	var payload map[string]string
	if err := parseJSONBody(r, &payload); err != nil {
		http.Error(rw, err.Error(), http.StatusBadRequest)
		return
	}
	fields := settingsSchema()
	allowed := make(map[string]settingField, len(fields))
	for _, f := range fields {
		allowed[f.Key] = f
	}
	updates := make(map[string]string, len(payload))
	for k, v := range payload {
		f, ok := allowed[k]
		if !ok {
			continue
		}
		val := strings.TrimSpace(v)
		val = strings.ReplaceAll(val, "\r", " ")
		val = strings.ReplaceAll(val, "\n", " ")
		if f.Type == "bool" {
			val = normalizeBoolString(val)
		}
		updates[k] = val
	}
	if err := writeEnvFile("./.env", updates); err != nil {
		http.Error(rw, err.Error(), http.StatusInternalServerError)
		return
	}
	rw.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(rw).Encode(struct {
		OK              bool   `json:"ok"`
		RestartRequired bool   `json:"restart_required"`
		Message         string `json:"message"`
	}{
		OK:              true,
		RestartRequired: true,
		Message:         "Настройки сохранены. Требуется перезапуск сервиса.",
	})
}

func (w *WebAdmin) restartPost(rw http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(rw, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// Delay restart so we can ответить до остановки процесса.
	cmd := exec.Command("sh", "-c", "sleep 1; sudo /usr/bin/systemctl restart trigger-admin-bot.service")
	if err := cmd.Start(); err != nil {
		http.Error(rw, fmt.Sprintf("restart failed: %v", err), http.StatusInternalServerError)
		return
	}
	rw.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(rw).Encode(struct {
		OK bool `json:"ok"`
	}{OK: true})
}

func settingsSchema() []settingField {
	return []settingField{
		{Key: "ALLOWED_CHAT_IDS", Label: "Разрешённые чаты (через запятую)", Type: "string", Description: ""},
		{Key: "ADMIN_CACHE_TTL_SEC", Label: "TTL кэша админов (сек)", Type: "int", Description: "120"},
		{Key: "USER_INDEX_MAX", Label: "Лимит пользователей в индексе", Type: "int", Description: "800"},
		{Key: "CHAT_RECENT_MAX_MESSAGES", Label: "Сообщений для контекста", Type: "int", Description: "8"},
		{Key: "CHAT_RECENT_MAX_AGE_SEC", Label: "TTL контекста (сек)", Type: "int", Description: "1800"},
		{Key: "OLENYAM_CONTEXT_MESSAGES", Label: "Контекст для GPT (сообщений)", Type: "int", Description: "4"},
		{Key: "USER_DAILY_BOT_MESSAGES_LIMIT", Label: "Лимит GPT-ответов на пользователя за 4 часа (0 = выкл)", Type: "int", Description: "12"},
		{Key: "DEBUG_TRIGGER_LOG", Label: "Лог триггеров (debug)", Type: "bool", Description: "false"},
		{Key: "DEBUG_GPT_LOG", Label: "Лог GPT (debug)", Type: "bool", Description: "false"},
		{Key: "LOG_TEXT_CLIP_CHARS", Label: "Обрезка текста в логах (0 = без обрезки)", Type: "int", Description: "200"},
		{Key: "CHAT_ERROR_LOG", Label: "Отправка ошибок в чат", Type: "bool", Description: "true"},
		{Key: "GPT_PROMPT_DEBOUNCE_SEC", Label: "Debounce GPT (сек)", Type: "int", Description: "10"},
		{Key: "GPT_REPLY_REACTION_CHANCE_PERCENT", Label: "GPT: шанс реакции на reply (%)", Type: "int", Description: "25"},
		{Key: "GPT_MARKDOWN_DIVIDER_EMOJI", Label: "GPT: emoji для разделителя '---'", Type: "string", Description: `<tg-emoji emoji-id="5213083123218147891">〰️</tg-emoji>`},
		{Key: "SERPAPI_ENGINE", Label: "SerpAPI engine", Type: "string", Description: "google_images"},
		{Key: "OPENAI_MODEL", Label: "OpenAI model (chat)", Type: "string", Description: "gpt-5-mini"},
		{Key: "SPOTIFY_AUDIO_INTERACTIVE", Label: "Spotify: интерактивный выбор", Type: "bool", Description: "true"},
		{Key: "SPOTIFY_AUDIO_WORKERS", Label: "Spotify: воркеры скачивания", Type: "int", Description: "1"},
		{Key: "SPOTIFY_AUDIO_QUEUE", Label: "Spotify: размер очереди", Type: "int", Description: "8"},
		{Key: "AUDIO_FORMAT", Label: "Формат аудио", Type: "string", Description: "mp3", Options: []string{"mp3", "m4a", "flac", "opus", "wav"}},
		{Key: "AUDIO_QUALITY", Label: "Качество аудио", Type: "string", Description: "320K", Options: []string{"320K", "256K", "192K", "160K", "128K", "96K", "0"}},
		{Key: "MEDIA_DOWNLOAD_MAX_MB", Label: "Ссылки: лимит файла (МБ)", Type: "int", Description: "50"},
		{Key: "TELEGRAM_UPLOAD_MAX_MB", Label: "Telegram: лимит отправки файла (МБ)", Type: "int", Description: "50"},
		{Key: "MEDIA_DOWNLOAD_MAX_HEIGHT", Label: "Ссылки: максимум качества (высота)", Type: "int", Description: "720", Options: []string{"360", "480", "720"}},
		{Key: "MEDIA_DOWNLOAD_INTERACTIVE", Label: "Ссылки: выбор аудио/видео", Type: "bool", Description: "true"},
		{Key: "MEDIA_DOWNLOAD_WORKERS", Label: "Ссылки: воркеры скачивания", Type: "int", Description: "1"},
		{Key: "MEDIA_DOWNLOAD_QUEUE", Label: "Ссылки: размер очереди", Type: "int", Description: "8"},
	}
}

func loadEnvSettings(fields []settingField) map[string]string {
	out := make(map[string]string, len(fields))
	fileVals := readEnvFile("./.env")
	for _, f := range fields {
		if v, ok := fileVals[f.Key]; ok {
			out[f.Key] = v
			continue
		}
		if v := strings.TrimSpace(os.Getenv(f.Key)); v != "" {
			out[f.Key] = v
			continue
		}
		if f.Description != "" {
			out[f.Key] = f.Description
		}
	}
	return out
}

func readEnvFile(path string) map[string]string {
	body, err := os.ReadFile(path)
	if err != nil {
		return map[string]string{}
	}
	lines := strings.Split(string(body), "\n")
	out := make(map[string]string, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if idx := strings.Index(line, "="); idx > 0 {
			key := strings.TrimSpace(line[:idx])
			val := strings.TrimSpace(line[idx+1:])
			out[key] = strings.Trim(val, `"'`)
		}
	}
	return out
}

func writeEnvFile(path string, updates map[string]string) error {
	body, _ := os.ReadFile(path)
	lines := strings.Split(string(body), "\n")
	seen := make(map[string]bool, len(updates))
	out := make([]string, 0, len(lines)+len(updates))
	for _, line := range lines {
		if strings.TrimSpace(line) == "" || strings.HasPrefix(strings.TrimSpace(line), "#") || !strings.Contains(line, "=") {
			out = append(out, line)
			continue
		}
		idx := strings.Index(line, "=")
		key := strings.TrimSpace(line[:idx])
		if val, ok := updates[key]; ok {
			out = append(out, key+"="+formatEnvValue(val))
			seen[key] = true
		} else {
			out = append(out, line)
		}
	}
	for key, val := range updates {
		if seen[key] {
			continue
		}
		out = append(out, key+"="+formatEnvValue(val))
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(strings.Join(out, "\n")), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func formatEnvValue(v string) string {
	if v == "" {
		return ""
	}
	v = strings.ReplaceAll(v, "\r", " ")
	v = strings.ReplaceAll(v, "\n", " ")
	return strconv.Quote(v)
}

func normalizeBoolString(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	if v == "1" || v == "true" || v == "yes" || v == "on" {
		return "true"
	}
	return "false"
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
	case model.TriggerModeOnlyRepliesToSelfNoMedia:
		return "bi-chat-text"
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
	case model.ActionTypeSendFile:
		return "bi-file-earmark-arrow-up"
	case model.ActionTypeSendGIF:
		return "bi-film"
	case model.ActionTypeSendSticker:
		return "bi-file-earmark-image"
	case model.ActionTypeDelete:
		return "bi-trash"
	case model.ActionTypeDeletePortrait:
		return "bi-person-dash"
	case model.ActionTypeGPTPrompt:
		return "bi-cpu"
	case model.ActionTypeGPTImage:
		return "bi-image"
	case model.ActionTypeSearchImage:
		return "bi-search"
	case model.ActionTypeSpotifyMusic:
		return "bi-music-note-beamed"
	case model.ActionTypeMusic:
		return "bi-music-player"
	case model.ActionTypeYandexMusic:
		return "bi-disc"
	case model.ActionTypeMediaAudio:
		return "bi-cloud-download"
	case model.ActionTypeMediaTikTok:
		return "bi-tiktok"
	case model.ActionTypeMediaX:
		return "bi-twitter-x"
	case model.ActionTypeUserLimitLow:
		return "bi-battery-half"
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
		PassThrough   bool               `json:"pass_through"`
		Chance        int                `json:"chance"`
	}
	if err := parseJSONBody(r, &payload); err != nil {
		http.Error(rw, err.Error(), http.StatusBadRequest)
		return
	}
	actionType, ok := parseActionType(payload.ActionType)
	if !ok {
		http.Error(rw, "unknown action_type", http.StatusBadRequest)
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
		ActionType:    actionType,
		ResponseText:  payload.ResponseText,
		Reply:         payload.Reply,
		Preview:       payload.Preview,
		DeleteSource:  payload.DeleteSource,
		PassThrough:   payload.PassThrough,
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
	if err := parseJSONBody(r, &payload); err != nil {
		http.Error(rw, err.Error(), http.StatusBadRequest)
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
	var payload struct {
		IDs []int64 `json:"ids"`
	}
	if err := parseJSONBody(r, &payload); err != nil {
		http.Error(rw, err.Error(), http.StatusBadRequest)
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
	if err := parseJSONBody(r, &payload); err != nil {
		http.Error(rw, err.Error(), http.StatusBadRequest)
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
	var payload struct {
		Raw string `json:"raw"`
	}
	if err := parseJSONBody(r, &payload); err != nil {
		http.Error(rw, err.Error(), http.StatusBadRequest)
		return
	}
	raw := strings.TrimSpace(payload.Raw)
	if raw == "" {
		http.Error(rw, "raw is required", http.StatusBadRequest)
		return
	}
	added, err := w.store.ImportJSON([]byte(raw))
	if err != nil {
		http.Error(rw, err.Error(), http.StatusBadRequest)
		return
	}
	log.Printf("web import added=%d took=%s", added, time.Since(started))
	rw.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(rw).Encode(struct {
		OK    bool `json:"ok"`
		Added int  `json:"added"`
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
