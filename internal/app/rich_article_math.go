package app

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	stddraw "image/draw"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	xdraw "golang.org/x/image/draw"
)

type richArticleRenderedAttachment struct {
	ID       string
	FileName string
	PNG      []byte
	Document bool
}

type richArticleFenceRange struct {
	Start int
	End   int
	Lang  string
	Body  string
}

var richArticleFenceBlockRe = regexp.MustCompile("(?ms)^```[ \t]*([[:alnum:]_-]*)[^\\n]*\\n(.*?)\\n```[ \t]*$")
var richArticleMathBlockRe = regexp.MustCompile("(?s)(\\\\\\[(.*?)\\\\\\]|\\$\\$(.*?)\\$\\$|```math\\s*\\n(.*?)\\n```)")
var richArticleDiagramBlockRe = regexp.MustCompile("(?is)```\\s*(mermaid|mmd|dot|graphviz)\\s*\\n(.*?)\\n```")
var richArticleUnsupportedLatexRe = regexp.MustCompile(`(?is)\\begin\{(array|cases|aligned|gathered|matrix|pmatrix|bmatrix|vmatrix|Vmatrix|smallmatrix|split|tikzpicture|tikzcd|forest|scope)\}|\\Tree\b|\\(boxed|ce|substack|textbf)\b`)
var richArticleStandaloneLatexEnvRe = regexp.MustCompile(`(?is)^\\begin\{(align\*?|alignat\*?|flalign\*?|gather\*?|multline\*?|equation\*?|tikzpicture|tikzcd|forest)\}`)
var richArticleTGPhotoMarkdownLinkRe = regexp.MustCompile(`!?\[[^\]\n]*\]\(\s*tg://photo\?id=([A-Za-z0-9_.:-]+)\s*\)`)
var richArticleTGPhotoPlainURLRe = regexp.MustCompile(`tg://photo\?id=([A-Za-z0-9_.:-]+)`)
var richArticleLatexForbiddenCommandRe = regexp.MustCompile(`(?i)\\(?:catcode|csname|def|directlua|documentclass|edef|every[a-z]*|futurelet|gdef|href|immediate|include|includegraphics|input|jobname|let|lua[a-z]*|newread|newwrite|nolinkurl|openin|openout|pdf[a-z]*|primitive|read|special|url|usepackage|write|xdef)\b`)
var richArticleLatexForbiddenEnvRe = regexp.MustCompile(`(?i)\\(?:begin|end)\s*\{\s*document\s*\}`)
var richArticleExternalURIRe = regexp.MustCompile(`(?i)(?:https?|file|data|javascript)://|javascript:`)
var richArticleMermaidForbiddenRe = regexp.MustCompile(`(?is)<\s*/?\s*(script|img|iframe|object|embed|link|style|svg|math)\b|\bclick\s+\S+\s+(?:href|call)\b|['"]?securityLevel['"]?\s*:`)
var richArticleGraphvizForbiddenAttrRe = regexp.MustCompile(`(?i)\b(?:href|image|imagepath|shapefile|stylesheet|target|tooltip|URL)\s*=`)
var richArticleFormulaNonce atomic.Uint64
var errRichArticleLatexRendererUnavailable = errors.New("system LaTeX renderer unavailable")
var errRichArticleDiagramRendererUnavailable = errors.New("system diagram renderer unavailable")
var errRichArticleUnsafeInput = errors.New("unsafe rich article renderer input")

const (
	telegramPhotoMaxBytes        = 10 * 1024 * 1024
	telegramPhotoMaxDimensionSum = 10000
	telegramPhotoMaxAspectRatio  = 20.0

	// Telegram technically accepts some very wide images as photos, but chat
	// clients squash them into unreadable strips. Keep large diagrams as files.
	richArticleReadablePhotoMaxWidth  = 4096
	richArticleReadablePhotoMaxHeight = 4096

	// Telegram rich articles render inline media at the card width. Keep our
	// formula/diagram PNGs on the same canvas instead of letting clients stretch
	// arbitrary LaTeX bounding boxes.
	defaultRichArticleInlineImageWidth = 573
)

func renderRichArticleMediaBlocks(markdown string) (string, []richArticleRenderedAttachment, error) {
	renderedMarkdown, attachments, err := renderRichArticleFencedMediaBlocks(markdown)
	if err != nil {
		return markdown, nil, err
	}

	renderedMarkdown, mathAttachments, err := renderUnsupportedRichArticleMath(renderedMarkdown)
	if err != nil {
		return markdown, nil, err
	}
	if len(mathAttachments) > 0 {
		attachments = append(attachments, mathAttachments...)
	}

	renderedMarkdown, diagramAttachments, err := renderRichArticleDiagramBlocks(renderedMarkdown)
	if err != nil {
		return markdown, nil, err
	}
	if len(diagramAttachments) > 0 {
		attachments = append(attachments, diagramAttachments...)
	}
	return renderedMarkdown, attachments, nil
}

