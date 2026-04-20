package app

import (
	"bytes"
	"fmt"
	htmltmpl "html/template"
	"math/rand"
	"net/url"
	"os"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"trigger-admin-bot/internal/mediadl"
)

func normalizeTelegramLineBreaks(s string) string {
	s = strings.ReplaceAll(s, "\\r\\n", "\n")
	s = strings.ReplaceAll(s, "\\n", "\n")
	s = strings.ReplaceAll(s, "\\r", "\n")
	s = strings.ReplaceAll(s, "<br>", "\n")
	s = strings.ReplaceAll(s, "<br/>", "\n")
	s = strings.ReplaceAll(s, "<br />", "\n")
	return s
}

func escapeMarkdownV2Text(s string) string {
	replacer := strings.NewReplacer(
		`\\`, `\\\\`,
		"_", `\_`,
		"*", `\*`,
		"[", `\[`,
		"]", `\]`,
		"(", `\(`,
		")", `\)`,
		"~", `\~`,
		"`", "\\`",
		">", `\>`,
		"#", `\#`,
		"+", `\+`,
		"-", `\-`,
		"=", `\=`,
		"|", `\|`,
		"{", `\{`,
		"}", `\}`,
		".", `\.`,
		"!", `\!`,
	)
	return replacer.Replace(s)
}

func escapeMarkdownV2Code(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, "`", "\\`")
	return s
}

func escapeMarkdownV2PreservingFences(s string) string {
	var out strings.Builder
	for {
		start := strings.Index(s, "```")
		if start < 0 {
			out.WriteString(escapeMarkdownV2Text(s))
			break
		}
		out.WriteString(escapeMarkdownV2Text(s[:start]))
		rest := s[start+3:]
		end := strings.Index(rest, "```")
		if end < 0 {
			// broken fence: treat all as plain text
			out.WriteString(escapeMarkdownV2Text("```" + rest))
			break
		}
		block := rest[:end]
		nl := strings.Index(block, "\n")
		code := block
		if nl >= 0 {
			code = block[nl+1:]
		}
		out.WriteString("```\n")
		out.WriteString(escapeMarkdownV2Code(code))
		out.WriteString("\n```")
		s = rest[end+3:]
	}
	return out.String()
}

func fetchChatAdminStatus(bot *tgbotapi.BotAPI, chatID int64, userID int64) bool {
	cfg := tgbotapi.GetChatMemberConfig{
		ChatConfigWithUser: tgbotapi.ChatConfigWithUser{
			ChatID: chatID,
			UserID: userID,
		},
	}
	member, err := bot.GetChatMember(cfg)
	if err != nil {
		return false
	}
	return member.Status == "administrator" || member.Status == "creator"
}

type templateContext struct {
	Bot            *tgbotapi.BotAPI
	Msg            *tgbotapi.Message
	CapturingText  string
	MatchText      string
	CaseSensitive  bool
	TemplateLookup func(string) string
}

func newTemplateContext(bot *tgbotapi.BotAPI, msg *tgbotapi.Message, tr *Trigger, lookup func(string) string) templateContext {
	if tr == nil {
		return templateContext{Bot: bot, Msg: msg, TemplateLookup: lookup}
	}
	return templateContext{
		Bot:            bot,
		Msg:            msg,
		CapturingText:  tr.CapturingText,
		MatchText:      tr.MatchText,
		CaseSensitive:  tr.CaseSensitive,
		TemplateLookup: lookup,
	}
}

func buildPromptFromMessage(ctx templateContext, promptTemplate string) string {
	prompt := strings.TrimSpace(promptTemplate)
	if prompt == "" {
		prompt = "Ответь коротко и по делу."
	}

	if ctx.Msg == nil {
		return prompt
	}

	if strings.Contains(promptTemplate, "{{") {
		return renderTemplateWithMessage(ctx, prompt)
	}

	replacements := buildMessageTemplateReplacements(ctx.Bot, ctx.Msg)
	return prompt + "\n\nСообщение пользователя:\n" + replacements["{{message}}"]
}

