package app

import (
	"encoding/json"
	"testing"
)

func TestIsInsufficientQuotaResponse(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   bool
	}{
		{"openai quota", 429, `{"error":{"code":"insufficient_quota"}}`, true},
		{"deepseek balance", 429, `{"error":{"message":"Insufficient Balance"}}`, true},
		{"other 429", 429, `{"error":{"message":"rate limit"}}`, false},
		{"ok status", 200, `{"error":{"code":"insufficient_quota"}}`, false},
	}
	for _, c := range cases {
		if got := isInsufficientQuotaResponse(c.status, []byte(c.body)); got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}

func TestDeepseekChatEndpoint(t *testing.T) {
	t.Setenv("DEEPSEEK_BASE_URL", "")
	if got := deepseekChatEndpoint(); got != "https://api.deepseek.com/v1/chat/completions" {
		t.Fatalf("default: %s", got)
	}
	t.Setenv("DEEPSEEK_BASE_URL", "https://api.deepseek.com")
	if got := deepseekChatEndpoint(); got != "https://api.deepseek.com/v1/chat/completions" {
		t.Fatalf("base: %s", got)
	}
	t.Setenv("DEEPSEEK_BASE_URL", "https://api.deepseek.com/v1")
	if got := deepseekChatEndpoint(); got != "https://api.deepseek.com/v1/chat/completions" {
		t.Fatalf("base v1: %s", got)
	}
	t.Setenv("DEEPSEEK_BASE_URL", "https://example.org/api/")
	if got := deepseekChatEndpoint(); got != "https://example.org/api/v1/chat/completions" {
		t.Fatalf("custom: %s", got)
	}
}

func TestDeepseekPayloadFromOpenAI(t *testing.T) {
	orig := map[string]interface{}{
		"model": "gpt-4.1",
		"messages": []map[string]interface{}{
			{
				"role": "user",
				"content": []map[string]interface{}{
					{"type": "text", "text": "привет"},
					{"type": "image_url", "image_url": map[string]string{"url": "data:image/png;base64,abc"}},
					{"type": "text", "text": "что на картинке?"},
				},
			},
			{"role": "system", "content": "ты бот"},
		},
	}
	t.Setenv("DEEPSEEK_MODEL", "deepseek-chat")
	cp := deepseekPayloadFromOpenAI(orig)

	if orig["model"] != "gpt-4.1" {
		t.Fatalf("original payload mutated: %v", orig["model"])
	}
	if cp["model"] != "deepseek-chat" {
		t.Fatalf("model not swapped: %v", cp["model"])
	}
	msgs, ok := cp["messages"].([]interface{})
	if !ok || len(msgs) != 2 {
		t.Fatalf("unexpected messages: %#v", cp["messages"])
	}
	first := msgs[0].(map[string]interface{})
	content, ok := first["content"].(string)
	if !ok {
		t.Fatalf("content should be a string after strip, got %T", first["content"])
	}
	if content != "привет\nчто на картинке?" {
		t.Fatalf("unexpected stripped content: %q", content)
	}
	if _, err := json.Marshal(cp); err != nil {
		t.Fatalf("cp must marshal: %v", err)
	}
}
