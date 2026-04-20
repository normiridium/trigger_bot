package app

import "testing"

func TestParseOrientationChoice(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{in: "portrait", want: "portrait"},
		{in: "Портрет", want: "portrait"},
		{in: "landscape", want: "landscape"},
		{in: "панорама города", want: "landscape"},
		{in: "square", want: "square"},
		{in: "что-то непонятное", want: "square"},
	}
	for _, tc := range cases {
		if got := parseOrientationChoice(tc.in); got != tc.want {
			t.Fatalf("parseOrientationChoice(%q)=%q want=%q", tc.in, got, tc.want)
		}
	}
}
