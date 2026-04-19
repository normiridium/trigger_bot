package app

import (
	"fmt"
	"math/rand"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"

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
var templateExprPattern = regexp.MustCompile(`\{\{([^}]+)\}\}`)

var capturingChoiceCache = struct {
	mu    sync.RWMutex
	items map[string][]string
}{
	items: make(map[string][]string),
}

func applyCapturingTemplate(s, capture, matchText string, caseSensitive bool) string {
	if strings.TrimSpace(s) == "" {
		return s
	}
	cleanCapture := strings.TrimSpace(capture)
	choice := deriveCapturingChoice(matchText, cleanCapture, caseSensitive)
	vars := map[string]string{
		"capturing_text":   cleanCapture,
		"capturing_choice": choice,
		"capturing_option": choice,
	}
	out := applyTemplatePipes(s, vars)
	for key, val := range vars {
		out = strings.ReplaceAll(out, "{{"+key+"}}", val)
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

func applyTemplatePipes(s string, vars map[string]string) string {
	if strings.TrimSpace(s) == "" {
		return s
	}
	return templateExprPattern.ReplaceAllStringFunc(s, func(m string) string {
		expr := strings.TrimSpace(m[2 : len(m)-2])
		if expr == "" || !strings.Contains(expr, "|") {
			return m
		}
		out, ok := evalTemplatePipe(expr, vars)
		if !ok {
			return m
		}
		return out
	})
}

func evalTemplatePipe(expr string, vars map[string]string) (string, bool) {
	parts := strings.Split(expr, "|")
	if len(parts) == 0 {
		return "", false
	}
	base := strings.TrimSpace(parts[0])
	val, ok := vars[base]
	if !ok {
		return "", false
	}
	var list []string
	for _, raw := range parts[1:] {
		op := strings.TrimSpace(raw)
		name, argStr := splitPipeOp(op)
		args := parsePipeArgs(argStr)
		switch name {
		case "split":
			delim := ""
			if len(args) > 0 {
				delim = args[0]
			}
			if delim == "" {
				list = []string{val}
				continue
			}
			items := strings.Split(val, delim)
			for i := range items {
				items[i] = strings.TrimSpace(items[i])
			}
			list = items
			continue
		case "index":
			if len(args) == 0 {
				return "", true
			}
			idx, err := strconv.Atoi(args[0])
			if err != nil || idx < 0 {
				return "", true
			}
			if list == nil {
				return "", true
			}
			if idx >= len(list) {
				return "", true
			}
			val = list[idx]
			list = nil
			continue
		case "contains":
			if len(args) == 0 {
				val = "false"
				continue
			}
			if strings.Contains(val, args[0]) {
				val = "true"
			} else {
				val = "false"
			}
			continue
		case "istrue":
			if isTruthy(val) {
				if len(args) > 0 {
					val = args[0]
				} else {
					val = ""
				}
			} else {
				if len(args) > 1 {
					val = args[1]
				} else {
					val = ""
				}
			}
			continue
		case "gender":
			var variants genderVariants
			if len(args) > 0 {
				variants.Male = args[0]
			}
			if len(args) > 1 {
				variants.Female = args[1]
			}
			if len(args) > 2 {
				variants.Neuter = args[2]
			}
			if len(args) > 3 {
				variants.Plural = args[3]
			}
			if len(args) > 4 {
				variants.Unknown = args[4]
			}
			val = resolveGenderVariant(val, variants)
			continue
		default:
			continue
		}
	}
	return strings.TrimSpace(val), true
}

func splitPipeOp(s string) (string, string) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", ""
	}
	if idx := strings.IndexFunc(s, func(r rune) bool { return r == ' ' || r == '\t' }); idx >= 0 {
		return strings.TrimSpace(s[:idx]), strings.TrimSpace(s[idx+1:])
	}
	return s, ""
}

func parsePipeArgs(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	args := make([]string, 0, 2)
	for len(s) > 0 {
		s = strings.TrimLeft(s, " \t")
		if s == "" {
			break
		}
		if s[0] == '"' {
			end := strings.Index(s[1:], "\"")
			if end < 0 {
				args = append(args, s[1:])
				break
			}
			args = append(args, s[1:1+end])
			s = s[2+end:]
			continue
		}
		next := len(s)
		for i, r := range s {
			if r == ' ' || r == '\t' {
				next = i
				break
			}
		}
		args = append(args, s[:next])
		if next >= len(s) {
			break
		}
		s = s[next:]
	}
	return args
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
	template = expandTemplateCalls(template, ctx.TemplateLookup)
	vars := buildTemplateVars(ctx)
	out := applyTemplatePipes(template, vars)
	for key, val := range vars {
		out = strings.ReplaceAll(out, "{{"+key+"}}", val)
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

func buildTemplateVars(ctx templateContext) map[string]string {
	vars := make(map[string]string, 24)
	if ctx.Msg != nil {
		replacements := buildMessageTemplateReplacements(ctx.Bot, ctx.Msg)
		for tag, value := range replacements {
			name := strings.TrimSpace(strings.TrimPrefix(tag, "{{"))
			name = strings.TrimSuffix(name, "}}")
			if name != "" {
				vars[name] = value
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