func renderRichArticleFencedMediaBlocks(markdown string) (string, []richArticleRenderedAttachment, error) {
	if !strings.Contains(markdown, "```") {
		return markdown, nil, nil
	}
	fences := richArticleFencedCodeRanges(markdown)
	if len(fences) == 0 {
		return markdown, nil, nil
	}

	var out strings.Builder
	out.Grow(len(markdown))
	last := 0
	attachments := make([]richArticleRenderedAttachment, 0, len(fences))
	nonce := nextRichArticleFormulaNonce()
	for _, fence := range fences {
		if fence.Start < last || !richArticleFenceLangIsRenderedMedia(fence.Lang) {
			continue
		}
		body := strings.TrimSpace(fence.Body)
		if body == "" {
			continue
		}

		pngBytes, err := renderRichArticleFencePNG(fence.Lang, body)
		if err != nil {
			if errors.Is(err, errRichArticleLatexRendererUnavailable) {
				continue
			}
			return markdown, nil, err
		}
		n := len(attachments) + 1
		id := richArticleRenderedFenceID(fence.Lang, body, n, nonce)
		attachment, err := newRichArticleRenderedAttachment(id, pngBytes)
		if err != nil {
			return markdown, nil, err
		}
		attachments = append(attachments, attachment)
		out.WriteString(markdown[last:fence.Start])
		out.WriteString("\n\n")
		out.WriteString(richArticleRenderedDiagramReference(attachment))
		out.WriteString("\n\n")
		last = fence.End
	}
	if len(attachments) == 0 {
		return markdown, nil, nil
	}
	out.WriteString(markdown[last:])
	return out.String(), attachments, nil
}

func renderUnsupportedRichArticleMath(markdown string) (string, []richArticleRenderedAttachment, error) {
	if !strings.Contains(markdown, `\[`) && !strings.Contains(markdown, "$$") && !strings.Contains(markdown, "```math") {
		return markdown, nil, nil
	}
	matches := richArticleMathBlockRe.FindAllStringSubmatchIndex(markdown, -1)
	if len(matches) == 0 {
		return markdown, nil, nil
	}

	var out strings.Builder
	out.Grow(len(markdown))
	last := 0
	attachments := make([]richArticleRenderedAttachment, 0, len(matches))
	nonce := nextRichArticleFormulaNonce()
	codeFences := richArticleFencedCodeRanges(markdown)
	for _, loc := range matches {
		if len(loc) < 10 || loc[0] < last {
			continue
		}
		full := markdown[loc[0]:loc[1]]
		if richArticleOffsetInNonMathFence(loc[0], codeFences) && !strings.HasPrefix(strings.ToLower(full), "```math") {
			continue
		}
		body := richArticleMathBlockBody(markdown, loc)
		if !shouldRenderRichArticleMathBlock(full, body) {
			continue
		}
		pngBytes, err := renderLatexBlockPNG(body)
		if err != nil {
			if errors.Is(err, errRichArticleLatexRendererUnavailable) {
				continue
			}
			return markdown, nil, err
		}
		n := len(attachments) + 1
		id := richArticleRenderedFormulaID(body, n, nonce)
		attachment, err := newRichArticleRenderedAttachment(id, pngBytes)
		if err != nil {
			return markdown, nil, err
		}
		attachments = append(attachments, attachment)
		out.WriteString(markdown[last:loc[0]])
		out.WriteString("\n\n")
		out.WriteString(richArticleRenderedFormulaReference(attachment))
		out.WriteString("\n\n")
		last = loc[1]
	}
	if len(attachments) == 0 {
		return markdown, nil, nil
	}
	out.WriteString(markdown[last:])
	return out.String(), attachments, nil
}

func richArticleFencedCodeRanges(markdown string) []richArticleFenceRange {
	matches := richArticleFenceBlockRe.FindAllStringSubmatchIndex(markdown, -1)
	if len(matches) == 0 {
		return nil
	}
	ranges := make([]richArticleFenceRange, 0, len(matches))
	for _, loc := range matches {
		if len(loc) < 6 || loc[0] < 0 || loc[1] < loc[0] {
			continue
		}
		lang := ""
		if loc[2] >= 0 && loc[3] >= loc[2] {
			lang = strings.ToLower(strings.TrimSpace(markdown[loc[2]:loc[3]]))
		}
		body := ""
		if loc[4] >= 0 && loc[5] >= loc[4] {
			body = markdown[loc[4]:loc[5]]
		}
		ranges = append(ranges, richArticleFenceRange{
			Start: loc[0],
			End:   loc[1],
			Lang:  lang,
			Body:  body,
		})
	}
	return ranges
}

