package mediadl

import (
	"errors"
	"strings"
	"testing"
)

func TestIsYTDLPAgeRestricted(t *testing.T) {
	cases := map[string]bool{
		"ERROR: [youtube] x: Sign in to confirm your age. This video may be inappropriate": true,
		"ERROR: [youtube] x: Sorry, this content is age-restricted.":                       true,
		"ERROR: this video is age restricted":                                              true,
		"YouTube is requiring account age-verification":                                    true,
		"ERROR: [youtube] x: Requested format is not available":                            false,
		"ERROR: [soundcloud] y: This video is DRM protected":                               false,
	}
	for msg, want := range cases {
		if got := isYTDLPAgeRestricted(errors.New(msg)); got != want {
			t.Fatalf("isYTDLPAgeRestricted(%q) = %v, ожидалось %v", msg, got, want)
		}
	}
	if isYTDLPAgeRestricted(nil) {
		t.Fatal("nil не должен считаться возрастным ограничением")
	}
}

func TestWithAuthCookiesReplacesCookies(t *testing.T) {
	args := []string{"--no-playlist", "--cookies", "/old/cookies.txt", "-f", "bestaudio", "https://youtu.be/x"}
	got := strings.Join(withAuthCookies(args, "/auth/cookies.txt"), " ")
	if !strings.Contains(got, "--cookies /auth/cookies.txt") {
		t.Fatalf("cookies не подменены: %s", got)
	}
	if strings.Contains(got, "/old/cookies.txt") {
		t.Fatalf("старый файл остался: %s", got)
	}
	if !strings.HasSuffix(got, "https://youtu.be/x") {
		t.Fatalf("URL должен остаться последним: %s", got)
	}
}

func TestWithAuthCookiesInsertsWhenMissing(t *testing.T) {
	args := []string{"--no-playlist", "-f", "bestaudio", "https://youtu.be/x"}
	got := withAuthCookies(args, "/auth/cookies.txt")
	joined := strings.Join(got, " ")
	if !strings.Contains(joined, "--cookies /auth/cookies.txt") {
		t.Fatalf("cookies не добавлены: %s", joined)
	}
	if got[len(got)-1] != "https://youtu.be/x" {
		t.Fatalf("URL должен остаться последним: %v", got)
	}
	if argsUseCookiesFile(args, "/auth/cookies.txt") {
		t.Fatal("исходные аргументы не содержат этот файл cookies")
	}
	if !argsUseCookiesFile(got, "/auth/cookies.txt") {
		t.Fatal("после подстановки файл должен находиться")
	}
}