func resolveMessageImageURL(bot *tgbotapi.BotAPI, msg *tgbotapi.Message) (string, bool) {
	if bot == nil || msg == nil {
		return "", false
	}
	if fileID := extractImageFileID(msg); fileID != "" {
		if url, err := bot.GetFileDirectURL(fileID); err == nil && strings.TrimSpace(url) != "" {
			return strings.TrimSpace(url), true
		}
	}
	if msg.ReplyToMessage != nil {
		if fileID := extractImageFileID(msg.ReplyToMessage); fileID != "" {
			if url, err := bot.GetFileDirectURL(fileID); err == nil && strings.TrimSpace(url) != "" {
				return strings.TrimSpace(url), true
			}
		}
	}
	return "", false
}

func extractImageFileID(msg *tgbotapi.Message) string {
	if msg == nil {
		return ""
	}
	if len(msg.Photo) > 0 {
		best := msg.Photo[0]
		for _, p := range msg.Photo {
			if p.FileSize > best.FileSize {
				best = p
			}
		}
		return strings.TrimSpace(best.FileID)
	}
	if msg.Document != nil {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(msg.Document.MimeType)), "image/") {
			return strings.TrimSpace(msg.Document.FileID)
		}
	}
	return ""
}

var regexQuantifierPattern = regexp.MustCompile(`\{[^}]*\}`)
var regexSpacePattern = regexp.MustCompile(`\\s\+|\\s\*|\\s`)
var legacyTemplateActionRe = regexp.MustCompile(`\{\{[-]?\s*([^{}]+?)\s*[-]?\}\}`)
var legacyPipeIndexRe = regexp.MustCompile(`\|\s*index\s+(-?\d+)`)

var capturingChoiceCache = struct {
	mu    sync.RWMutex
	items map[string][]string
}{
	items: make(map[string][]string),
}

var responseTemplateCache = struct {
	mu    sync.RWMutex
	items map[string]*htmltmpl.Template
}{
	items: make(map[string]*htmltmpl.Template),
}

var responseTemplateFuncsMu sync.RWMutex

var responseTemplateFuncs = htmltmpl.FuncMap{
	"default": func(def string, v interface{}) string {
		if isTemplateEmptyValue(v) {
			return strings.TrimSpace(def)
		}
		return toTemplateString(v)
	},
	"trim": func(v interface{}) string {
		return strings.TrimSpace(toTemplateString(v))
	},
	"lower": func(v interface{}) string {
		return strings.ToLower(toTemplateString(v))
	},
	"upper": func(v interface{}) string {
		return strings.ToUpper(toTemplateString(v))
	},
	"title": func(v interface{}) string {
		return titleCaseWords(toTemplateString(v))
	},
	"replace": func(old, new string, v interface{}) string {
		return strings.ReplaceAll(toTemplateString(v), old, new)
	},
	"truncate": func(limit int, v interface{}) string {
		s := toTemplateString(v)
		if limit <= 0 {
			return ""
		}
		runes := []rune(s)
		if len(runes) <= limit {
			return s
		}
		return strings.TrimSpace(string(runes[:limit]))
	},
	"join": func(sep string, v interface{}) string {
		items := toStringSlice(v)
		if len(items) == 0 {
			return ""
		}
		return strings.Join(items, sep)
	},
	"first": func(v interface{}) string {
		items := toStringSlice(v)
		if len(items) == 0 {
			return ""
		}
		return strings.TrimSpace(items[0])
	},
	"last": func(v interface{}) string {
		items := toStringSlice(v)
		if len(items) == 0 {
			return ""
		}
		return strings.TrimSpace(items[len(items)-1])
	},
	"now": func() time.Time {
		return time.Now()
	},
	"date": func(layout string, v interface{}) string {
		layout = strings.TrimSpace(layout)
		if layout == "" {
			layout = time.RFC3339
		}
		tm, ok := toTemplateTime(v)
		if !ok {
			return ""
		}
		return tm.Format(layout)
	},
	"split": func(sep string, in interface{}) []string {
		src := toTemplateString(in)
		if sep == "" {
			return []string{strings.TrimSpace(src)}
		}
		parts := strings.Split(src, sep)
		for i := range parts {
			parts[i] = strings.TrimSpace(parts[i])
		}
		return parts
	},
	"contains": func(needle string, in interface{}) bool {
		return strings.Contains(toTemplateString(in), needle)
	},
	"pick": func(idx int, items []string) string {
		if idx < 0 || idx >= len(items) {
			return ""
		}
		return strings.TrimSpace(items[idx])
	},
	"istrue": func(args ...interface{}) string {
		if len(args) == 0 {
			return ""
		}
		whenTrue := ""
		whenFalse := ""
		var raw interface{}
		switch len(args) {
		case 1:
			raw = args[0]
		case 2:
			whenTrue = toTemplateString(args[0])
			raw = args[1]
		default:
			whenTrue = toTemplateString(args[0])
			whenFalse = toTemplateString(args[1])
			raw = args[2]
		}
		if isTruthy(toTemplateString(raw)) {
			return whenTrue
		}
		return whenFalse
	},
	"gender": func(args ...interface{}) string {
		if len(args) == 0 {
			return ""
		}
		val := toTemplateString(args[len(args)-1])
		var variants genderVariants
		if len(args) > 1 {
			variants.Male = toTemplateString(args[0])
		}
		if len(args) > 2 {
			variants.Female = toTemplateString(args[1])
		}
		if len(args) > 3 {
			variants.Neuter = toTemplateString(args[2])
		}
		if len(args) > 4 {
			variants.Plural = toTemplateString(args[3])
		}
		if len(args) > 5 {
			variants.Unknown = toTemplateString(args[4])
		}
		return resolveGenderVariant(val, variants)
	},
}