func richArticleOffsetInNonMathFence(offset int, fences []richArticleFenceRange) bool {
	for _, fence := range fences {
		if offset < fence.Start || offset >= fence.End {
			continue
		}
		return fence.Lang != "math"
	}
	return false
}

func richArticleFenceLangIsRenderedMedia(lang string) bool {
	switch strings.ToLower(strings.TrimSpace(lang)) {
	case "math", "latex", "tex", "mermaid", "mmd", "dot", "graphviz":
		return true
	default:
		return false
	}
}

func renderRichArticleFencePNG(lang, body string) ([]byte, error) {
	switch strings.ToLower(strings.TrimSpace(lang)) {
	case "math", "latex", "tex":
		return renderLatexBlockPNG(body)
	case "mermaid", "mmd", "dot", "graphviz":
		return renderRichArticleDiagramPNG(lang, body)
	default:
		return nil, fmt.Errorf("unsupported rendered fence language %q", lang)
	}
}

func renderRichArticleDiagramBlocks(markdown string) (string, []richArticleRenderedAttachment, error) {
	if !strings.Contains(markdown, "```") {
		return markdown, nil, nil
	}
	matches := richArticleDiagramBlockRe.FindAllStringSubmatchIndex(markdown, -1)
	if len(matches) == 0 {
		return markdown, nil, nil
	}

	var out strings.Builder
	out.Grow(len(markdown))
	last := 0
	attachments := make([]richArticleRenderedAttachment, 0, len(matches))
	nonce := nextRichArticleFormulaNonce()
	for _, loc := range matches {
		if len(loc) < 6 || loc[0] < last {
			continue
		}
		lang := strings.ToLower(strings.TrimSpace(markdown[loc[2]:loc[3]]))
		body := strings.TrimSpace(markdown[loc[4]:loc[5]])
		if body == "" {
			continue
		}
		pngBytes, err := renderRichArticleDiagramPNG(lang, body)
		if err != nil {
			return markdown, nil, err
		}
		n := len(attachments) + 1
		id := richArticleRenderedDiagramID(lang, body, n, nonce)
		attachment, err := newRichArticleRenderedAttachment(id, pngBytes)
		if err != nil {
			return markdown, nil, err
		}
		attachments = append(attachments, attachment)
		out.WriteString(markdown[last:loc[0]])
		out.WriteString("\n\n")
		out.WriteString(richArticleRenderedDiagramReference(attachment))
		out.WriteString("\n\n")
		last = loc[1]
	}
	if len(attachments) == 0 {
		return markdown, nil, nil
	}
	out.WriteString(markdown[last:])
	return out.String(), attachments, nil
}

func renderRichArticleDiagramPNG(lang, src string) ([]byte, error) {
	switch strings.ToLower(strings.TrimSpace(lang)) {
	case "mermaid", "mmd":
		return renderMermaidDiagramPNG(src)
	case "dot", "graphviz":
		return renderGraphvizDiagramPNG(src)
	default:
		return nil, fmt.Errorf("unsupported diagram language %q", lang)
	}
}

func shouldRenderRichArticleMathBlock(full, body string) bool {
	body = strings.TrimSpace(body)
	if body == "" {
		return false
	}
	if strings.HasPrefix(full, `\[`) {
		return true
	}
	return richArticleUnsupportedLatexRe.MatchString(body)
}

func richArticleMathBlockBody(markdown string, loc []int) string {
	for i := 4; i+1 < len(loc); i += 2 {
		if loc[i] >= 0 && loc[i+1] >= loc[i] {
			return markdown[loc[i]:loc[i+1]]
		}
	}
	return ""
}

func renderLatexBlockPNG(src string) ([]byte, error) {
	src = strings.TrimSpace(strings.ReplaceAll(src, "\r\n", "\n"))
	if src == "" {
		return nil, fmt.Errorf("empty formula")
	}
	if err := validateRichArticleLatexSource(src); err != nil {
		return nil, err
	}
	return renderLatexBlockPNGWithSystemLatex(src)
}

func validateRichArticleLatexSource(src string) error {
	if richArticleLatexForbiddenEnvRe.MatchString(src) {
		return fmt.Errorf("%w: LaTeX document environment is not allowed", errRichArticleUnsafeInput)
	}
	if richArticleExternalURIRe.MatchString(src) {
		return fmt.Errorf("%w: external LaTeX URLs are not allowed", errRichArticleUnsafeInput)
	}
	if loc := richArticleLatexForbiddenCommandRe.FindStringIndex(src); loc != nil {
		return fmt.Errorf("%w: forbidden LaTeX command near %q", errRichArticleUnsafeInput, clipText(src[loc[0]:], 80))
	}
	if strings.Contains(src, "^^") {
		return fmt.Errorf("%w: TeX ^^ escape syntax is not allowed", errRichArticleUnsafeInput)
	}
	return nil
}

