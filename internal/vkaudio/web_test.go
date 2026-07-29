package vkaudio

import "testing"

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
