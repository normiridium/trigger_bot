package app

import "testing"

func TestCoubTargetVideoKbps(t *testing.T) {
	cases := []struct {
		name     string
		duration float64
		limitMB  int
		audio    int
		wantMin  int
		wantMax  int
	}{
		{"260с на 50МБ", 260, 50, 128, 1250, 1420},
		{"10с на 50МБ", 10, 50, 128, 300, 40000},
		{"0 длительность", 0, 50, 128, 0, 0},
		{"нет лимита", 260, 0, 128, 0, 0},
	}
	for _, tc := range cases {
		got := coubTargetVideoKbps(tc.duration, tc.limitMB, tc.audio)
		if got < tc.wantMin || got > tc.wantMax {
			t.Fatalf("%s: получили %d kbps, ожидали %d..%d", tc.name, got, tc.wantMin, tc.wantMax)
		}
	}
}

func TestCoubTargetVideoKbpsNeverBelow300(t *testing.T) {
	// очень длинное видео и маленький лимит: ниже 300 kbps не опускаемся
	if got := coubTargetVideoKbps(3600, 10, 128); got != 300 {
		t.Fatalf("ожидали пол 300 kbps, получили %d", got)
	}
}

func TestCoubLoopGOP(t *testing.T) {
	cases := map[float64]int{25: 25, 30: 30, 29.97: 30, 24: 25, 0: 25, 60: 60}
	for fps, want := range cases {
		if got := coubLoopGOP(fps); got != want {
			t.Fatalf("fps=%.2f: ожидали %d, получили %d", fps, want, got)
		}
	}
}