func renderLatexBlockPNGWithSystemLatex(src string) ([]byte, error) {
	pdflatexBin, err := systemLatexBin("TRIGGER_BOT_PDFLATEX_BIN", "pdflatex")
	if err != nil {
		return nil, err
	}
	pdftocairoBin, err := systemLatexBin("TRIGGER_BOT_PDFTOCAIRO_BIN", "pdftocairo")
	if err != nil {
		return nil, err
	}

	workDir, err := os.MkdirTemp("", "trigger-admin-bot-latex-*")
	if err != nil {
		return nil, fmt.Errorf("create LaTeX temp dir: %w", err)
	}
	defer os.RemoveAll(workDir)

	texPath := filepath.Join(workDir, "formula.tex")
	if err := os.WriteFile(texPath, []byte(buildStandaloneLatexDocument(src)), 0o600); err != nil {
		return nil, fmt.Errorf("write LaTeX document: %w", err)
	}

	timeout := systemLatexRendererTimeout()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, pdflatexBin, "-no-shell-escape", "-interaction=nonstopmode", "-halt-on-error", "-output-directory", workDir, texPath)
	cmd.Dir = workDir
	cmd.Env = richArticleRendererEnv(workDir,
		"openin_any=p",
		"openout_any=p",
		"shell_escape=f",
		"TEXMFOUTPUT="+workDir,
	)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return nil, fmt.Errorf("system LaTeX renderer timed out after %s", timeout)
		}
		return nil, fmt.Errorf("system LaTeX renderer failed: %w: %s", err, clipText(readLatexLog(workDir), 900))
	}

	pdfPath := filepath.Join(workDir, "formula.pdf")
	if _, err := os.Stat(pdfPath); err != nil {
		return nil, fmt.Errorf("system LaTeX renderer did not produce PDF: %w: %s", err, clipText(stdout.String()+stderr.String(), 600))
	}

	pngPrefix := filepath.Join(workDir, "formula")
	cmd = exec.CommandContext(ctx, pdftocairoBin, "-png", "-singlefile", "-r", "180", pdfPath, pngPrefix)
	cmd.Dir = workDir
	cmd.Env = richArticleRendererEnv(workDir)
	stdout.Reset()
	stderr.Reset()
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return nil, fmt.Errorf("system LaTeX PNG conversion timed out after %s", timeout)
		}
		return nil, fmt.Errorf("system LaTeX PNG conversion failed: %w: %s", err, clipText(stderr.String(), 600))
	}

	pngPath := pngPrefix + ".png"
	pngBytes, err := os.ReadFile(pngPath)
	if err != nil {
		return nil, fmt.Errorf("read rendered LaTeX PNG: %w", err)
	}
	if !isPNGBytes(pngBytes) {
		return nil, fmt.Errorf("system LaTeX renderer returned non-png output (%d bytes)", len(pngBytes))
	}
	return append([]byte(nil), pngBytes...), nil
}

func renderMermaidDiagramPNG(src string) ([]byte, error) {
	src = strings.TrimSpace(strings.ReplaceAll(src, "\r\n", "\n"))
	if src == "" {
		return nil, fmt.Errorf("empty Mermaid diagram")
	}
	if err := validateRichArticleMermaidSource(src); err != nil {
		return nil, err
	}
	mmdcBin, err := systemDiagramBin("TRIGGER_BOT_MERMAID_BIN", "mmdc")
	if err != nil {
		return nil, err
	}
	chromiumBin, err := systemDiagramBin("TRIGGER_BOT_CHROMIUM_BIN", "chromium")
	if err != nil {
		return nil, err
	}

	workDir, err := os.MkdirTemp("", "trigger-admin-bot-mermaid-*")
	if err != nil {
		return nil, fmt.Errorf("create Mermaid temp dir: %w", err)
	}
	defer os.RemoveAll(workDir)

	inputPath := filepath.Join(workDir, "diagram.mmd")
	outputPath := filepath.Join(workDir, "diagram.png")
	configPath := filepath.Join(workDir, "puppeteer.json")
	if err := os.WriteFile(inputPath, []byte(src), 0o600); err != nil {
		return nil, fmt.Errorf("write Mermaid input: %w", err)
	}
	configBytes, err := json.Marshal(struct {
		ExecutablePath string   `json:"executablePath"`
		Args           []string `json:"args"`
	}{
		ExecutablePath: chromiumBin,
		Args: []string{
			"--no-sandbox",
			"--disable-setuid-sandbox",
			"--disable-dev-shm-usage",
			"--disable-gpu",
			"--disable-extensions",
			"--disable-background-networking",
			"--disable-sync",
			"--disable-default-apps",
			"--disable-component-update",
			"--no-first-run",
			"--user-data-dir=" + filepath.Join(workDir, "chromium-profile"),
			"--host-resolver-rules=MAP * 0.0.0.0,EXCLUDE localhost",
		},
	})
	if err != nil {
		return nil, fmt.Errorf("build Mermaid puppeteer config: %w", err)
	}
	if err := os.WriteFile(configPath, configBytes, 0o600); err != nil {
		return nil, fmt.Errorf("write Mermaid puppeteer config: %w", err)
	}

	timeout := systemDiagramRendererTimeout()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, mmdcBin, "-p", configPath, "-i", inputPath, "-o", outputPath, "-b", "white")
	cmd.Dir = workDir
	cmd.Env = richArticleRendererEnv(workDir)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return nil, fmt.Errorf("Mermaid renderer timed out after %s", timeout)
		}
		return nil, fmt.Errorf("Mermaid renderer failed: %w: %s", err, clipText(stdout.String()+stderr.String(), 900))
	}

	pngBytes, err := os.ReadFile(outputPath)
	if err != nil {
		return nil, fmt.Errorf("read rendered Mermaid PNG: %w", err)
	}
	if !isPNGBytes(pngBytes) {
		return nil, fmt.Errorf("Mermaid renderer returned non-png output (%d bytes)", len(pngBytes))
	}
	return append([]byte(nil), pngBytes...), nil
}