func RegisterResponseTemplateFunc(name string, fn interface{}) {
	name = strings.TrimSpace(name)
	if name == "" || fn == nil {
		return
	}
	responseTemplateFuncsMu.Lock()
	responseTemplateFuncs[name] = fn
	responseTemplateFuncsMu.Unlock()
	responseTemplateCache.mu.Lock()
	responseTemplateCache.items = make(map[string]*htmltmpl.Template)
	responseTemplateCache.mu.Unlock()
}

var reservedTemplateWords = map[string]struct{}{
	"if":       {},
	"else":     {},
	"end":      {},
	"range":    {},
	"with":     {},
	"template": {},
	"block":    {},
	"define":   {},
	"nil":      {},
	"true":     {},
	"false":    {},
}

func applyCapturingTemplate(s, capture, matchText string, caseSensitive bool) string {
	if strings.TrimSpace(s) == "" {
		return s
	}
	cleanCapture := strings.TrimSpace(capture)
	choice := deriveCapturingChoice(matchText, cleanCapture, caseSensitive)
	vars := map[string]interface{}{
		"capturing_text":   cleanCapture,
		"capturing_choice": choice,
		"capturing_option": choice,
	}
	out, err := renderResponseTemplate(s, vars, nil)
	if err != nil {
		return applySimpleTemplateVars(s, vars)
	}
	return out
}

func deriveCapturingChoice(matchText, capture string, caseSensitive bool) string {
	capture = strings.TrimSpace(capture)
	if capture == "" || strings.TrimSpace(matchText) == "" {
		return ""
	}
	candidates := capturingChoiceCandidates(matchText)
	best := ""
	bestScore := 0
	for _, clean := range candidates {
		score := matchChoiceScore(clean, capture, caseSensitive)
		if score > bestScore {
			bestScore = score
			best = clean
		}
	}
	return best
}

