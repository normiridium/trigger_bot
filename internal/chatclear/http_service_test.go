package chatclear

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHTTPServiceGetHistorySupportsLargeResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-TG-Ops-Token") != "secret" {
			t.Fatalf("missing auth token")
		}
		var envelope struct {
			Command string         `json:"command"`
			Payload historyPayload `json:"payload"`
		}
		if err := json.NewDecoder(r.Body).Decode(&envelope); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if envelope.Command != "get_history" || envelope.Payload.Limit != 1000 || envelope.Payload.ChatID != -100123 {
			t.Fatalf("unexpected request: %+v", envelope)
		}
		messages := make([]HistoryMessage, 1000)
		for i := range messages {
			messages[i] = HistoryMessage{ID: i + 1, Date: 1000 + int64(i), Text: strings.Repeat("x", 100)}
		}
		_ = json.NewEncoder(w).Encode(historyResp{OK: true, Messages: messages})
	}))
	defer server.Close()

	service := &HTTPService{baseURL: server.URL, authToken: "secret", client: &http.Client{Timeout: time.Second}}
	result, err := service.GetHistory(context.Background(), HistoryRequest{ChatID: -100123, Limit: 1000})
	if err != nil {
		t.Fatalf("get history: %v", err)
	}
	if len(result.Messages) != 1000 {
		t.Fatalf("expected 1000 messages, got %d", len(result.Messages))
	}
}

func TestHTTPServiceGetHistoryReturnsServiceError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = fmt.Fprint(w, `{"ok":false,"error":"history failed"}`)
	}))
	defer server.Close()
	service := &HTTPService{baseURL: server.URL, client: server.Client()}
	_, err := service.GetHistory(context.Background(), HistoryRequest{ChatID: -100123})
	if err == nil || !strings.Contains(err.Error(), "history failed") {
		t.Fatalf("unexpected error: %v", err)
	}
}
