package main

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"trigger-admin-bot/internal/model"
)

func TestWebAdminAuthOK(t *testing.T) {
	w := NewWebAdmin(nil, "secret")

	req := httptest.NewRequest("GET", "/trigger_bot", nil)
	if w.authOK(req) {
		t.Fatalf("expected unauthorized without token")
	}

	req = httptest.NewRequest("GET", "/trigger_bot?token=secret", nil)
	if !w.authOK(req) {
		t.Fatalf("expected query token to authorize")
	}

	req = httptest.NewRequest("GET", "/trigger_bot", nil)
	req.Header.Set("X-Admin-Token", "secret")
	if !w.authOK(req) {
		t.Fatalf("expected header token to authorize")
	}

	open := NewWebAdmin(nil, "")
	if !open.authOK(httptest.NewRequest("GET", "/trigger_bot", nil)) {
		t.Fatalf("expected empty admin token to allow all")
	}
}

func TestNormalizeBoolString(t *testing.T) {
	truthy := []string{"1", "true", "yes", "on", " TRUE "}
	for _, v := range truthy {
		if got := normalizeBoolString(v); got != "true" {
			t.Fatalf("normalizeBoolString(%q) = %q, want true", v, got)
		}
	}

	falsy := []string{"0", "false", "off", "", "nope"}
	for _, v := range falsy {
		if got := normalizeBoolString(v); got != "false" {
			t.Fatalf("normalizeBoolString(%q) = %q, want false", v, got)
		}
	}
}

func TestFormatEnvValue(t *testing.T) {
	if got := formatEnvValue("320K"); got != "320K" {
		t.Fatalf("unexpected simple env value: %q", got)
	}
	if got := formatEnvValue("hello world"); got != `"hello world"` {
		t.Fatalf("expected quoted value, got %q", got)
	}
	if got := formatEnvValue(""); got != "" {
		t.Fatalf("expected empty value to stay empty")
	}
}

func TestReadWriteEnvFileRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	content := "# comment\nA=1\nB=hello\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("seed env: %v", err)
	}

	if err := writeEnvFile(path, map[string]string{"B": "hello world", "C": "x"}); err != nil {
		t.Fatalf("writeEnvFile: %v", err)
	}

	got := readEnvFile(path)
	if got["A"] != "1" {
		t.Fatalf("A mismatch: %q", got["A"])
	}
	if got["B"] != "hello world" {
		t.Fatalf("B mismatch: %q", got["B"])
	}
	if got["C"] != "x" {
		t.Fatalf("C mismatch: %q", got["C"])
	}
}

func TestLoadEnvSettingsPriority(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	if err := os.WriteFile(".env", []byte("KEY1=file\n"), 0o600); err != nil {
		t.Fatalf("write .env: %v", err)
	}
	if err := os.Setenv("KEY2", "from_env"); err != nil {
		t.Fatalf("setenv: %v", err)
	}
	t.Cleanup(func() { _ = os.Unsetenv("KEY2") })

	fields := []settingField{
		{Key: "KEY1", Description: "default1"},
		{Key: "KEY2", Description: "default2"},
		{Key: "KEY3", Description: "default3"},
	}
	vals := loadEnvSettings(fields)
	if vals["KEY1"] != "file" {
		t.Fatalf("expected KEY1 from file, got %q", vals["KEY1"])
	}
	if vals["KEY2"] != "from_env" {
		t.Fatalf("expected KEY2 from env, got %q", vals["KEY2"])
	}
	if vals["KEY3"] != "default3" {
		t.Fatalf("expected KEY3 default, got %q", vals["KEY3"])
	}
}

func TestIconMappings(t *testing.T) {
	if got := iconForTriggerMode(model.TriggerModeAll); got == "" {
		t.Fatalf("expected icon for trigger mode all")
	}
	if got := iconForAdminMode(model.AdminModeAdmins); got == "" {
		t.Fatalf("expected icon for admin mode admins")
	}
	if got := iconForMatchType(model.MatchTypeRegex); got == "" {
		t.Fatalf("expected icon for regex")
	}
	if got := iconForActionType(model.ActionTypeMediaAudio); got == "" {
		t.Fatalf("expected icon for media action")
	}
}