func capturingChoiceCandidates(matchText string) []string {
	key := strings.TrimSpace(matchText)
	if key == "" {
		return nil
	}
	capturingChoiceCache.mu.RLock()
	if items, ok := capturingChoiceCache.items[key]; ok {
		out := make([]string, len(items))
		copy(out, items)
		capturingChoiceCache.mu.RUnlock()
		return out
	}
	capturingChoiceCache.mu.RUnlock()

	groups := extractRegexGroups(key)
	seen := make(map[string]struct{}, 16)
	items := make([]string, 0, 16)
	for _, g := range groups {
		if !strings.Contains(g, "|") {
			continue
		}
		g = trimGroupPrefix(g)
		parts := strings.Split(g, "|")
		for _, part := range parts {
			clean := cleanRegexAlt(part)
			if clean == "" {
				continue
			}
			if _, ok := seen[clean]; ok {
				continue
			}
			seen[clean] = struct{}{}
			items = append(items, clean)
		}
	}

	capturingChoiceCache.mu.Lock()
	// Bound memory growth under highly dynamic patterns.
	if len(capturingChoiceCache.items) > 4096 {
		capturingChoiceCache.items = make(map[string][]string)
	}
	cached := make([]string, len(items))
	copy(cached, items)
	capturingChoiceCache.items[key] = cached
	capturingChoiceCache.mu.Unlock()

	return items
}

func isTruthy(v string) bool {
	v = strings.ToLower(strings.TrimSpace(v))
	if v == "" {
		return false
	}
	switch v {
	case "true", "1", "yes", "y", "да", "истина":
		return true
	default:
		return false
	}
}

func matchChoiceScore(candidate, capture string, caseSensitive bool) int {
	rawCandidate := strings.TrimSpace(candidate)
	if rawCandidate == "" {
		return 0
	}
	if !caseSensitive {
		candidate = strings.ToLower(candidate)
		capture = strings.ToLower(capture)
	}
	if candidate == capture {
		return 1000 + len(rawCandidate)
	}
	if strings.Contains(candidate, capture) {
		return 700 + len(rawCandidate)
	}
	// Avoid noisy micro-matches like "а", "у", "е" from grammar suffix groups.
	if len([]rune(strings.TrimSpace(rawCandidate))) < 3 {
		return 0
	}
	if strings.Contains(capture, candidate) {
		return 300 + len(rawCandidate)
	}
	return 0
}

func trimGroupPrefix(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "?") {
		if idx := strings.Index(s, ":"); idx >= 0 {
			s = s[idx+1:]
		}
	}
	return strings.TrimSpace(s)
}

func extractRegexGroups(pattern string) []string {
	if pattern == "" {
		return nil
	}
	out := make([]string, 0, 4)
	var stack []int
	escaped := false
	for i := 0; i < len(pattern); i++ {
		ch := pattern[i]
		if escaped {
			escaped = false
			continue
		}
		if ch == '\\' {
			escaped = true
			continue
		}
		if ch == '(' {
			stack = append(stack, i)
			continue
		}
		if ch == ')' && len(stack) > 0 {
			start := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if start+1 < i {
				out = append(out, pattern[start+1:i])
			}
		}
	}
	return out
}

func cleanRegexAlt(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	s = strings.ReplaceAll(s, "(?:", "")
	s = strings.ReplaceAll(s, "?:", "")
	s = strings.ReplaceAll(s, "(", "")
	s = strings.ReplaceAll(s, ")", "")
	s = strings.ReplaceAll(s, "^", "")
	s = strings.ReplaceAll(s, "$", "")
	s = regexSpacePattern.ReplaceAllString(s, " ")
	s = regexQuantifierPattern.ReplaceAllString(s, "")
	s = strings.ReplaceAll(s, "\\", "")
	s = strings.ReplaceAll(s, "?", "")
	s = strings.ReplaceAll(s, "*", "")
	s = strings.ReplaceAll(s, "+", "")
	s = strings.Join(strings.Fields(s), " ")
	return strings.TrimSpace(s)
}

func pickResponseVariantText(items []ResponseTextItem) string {
	if len(items) == 0 {
		return ""
	}
	nonEmpty := make([]string, 0, len(items))
	for _, it := range items {
		val := strings.TrimSpace(it.Text)
		if val != "" {
			nonEmpty = append(nonEmpty, val)
		}
	}
	if len(nonEmpty) == 0 {
		return ""
	}
	return nonEmpty[rand.Intn(len(nonEmpty))]
}

func renderTemplateWithMessage(ctx templateContext, template string) string {
	if strings.TrimSpace(template) == "" {
		return template
	}
	vars := buildTemplateVars(ctx)
	out, err := renderResponseTemplate(template, vars, ctx.TemplateLookup)
	if err != nil {
		return applySimpleTemplateVars(template, vars)
	}
	return out
}

