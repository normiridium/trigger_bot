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
var richArticleInlineMathRe = regexp.MustCompile(`\$([^$\n]+)\$`)
var richArticleAlternativeMathDelimiterRe = regexp.MustCompile(`(?s)\\\[(.*?)\\\]|\\\((.*?)\\\)`)
var richArticleBareListMarkerRe = regexp.MustCompile(`^(?:[0-9]+[.)]|[-*+])$`)
var richArticleDiagramBlockRe = regexp.MustCompile("(?is)```\\s*(mermaid|mmd|dot|graphviz)\\s*\\n(.*?)\\n```")
var richArticleUnsupportedLatexRe = regexp.MustCompile(`(?is)\\begin\{(array|cases|aligned|gathered|matrix|pmatrix|bmatrix|vmatrix|Vmatrix|smallmatrix|split|tikzpicture|tikzcd|forest|scope)\}|\\Tree\b|\\(boxed|ce|substack|textbf|xrightarrow|xleftarrow|overrightarrow|overleftarrow|rightarrow|leftarrow|leftrightarrow|Rightarrow|Leftarrow|Leftrightarrow|uparrow|downarrow|updownarrow|Uparrow|Downarrow|Updownarrow|swarrow|searrow|nearrow|nwarrow|mapsto)\b`)
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
	markdown = normalizeRichArticleMathDelimiters(markdown)
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

	renderedMarkdown, inlineMathAttachments, err := renderUnsupportedRichArticleInlineMath(renderedMarkdown)
	if err != nil {
		return markdown, nil, err
	}
	if len(inlineMathAttachments) > 0 {
		attachments = append(attachments, inlineMathAttachments...)
	}

	renderedMarkdown, diagramAttachments, err := renderRichArticleDiagramBlocks(renderedMarkdown)
	if err != nil {
		return markdown, nil, err
	}
	if len(diagramAttachments) > 0 {
		attachments = append(attachments, diagramAttachments...)
	}

	renderedMarkdown, fenAttachments, err := renderRichArticleFENLines(renderedMarkdown)
	if err != nil {
		return markdown, nil, err
	}
	if len(fenAttachments) > 0 {
		attachments = append(attachments, fenAttachments...)
	}
	return renderedMarkdown, attachments, nil
}

