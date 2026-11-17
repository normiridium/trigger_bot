package app

import "testing"

func TestPrivateChatTriggersAllows(t *testing.T) {
	cases := []struct {
		name    string
		enabled string
		ids     string
		userID  int64
		want    bool
	}{
		{name: "disabled by default", enabled: "", ids: "1,2", userID: 1, want: false},
		{name: "disabled explicitly", enabled: "false", ids: "", userID: 1, want: false},
		{name: "enabled empty list allows anyone", enabled: "true", ids: "", userID: 999, want: true},
		{name: "enabled list hit", enabled: "true", ids: "111, 5875460329", userID: 5875460329, want: true},
		{name: "enabled list miss", enabled: "true", ids: "111,222", userID: 5875460329, want: false},
		{name: "garbage list misses", enabled: "true", ids: "abc,-,", userID: 5, want: false},
		{name: "garbage entries skipped before hit", enabled: "true", ids: "abc, 5", userID: 5, want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("PRIVATE_CHAT_TRIGGERS_ENABLED", tc.enabled)
			t.Setenv("PRIVATE_CHAT_TRIGGER_USER_IDS", tc.ids)
			if got := privateChatTriggersAllows(tc.userID); got != tc.want {
				t.Fatalf("privateChatTriggersAllows(%d) = %v, want %v", tc.userID, got, tc.want)
			}
		})
	}
}
