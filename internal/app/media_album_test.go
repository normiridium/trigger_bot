package app

import (
	"strings"
	"testing"
)

func TestSplitMediaGroupChunks(t *testing.T) {
	tests := []struct {
		name string
		in   []string
		max  int
		want [][]string
	}{
		{
			name: "small set stays single album",
			in:   []string{"a", "b", "c"},
			max:  10,
			want: [][]string{{"a", "b", "c"}},
		},
		{
			name: "exact album size",
			in:   []string{"1", "2", "3", "4", "5", "6", "7", "8", "9", "10"},
			max:  10,
			want: [][]string{{"1", "2", "3", "4", "5", "6", "7", "8", "9", "10"}},
		},
		{
			name: "seventeen photos split 10+7",
			in:   []string{"1", "2", "3", "4", "5", "6", "7", "8", "9", "10", "11", "12", "13", "14", "15", "16", "17"},
			max:  10,
			want: [][]string{
				{"1", "2", "3", "4", "5", "6", "7", "8", "9", "10"},
				{"11", "12", "13", "14", "15", "16", "17"},
			},
		},
		{
			name: "empty input",
			in:   nil,
			max:  10,
			want: [][]string{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := splitMediaGroupChunks(tt.in, tt.max)
			if len(got) != len(tt.want) {
				t.Fatalf("chunks = %d, want %d (%v)", len(got), len(tt.want), got)
			}
			for i := range tt.want {
				if len(got[i]) != len(tt.want[i]) {
					t.Fatalf("chunk %d size = %d, want %d", i, len(got[i]), len(tt.want[i]))
				}
				for j := range tt.want[i] {
					if got[i][j] != tt.want[i][j] {
						t.Fatalf("chunk %d item %d = %q, want %q", i, j, got[i][j], tt.want[i][j])
					}
				}
			}
		})
	}
}

func TestSplitMediaGroupChunksDoesNotMutateInput(t *testing.T) {
	in := []string{"a", "b", "c"}
	_ = splitMediaGroupChunks(in, 2)
	if in[0] != "a" || in[1] != "b" || in[2] != "c" {
		t.Fatalf("input slice was mutated: %v", in)
	}
}

func TestBuildMediaAlbumCaption(t *testing.T) {
	got := buildMediaAlbumCaption(17, "Post by nikolalenivets", "https://www.instagram.com/p/ABC/", "instagram")
	if want := `<a href="https://www.instagram.com/p/ABC/">Post by nikolalenivets</a>`; !strings.Contains(got, want) {
		t.Fatalf("caption = %q, want it to contain %q", got, want)
	}
	if !strings.Contains(got, "17 фото") {
		t.Fatalf("caption = %q, want photo counter", got)
	}
	if empty := buildMediaAlbumCaption(1, "", "", "instagram"); !strings.Contains(empty, "1 фото") {
		t.Fatalf("caption = %q, want photo counter", empty)
	}
}