func buildResponseFromMessage(ctx templateContext, template string) string {
	return renderTemplateWithMessage(ctx, template)
}

func isOlenyamTrigger(tr *Trigger) bool {
	if tr == nil {
		return false
	}
	title := strings.ToLower(strings.TrimSpace(tr.Title))
	return strings.Contains(title, "оле-ням") || strings.Contains(title, "оленям") || strings.Contains(title, "оле ням")
}

func buildTemplateVars(ctx templateContext) map[string]interface{} {
	vars := make(map[string]interface{}, 24)
	if ctx.Msg != nil {
		replacements := buildMessageTemplateReplacements(ctx.Bot, ctx.Msg)
		for tag, value := range replacements {
			name := strings.TrimSpace(strings.TrimPrefix(tag, "{{"))
			name = strings.TrimSuffix(name, "}}")
			if name != "" {
				vars[name] = value
			}
		}
		// In this bot all placeholders are considered trusted and intended for Telegram HTML output.
		// Keep html/template for structure/funcs, but mark values as safe to avoid escaping.
		for k, v := range vars {
			if s, ok := v.(string); ok {
				vars[k] = htmltmpl.HTML(strings.TrimSpace(s))
			}
		}
	}
	cleanCapture := strings.TrimSpace(ctx.CapturingText)
	choice := deriveCapturingChoice(ctx.MatchText, cleanCapture, ctx.CaseSensitive)
	vars["capturing_text"] = cleanCapture
	vars["capturing_choice"] = choice
	vars["capturing_option"] = choice
	return vars
}

func renderResponseTemplate(input string, vars map[string]interface{}, lookup func(string) string) (string, error) {
	if strings.TrimSpace(input) == "" {
		return input, nil
	}
	resolved := expandTemplateCalls(input, lookup)
	normalized := normalizeLegacyTemplateSyntax(resolved, vars)
	tpl, err := cachedResponseTemplate(normalized)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := tpl.Execute(&buf, vars); err != nil {
		return "", err
	}
	return strings.TrimSpace(buf.String()), nil
}

func normalizeLegacyTemplateSyntax(input string, vars map[string]interface{}) string {
	if strings.TrimSpace(input) == "" {
		return input
	}
	varNames := make(map[string]struct{}, len(vars))
	for k := range vars {
		k = strings.TrimSpace(k)
		if k != "" {
			varNames[k] = struct{}{}
		}
	}
	out := legacyTemplateActionRe.ReplaceAllStringFunc(input, func(full string) string {
		m := legacyTemplateActionRe.FindStringSubmatch(full)
		if len(m) < 2 {
			return full
		}
		action := strings.TrimSpace(m[1])
		if action == "" {
			return full
		}
		first, rest := splitFirstActionToken(action)
		if first == "" {
			return full
		}
		if _, reserved := reservedTemplateWords[first]; reserved {
			return full
		}
		if _, ok := varNames[first]; !ok {
			return full
		}
		if strings.HasPrefix(first, ".") || strings.HasPrefix(first, "$") {
			return full
		}
		if rest == "" {
			return "{{ ." + first + " }}"
		}
		return "{{ ." + first + " " + rest + " }}"
	})
	out = legacyPipeIndexRe.ReplaceAllString(out, "| pick $1")
	return out
}

func splitFirstActionToken(action string) (string, string) {
	action = strings.TrimSpace(action)
	if action == "" {
		return "", ""
	}
	i := 0
	for i < len(action) {
		ch := action[i]
		if ch == ' ' || ch == '\t' || ch == '\n' || ch == '\r' || ch == '|' {
			break
		}
		i++
	}
	first := strings.TrimSpace(action[:i])
	rest := strings.TrimSpace(action[i:])
	return first, rest
}