func renderGraphvizDiagramPNG(src string) ([]byte, error) {
	src = strings.TrimSpace(strings.ReplaceAll(src, "\r\n", "\n"))
	if src == "" {
		return nil, fmt.Errorf("empty Graphviz diagram")
	}
	if err := validateRichArticleGraphvizSource(src); err != nil {
		return nil, err
	}
	dotBin, err := systemDiagramBin("TRIGGER_BOT_GRAPHVIZ_BIN", "dot")
	if err != nil {
		return nil, err
	}

	workDir, err := os.MkdirTemp("", "trigger-admin-bot-graphviz-*")
	if err != nil {
		return nil, fmt.Errorf("create Graphviz temp dir: %w", err)
	}
	defer os.RemoveAll(workDir)

	inputPath := filepath.Join(workDir, "diagram.dot")
	outputPath := filepath.Join(workDir, "diagram.png")
	if err := os.WriteFile(inputPath, []byte(src), 0o600); err != nil {
		return nil, fmt.Errorf("write Graphviz input: %w", err)
	}

	timeout := systemDiagramRendererTimeout()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, dotBin, "-Tpng", inputPath, "-o", outputPath, "-Gbgcolor=white")
	cmd.Dir = workDir
	cmd.Env = richArticleRendererEnv(workDir)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return nil, fmt.Errorf("Graphviz renderer timed out after %s", timeout)
		}
		return nil, fmt.Errorf("Graphviz renderer failed: %w: %s", err, clipText(stdout.String()+stderr.String(), 900))
	}

	pngBytes, err := os.ReadFile(outputPath)
	if err != nil {
		return nil, fmt.Errorf("read rendered Graphviz PNG: %w", err)
	}
	if !isPNGBytes(pngBytes) {
		return nil, fmt.Errorf("Graphviz renderer returned non-png output (%d bytes)", len(pngBytes))
	}
	return append([]byte(nil), pngBytes...), nil
}

func validateRichArticleMermaidSource(src string) error {
	if richArticleExternalURIRe.MatchString(src) {
		return fmt.Errorf("%w: external Mermaid URLs are not allowed", errRichArticleUnsafeInput)
	}
	if loc := richArticleMermaidForbiddenRe.FindStringIndex(src); loc != nil {
		return fmt.Errorf("%w: forbidden Mermaid syntax near %q", errRichArticleUnsafeInput, clipText(src[loc[0]:], 80))
	}
	return nil
}

func validateRichArticleGraphvizSource(src string) error {
	if richArticleExternalURIRe.MatchString(src) {
		return fmt.Errorf("%w: external Graphviz URLs are not allowed", errRichArticleUnsafeInput)
	}
	if loc := richArticleGraphvizForbiddenAttrRe.FindStringIndex(src); loc != nil {
		return fmt.Errorf("%w: forbidden Graphviz attribute near %q", errRichArticleUnsafeInput, clipText(src[loc[0]:], 80))
	}
	return nil
}

func richArticleRendererEnv(workDir string, extra ...string) []string {
	env := os.Environ()
	env = append(env,
		"HOME="+workDir,
		"TMPDIR="+workDir,
		"TEMP="+workDir,
		"TMP="+workDir,
	)
	env = append(env, extra...)
	return env
}

