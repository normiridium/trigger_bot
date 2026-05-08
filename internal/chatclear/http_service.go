package chatclear

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

var (
	ErrNotConfigured = errors.New("chat clear service is not configured")
	ErrBadRequest    = errors.New("chat clear request is invalid")
)

type HTTPService struct {
	baseURL   string
	authToken string
	client    *http.Client
}

type clearChatHTTPReq struct {
	ChatID   int64  `json:"chat_id"`
	Username string `json:"username,omitempty"`
}

type commandEnvelope struct {
	RequestID string      `json:"request_id,omitempty"`
	Command   string      `json:"command"`
	Payload   interface{} `json:"payload"`
}

type clearChatHTTPResp struct {
	OK    bool   `json:"ok"`
	Error string `json:"error"`
}

type authStartPayload struct {
	ChatID int64  `json:"chat_id"`
	Phone  string `json:"phone"`
}

type authStartResp struct {
	OK          bool   `json:"ok"`
	Error       string `json:"error"`
	ChallengeID string `json:"challenge_id"`
}

type authCompletePayload struct {
	ChallengeID string `json:"challenge_id"`
	Code        string `json:"code"`
	Password    string `json:"password"`
}

type authCompleteResp struct {
	OK         bool   `json:"ok"`
	Error      string `json:"error"`
	ChatID     int64  `json:"chat_id"`
	AccessHash int64  `json:"access_hash"`
}

func NewServiceFromEnv() Service {
	baseURL := strings.TrimRight(strings.TrimSpace(os.Getenv("CLEAR_CHAT_OPS_URL")), "/")
	if baseURL == "" {
		return noopService{}
	}
	timeoutSec := envInt("CLEAR_CHAT_OPS_TIMEOUT_SEC", 20)
	if timeoutSec <= 0 {
		timeoutSec = 20
	}
	return &HTTPService{
		baseURL:   baseURL,
		authToken: strings.TrimSpace(os.Getenv("CLEAR_CHAT_OPS_TOKEN")),
		client:    &http.Client{Timeout: time.Duration(timeoutSec) * time.Second},
	}
}

func (s *HTTPService) Clear(ctx context.Context, req Request) error {
	if s == nil || strings.TrimSpace(s.baseURL) == "" || s.client == nil {
		return ErrNotConfigured
	}
	if req.ChatID == 0 {
		return fmt.Errorf("%w: empty chat id", ErrBadRequest)
	}

	payload := clearChatHTTPReq{ChatID: req.ChatID, Username: strings.TrimSpace(req.Username)}
	cmdBody, err := json.Marshal(commandEnvelope{
		RequestID: fmt.Sprintf("clear-%d", time.Now().UnixNano()),
		Command:   "clear_chat",
		Payload:   payload,
	})
	if err != nil {
		return fmt.Errorf("marshal clear command: %w", err)
	}

	// Preferred command-bus endpoint.
	return s.postAndCheck(ctx, "/v1/command", cmdBody)
}

func (s *HTTPService) postAndCheck(ctx context.Context, path string, body []byte) error {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, s.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build request %s: %w", path, err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if s.authToken != "" {
		httpReq.Header.Set("X-TG-Ops-Token", s.authToken)
	}

	resp, err := s.client.Do(httpReq)
	if err != nil {
		return fmt.Errorf("call tg-ops-service %s: %w", path, err)
	}
	defer resp.Body.Close()

	respBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode >= 400 {
		parsed := clearChatHTTPResp{}
		if json.Unmarshal(respBytes, &parsed) == nil && strings.TrimSpace(parsed.Error) != "" {
			if resp.StatusCode == http.StatusNotImplemented {
				return ErrNotConfigured
			}
			return fmt.Errorf("tg-ops-service %s %d: %s", path, resp.StatusCode, parsed.Error)
		}
		if resp.StatusCode == http.StatusNotImplemented {
			return ErrNotConfigured
		}
		return fmt.Errorf("tg-ops-service %s %d: %s", path, resp.StatusCode, strings.TrimSpace(string(respBytes)))
	}
	return nil
}

type noopService struct{}

func (noopService) Clear(context.Context, Request) error { return ErrNotConfigured }
func (noopService) StartAuth(context.Context, AuthStartRequest) (AuthStartResult, error) {
	return AuthStartResult{}, ErrNotConfigured
}
func (noopService) CompleteAuth(context.Context, AuthCompleteRequest) (AuthCompleteResult, error) {
	return AuthCompleteResult{}, ErrNotConfigured
}
func (noopService) Available(context.Context) bool { return false }