func cachedResponseTemplate(src string) (*htmltmpl.Template, error) {
	key := strings.TrimSpace(src)
	if key == "" {
		key = src
	}
	responseTemplateCache.mu.RLock()
	if t, ok := responseTemplateCache.items[key]; ok {
		responseTemplateCache.mu.RUnlock()
		return t, nil
	}
	responseTemplateCache.mu.RUnlock()

	responseTemplateFuncsMu.RLock()
	funcs := make(htmltmpl.FuncMap, len(responseTemplateFuncs))
	for k, v := range responseTemplateFuncs {
		funcs[k] = v
	}
	responseTemplateFuncsMu.RUnlock()
	t, err := htmltmpl.New("response").Funcs(funcs).Option("missingkey=zero").Parse(src)
	if err != nil {
		return nil, err
	}

	responseTemplateCache.mu.Lock()
	if len(responseTemplateCache.items) > 4096 {
		responseTemplateCache.items = make(map[string]*htmltmpl.Template)
	}
	responseTemplateCache.items[key] = t
	responseTemplateCache.mu.Unlock()
	return t, nil
}

func applySimpleTemplateVars(input string, vars map[string]interface{}) string {
	out := input
	for key, val := range vars {
		out = strings.ReplaceAll(out, "{{"+key+"}}", toTemplateString(val))
	}
	return strings.TrimSpace(out)
}

func toTemplateString(v interface{}) string {
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t)
	case []byte:
		return strings.TrimSpace(string(t))
	case fmt.Stringer:
		return strings.TrimSpace(t.String())
	default:
		return strings.TrimSpace(fmt.Sprintf("%v", v))
	}
}

func titleCaseWords(s string) string {
	words := strings.Fields(strings.TrimSpace(s))
	if len(words) == 0 {
		return ""
	}
	for i, w := range words {
		runes := []rune(strings.ToLower(strings.TrimSpace(w)))
		if len(runes) == 0 {
			continue
		}
		runes[0] = unicode.ToUpper(runes[0])
		words[i] = string(runes)
	}
	return strings.Join(words, " ")
}

func toStringSlice(v interface{}) []string {
	switch t := v.(type) {
	case nil:
		return nil
	case []string:
		out := make([]string, 0, len(t))
		for _, it := range t {
			it = strings.TrimSpace(it)
			if it != "" {
				out = append(out, it)
			}
		}
		return out
	case []interface{}:
		out := make([]string, 0, len(t))
		for _, it := range t {
			s := toTemplateString(it)
			if s != "" {
				out = append(out, s)
			}
		}
		return out
	default:
		s := toTemplateString(v)
		if s == "" {
			return nil
		}
		return []string{s}
	}
}

func isTemplateEmptyValue(v interface{}) bool {
	if v == nil {
		return true
	}
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t) == ""
	case []string:
		return len(t) == 0
	case []interface{}:
		return len(t) == 0
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Bool:
		return !rv.Bool()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return rv.Int() == 0
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return rv.Uint() == 0
	case reflect.Float32, reflect.Float64:
		return rv.Float() == 0
	case reflect.Slice, reflect.Array, reflect.Map, reflect.String:
		return rv.Len() == 0
	case reflect.Interface, reflect.Pointer:
		return rv.IsNil()
	default:
		return false
	}
}

func toTemplateTime(v interface{}) (time.Time, bool) {
	switch t := v.(type) {
	case time.Time:
		return t, true
	case *time.Time:
		if t == nil {
			return time.Time{}, false
		}
		return *t, true
	case int64:
		return time.Unix(t, 0), true
	case int:
		return time.Unix(int64(t), 0), true
	case float64:
		return time.Unix(int64(t), 0), true
	case string:
		s := strings.TrimSpace(t)
		if s == "" {
			return time.Time{}, false
		}
		layouts := []string{
			time.RFC3339Nano,
			time.RFC3339,
			"2006-01-02 15:04:05",
			"2006-01-02 15:04",
			"2006-01-02",
		}
		for _, layout := range layouts {
			if tm, err := time.Parse(layout, s); err == nil {
				return tm, true
			}
		}
		return time.Time{}, false
	default:
		return time.Time{}, false
	}
}

