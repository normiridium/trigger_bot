package app

import (
	"os"
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