func systemLatexBin(envName, defaultName string) (string, error) {
	raw := strings.TrimSpace(os.Getenv(envName))
	if raw != "" {
		if filepath.IsAbs(raw) {
			if st, err := os.Stat(raw); err == nil && !st.IsDir() {
				return raw, nil
			}
			return "", fmt.Errorf("%w: %s is not available", errRichArticleLatexRendererUnavailable, envName)
		}
		if path, err := exec.LookPath(raw); err == nil {
			return path, nil
		}
		return "", fmt.Errorf("%w: %s=%s not found in PATH", errRichArticleLatexRendererUnavailable, envName, raw)
	}
	path, err := exec.LookPath(defaultName)
	if err != nil {
		return "", fmt.Errorf("%w: %s not found in PATH", errRichArticleLatexRendererUnavailable, defaultName)
	}
	return path, nil
}

func systemLatexRendererAvailable() bool {
	if _, err := systemLatexBin("TRIGGER_BOT_PDFLATEX_BIN", "pdflatex"); err != nil {
		return false
	}
	if _, err := systemLatexBin("TRIGGER_BOT_PDFTOCAIRO_BIN", "pdftocairo"); err != nil {
		return false
	}
	return true
}

func systemDiagramBin(envName, defaultName string) (string, error) {
	raw := strings.TrimSpace(os.Getenv(envName))
	if raw != "" {
		if filepath.IsAbs(raw) {
			if st, err := os.Stat(raw); err == nil && !st.IsDir() {
				return raw, nil
			}
			return "", fmt.Errorf("%w: %s is not available", errRichArticleDiagramRendererUnavailable, envName)
		}
		if path, err := exec.LookPath(raw); err == nil {
			return path, nil
		}
		return "", fmt.Errorf("%w: %s=%s not found in PATH", errRichArticleDiagramRendererUnavailable, envName, raw)
	}
	path, err := exec.LookPath(defaultName)
	if err != nil {
		return "", fmt.Errorf("%w: %s not found in PATH", errRichArticleDiagramRendererUnavailable, defaultName)
	}
	return path, nil
}

func systemMermaidRendererAvailable() bool {
	if _, err := systemDiagramBin("TRIGGER_BOT_MERMAID_BIN", "mmdc"); err != nil {
		return false
	}
	if _, err := systemDiagramBin("TRIGGER_BOT_CHROMIUM_BIN", "chromium"); err != nil {
		return false
	}
	return true
}

func systemGraphvizRendererAvailable() bool {
	if _, err := systemDiagramBin("TRIGGER_BOT_GRAPHVIZ_BIN", "dot"); err != nil {
		return false
	}
	return true
}

func systemLatexRendererTimeout() time.Duration {
	raw := strings.TrimSpace(os.Getenv("TRIGGER_BOT_LATEX_TIMEOUT_MS"))
	if raw == "" {
		return 15 * time.Second
	}
	ms, err := strconv.Atoi(raw)
	if err != nil || ms <= 0 {
		return 15 * time.Second
	}
	return time.Duration(ms) * time.Millisecond
}

func systemDiagramRendererTimeout() time.Duration {
	raw := strings.TrimSpace(os.Getenv("TRIGGER_BOT_DIAGRAM_TIMEOUT_MS"))
	if raw == "" {
		return 20 * time.Second
	}
	ms, err := strconv.Atoi(raw)
	if err != nil || ms <= 0 {
		return 20 * time.Second
	}
	return time.Duration(ms) * time.Millisecond
}

func buildStandaloneLatexDocument(src string) string {
	body := strings.TrimSpace(src)
	if !latexBodyCanStandAlone(body) {
		body = "\\[\n" + body + "\n\\]"
	}
	return `\documentclass[varwidth,border=8pt]{standalone}
\usepackage[T2A]{fontenc}
\usepackage[utf8]{inputenc}
\usepackage[english,russian]{babel}
\usepackage{amsmath,amssymb,mathtools}
\usepackage{xcolor}
\usepackage{chemfig}
\usepackage[version=4]{mhchem}
\usepackage{tikz}
\usepackage{pgfplots}
\usepackage{tikz-cd}
\usepackage{tikz-qtree}
\usepackage{forest}
\pgfplotsset{compat=1.18}
\usetikzlibrary{arrows.meta,positioning,calc,trees,matrix,decorations.pathmorphing,shapes.geometric}
\pagecolor[HTML]{FFFFFF}
\color[HTML]{111111}
\begin{document}
` + body + `
\end{document}
`
}

func latexBodyCanStandAlone(body string) bool {
	body = strings.TrimSpace(body)
	if strings.HasPrefix(body, `\[`) || strings.HasPrefix(body, "$$") || strings.Contains(body, `\[`) || strings.Contains(body, "$$") {
		return true
	}
	if richArticleStandaloneLatexEnvRe.MatchString(body) {
		return true
	}
	for _, prefix := range []string{
		`\Tree`,
	} {
		if strings.HasPrefix(body, prefix) {
			return true
		}
	}
	return false
}

