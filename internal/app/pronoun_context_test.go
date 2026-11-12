package app

import (
	"strings"
	"testing"
	"time"
)

func TestRenderRecentContextLinesAddsPronoun(t *testing.T) {
	setChatMemberTagResolver(func(chatID, userID int64) string {
		if userID == 111 {
			return "она"
		}
		if userID == 222 {
			return "он"
		}
		return ""
	})
	defer setChatMemberTagResolver(nil)

	items := []recentChatMessage{
		{MessageID: 1, UserID: 111, UserName: "Аня", Text: "привет", At: time.Now()},
		{MessageID: 2, UserID: 222, UserName: "Валера", Text: "здарова", At: time.Now()},
		{MessageID: 3, UserID: 333, UserName: "Гость", Text: "а я без тега", At: time.Now()},
	}
	got := renderRecentContextLines(-100123, items, 30*time.Minute, 12)
	if !strings.Contains(got, "Аня (она):") {
		t.Fatalf("expected female pronoun in %q", got)
	}
	if !strings.Contains(got, "Валера (он):") {
		t.Fatalf("expected male pronoun in %q", got)
	}
	if strings.Contains(got, "Гость (") {
		t.Fatalf("participant without tag must stay plain: %q", got)
	}
}