func buildImageSearchQueryFromMessage(ctx templateContext, queryTemplate string) string {
	query := strings.TrimSpace(renderTemplateWithMessage(ctx, queryTemplate))
	if ctx.Msg == nil {
		return query
	}
	if query == "" {
		replacements := buildMessageTemplateReplacements(ctx.Bot, ctx.Msg)
		return strings.TrimSpace(replacements["{{message}}"])
	}
	return strings.TrimSpace(query)
}

func buildSpotifyMusicQueryFromMessage(ctx templateContext, queryTemplate string) string {
	query := strings.TrimSpace(renderTemplateWithMessage(ctx, queryTemplate))
	if ctx.Msg == nil {
		return query
	}
	if query == "" {
		replacements := buildMessageTemplateReplacements(ctx.Bot, ctx.Msg)
		return strings.TrimSpace(replacements["{{message}}"])
	}
	return strings.TrimSpace(query)
}

func buildMediaDownloadQueryFromMessage(ctx templateContext, queryTemplate string) string {
	query := strings.TrimSpace(renderTemplateWithMessage(ctx, queryTemplate))
	if ctx.Msg == nil {
		return query
	}
	if query == "" {
		return strings.TrimSpace(firstNonEmptyUserText(ctx.Msg))
	}
	return strings.TrimSpace(query)
}

func mediaModeAndInteractivity(service string, interactive bool) (mode string, useInteractive bool) {
	service = strings.ToLower(strings.TrimSpace(service))
	switch service {
	case "soundcloud":
		return mediadl.ModeAudio, false
	case "instagram", "tiktok":
		return mediadl.ModeAuto, false
	case "x":
		return mediadl.ModeVideo, false
	default:
		return mediadl.ModeAudio, interactive
	}
}

func firstNonEmptyUserText(msg *tgbotapi.Message) string {
	if msg == nil {
		return ""
	}
	if v := strings.TrimSpace(msg.Text); v != "" {
		return v
	}
	return strings.TrimSpace(msg.Caption)
}

func extractSupportedMediaURL(input string) string {
	matches := supportedMediaURLRe.FindAllString(input, 8)
	for _, raw := range matches {
		if norm, _, ok := mediadl.NormalizeSupportedURL(raw); ok {
			return norm
		}
	}
	return ""
}

func extractSupportedMediaURLByService(input string, service string) string {
	service = strings.ToLower(strings.TrimSpace(service))
	if service == "" {
		return extractSupportedMediaURL(input)
	}
	matches := supportedMediaURLRe.FindAllString(input, 8)
	for _, raw := range matches {
		norm, gotService, ok := mediadl.NormalizeSupportedURL(raw)
		if !ok {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(gotService), service) {
			return norm
		}
	}
	return ""
}

func extractYandexMusicURL(input string) string {
	matches := supportedMediaURLRe.FindAllString(input, 8)
	for _, raw := range matches {
		u, err := url.Parse(strings.TrimSpace(raw))
		if err != nil {
			continue
		}
		host := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(u.Hostname())), "www.")
		if host != "music.yandex.ru" && host != "music.yandex.com" {
			continue
		}
		path := strings.ToLower(strings.TrimSpace(u.Path))
		// Allow only track links.
		if strings.Contains(path, "/track/") {
			return strings.TrimSpace(raw)
		}
	}
	return ""
}

func firstNonEmptyEnv(keys ...string) string {
	for _, key := range keys {
		if v := strings.TrimSpace(os.Getenv(strings.TrimSpace(key))); v != "" {
			return v
		}
	}
	return ""
}

