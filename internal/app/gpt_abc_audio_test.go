package app

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

func TestExtractFirstABCFromGPTResponse(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
		ok   bool
	}{
		{
			name: "fenced abc block",
			in:   "Вот мелодия:\n```abc\nX:1\nM:4/4\nK:C\n| C D E F | G4 |\n```\nНаслаждайся!",
			want: "X:1\nM:4/4\nK:C\n| C D E F | G4 |",
			ok:   true,
		},
		{
			name: "fence with language suffix",
			in:   "```abc\nX:2\nK:G\n| G A B c |\n```",
			want: "X:2\nK:G\n| G A B c |",
			ok:   true,
		},
		{
			name: "bare abc without fence",
			in:   "Вот:\nX:3\nM:3/4\nK:D\n| D E F | G3 |",
			want: "X:3\nM:3/4\nK:D\n| D E F | G3 |",
			ok:   true,
		},
		{
			name: "no abc at all",
			in:   "Я не умею писать музыку, извини.",
			want: "",
			ok:   false,
		},
		{
			name: "empty fence",
			in:   "```abc\n```",
			want: "",
			ok:   false,
		},
	}
	for _, c := range cases {
		got, ok := extractFirstABCFromGPTResponse(c.in)
		if ok != c.ok {
			t.Fatalf("%s: ok=%v want %v (got=%q)", c.name, ok, c.ok, got)
		}
		if got != c.want {
			t.Fatalf("%s: got %q want %q", c.name, got, c.want)
		}
	}
}

// TestRenderGPTABCResponseToAudio exercises the full ABC -> MIDI -> WAV -> OGG
// chain; it is skipped when the renderer binaries are not installed.
func TestRenderGPTABCResponseToAudio(t *testing.T) {
	if _, err := systemDiagramBin("TRIGGER_BOT_ABC2MIDI_BIN", "abc2midi"); err != nil {
		t.Skipf("abc2midi not available: %v", err)
	}
	if _, err := systemDiagramBin("TRIGGER_BOT_TIMIDITY_BIN", "timidity"); err != nil {
		t.Skipf("timidity not available: %v", err)
	}
	const abc = "X:1\nM:4/4\nL:1/8\nQ:1/4=120\nK:C\n| C D E F | G2 A2 | G F E D | C4 |\n"
	oggPath, err := renderGPTABCResponseToAudio(abc)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	st, err := os.Stat(oggPath)
	if err != nil {
		t.Fatalf("stat output: %v", err)
	}
	if st.Size() == 0 {
		t.Fatalf("ogg output is empty")
	}
	_ = os.RemoveAll(oggPath)

	if _, err := renderGPTABCResponseToAudio("no abc header here"); err == nil {
		t.Fatalf("expected error for source without X: header")
	}
}

// abcTwoVoicesWithBlankLines — реальная ABC от модели, на которой бот прислал
// секунду звука вместо мелодии: голоса разделены пустыми строками.
const abcTwoVoicesWithBlankLines = `X:1
T:Крымский разрушенный мост
M:4/4
L:1/8
Q:1/4=66
K:Dm

V:1
%%MIDI program 40
D4 F2 A2 | G2 F2 E2 D2 | F4 E2 D2 | D6 z2 |
A2 c2 B2 A2 | G4 F2 E2 | F2 A2 G2 F2 | E6 z2 |
D2 F2 A2 d2 | c4 B2 A2 | B2 A2 G2 F2 | E6 z2 |
A2 c2 e2 c2 | d4 c2 B2 | A2 G2 F2 E2 | D6 z2 |
d4 c2 A2 | B4 A2 G2 | c4 B2 A2 | G4 F2 E2 |
F2 A2 d2 c2 | B4 A2 G2 | A2 c2 B2 A2 | G6 z2 |
F4 E2 D2 | A4 G2 F2 | E2 F2 G2 A2 | D6 z2 |
D2 F2 A2 F2 | G2 F2 E2 D2 | F4 E2 D2 | D8 |]

V:2
%%MIDI program 0
D, A, D A F A D A | G, D G D B D G D | D, A, D A F A D A | D, A, D A F A D A |
D, A, D A F A D A | G, D G D B D G D | D, A, D A F A D A | A, E A E C E A E |
D, A, D A F A D A | B, F B F d F B F | G, D G D B D G D | A, E A E C E A E |
D, A, D A F A D A | B, F B F d F B F | G, D G D B D G D | D, A, D A F A D A |
D,4 A,2 D2 | B,4 F,2 B2 | G,4 D,2 G2 | A,4 E,2 A2 |
D,4 A,2 D2 | B,4 F,2 B2 | G,4 D,2 G2 | A,4 E,2 A2 |
D, A, D A F A D A | G, D G D B D G D | A, E A E C E A E | D, A, D A F A D A |
D,4 A,2 F2 | G,4 D2 B2 | A,4 E2 C2 | D,8 |]
`

func TestNormalizeABCSourceDropsBlankLines(t *testing.T) {
	got := normalizeABCSource("X:1\nT:Тест\n\nV:1\nC D E F |\n\nV:2\nC, D, E, F, |\n")
	for _, line := range strings.Split(got, "\n") {
		if strings.TrimSpace(line) == "" {
			t.Fatalf("пустая строка осталась: %q", got)
		}
	}
	if !strings.HasPrefix(got, "X:1") || !strings.Contains(got, "V:2") {
		t.Fatalf("структура потеряна: %q", got)
	}
	if strings.Contains(got, "```") {
		t.Fatalf("заборы не вычищены: %q", got)
	}
}

// TestRenderABCMultiVoiceWithBlankLines — регресс на «секунду пустышки»:
// раньше abc2midi видел в пустых строках конец тюна и рендерил 1 с.
func TestRenderABCMultiVoiceWithBlankLines(t *testing.T) {
	if _, err := systemDiagramBin("TRIGGER_BOT_ABC2MIDI_BIN", "abc2midi"); err != nil {
		t.Skipf("abc2midi not available: %v", err)
	}
	if _, err := systemDiagramBin("TRIGGER_BOT_TIMIDITY_BIN", "timidity"); err != nil {
		t.Skipf("timidity not available: %v", err)
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skipf("ffprobe not available: %v", err)
	}
	oggPath, err := renderGPTABCResponseToAudio(abcTwoVoicesWithBlankLines)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	defer os.RemoveAll(oggPath)
	out, err := exec.Command("ffprobe", "-v", "error", "-show_entries", "format=duration", "-of", "default=nw=1:nk=1", oggPath).Output()
	if err != nil {
		t.Fatalf("ffprobe: %v", err)
	}
	dur, convErr := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	if convErr != nil {
		t.Fatalf("длительность не разобрана: %v (%q)", convErr, string(out))
	}
	if dur < 60 {
		t.Fatalf("двухголосный тюн отрендерился за %.1f с — пустые строки снова режут мелодию", dur)
	}
}
