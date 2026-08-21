package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image/png"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"trigger-admin-bot/internal/bottmp"
)

var (
	gptResponseSVGRe            = regexp.MustCompile(`(?is)<svg\b[^>]*>.*?</svg>`)
	gptSVGCommentRe             = regexp.MustCompile(`(?s)<!--.*?-->`)
	gptSVGOpeningTagRe          = regexp.MustCompile(`(?is)<svg\b([^>]*)>`)
	gptSVGDimAttrRe             = regexp.MustCompile(`(?i)\b(width|height)\s*=\s*["']\s*([^"']+)\s*["']`)
	gptSVGViewBoxRe             = regexp.MustCompile(`(?i)\bviewBox\s*=\s*["']\s*[-+0-9.,]+\s+[-+0-9.,]+\s+([-+0-9.,]+)\s+([-+0-9.,]+)\s*["']`)
	gptSVGAllowedNamespaceURIRe = regexp.MustCompile(`(?is)\s+xmlns(?::[a-z0-9_-]+)?\s*=\s*["']https?://www\.w3\.org/[^"']*["']`)
	gptSVGForbiddenTagRe        = regexp.MustCompile(`(?is)<\s*/?\s*(script|foreignObject|iframe|object|embed|audio|video|canvas|link|meta)\b`)
	gptSVGForbiddenAttrRe       = regexp.MustCompile(`(?is)\s(on[a-z]+|src)\s*=`)
	gptSVGForbiddenURIRe        = regexp.MustCompile(`(?is)(javascript:|data:|file:|blob:|@import\b|https?://)`)
	gptSVGURLFuncRe             = regexp.MustCompile(`(?is)url\s*\(\s*['"]?([^'")\s]+)`)
)

func extractFirstSVGFromGPTResponse(text string) (string, bool) {
	match := strings.TrimSpace(gptResponseSVGRe.FindString(text))
	if match == "" {
		return "", false
	}
	return match, true
}

func renderGPTSVGResponseToPNG(svgSource string) ([]byte, error) {
	svgSource = normalizeGPTSVGForRender(svgSource)
	if err := validateGPTSVGResponse(svgSource); err != nil {
		return nil, err
	}
	width, height := gptSVGRenderSize(svgSource)

	rsvgBin, err := systemDiagramBin("TRIGGER_BOT_RSVG_CONVERT_BIN", "rsvg-convert")
	if err != nil {
		return nil, err
	}
	workDir, err := bottmp.MkdirTemp("gpt-svg-render-*")
	if err != nil {
		return nil, fmt.Errorf("create SVG render temp dir: %w", err)
	}
	defer os.RemoveAll(workDir)

	svgPath := filepath.Join(workDir, "input.svg")
	outPath := filepath.Join(workDir, "svg.png")
	if err := os.WriteFile(svgPath, []byte(svgSource), 0o600); err != nil {
		return nil, fmt.Errorf("write SVG input: %w", err)
	}

	timeout := time.Duration(envInt("GPT_SVG_RENDER_TIMEOUT_SEC", 15)) * time.Second
	if timeout < 3*time.Second {
		timeout = 3 * time.Second
	}
	if timeout > 60*time.Second {
		timeout = 60 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, rsvgBin,
		"--format", "png",
		"--background-color", "white",
		"--width", strconv.Itoa(width),
		"--height", strconv.Itoa(height),
		"--output", outPath,
		svgPath,
	)
	cmd.Dir = workDir
	cmd.Env = richArticleRendererEnv(workDir)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return nil, fmt.Errorf("SVG renderer timed out after %s", timeout)
		}
		return nil, fmt.Errorf("SVG renderer failed: %w: %s", err, clipText(stderr.String(), 900))
	}
	pngBytes, err := os.ReadFile(outPath)
	if err != nil {
		return nil, fmt.Errorf("read rendered SVG PNG: %w", err)
	}
	if !isPNGBytes(pngBytes) {
		return nil, fmt.Errorf("SVG renderer returned non-png output (%d bytes)", len(pngBytes))
	}
	if _, err := png.DecodeConfig(bytes.NewReader(pngBytes)); err != nil {
		return nil, fmt.Errorf("decode rendered SVG PNG: %w", err)
	}
	return pngBytes, nil
}