// Telegram Rich Markdown uses dollar delimiters for native formulas. Models
// also commonly emit the equivalent LaTeX delimiters, so normalize only those
// delimiters while leaving formula bodies and fenced code untouched.
func normalizeRichArticleMathDelimiters(markdown string) string {
	if !strings.Contains(markdown, `\(`) && !strings.Contains(markdown, `\[`) {
		return markdown
	}
	matches := richArticleAlternativeMathDelimiterRe.FindAllStringSubmatchIndex(markdown, -1)
	if len(matches) == 0 {
		return markdown
	}

	fences := richArticleFencedCodeRanges(markdown)
	var out strings.Builder
	out.Grow(len(markdown))
	last := 0
	for _, loc := range matches {
		if len(loc) < 6 || loc[0] < last || richArticleRangeInFence(loc[0], loc[1], fences) {
			continue
		}
		out.WriteString(markdown[last:loc[0]])
		switch {
		case loc[2] >= 0:
			out.WriteString("$$")
			out.WriteString(strings.TrimSpace(markdown[loc[2]:loc[3]]))
			out.WriteString("$$")
		case loc[4] >= 0:
			out.WriteByte('$')
			out.WriteString(strings.TrimSpace(markdown[loc[4]:loc[5]]))
			out.WriteByte('$')
		default:
			out.WriteString(markdown[loc[0]:loc[1]])
		}
		last = loc[1]
	}
	if last == 0 {
		return markdown
	}
	out.WriteString(markdown[last:])
	return out.String()
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

func renderUnsupportedRichArticleInlineMath(markdown string) (string, []richArticleRenderedAttachment, error) {
	if !strings.Contains(markdown, "$") {
		return markdown, nil, nil
	}
	matches := richArticleInlineMathRe.FindAllStringSubmatchIndex(markdown, -1)
	if len(matches) == 0 {
		return markdown, nil, nil
	}

	fences := richArticleFencedCodeRanges(markdown)
	var out strings.Builder
	out.Grow(len(markdown))
	last := 0
	attachments := make([]richArticleRenderedAttachment, 0, len(matches))
	nonce := nextRichArticleFormulaNonce()
	for _, loc := range matches {
		if len(loc) < 4 || loc[0] < last || richArticleRangeInFence(loc[0], loc[1], fences) {
			continue
		}
		// A match inside $$...$$ is a native display formula handled above.
		if (loc[0] > 0 && markdown[loc[0]-1] == '$') || (loc[1] < len(markdown) && markdown[loc[1]] == '$') {
			continue
		}
		body := strings.TrimSpace(markdown[loc[2]:loc[3]])
		if !richArticleUnsupportedLatexRe.MatchString(body) {
			continue
		}
		pngBytes, err := renderLatexBlockPNG(body)
		if err != nil {
			if errors.Is(err, errRichArticleLatexRendererUnavailable) {
				continue
			}
			return markdown, nil, err
		}
		id := richArticleRenderedFormulaID(body, len(attachments)+1, nonce)
		attachment, err := newRichArticleRenderedAttachment(id, pngBytes)
		if err != nil {
			return markdown, nil, err
		}
		attachments = append(attachments, attachment)
		out.WriteString(richArticleInlineMediaPrefix(markdown[last:loc[0]]))
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

func richArticleInlineMediaPrefix(prefix string) string {
	lineStart := strings.LastIndexByte(prefix, '\n') + 1
	linePrefix := prefix[lineStart:]
	marker := strings.TrimSpace(linePrefix)
	if !richArticleBareListMarkerRe.MatchString(marker) {
		return prefix
	}
	// Rich Markdown media must be a separate block. Keeping a bare list marker
	// before it creates an empty list item in which Telegram drops the image.
	return prefix[:lineStart] + "**" + marker + "**"
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
	case "math", "latex", "tex", "mermaid", "mmd", "dot", "graphviz", "fen", "chess", "chessboard":
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
	case "fen", "chess", "chessboard":
		return renderRichArticleFENBoardPNG(body)
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

func renderRichArticleFENLines(markdown string) (string, []richArticleRenderedAttachment, error) {
	if !strings.Contains(markdown, "/") {
		return markdown, nil, nil
	}

	fences := richArticleFencedCodeRanges(markdown)
	var out strings.Builder
	out.Grow(len(markdown))
	last := 0
	pos := 0
	attachments := []richArticleRenderedAttachment{}
	nonce := nextRichArticleFormulaNonce()
	for pos <= len(markdown) {
		lineStart := pos
		nextNewline := strings.IndexByte(markdown[pos:], '\n')
		lineEnd := len(markdown)
		if nextNewline >= 0 {
			lineEnd = pos + nextNewline
		}
		if !richArticleRangeInFence(lineStart, lineEnd, fences) {
			prefix, fen, ok := richArticleFENLineCandidate(markdown[lineStart:lineEnd])
			if ok {
				pngBytes, err := renderRichArticleFENBoardPNG(fen)
				if err != nil {
					return markdown, nil, err
				}
				n := len(attachments) + 1
				id := richArticleRenderedFenceID("fen-line", fen, n, nonce)
				attachment, err := newRichArticleRenderedAttachment(id, pngBytes)
				if err != nil {
					return markdown, nil, err
				}
				attachments = append(attachments, attachment)
				out.WriteString(markdown[last:lineStart])
				if prefix != "" {
					out.WriteString(strings.TrimSpace(prefix))
					out.WriteString("\n\n")
				}
				out.WriteString(richArticleRenderedDiagramReference(attachment))
				last = lineEnd
			}
		}
		if nextNewline < 0 {
			break
		}
		pos = lineEnd + 1
	}
	if len(attachments) == 0 {
		return markdown, nil, nil
	}
	out.WriteString(markdown[last:])
	return out.String(), attachments, nil
}

func richArticleRangeInFence(start, end int, fences []richArticleFenceRange) bool {
	for _, fence := range fences {
		if start >= fence.Start && start < fence.End {
			return true
		}
		if end > fence.Start && end <= fence.End {
			return true
		}
	}
	return false
}

func richArticleFENLineCandidate(line string) (prefix, fen string, ok bool) {
	rest := strings.TrimSpace(line)
	if rest == "" || !strings.Contains(rest, "/") {
		return "", "", false
	}
	prefix, rest = richArticleStripFENLinePrefix(rest)
	rest = richArticleStripFENLabel(rest)
	if rest == "" || !strings.Contains(rest, "/") {
		return "", "", false
	}
	if _, err := parseRichArticleFEN(rest); err != nil {
		return "", "", false
	}
	return prefix, rest, true
}

func richArticleStripFENLinePrefix(s string) (prefix, rest string) {
	fields := strings.Fields(s)
	if len(fields) < 2 {
		return "", s
	}
	marker := fields[0]
	if richArticleFENListMarker(marker) {
		return marker, strings.TrimSpace(strings.TrimPrefix(s, marker))
	}
	return "", s
}

func richArticleFENListMarker(s string) bool {
	if s == "-" || s == "*" || s == "•" {
		return true
	}
	if len(s) < 2 {
		return false
	}
	last := s[len(s)-1]
	if last != '.' && last != ')' {
		return false
	}
	for _, ch := range s[:len(s)-1] {
		if ch < '0' || ch > '9' {
			return false
		}
	}
	return true
}

func richArticleStripFENLabel(s string) string {
	lower := strings.ToLower(s)
	for _, label := range []string{"fen", "фен"} {
		if strings.HasPrefix(lower, label+":") {
			return strings.TrimSpace(s[len(label)+1:])
		}
		if strings.HasPrefix(lower, label+" -") {
			return strings.TrimSpace(s[len(label)+2:])
		}
		if strings.HasPrefix(lower, label+" —") {
			return strings.TrimSpace(s[len(label)+len(" —"):])
		}
	}
	return s
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
	_ = full
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

type richArticleFENPosition struct {
	Squares [8][8]rune
}

func renderRichArticleFENBoardPNG(src string) ([]byte, error) {
	return renderRichArticleFENBoardPNGWithOrientation(src, true)
}

func renderRichArticleFENBoardPNGWithOrientation(src string, whiteBottom bool) ([]byte, error) {
	pos, err := parseRichArticleFEN(src)
	if err != nil {
		return nil, err
	}
	if err := loadQuoteFonts(); err != nil {
		return nil, fmt.Errorf("load chess board font: %w", err)
	}

	const (
		canvasW   = 640
		canvasH   = 640
		boardSize = 480
		square    = boardSize / 8
		boardX    = 80
		boardY    = 64
	)

	canvas := image.NewRGBA(image.Rect(0, 0, canvasW, canvasH))
	stddraw.Draw(canvas, canvas.Bounds(), image.NewUniform(color.RGBA{R: 246, G: 241, B: 232, A: 255}), image.Point{}, stddraw.Src)
	fillRoundedRect(canvas, image.Rect(boardX-44, boardY-38, boardX+boardSize+44, boardY+boardSize+38), 18, color.RGBA{R: 214, G: 201, B: 184, A: 255})
	fillRoundedRect(canvas, image.Rect(boardX-38, boardY-32, boardX+boardSize+38, boardY+boardSize+32), 16, color.RGBA{R: 252, G: 249, B: 242, A: 255})
	fillRoundedRect(canvas, image.Rect(boardX-6, boardY-6, boardX+boardSize+6, boardY+boardSize+6), 8, color.RGBA{R: 82, G: 60, B: 42, A: 255})

	light := color.RGBA{R: 238, G: 216, B: 181, A: 255}
	dark := color.RGBA{R: 168, G: 111, B: 68, A: 255}
	for rank := 0; rank < 8; rank++ {
		for file := 0; file < 8; file++ {
			c := light
			if (rank+file)%2 == 1 {
				c = dark
			}
			x := boardX + file*square
			y := boardY + rank*square
			stddraw.Draw(canvas, image.Rect(x, y, x+square, y+square), image.NewUniform(c), image.Point{}, stddraw.Src)
		}
	}

	labelFace, err := quoteFontFace(true, 18)
	if err != nil {
		return nil, fmt.Errorf("create chess coordinate font: %w", err)
	}
	defer closeQuoteFace(labelFace)
	pieceFace, err := quoteFontFace(false, 48)
	if err != nil {
		return nil, fmt.Errorf("create chess piece font: %w", err)
	}
	defer closeQuoteFace(pieceFace)

	labelColor := color.RGBA{R: 93, G: 72, B: 52, A: 255}
	labelMetrics := labelFace.Metrics()
	labelBaselineOffset := (square-labelMetrics.Height.Ceil())/2 + labelMetrics.Ascent.Ceil()
	for i := 0; i < 8; i++ {
		fileIndex := i
		rankIndex := i
		if !whiteBottom {
			fileIndex = 7 - i
			rankIndex = 7 - i
		}
		file := string(rune('a' + fileIndex))
		fileX := boardX + i*square + (square-quoteStringWidth(labelFace, file))/2
		quoteDrawString(canvas, labelFace, file, fileX, boardY-14, labelColor)
		quoteDrawString(canvas, labelFace, file, fileX, boardY+boardSize+28, labelColor)

		rank := strconv.Itoa(8 - rankIndex)
		rankY := boardY + i*square + labelBaselineOffset
		leftRankX := boardX - 24 - quoteStringWidth(labelFace, rank)/2
		rightRankX := boardX + boardSize + 24 - quoteStringWidth(labelFace, rank)/2
		quoteDrawString(canvas, labelFace, rank, leftRankX, rankY, labelColor)
		quoteDrawString(canvas, labelFace, rank, rightRankX, rankY, labelColor)
	}

	metrics := pieceFace.Metrics()
	ascent := metrics.Ascent.Ceil()
	descent := metrics.Descent.Ceil()
	for rank := 0; rank < 8; rank++ {
		for file := 0; file < 8; file++ {
			srcRank := rank
			srcFile := file
			if !whiteBottom {
				srcRank = 7 - rank
				srcFile = 7 - file
			}
			boardPiece := pos.Squares[srcRank][srcFile]
			piece := richArticleChessPieceGlyph(boardPiece)
			if piece == "" {
				continue
			}
			x := boardX + file*square + (square-quoteStringWidth(pieceFace, piece))/2
			y := boardY + rank*square + (square+ascent-descent)/2
			pieceColor := richArticleChessPieceColor(boardPiece)
			shadowColor := color.RGBA{R: 62, G: 45, B: 34, A: 110}
			if boardPiece >= 'A' && boardPiece <= 'Z' {
				outlineColor := color.RGBA{R: 96, G: 72, B: 50, A: 155}
				for _, off := range [][2]int{{-1, -1}, {0, -1}, {1, -1}, {-1, 0}, {1, 0}, {-1, 1}, {0, 1}, {1, 1}} {
					quoteDrawString(canvas, pieceFace, piece, x+off[0], y+off[1], outlineColor)
				}
				shadowColor = color.RGBA{R: 62, G: 45, B: 34, A: 145}
			}
			quoteDrawString(canvas, pieceFace, piece, x+2, y+3, shadowColor)
			quoteDrawString(canvas, pieceFace, piece, x, y, pieceColor)
		}
	}

	var out bytes.Buffer
	if err := png.Encode(&out, canvas); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func parseRichArticleFEN(src string) (richArticleFENPosition, error) {
	src = strings.TrimSpace(strings.ReplaceAll(src, "\r\n", "\n"))
	var pos richArticleFENPosition
	if src == "" {
		return pos, fmt.Errorf("empty FEN position")
	}
	if len([]rune(src)) > 512 {
		return pos, fmt.Errorf("FEN position is too long")
	}
	fields := strings.Fields(src)
	if len(fields) == 0 {
		return pos, fmt.Errorf("empty FEN position")
	}
	if len(fields) > 6 {
		return pos, fmt.Errorf("invalid FEN position: expected at most 6 fields")
	}
	placement := fields[0]
	ranks := strings.Split(placement, "/")
	if len(ranks) != 8 {
		return pos, fmt.Errorf("invalid FEN board: expected 8 ranks")
	}
	for rank, row := range ranks {
		file := 0
		for _, ch := range row {
			if ch >= '1' && ch <= '8' {
				file += int(ch - '0')
				if file > 8 {
					return pos, fmt.Errorf("invalid FEN board: rank %d has too many files", 8-rank)
				}
				continue
			}
			if !richArticleFENPieceAllowed(ch) {
				return pos, fmt.Errorf("invalid FEN board: unsupported piece %q", ch)
			}
			if file >= 8 {
				return pos, fmt.Errorf("invalid FEN board: rank %d has too many files", 8-rank)
			}
			pos.Squares[rank][file] = ch
			file++
		}
		if file != 8 {
			return pos, fmt.Errorf("invalid FEN board: rank %d has %d files", 8-rank, file)
		}
	}
	if len(fields) >= 2 && fields[1] != "w" && fields[1] != "b" {
		return pos, fmt.Errorf("invalid FEN side to move %q", fields[1])
	}
	if len(fields) >= 3 && !richArticleFENCastlingAllowed(fields[2]) {
		return pos, fmt.Errorf("invalid FEN castling field %q", fields[2])
	}
	if len(fields) >= 4 && !richArticleFENEnPassantAllowed(fields[3]) {
		return pos, fmt.Errorf("invalid FEN en passant field %q", fields[3])
	}
	if len(fields) >= 5 {
		if n, err := strconv.Atoi(fields[4]); err != nil || n < 0 {
			return pos, fmt.Errorf("invalid FEN halfmove clock %q", fields[4])
		}
	}
	if len(fields) >= 6 {
		if n, err := strconv.Atoi(fields[5]); err != nil || n <= 0 {
			return pos, fmt.Errorf("invalid FEN fullmove number %q", fields[5])
		}
	}
	return pos, nil
}

func richArticleFENPieceAllowed(ch rune) bool {
	switch ch {
	case 'K', 'Q', 'R', 'B', 'N', 'P', 'k', 'q', 'r', 'b', 'n', 'p':
		return true
	default:
		return false
	}
}

func richArticleFENCastlingAllowed(field string) bool {
	if field == "-" {
		return true
	}
	seen := map[rune]bool{}
	for _, ch := range field {
		if ch != 'K' && ch != 'Q' && ch != 'k' && ch != 'q' {
			return false
		}
		if seen[ch] {
			return false
		}
		seen[ch] = true
	}
	return len(seen) > 0
}

func richArticleFENEnPassantAllowed(field string) bool {
	if field == "-" {
		return true
	}
	if len(field) != 2 {
		return false
	}
	file := field[0]
	rank := field[1]
	return file >= 'a' && file <= 'h' && (rank == '3' || rank == '6')
}

func richArticleChessPieceGlyph(piece rune) string {
	switch piece {
	case 'K':
		return "♔"
	case 'Q':
		return "♕"
	case 'R':
		return "♖"
	case 'B':
		return "♗"
	case 'N':
		return "♘"
	case 'P':
		return "♙"
	case 'k':
		return "♚"
	case 'q':
		return "♛"
	case 'r':
		return "♜"
	case 'b':
		return "♝"
	case 'n':
		return "♞"
	case 'p':
		return "♟"
	default:
		return ""
	}
}

func richArticleChessPieceColor(piece rune) color.Color {
	if piece >= 'A' && piece <= 'Z' {
		return color.RGBA{R: 255, G: 253, B: 245, A: 255}
	}
	return color.RGBA{R: 28, G: 30, B: 36, A: 255}
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
