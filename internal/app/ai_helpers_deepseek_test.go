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

func TestDeepseekEffort(t *testing.T) {
	t.Setenv("DEEPSEEK_EFFORT", "")
	if got := deepseekEffort(); got != "max" {
		t.Fatalf("default effort must be max, got %q", got)
	}
	t.Setenv("DEEPSEEK_EFFORT", "high")
	if got := deepseekEffort(); got != "high" {
		t.Fatalf("explicit effort, got %q", got)
	}
	t.Setenv("DEEPSEEK_EFFORT", "weird")
	if got := deepseekEffort(); got != "max" {
		t.Fatalf("invalid effort must fall back to max, got %q", got)
	}
}

func TestDeepseekPayloadFromOpenAI(t *testing.T) {
	t.Setenv("DEEPSEEK_MODEL", "")
	t.Setenv("DEEPSEEK_MODEL_MULTIMODAL", "")
	t.Setenv("DEEPSEEK_EFFORT", "")

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
	cp := deepseekPayloadFromOpenAI(orig)

	if orig["model"] != "gpt-4.1" {
		t.Fatalf("original payload mutated: %v", orig["model"])
	}
	// With images -> multimodal model (flash), images preserved.
	if cp["model"] != "deepseek-flash" {
		t.Fatalf("image payload must route to flash, got %v", cp["model"])
	}
	if cp["effort"] != "max" {
		t.Fatalf("expected effort=max, got %v", cp["effort"])
	}
	msgs, ok := cp["messages"].([]interface{})
	if !ok || len(msgs) != 2 {
		t.Fatalf("unexpected messages: %#v", cp["messages"])
	}
	first := msgs[0].(map[string]interface{})
	content, ok := first["content"].([]interface{})
	if !ok {
		t.Fatalf("content should stay an array (images passed through), got %T", first["content"])
	}
	if len(content) != 3 {
		t.Fatalf("expected 3 content parts, got %d", len(content))
	}
	img, ok := content[1].(map[string]interface{})
	if !ok || img["type"] != "image_url" {
		t.Fatalf("image part lost: %#v", img)
	}

	// Text-only payload -> text model (v4-pro).
	textPayload := map[string]interface{}{
		"model": "gpt-4.1",
		"messages": []map[string]interface{}{
			{"role": "user", "content": "привет, как дела?"},
		},
	}
	cp2 := deepseekPayloadFromOpenAI(textPayload)
	if cp2["model"] != "deepseek-v4-pro" {
		t.Fatalf("text payload must route to v4-pro, got %v", cp2["model"])
	}
	if cp2["effort"] != "max" {
		t.Fatalf("expected effort=max, got %v", cp2["effort"])
	}

	if _, err := json.Marshal(cp); err != nil {
		t.Fatalf("cp must marshal: %v", err)
	}
}