func (s *HTTPService) StartAuth(ctx context.Context, req AuthStartRequest) (AuthStartResult, error) {
	if s == nil || strings.TrimSpace(s.baseURL) == "" || s.client == nil {
		return AuthStartResult{}, ErrNotConfigured
	}
	body, err := json.Marshal(commandEnvelope{
		RequestID: fmt.Sprintf("auth-start-%d", time.Now().UnixNano()),
		Command:   "auth_user_start",
		Payload: authStartPayload{
			ChatID: req.ChatID,
			Phone:  strings.TrimSpace(req.Phone),
		},
	})
	if err != nil {
		return AuthStartResult{}, fmt.Errorf("marshal auth_user_start: %w", err)
	}
	data, err := s.postForJSON(ctx, "/v1/command", body)
	if err != nil {
		return AuthStartResult{}, err
	}
	var out authStartResp
	if err := json.Unmarshal(data, &out); err != nil {
		return AuthStartResult{}, fmt.Errorf("decode auth_user_start: %w", err)
	}
	if !out.OK || strings.TrimSpace(out.ChallengeID) == "" {
		if strings.TrimSpace(out.Error) != "" {
			return AuthStartResult{}, errors.New(out.Error)
		}
		return AuthStartResult{}, errors.New("auth_user_start failed")
	}
	return AuthStartResult{ChallengeID: out.ChallengeID}, nil
}

func (s *HTTPService) CompleteAuth(ctx context.Context, req AuthCompleteRequest) (AuthCompleteResult, error) {
	if s == nil || strings.TrimSpace(s.baseURL) == "" || s.client == nil {
		return AuthCompleteResult{}, ErrNotConfigured
	}
	body, err := json.Marshal(commandEnvelope{
		RequestID: fmt.Sprintf("auth-complete-%d", time.Now().UnixNano()),
		Command:   "auth_user_complete",
		Payload: authCompletePayload{
			ChallengeID: strings.TrimSpace(req.ChallengeID),
			Code:        strings.TrimSpace(req.Code),
			Password:    strings.TrimSpace(req.Password),
		},
	})
	if err != nil {
		return AuthCompleteResult{}, fmt.Errorf("marshal auth_user_complete: %w", err)
	}
	data, err := s.postForJSON(ctx, "/v1/command", body)
	if err != nil {
		return AuthCompleteResult{}, err
	}
	var out authCompleteResp
	if err := json.Unmarshal(data, &out); err != nil {
		return AuthCompleteResult{}, fmt.Errorf("decode auth_user_complete: %w", err)
	}
	if !out.OK {
		if strings.TrimSpace(out.Error) != "" {
			return AuthCompleteResult{}, errors.New(out.Error)
		}
		return AuthCompleteResult{}, errors.New("auth_user_complete failed")
	}
	return AuthCompleteResult{ChatID: out.ChatID, AccessHash: out.AccessHash}, nil
}

func (s *HTTPService) Available(ctx context.Context) bool {
	if s == nil || strings.TrimSpace(s.baseURL) == "" || s.client == nil {
		return false
	}
	hctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(hctx, http.MethodGet, s.baseURL+"/healthz", nil)
	if err != nil {
		return false
	}
	if s.authToken != "" {
		req.Header.Set("X-TG-Ops-Token", s.authToken)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode >= 200 && resp.StatusCode < 300
}

func envInt(key string, fallback int) int {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	i, err := strconv.Atoi(v)
	if err != nil {
		return fallback
	}
	return i
}

func (s *HTTPService) postForJSON(ctx context.Context, path string, body []byte) ([]byte, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, s.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build request %s: %w", path, err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if s.authToken != "" {
		httpReq.Header.Set("X-TG-Ops-Token", s.authToken)
	}
	resp, err := s.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("call tg-ops-service %s: %w", path, err)
	}
	defer resp.Body.Close()
	respBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	if resp.StatusCode >= 400 {
		parsed := clearChatHTTPResp{}
		if json.Unmarshal(respBytes, &parsed) == nil && strings.TrimSpace(parsed.Error) != "" {
			if resp.StatusCode == http.StatusNotImplemented {
				return nil, ErrNotConfigured
			}
			return nil, fmt.Errorf("tg-ops-service %s %d: %s", path, resp.StatusCode, parsed.Error)
		}
		return nil, fmt.Errorf("tg-ops-service %s %d: %s", path, resp.StatusCode, strings.TrimSpace(string(respBytes)))
	}
	return respBytes, nil
}