func readLatexLog(workDir string) string {
	for _, name := range []string{"formula.log", "formula.out"} {
		b, err := os.ReadFile(filepath.Join(workDir, name))
		if err == nil && len(b) > 0 {
			return string(b)
		}
	}
	return ""
}

func richArticleRenderedFormulaMarkdown(id string) string {
	return fmt.Sprintf(`![](tg://photo?id=%s)`, id)
}

func richArticleRenderedFormulaReference(attachment richArticleRenderedAttachment) string {
	if attachment.Document {
		return fmt.Sprintf("Формула слишком большая для встроенной картинки, PNG прикреплён файлом: `%s`.", attachment.FileName)
	}
	return richArticleRenderedFormulaMarkdown(attachment.ID)
}

func richArticleRenderedDiagramReference(attachment richArticleRenderedAttachment) string {
	if attachment.Document {
		return fmt.Sprintf("Диаграмма слишком большая для встроенной картинки, PNG прикреплён файлом: `%s`.", attachment.FileName)
	}
	return richArticleRenderedFormulaMarkdown(attachment.ID)
}

func sanitizeRichArticlePhotoLinks(markdown string, attachments []richArticleRenderedAttachment) string {
	if !strings.Contains(markdown, "tg://photo?id=") {
		return markdown
	}
	allowed := richArticleAllowedPhotoIDs(attachments)
	fences := richArticleFencedCodeRanges(markdown)
	if len(fences) == 0 {
		return sanitizeRichArticlePhotoLinksOutsideFence(markdown, allowed)
	}

	var out strings.Builder
	out.Grow(len(markdown))
	last := 0
	for _, fence := range fences {
		if fence.Start < last {
			continue
		}
		out.WriteString(sanitizeRichArticlePhotoLinksOutsideFence(markdown[last:fence.Start], allowed))
		out.WriteString(markdown[fence.Start:fence.End])
		last = fence.End
	}
	out.WriteString(sanitizeRichArticlePhotoLinksOutsideFence(markdown[last:], allowed))
	return out.String()
}

func richArticleAllowedPhotoIDs(attachments []richArticleRenderedAttachment) map[string]struct{} {
	allowed := make(map[string]struct{}, len(attachments))
	for _, attachment := range attachments {
		id := strings.TrimSpace(attachment.ID)
		if id == "" || attachment.Document {
			continue
		}
		allowed[id] = struct{}{}
	}
	return allowed
}

func sanitizeRichArticlePhotoLinksOutsideFence(markdown string, allowed map[string]struct{}) string {
	if !strings.Contains(markdown, "tg://photo?id=") {
		return markdown
	}

	matches := richArticleTGPhotoMarkdownLinkRe.FindAllStringSubmatchIndex(markdown, -1)
	if len(matches) == 0 {
		return sanitizeRichArticlePlainPhotoURLs(markdown, allowed)
	}

	var out strings.Builder
	out.Grow(len(markdown))
	last := 0
	for _, loc := range matches {
		if len(loc) < 4 || loc[0] < last {
			continue
		}
		out.WriteString(sanitizeRichArticlePlainPhotoURLs(markdown[last:loc[0]], allowed))
		full := markdown[loc[0]:loc[1]]
		id := markdown[loc[2]:loc[3]]
		if _, ok := allowed[id]; ok {
			out.WriteString(full)
		} else {
			out.WriteString(richArticleInlineCodeLiteral(full))
		}
		last = loc[1]
	}
	out.WriteString(sanitizeRichArticlePlainPhotoURLs(markdown[last:], allowed))
	return out.String()
}

func sanitizeRichArticlePlainPhotoURLs(markdown string, allowed map[string]struct{}) string {
	if !strings.Contains(markdown, "tg://photo?id=") {
		return markdown
	}
	return richArticleTGPhotoPlainURLRe.ReplaceAllStringFunc(markdown, func(match string) string {
		sub := richArticleTGPhotoPlainURLRe.FindStringSubmatch(match)
		if len(sub) == 2 {
			if _, ok := allowed[sub[1]]; ok {
				return match
			}
		}
		return richArticleInlineCodeLiteral(match)
	})
}

func richArticleInlineCodeLiteral(s string) string {
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "`", "'")
	return "`" + s + "`"
}

func newRichArticleRenderedAttachment(id string, pngBytes []byte) (richArticleRenderedAttachment, error) {
	attachment := richArticleRenderedAttachment{
		ID:       id,
		FileName: id + ".png",
		PNG:      pngBytes,
		Document: richArticleFormulaShouldAttachAsDocument(pngBytes),
	}
	if attachment.Document {
		return attachment, nil
	}

	normalized, err := normalizeRichArticleInlineImagePNG(pngBytes)
	if err != nil {
		return richArticleRenderedAttachment{}, err
	}
	attachment.PNG = normalized
	return attachment, nil
}

