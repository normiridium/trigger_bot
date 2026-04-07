package main

import "testing"

func TestTriggerMatchesBasic(t *testing.T) {
	tests := []struct {
		name string
		tr   Trigger
		in   string
		want bool
	}{
		{
			name: "full insensitive",
			tr:   Trigger{MatchText: "Привет", MatchType: "full", CaseSensitive: false},
			in:   "привет",
			want: true,
		},
		{
			name: "partial insensitive",
			tr:   Trigger{MatchText: "кац", MatchType: "partial", CaseSensitive: false},
			in:   "максим кац мой герой",
			want: true,
		},
		{
			name: "regex insensitive",
			tr:   Trigger{MatchText: `дмитрий\s*гудков`, MatchType: "regex", CaseSensitive: false},
			in:   "Дмитрий    Гудков",
			want: true,
		},
		{
			name: "starts",
			tr:   Trigger{MatchText: "оле", MatchType: "starts", CaseSensitive: false},
			in:   "Оле-ням привет",
			want: true,
		},
		{
			name: "ends",
			tr:   Trigger{MatchText: "герой", MatchType: "ends", CaseSensitive: false},
			in:   "максим кац мой герой",
			want: true,
		},
		{
			name: "empty incoming no match",
			tr:   Trigger{MatchText: "x", MatchType: "partial"},
			in:   "",
			want: false,
		},
		{
			name: "empty trigger matches any non-empty",
			tr:   Trigger{MatchText: "", MatchType: "partial"},
			in:   "что угодно",
			want: true,
		},
	}

	for _, tc := range tests {
		got := TriggerMatches(tc.tr, tc.in)
		if got != tc.want {
			t.Fatalf("%s: got=%v want=%v", tc.name, got, tc.want)
		}
	}
}

func TestTriggerMatchesRegexInvalid(t *testing.T) {
	tr := Trigger{MatchText: "([", MatchType: "regex", CaseSensitive: false}
	if TriggerMatches(tr, "abc") {
		t.Fatalf("invalid regex must not match")
	}
}