func normalizeGPTSVGForRender(svgSource string) string {
	svgSource = strings.TrimSpace(svgSource)
	if svgSource == "" {
		return ""
	}
	return strings.TrimSpace(gptSVGCommentRe.ReplaceAllString(svgSource, ""))
}

func validateGPTSVGResponse(svgSource string) error {
	if strings.TrimSpace(svgSource) == "" {
		return errors.New("SVG response is empty")
	}
	maxBytes := envInt("GPT_SVG_RENDER_MAX_BYTES", 256*1024)
	if maxBytes < 4096 {
		maxBytes = 4096
	}
	if len([]byte(svgSource)) > maxBytes {
		return errors.New("SVG response is too large")
	}
	if !strings.Contains(strings.ToLower(svgSource), "<svg") {
		return errors.New("SVG response does not contain svg root")
	}
	if gptSVGForbiddenTagRe.MatchString(svgSource) {
		return errors.New("SVG contains forbidden executable/media tag")
	}
	if gptSVGForbiddenAttrRe.MatchString(svgSource) {
		return errors.New("SVG contains forbidden external/event attribute")
	}
	withoutNamespaces := gptSVGAllowedNamespaceURIRe.ReplaceAllString(svgSource, "")
	if gptSVGForbiddenURIRe.MatchString(withoutNamespaces) {
		return errors.New("SVG contains forbidden external URI")
	}
	for _, sub := range gptSVGURLFuncRe.FindAllStringSubmatch(withoutNamespaces, -1) {
		if len(sub) >= 2 && !strings.HasPrefix(strings.TrimSpace(sub[1]), "#") {
			return errors.New("SVG contains forbidden external URI")
		}
	}
	return nil
}

func gptSVGRenderSize(svgSource string) (int, int) {
	const defaultW = 768
	const defaultH = 512
	width := 0.0
	height := 0.0
	if m := gptSVGOpeningTagRe.FindStringSubmatch(svgSource); len(m) > 1 {
		attrs := m[1]
		for _, sub := range gptSVGDimAttrRe.FindAllStringSubmatch(attrs, -1) {
			if len(sub) < 3 {
				continue
			}
			v, ok := parseSVGLength(sub[2])
			if !ok {
				continue
			}
			switch strings.ToLower(sub[1]) {
			case "width":
				width = v
			case "height":
				height = v
			}
		}
		if (width <= 0 || height <= 0) && gptSVGViewBoxRe.MatchString(attrs) {
			sub := gptSVGViewBoxRe.FindStringSubmatch(attrs)
			if len(sub) >= 3 {
				if width <= 0 {
					width, _ = strconv.ParseFloat(strings.ReplaceAll(sub[1], ",", "."), 64)
				}
				if height <= 0 {
					height, _ = strconv.ParseFloat(strings.ReplaceAll(sub[2], ",", "."), 64)
				}
			}
		}
	}
	if width <= 0 {
		width = defaultW
	}
	if height <= 0 {
		height = defaultH
	}
	maxDim := envInt("GPT_SVG_RENDER_MAX_DIM", 2048)
	if maxDim < 256 {
		maxDim = 256
	}
	if maxDim > 4096 {
		maxDim = 4096
	}
	minDim := 64.0
	scale := math.Min(float64(maxDim)/width, float64(maxDim)/height)
	if scale < 1 {
		width *= scale
		height *= scale
	}
	if width < minDim {
		width = minDim
	}
	if height < minDim {
		height = minDim
	}
	return int(math.Ceil(width)), int(math.Ceil(height))
}

func parseSVGLength(raw string) (float64, bool) {
	s := strings.TrimSpace(strings.ToLower(raw))
	if s == "" || strings.Contains(s, "%") {
		return 0, false
	}
	for _, unit := range []string{"px", "pt", "mm", "cm", "in"} {
		s = strings.TrimSuffix(s, unit)
	}
	v, err := strconv.ParseFloat(strings.ReplaceAll(strings.TrimSpace(s), ",", "."), 64)
	if err != nil || v <= 0 {
		return 0, false
	}
	return v, true
}
