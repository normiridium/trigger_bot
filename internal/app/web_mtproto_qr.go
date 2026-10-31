package app

// Admin panel handlers for QR login of the per-chat MTProto session.
//
// The code-based challenge cannot complete: Telegram refuses to deliver login
// codes for the bound account (sentCodeTypeApp + 406 SEND_CODE_UNAVAILABLE on
// resend). QR login sidesteps codes entirely — tg-ops-service holds the session
// and serves a refreshed QR image, the admin panel just polls it.

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"trigger-admin-bot/internal/chatclear"
)

func qrStateJSON(state chatclear.QRState) map[string]interface{} {
	return map[string]interface{}{
		"ok":          true,
		"chat_id":     state.ChatID,
		"status":      state.Status,
		"png_b64":     state.PNGBase64,
		"expires_at":  state.ExpiresAt,
		"error":       state.Error,
		"access_hash": state.AccessHash,
	}
}

func (w *WebAdmin) mtprotoQRStart(rw http.ResponseWriter, r *http.Request) {
	var payload struct {
		ChatID int64 `json:"chat_id"`
	}
	if err := parseJSONBody(r, &payload); err != nil {
		http.Error(rw, err.Error(), http.StatusBadRequest)
		return
	}
	if w == nil || w.clearOps == nil {
		http.Error(rw, "chat clear service is not configured", http.StatusNotImplemented)
		return
	}
	if payload.ChatID == 0 {
		writeJSON(rw, http.StatusBadRequest, map[string]interface{}{"ok": false, "error": "выберите чат"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	state, err := w.clearOps.StartQR(ctx, payload.ChatID)
	if err != nil {
		writeJSON(rw, http.StatusBadGateway, map[string]interface{}{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(rw, http.StatusOK, qrStateJSON(state))
}

func (w *WebAdmin) mtprotoQRState(rw http.ResponseWriter, r *http.Request) {
	chatID, _ := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("chat_id")), 10, 64)
	if chatID == 0 {
		http.Error(rw, "chat_id is required", http.StatusBadRequest)
		return
	}
	if w == nil || w.clearOps == nil {
		http.Error(rw, "chat clear service is not configured", http.StatusNotImplemented)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	state, err := w.clearOps.QRState(ctx, chatID)
	if err != nil {
		writeJSON(rw, http.StatusBadGateway, map[string]interface{}{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(rw, http.StatusOK, qrStateJSON(state))
}

func (w *WebAdmin) mtprotoQRPassword(rw http.ResponseWriter, r *http.Request) {
	var payload struct {
		ChatID   int64  `json:"chat_id"`
		Password string `json:"password"`
	}
	if err := parseJSONBody(r, &payload); err != nil {
		http.Error(rw, err.Error(), http.StatusBadRequest)
		return
	}
	if w == nil || w.clearOps == nil {
		http.Error(rw, "chat clear service is not configured", http.StatusNotImplemented)
		return
	}
	if payload.ChatID == 0 || strings.TrimSpace(payload.Password) == "" {
		writeJSON(rw, http.StatusBadRequest, map[string]interface{}{"ok": false, "error": "нужны chat_id и пароль"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	state, err := w.clearOps.SubmitQRPassword(ctx, payload.ChatID, payload.Password)
	if err != nil {
		writeJSON(rw, http.StatusBadGateway, map[string]interface{}{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(rw, http.StatusOK, qrStateJSON(state))
}

func (w *WebAdmin) mtprotoQRStop(rw http.ResponseWriter, r *http.Request) {
	var payload struct {
		ChatID int64 `json:"chat_id"`
	}
	if err := parseJSONBody(r, &payload); err != nil {
		http.Error(rw, err.Error(), http.StatusBadRequest)
		return
	}
	if w == nil || w.clearOps == nil {
		http.Error(rw, "chat clear service is not configured", http.StatusNotImplemented)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	if err := w.clearOps.StopQR(ctx, payload.ChatID); err != nil {
		writeJSON(rw, http.StatusBadGateway, map[string]interface{}{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(rw, http.StatusOK, map[string]interface{}{"ok": true, "status": "stopped"})
}
