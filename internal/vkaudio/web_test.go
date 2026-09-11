package vkaudio

import (
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"testing"
)

func TestNormalizeVKWebBaseDefaultsToVKRU(t *testing.T) {
	got, err := normalizeVKWebBase("")
	if err != nil {
		t.Fatalf("normalizeVKWebBase returned error: %v", err)
	}
	if got != "https://vk.ru" {
		t.Fatalf("normalizeVKWebBase default=%q, want https://vk.ru", got)
	}
}

func TestNormalizeVKWebBase(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{in: "vk.ru", want: "https://vk.ru"},
		{in: "https://vk.ru/", want: "https://vk.ru"},
		{in: "https://vk.com", want: "https://vk.com"},
	}
	for _, tc := range tests {
		got, err := normalizeVKWebBase(tc.in)
		if err != nil {
			t.Fatalf("normalizeVKWebBase(%q) returned error: %v", tc.in, err)
		}
		if got != tc.want {
			t.Fatalf("normalizeVKWebBase(%q)=%q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestVKLoginBase(t *testing.T) {
	tests := []struct {
		base string
		want string
	}{
		{base: "https://vk.ru", want: "https://login.vk.ru"},
		{base: "https://m.vk.ru", want: "https://login.vk.ru"},
		{base: "https://vk.com", want: "https://login.vk.com"},
	}
	for _, tc := range tests {
		if got := vkLoginBase(tc.base); got != tc.want {
			t.Fatalf("vkLoginBase(%q)=%q, want %q", tc.base, got, tc.want)
		}
	}
}

func TestLoadNetscapeCookiesAliasesVKDomains(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cookies.txt")
	body := "# Netscape HTTP Cookie File\n" +
		".vk.com\tTRUE\t/\tTRUE\t2147483647\tremixsid\tfrom-com\n" +
		".vk.ru\tTRUE\t/\tTRUE\t2147483647\tremixlang\tru\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := loadNetscapeCookies(jar, path); err != nil {
		t.Fatalf("load cookies: %v", err)
	}
	u, _ := url.Parse("https://vk.ru/")
	got := map[string]string{}
	for _, ck := range jar.Cookies(u) {
		got[ck.Name] = ck.Value
	}
	if got["remixsid"] != "from-com" {
		t.Fatalf("expected vk.com remixsid alias on vk.ru, got %q", got["remixsid"])
	}
	if got["remixlang"] != "ru" {
		t.Fatalf("expected original vk.ru cookie, got %q", got["remixlang"])
	}
}

func TestLoadNetscapeCookiesKeepsNativeVKDomainValue(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cookies.txt")
	body := "# Netscape HTTP Cookie File\n" +
		".vk.com\tTRUE\t/\tTRUE\t2147483647\tremixsid\tfrom-com\n" +
		".vk.ru\tTRUE\t/\tTRUE\t2147483647\tremixsid\tfrom-ru\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := loadNetscapeCookies(jar, path); err != nil {
		t.Fatalf("load cookies: %v", err)
	}
	u, _ := url.Parse("https://vk.ru/")
	for _, ck := range jar.Cookies(u) {
		if ck.Name == "remixsid" && ck.Value != "from-ru" {
			t.Fatalf("native vk.ru cookie must win, got %q", ck.Value)
		}
	}
}
