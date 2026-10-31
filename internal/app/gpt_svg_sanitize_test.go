package app

import (
	"strings"
	"testing"
)

func TestSanitizeGPTSVGForRender(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		want    []string
		notWant []string
	}{
		{
			name:    "script removed",
			input:   `<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script><rect width="10" height="10"/></svg>`,
			want:    []string{"<rect"},
			notWant: []string{"script", "alert"},
		},
		{
			name:    "foreignObject removed",
			input:   `<svg xmlns="http://www.w3.org/2000/svg"><foreignObject><div>x</div></foreignObject></svg>`,
			want:    []string{"<svg"},
			notWant: []string{"foreignObject", "<div"},
		},
		{
			name:    "external image href dropped",
			input:   `<svg xmlns="http://www.w3.org/2000/svg"><image href="http://evil.example/a.png"/></svg>`,
			want:    []string{`href=""`},
			notWant: []string{"evil.example"},
		},
		{
			name:    "xlink external href dropped",
			input:   `<svg xmlns:xlink="http://www.w3.org/1999/xlink"><use xlink:href="https://evil.example/x.svg#id"/></svg>`,
			want:    []string{`xlink:href=""`},
			notWant: []string{"evil.example"},
		},
		{
			name:    "internal fragment kept",
			input:   `<svg xmlns="http://www.w3.org/2000/svg"><defs><linearGradient id="g"/></defs><rect fill="url(#g)" width="5" height="5"/></svg>`,
			want:    []string{"url(#g)", `id="g"`},
			notWant: []string{"none"},
		},
		{
			name:    "css import removed",
			input:   `<svg xmlns="http://www.w3.org/2000/svg"><style>@import "http://evil.example/x.css"; rect{fill:red}</style><rect width="5" height="5"/></svg>`,
			want:    []string{"rect{fill:red}"},
			notWant: []string{"evil.example", "@import"},
		},
		{
			name:    "css external url replaced",
			input:   `<svg xmlns="http://www.w3.org/2000/svg"><rect style="fill:url(http://evil.example/x.svg#g)" width="5" height="5"/></svg>`,
			want:    []string{"none"},
			notWant: []string{"evil.example"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sanitizeGPTSVGForRender(tc.input)
			for _, want := range tc.want {
				if !strings.Contains(got, want) {
					t.Fatalf("expected %q in %q", want, got)
				}
			}
			for _, bad := range tc.notWant {
				if strings.Contains(got, bad) {
					t.Fatalf("did not expect %q in %q", bad, got)
				}
			}
		})
	}
}