func buildMessageTemplateReplacements(bot *tgbotapi.BotAPI, msg *tgbotapi.Message) map[string]string {
	if msg == nil || msg.From == nil || msg.Chat == nil {
		return map[string]string{
			"{{message}}":   "",
			"{{user_text}}": "",
		}
	}
	extractLabel := func(s string) string {
		s = strings.TrimSpace(s)
		if s == "" {
			return ""
		}
		re := regexp.MustCompile(`\(([^()]{1,80})\)`)
		m := re.FindStringSubmatch(s)
		if len(m) > 1 {
			return strings.TrimSpace(m[1])
		}
		return ""
	}
	buildDisplayName := func(u *tgbotapi.User) string {
		return buildUserDisplayName(u)
	}

	userText := strings.TrimSpace(msg.Text)
	if userText == "" {
		userText = strings.TrimSpace(msg.Caption)
	}
	userDisplayName := buildDisplayName(msg.From)
	userUsername := strings.TrimSpace(msg.From.UserName)
	userLabel := extractLabel(userDisplayName)
	senderTag := ""
	if bot != nil && msg.From != nil {
		senderTag = getChatMemberTagRaw(bot.Token, msg.Chat.ID, msg.From.ID)
	}
	senderTagDisplay := senderTag
	if strings.TrimSpace(senderTagDisplay) == "" {
		senderTagDisplay = "не указан"
	}

	replyText := ""
	replyUserID := ""
	replyFirstName := ""
	replyUsername := ""
	replyDisplayName := ""
	replyLabel := ""
	replyUserLink := ""
	replySenderTag := ""
	if msg.ReplyToMessage != nil {
		replyText = strings.TrimSpace(msg.ReplyToMessage.Text)
		if replyText == "" {
			replyText = strings.TrimSpace(msg.ReplyToMessage.Caption)
		}
		if msg.ReplyToMessage.From != nil {
			replyUserID = strconv.FormatInt(msg.ReplyToMessage.From.ID, 10)
			replyFirstName = strings.TrimSpace(msg.ReplyToMessage.From.FirstName)
			replyUsername = strings.TrimSpace(msg.ReplyToMessage.From.UserName)
			replyDisplayName = buildDisplayName(msg.ReplyToMessage.From)
			replyLabel = extractLabel(replyDisplayName)
			replyUserLink = buildUserLink(msg.ReplyToMessage.From)
			if bot != nil {
				replySenderTag = getChatMemberTagRaw(bot.Token, msg.Chat.ID, msg.ReplyToMessage.From.ID)
			}
		}
	}
	replySenderTagDisplay := replySenderTag
	if strings.TrimSpace(replySenderTagDisplay) == "" {
		replySenderTagDisplay = "не указан"
	}

	chatTitle := strings.TrimSpace(msg.Chat.Title)

	return map[string]string{
		"{{message}}":            userText,
		"{{user_text}}":          userText,
		"{{user_id}}":            strconv.FormatInt(msg.From.ID, 10),
		"{{user_first_name}}":    strings.TrimSpace(msg.From.FirstName),
		"{{user_username}}":      userUsername,
		"{{user_display_name}}":  userDisplayName,
		"{{user_label}}":         userLabel,
		"{{sender_tag}}":         senderTagDisplay,
		"{{user_link}}":          buildUserLink(msg.From),
		"{{chat_id}}":            strconv.FormatInt(msg.Chat.ID, 10),
		"{{chat_title}}":         chatTitle,
		"{{reply_text}}":         replyText,
		"{{reply_user_id}}":      replyUserID,
		"{{reply_first_name}}":   replyFirstName,
		"{{reply_username}}":     replyUsername,
		"{{reply_display_name}}": replyDisplayName,
		"{{reply_label}}":        replyLabel,
		"{{reply_user_link}}":    replyUserLink,
		"{{reply_sender_tag}}":   replySenderTagDisplay,
	}
}

func buildUserDisplayName(u *tgbotapi.User) string {
	if u == nil {
		return ""
	}
	full := strings.TrimSpace(strings.TrimSpace(u.FirstName) + " " + strings.TrimSpace(u.LastName))
	if full != "" {
		return full
	}
	if strings.TrimSpace(u.UserName) != "" {
		return "@" + strings.TrimSpace(u.UserName)
	}
	return strconv.FormatInt(u.ID, 10)
}

func buildUserLink(u *tgbotapi.User) string {
	if u == nil {
		return "Участник без имени"
	}
	name := strings.TrimSpace(u.FirstName)
	if name == "" {
		name = strings.TrimSpace(u.UserName)
	}
	if name == "" {
		name = "Участник без имени"
	}
	return fmt.Sprintf("<a href=\"tg://user?id=%d\">%s</a>", u.ID, name)
}
