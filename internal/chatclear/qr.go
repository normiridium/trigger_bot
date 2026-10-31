package chatclear

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// QRState is a snapshot of the service-side QR login attempt.
type QRState struct {
	ChatID     int64  `json:"chat_id"`
	Status     string `json:"status"`
	PNGBase64  string `json:"png_b64,omitempty"`
	ExpiresAt  string `json:"expires_at,omitempty"`
	Error      string `json:"error,omitempty"`
	AccessHash int64  `json:"access_hash,omitempty"`
	UpdatedAt  string `json:"updated_at,omitempty"`
}

// QR login statuses mirror mtproto.QRStatus* in tg-ops-service.
const (
	QRStatusWaitingScan  = "waiting_scan"
	QRStatusNeedPassword = "need_password"
	QRStatusAuthorized   = "authorized"
	QRStatusError        = "error"
	QRStatusStopped      = "stopped"
)

type qrChatPayload struct {
	ChatID int64 `json:"chat_id"`
}

type qrPasswordPayload struct {
	ChatID   int64  `json:"chat_id"`
	Password string `json:"password"`
}

func (noopService) StartQR(context.Context, int64) (QRState, error) {
	return QRState{}, ErrNotConfigured
}

func (noopService) QRState(context.Context, int64) (QRState, error) {
	return QRState{}, ErrNotConfigured
}

func (noopService) SubmitQRPassword(context.Context, int64, string) (QRState, error) {
	return QRState{}, ErrNotConfigured
}

func (noopService) StopQR(context.Context, int64) error { return ErrNotConfigured }

// StartQR begins a QR login for the chat and returns the first token image.
func (s *HTTPService) StartQR(ctx context.Context, chatID int64) (QRState, error) {
	return s.callQR(ctx, "auth_user_qr_start", qrChatPayload{ChatID: chatID}, 45*time.Second)
}

// QRState polls the current state of the QR login.
func (s *HTTPService) QRState(ctx context.Context, chatID int64) (QRState, error) {
	return s.callQR(ctx, "auth_user_qr_state", qrChatPayload{ChatID: chatID}, 20*time.Second)
}

// SubmitQRPassword finishes a QR login that stopped at the cloud password.
func (s *HTTPService) SubmitQRPassword(ctx context.Context, chatID int64, password string) (QRState, error) {
	return s.callQR(ctx, "auth_user_qr_password", qrPasswordPayload{
		ChatID:   chatID,
		Password: strings.TrimSpace(password),
	}, 90*time.Second)
}

// StopQR aborts a running QR login attempt.
func (s *HTTPService) StopQR(ctx context.Context, chatID int64) error {
	if s == nil || strings.TrimSpace(s.baseURL) == "" || s.client == nil {
		return ErrNotConfigured
	}
	body, err := json.Marshal(commandEnvelope{
		RequestID: fmt.Sprintf("qr-stop-%d", time.Now().UnixNano()),
		Command:   "auth_user_qr_stop",
		Payload:   mustJSON(qrChatPayload{ChatID: chatID}),
	})
	if err != nil {
		return fmt.Errorf("marshal auth_user_qr_stop: %w", err)
	}
	if _, err := s.postForJSON(ctx, "/v1/command", body); err != nil {
		return err
	}
	return nil
}

func (s *HTTPService) callQR(ctx context.Context, command string, payload any, timeout time.Duration) (QRState, error) {
	if s == nil || strings.TrimSpace(s.baseURL) == "" || s.client == nil {
		return QRState{}, ErrNotConfigured
	}
	body, err := json.Marshal(commandEnvelope{
		RequestID: fmt.Sprintf("%s-%d", command, time.Now().UnixNano()),
		Command:   command,
		Payload:   mustJSON(payload),
	})
	if err != nil {
		return QRState{}, fmt.Errorf("marshal %s: %w", command, err)
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	data, err := s.postForJSON(callCtx, "/v1/command", body)
	if err != nil {
		return QRState{}, err
	}
	var out QRState
	if err := json.Unmarshal(data, &out); err != nil {
		return QRState{}, fmt.Errorf("decode %s: %w", command, err)
	}
	return out, nil
}

func mustJSON(v any) json.RawMessage {
	raw, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage("{}")
	}
	return raw
}
