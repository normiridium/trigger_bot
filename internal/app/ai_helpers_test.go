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

func TestSanitizeParticipantPortraitMessageDropsBotAddress(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{in: "Оленям, мой портрет", want: "мой портрет"},
		{in: "Оле-ням: оцени фото", want: "оцени фото"},
		{in: "Привет, оленям, что думаешь?", want: "Привет, что думаешь"},
		{in: "Оленька — расскажи про химию", want: "расскажи про химию"},
	}
	for _, tc := range cases {
		if got := sanitizeParticipantPortraitMessage(tc.in); got != tc.want {
			t.Fatalf("sanitizeParticipantPortraitMessage(%q)=%q want %q", tc.in, got, tc.want)
		}
	}
}
