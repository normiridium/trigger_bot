package app

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"trigger-admin-bot/internal/model"
)

func TestTokenHashHex(t *testing.T) {
	a := tokenHashHex("abc")
	b := tokenHashHex("abc")
	c := tokenHashHex("abcd")
	if a == "" || b == "" || c == "" {
		t.Fatalf("expected non-empty hashes")
	}
	if a != b {
		t.Fatalf("same input must produce same hash")
	}
	if a == c {
		t.Fatalf("different input must produce different hash")
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
	if got := formatEnvValue("320K"); got != `"320K"` {
		t.Fatalf("unexpected simple env value: %q", got)
	}
	if got := formatEnvValue("hello world"); got != `"hello world"` {
		t.Fatalf("expected quoted value, got %q", got)
	}
	if got := formatEnvValue("a\nb"); strings.Contains(got, "\n") {
		t.Fatalf("expected newlines to be sanitized, got %q", got)
	}
	if got := formatEnvValue(""); got != "" {
		t.Fatalf("expected empty value to stay empty")
	}
}

func TestParseJSONBody(t *testing.T) {
	var payload struct {
		Name string `json:"name"`
	}
	req := httptest.NewRequest("POST", "/x", bytes.NewBufferString(`{"name":"ok"}`))
	req.Header.Set("Content-Type", "application/json")
	if err := parseJSONBody(req, &payload); err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}
	if payload.Name != "ok" {
		t.Fatalf("payload mismatch: %#v", payload)
	}

	reqBadMethod := httptest.NewRequest("GET", "/x", nil)
	reqBadMethod.Header.Set("Content-Type", "application/json")
	if err := parseJSONBody(reqBadMethod, &payload); err == nil {
		t.Fatalf("expected method validation error")
	}

	reqBadCT := httptest.NewRequest("POST", "/x", bytes.NewBufferString(`{"name":"ok"}`))
	reqBadCT.Header.Set("Content-Type", "text/plain")
	if err := parseJSONBody(reqBadCT, &payload); err == nil {
		t.Fatalf("expected content-type validation error")
	}

	reqUnknown := httptest.NewRequest("POST", "/x", bytes.NewBufferString(`{"name":"ok","x":1}`))
	reqUnknown.Header.Set("Content-Type", "application/json")
	if err := parseJSONBody(reqUnknown, &payload); err == nil {
		t.Fatalf("expected unknown field error")
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

func TestAuthStateResponseShape(t *testing.T) {
	w := NewWebAdmin(nil, "")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/trigger_bot/auth_state", nil)
	w.authStateJSON(rec, req)
	if rec.Code != 500 {
		t.Fatalf("expected 500 with nil store, got %d", rec.Code)
	}
}

func TestWriteJSONHelper(t *testing.T) {
	rec := httptest.NewRecorder()
	writeJSON(rec, 200, map[string]string{"ok": "yes"})
	if rec.Code != 200 {
		t.Fatalf("status mismatch: %d", rec.Code)
	}
	var out map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("invalid json body: %v", err)
	}
	if out["ok"] != "yes" {
		t.Fatalf("payload mismatch: %#v", out)
	}
}

func TestCSRFCookieValidation(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/trigger_bot/save", nil)
	req.AddCookie(&http.Cookie{Name: adminCSRFCookieName, Value: "abc123"})
	req.Header.Set("X-CSRF-Token", "abc123")
	if !csrfTokenValid(req) {
		t.Fatalf("expected csrf token to validate")
	}
	reqBad := httptest.NewRequest(http.MethodPost, "/trigger_bot/save", nil)
	reqBad.AddCookie(&http.Cookie{Name: adminCSRFCookieName, Value: "abc123"})
	reqBad.Header.Set("X-CSRF-Token", "zzz")
	if csrfTokenValid(reqBad) {
		t.Fatalf("expected csrf token mismatch to fail")
	}
}

func TestIsStateChangingMethod(t *testing.T) {
	if isStateChangingMethod(http.MethodGet) {
		t.Fatalf("GET must not be state changing")
	}
	if !isStateChangingMethod(http.MethodPost) {
		t.Fatalf("POST must be state changing")
	}
}

func TestEnsureCSRFCookie(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/trigger_bot", nil)
	token, err := ensureCSRFCookie(rec, req)
	if err != nil {
		t.Fatalf("ensureCSRFCookie failed: %v", err)
	}
	if strings.TrimSpace(token) == "" {
		t.Fatalf("expected csrf token")
	}
	got := rec.Result().Cookies()
	found := false
	for _, c := range got {
		if c.Name == adminCSRFCookieName {
			found = true
			if strings.TrimSpace(c.Value) == "" {
				t.Fatalf("csrf cookie value is empty")
			}
		}
	}
	if !found {
		t.Fatalf("expected csrf cookie to be set")
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
	if got := iconForActionType(model.ActionTypeSendGIF); got == "" {
		t.Fatalf("expected icon for send gif action")
	}
}