func normalizeRichArticleInlineImagePNG(pngBytes []byte) ([]byte, error) {
	img, err := png.Decode(bytes.NewReader(pngBytes))
	if err != nil {
		return nil, err
	}
	srcBounds := img.Bounds()
	srcW := srcBounds.Dx()
	srcH := srcBounds.Dy()
	if srcW <= 0 || srcH <= 0 {
		return nil, fmt.Errorf("invalid rich article image size %dx%d", srcW, srcH)
	}

	targetW := richArticleInlineImageWidth()
	targetH := srcH
	drawW := srcW
	drawH := srcH
	if srcW > targetW {
		drawW = targetW
		drawH = (srcH*targetW + srcW/2) / srcW
		if drawH < 1 {
			drawH = 1
		}
		targetH = drawH
	}

	canvas := image.NewRGBA(image.Rect(0, 0, targetW, targetH))
	stddraw.Draw(canvas, canvas.Bounds(), image.NewUniform(color.White), image.Point{}, stddraw.Src)
	dst := image.Rect((targetW-drawW)/2, 0, (targetW-drawW)/2+drawW, drawH)
	if drawW == srcW && drawH == srcH {
		stddraw.Draw(canvas, dst, img, srcBounds.Min, stddraw.Over)
	} else {
		xdraw.CatmullRom.Scale(canvas, dst, img, srcBounds, stddraw.Over, nil)
	}

	var out bytes.Buffer
	if err := png.Encode(&out, canvas); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func richArticleInlineImageWidth() int {
	width := envInt("TRIGGER_BOT_RICH_ARTICLE_IMAGE_WIDTH", defaultRichArticleInlineImageWidth)
	if width < 160 {
		return 160
	}
	if width > richArticleReadablePhotoMaxWidth {
		return richArticleReadablePhotoMaxWidth
	}
	return width
}

func richArticleFormulaShouldAttachAsDocument(pngBytes []byte) bool {
	if len(pngBytes) > telegramPhotoMaxBytes {
		return true
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(pngBytes))
	if err != nil {
		return false
	}
	return richArticleFormulaDimensionsNeedDocument(cfg.Width, cfg.Height, len(pngBytes))
}

func richArticleFormulaDimensionsNeedDocument(width, height, bytesLen int) bool {
	if bytesLen > telegramPhotoMaxBytes {
		return true
	}
	if width <= 0 || height <= 0 {
		return false
	}
	if width+height > telegramPhotoMaxDimensionSum {
		return true
	}
	if richArticleAspectRatio(width, height) > telegramPhotoMaxAspectRatio {
		return true
	}
	if width > richArticleReadablePhotoMaxWidth || height > richArticleReadablePhotoMaxHeight {
		return true
	}
	return false
}

func richArticleAspectRatio(width, height int) float64 {
	if width <= 0 || height <= 0 {
		return 1
	}
	if width > height {
		return float64(width) / float64(height)
	}
	return float64(height) / float64(width)
}

func richArticleRenderedFormulaID(body string, index int, nonce string) string {
	sum := sha1.Sum([]byte(body))
	hash := hex.EncodeToString(sum[:])[:10]
	if nonce == "" {
		nonce = nextRichArticleFormulaNonce()
	}
	return fmt.Sprintf("formula_%s_%d_%s", nonce, index, hash)
}

func richArticleRenderedDiagramID(lang, body string, index int, nonce string) string {
	sum := sha1.Sum([]byte(lang + "\n" + body))
	hash := hex.EncodeToString(sum[:])[:10]
	if nonce == "" {
		nonce = nextRichArticleFormulaNonce()
	}
	return fmt.Sprintf("diagram_%s_%d_%s", nonce, index, hash)
}

func richArticleRenderedFenceID(lang, body string, index int, nonce string) string {
	sum := sha1.Sum([]byte("fence\n" + lang + "\n" + body))
	hash := hex.EncodeToString(sum[:])[:10]
	if nonce == "" {
		nonce = nextRichArticleFormulaNonce()
	}
	return fmt.Sprintf("rendered_%s_%d_%s", nonce, index, hash)
}

func nextRichArticleFormulaNonce() string {
	n := richArticleFormulaNonce.Add(1)
	return strconv.FormatInt(time.Now().UnixNano(), 36) + "_" + strconv.FormatUint(n, 36)
}

func isPNGBytes(b []byte) bool {
	return len(b) >= 8 &&
		b[0] == 0x89 &&
		b[1] == 'P' &&
		b[2] == 'N' &&
		b[3] == 'G' &&
		b[4] == '\r' &&
		b[5] == '\n' &&
		b[6] == 0x1a &&
		b[7] == '\n'
}
