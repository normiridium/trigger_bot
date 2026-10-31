package app

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func TestSVGRefinePayload(t *testing.T) {
	t.Setenv("GPT_SVG_REFINE_PROMPT", "")
	svg := "<svg xmlns=\"http://www.w3.org/2000/svg\"><rect width=\"10\" height=\"10\"/></svg>"
	png := []byte{0x89, 'P', 'N', 'G', 1, 2, 3}
	p := svgRefinePayload(svg, png)
	if p["model"] != deepseekMultimodalModel() {
		t.Fatalf("model: %v", p["model"])
	}
	msgs, ok := p["messages"].([]map[string]interface{})
	if !ok || len(msgs) != 1 {
		t.Fatalf("messages: %#v", p["messages"])
	}
	content, ok := msgs[0]["content"].([]map[string]interface{})
	if !ok || len(content) != 2 {
		t.Fatalf("content parts: %#v", content)
	}
	imgURL, ok := content[1]["image_url"].(map[string]string)
	if !ok || !strings.HasPrefix(imgURL["url"], "data:image/png;base64,") {
		t.Fatalf("image_url: %#v", imgURL)
	}
	txt, _ := content[0]["text"].(string)
	if !strings.Contains(txt, svg) {
		t.Fatalf("prompt must carry the svg source, got %q", txt[:80])
	}
}

func TestSVGRefineEffort(t *testing.T) {
	t.Setenv("GPT_SVG_REFINE_EFFORT", "")
	if got := svgRefineEffort(); got != "low" {
		t.Fatalf("default effort: %q", got)
	}
	t.Setenv("GPT_SVG_REFINE_EFFORT", "max")
	if got := svgRefineEffort(); got != "max" {
		t.Fatalf("explicit effort: %q", got)
	}
	t.Setenv("GPT_SVG_REFINE_EFFORT", "bogus")
	if got := svgRefineEffort(); got != "low" {
		t.Fatalf("invalid effort: %q", got)
	}
}

// TestRefineGPTSVGPNGIntegration exercises the full vision loop against the
// live provider. It is skipped unless DEEPSEEK_API_KEY is present.
func TestRefineGPTSVGPNGIntegration(t *testing.T) {
	if os.Getenv("DEEPSEEK_API_KEY") == "" {
		t.Skip("DEEPSEEK_API_KEY not set")
	}
	t.Setenv("GPT_SVG_REFINE_ENABLED", "true")
	t.Setenv("GPT_SVG_REFINE_MAX_ROUNDS", "2")
	svg := `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 400 400">
  <rect x="0" y="0" width="400" height="400" fill="#ffe0b0"/>
  <circle cx="200" cy="200" r="90" fill="#ffd54f"/>
  <circle cx="140" cy="180" r="10" fill="black"/>
  <circle cx="150" cy="180" r="10" fill="black"/>
  <rect x="120" y="260" width="160" height="15" fill="black"/>
</svg>`
	png, tokens, err := refineGPTSVGPNG(svg)
	if err != nil {
		t.Fatalf("refine failed: %v", err)
	}
	t.Logf("vision tokens=%d", tokens)
	if !bytes.HasPrefix(png, []byte{0x89, 'P', 'N', 'G'}) {
		t.Fatalf("result is not a png")
	}
	t.Logf("final png size=%d", len(png))
}
