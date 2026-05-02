package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	htmlstd "html"
	htmltmpl "html/template"
	"io"
	"log"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"os/exec"
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
var webSnippetURLRe = regexp.MustCompile(`https?://[^\s)>\]]+`)
var webSnippetMarkdownLinkRe = regexp.MustCompile(`\[(.*?)\]\((https?://[^\s)]+)\)`)
var webSnippetAngleURLRe = regexp.MustCompile(`<\s*https?://[^>]+>`)
var webSearchLineDropRe = regexp.MustCompile(`(?i)^\s*(источник|sources?|source|url|ссылка)\s*[:\-]`)
var legacyTemplateActionRe = regexp.MustCompile(`\{\{[-]?\s*([^{}]+?)\s*[-]?\}\}`)
var legacyPipeIndexRe = regexp.MustCompile(`\|\s*index\s+(-?\d+)`)
var chatContextActionRe = regexp.MustCompile(`\{\{\s*chat_context(?:\s+(\d+))?\s*\}\}`)

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
var participantPortraitResolverMu sync.RWMutex
var participantPortraitResolver func(chatID, userID int64) string
var participantPortraitRemainingResolverMu sync.RWMutex
var participantPortraitRemainingResolver func(chatID, userID int64) int
var chatContextResolverMu sync.RWMutex
var chatContextResolver func(chatID int64, limit int) string
var chatSummaryResolverMu sync.RWMutex
var chatSummaryResolver func(chatID int64) string
var chatAdminsCache = struct {
	mu    sync.RWMutex
	items map[int64]chatAdminsCacheEntry
}{
	items: make(map[int64]chatAdminsCacheEntry),
}
var templateWeatherCache = struct {
	mu    sync.RWMutex
	items map[string]weatherCacheEntry
}{
	items: make(map[string]weatherCacheEntry),
}

type weatherCacheEntry struct {
	value     string
	expiresAt time.Time
}

type chatAdminsCacheEntry struct {
	value     []map[string]interface{}
	expiresAt time.Time
}

type cachedReplyAudioDetails struct {
	details   replyAudioDetails
	expiresAt time.Time
}

type replyAudioDetails struct {
	Title  string
	Artist string
	Album  string
	Year   string
	Track  string
	Text   string
}

var replyAudioDetailsCache = struct {
	mu    sync.RWMutex
	items map[string]cachedReplyAudioDetails
}{
	items: make(map[string]cachedReplyAudioDetails),
}

func setParticipantPortraitResolver(fn func(chatID, userID int64) string) {
	participantPortraitResolverMu.Lock()
	participantPortraitResolver = fn
	participantPortraitResolverMu.Unlock()
}

func setParticipantPortraitRemainingResolver(fn func(chatID, userID int64) int) {
	participantPortraitRemainingResolverMu.Lock()
	participantPortraitRemainingResolver = fn
	participantPortraitRemainingResolverMu.Unlock()
}

func resolveParticipantPortrait(chatID, userID int64) string {
	if chatID == 0 || userID == 0 {
		return ""
	}
	participantPortraitResolverMu.RLock()
	fn := participantPortraitResolver
	participantPortraitResolverMu.RUnlock()
	if fn == nil {
		return ""
	}
	return strings.TrimSpace(fn(chatID, userID))
}

func resolveParticipantPortraitRemaining(chatID, userID int64) int {
	if chatID == 0 || userID == 0 {
		return participantPortraitBatchSize
	}
	participantPortraitRemainingResolverMu.RLock()
	fn := participantPortraitRemainingResolver
	participantPortraitRemainingResolverMu.RUnlock()
	if fn == nil {
		return participantPortraitBatchSize
	}
	remaining := fn(chatID, userID)
	if remaining < 0 {
		return 0
	}
	if remaining > participantPortraitBatchSize {
		return participantPortraitBatchSize
	}
	return remaining
}

func setChatContextResolver(fn func(chatID int64, limit int) string) {
	chatContextResolverMu.Lock()
	chatContextResolver = fn
	chatContextResolverMu.Unlock()
}

func resolveChatContext(chatID int64, limit int) string {
	if chatID == 0 {
		return ""
	}
	if limit <= 0 {
		limit = 12
	}
	chatContextResolverMu.RLock()
	fn := chatContextResolver
	chatContextResolverMu.RUnlock()
	if fn == nil {
		return ""
	}
	return strings.TrimSpace(fn(chatID, limit))
}

func setChatSummaryResolver(fn func(chatID int64) string) {
	chatSummaryResolverMu.Lock()
	chatSummaryResolver = fn
	chatSummaryResolverMu.Unlock()
}

func resolveChatSummary(chatID int64) string {
	if chatID == 0 {
		return ""
	}
	chatSummaryResolverMu.RLock()
	fn := chatSummaryResolver
	chatSummaryResolverMu.RUnlock()
	if fn == nil {
		return ""
	}
	return strings.TrimSpace(fn(chatID))
}

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
	"get": func(field string, v interface{}) []string {
		field = strings.TrimSpace(field)
		if field == "" || v == nil {
			return nil
		}
		rv := reflect.ValueOf(v)
		for rv.Kind() == reflect.Ptr {
			if rv.IsNil() {
				return nil
			}
			rv = rv.Elem()
		}
		if rv.Kind() != reflect.Slice && rv.Kind() != reflect.Array {
			return nil
		}
		out := make([]string, 0, rv.Len())
		for i := 0; i < rv.Len(); i++ {
			item := rv.Index(i).Interface()
			val := extractObjectField(item, field)
			if val != "" {
				out = append(out, val)
			}
		}
		return out
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
	"regexp_replace": func(pattern, repl string, v interface{}) string {
		src := toTemplateString(v)
		if strings.TrimSpace(pattern) == "" || src == "" {
			return src
		}
		re, err := regexp.Compile(pattern)
		if err != nil {
			return src
		}
		return re.ReplaceAllString(src, repl)
	},
	"rune_len": func(v interface{}) int {
		return len([]rune(toTemplateString(v)))
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
	"chance": func(percent int) bool {
		if percent <= 0 {
			return false
		}
		if percent >= 100 {
			return true
		}
		return rand.Intn(100) < percent
	},
	"weather": func(city interface{}) string {
		name := strings.TrimSpace(toTemplateString(city))
		if name == "" {
			return ""
		}
		return resolveWeatherNow(name)
	},
	"time_of_day": func(tz interface{}) string {
		loc := loadTemplateLocation(toTemplateString(tz))
		return describeTimeOfDay(time.Now().In(loc).Hour())
	},
	"time_hm": func(tz interface{}) string {
		loc := loadTemplateLocation(toTemplateString(tz))
		return time.Now().In(loc).Format("15:04")
	},
	"weekday": func(tz interface{}) string {
		loc := loadTemplateLocation(toTemplateString(tz))
		return russianWeekdayName(time.Now().In(loc).Weekday())
	},
	"web_search": func(query interface{}, args ...interface{}) string {
		opts := parseWebSearchCall(query, args...)
		return resolveWebSearchContext(opts.Query, opts.Condition, opts.Compact)
	},
}

type webSearchCallOptions struct {
	Query     string
	Condition string
	Compact   bool
}

func parseWebSearchCall(query interface{}, args ...interface{}) webSearchCallOptions {
	opts := webSearchCallOptions{
		Query: strings.TrimSpace(toTemplateString(query)),
	}
	if isWebSearchCompactFlag(opts.Query) {
		opts.Compact = true
		opts.Query = ""
	} else if isWebSearchFullFlag(opts.Query) {
		opts.Compact = false
		opts.Query = ""
	}

	for _, arg := range args {
		if b, ok := arg.(bool); ok {
			opts.Compact = b
			continue
		}
		s := strings.TrimSpace(toTemplateString(arg))
		if s == "" {
			continue
		}
		switch {
		case isWebSearchCompactFlag(s):
			opts.Compact = true
		case isWebSearchFullFlag(s):
			opts.Compact = false
		case opts.Condition == "":
			opts.Condition = s
		}
	}

	// Go template pipelines pass the previous result as the LAST argument.
	// For `{{ .message | web_search "cond" }}` this function receives:
	//   query="cond", condition=".message"
	// Detect and normalize this case.
	if opts.Condition != "" && looksLikeWebSearchCondition(opts.Query) && !looksLikeWebSearchCondition(opts.Condition) {
		opts.Query, opts.Condition = opts.Condition, opts.Query
	}
	// For `{{ .message | web_search "компактно" }}`:
	//   query="компактно", condition=".message"
	if opts.Query == "" && opts.Condition != "" {
		opts.Query, opts.Condition = opts.Condition, ""
	}
	return opts
}

func looksLikeWebSearchCondition(s string) bool {
	v := strings.ToLower(strings.TrimSpace(s))
	if v == "" {
		return false
	}
	markers := []string{
		"искать",
		"поиск",
		"search",
		"only if",
		"только если",
		"if ",
		"если ",
		"__no_search__",
	}
	for _, m := range markers {
		if strings.Contains(v, m) {
			return true
		}
	}
	return false
}

func isWebSearchCompactFlag(s string) bool {
	v := strings.ToLower(strings.TrimSpace(s))
	switch v {
	case "кратко", "компактно", "short", "brief", "compact":
		return true
	default:
		return false
	}
}

func isWebSearchFullFlag(s string) bool {
	v := strings.ToLower(strings.TrimSpace(s))
	switch v {
	case "полно", "подробно", "full", "verbose", "detailed":
		return true
	default:
		return false
	}
}

func resolveWeatherNow(city string) string {
	city = strings.TrimSpace(city)
	if city == "" {
		return ""
	}
	key := strings.ToLower(city)
	now := time.Now()
	templateWeatherCache.mu.RLock()
	if cached, ok := templateWeatherCache.items[key]; ok && now.Before(cached.expiresAt) {
		templateWeatherCache.mu.RUnlock()
		return cached.value
	}
	templateWeatherCache.mu.RUnlock()

	var (
		val string
		err error
	)
	for _, candidate := range weatherCityCandidates(city) {
		val, err = fetchWeatherNow(candidate)
		if err == nil && strings.TrimSpace(val) != "" {
			break
		}
	}
	ttl := 12 * time.Minute
	if err != nil {
		val = "н/д"
		ttl = 2 * time.Minute
	}

	templateWeatherCache.mu.Lock()
	if len(templateWeatherCache.items) > 2048 {
		templateWeatherCache.items = make(map[string]weatherCacheEntry)
	}
	templateWeatherCache.items[key] = weatherCacheEntry{
		value:     val,
		expiresAt: now.Add(ttl),
	}
	templateWeatherCache.mu.Unlock()
	return val
}

func weatherCityCandidates(city string) []string {
	orig := strings.TrimSpace(city)
	if orig == "" {
		return nil
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, 8)
	add := func(v string) {
		v = strings.TrimSpace(v)
		if v == "" {
			return
		}
		k := strings.ToLower(v)
		if _, ok := seen[k]; ok {
			return
		}
		seen[k] = struct{}{}
		out = append(out, v)
	}
	add(orig)

	lower := strings.ToLower(orig)
	r := []rune(lower)
	if len(r) < 2 {
		return out
	}

	replaceLast := func(to string) string {
		return string(r[:len(r)-1]) + to
	}
	replaceLast2 := func(to string) string {
		if len(r) < 3 {
			return ""
		}
		return string(r[:len(r)-2]) + to
	}

	switch {
	case strings.HasSuffix(lower, "и"):
		add(replaceLast("ь"))
		add(replaceLast("а"))
		add(replaceLast("я"))
	case strings.HasSuffix(lower, "е"):
		add(replaceLast("а"))
		add(replaceLast("я"))
	case strings.HasSuffix(lower, "у"):
		add(replaceLast("а"))
		add(replaceLast("я"))
	case strings.HasSuffix(lower, "ю"):
		add(replaceLast("я"))
	case strings.HasSuffix(lower, "ой"), strings.HasSuffix(lower, "ей"):
		add(replaceLast2("а"))
		add(replaceLast2("я"))
	case strings.HasSuffix(lower, "ом"), strings.HasSuffix(lower, "ем"):
		add(replaceLast2(""))
	}

	return out
}

func fetchWeatherNow(city string) (string, error) {
	client := &http.Client{Timeout: 6 * time.Second}

	geoURL := "https://geocoding-api.open-meteo.com/v1/search?count=1&language=ru&format=json&name=" + url.QueryEscape(city)
	geoReq, err := http.NewRequest(http.MethodGet, geoURL, nil)
	if err != nil {
		return "", err
	}
	geoResp, err := client.Do(geoReq)
	if err != nil {
		return "", err
	}
	defer geoResp.Body.Close()
	var geo struct {
		Results []struct {
			Name      string  `json:"name"`
			Latitude  float64 `json:"latitude"`
			Longitude float64 `json:"longitude"`
		} `json:"results"`
	}
	if err := json.NewDecoder(geoResp.Body).Decode(&geo); err != nil {
		return "", err
	}
	if len(geo.Results) == 0 {
		return "", fmt.Errorf("city not found")
	}
	point := geo.Results[0]

	weatherURL := fmt.Sprintf(
		"https://api.open-meteo.com/v1/forecast?latitude=%.6f&longitude=%.6f&current=temperature_2m,apparent_temperature,weather_code&timezone=auto",
		point.Latitude,
		point.Longitude,
	)
	wReq, err := http.NewRequest(http.MethodGet, weatherURL, nil)
	if err != nil {
		return "", err
	}
	wResp, err := client.Do(wReq)
	if err != nil {
		return "", err
	}
	defer wResp.Body.Close()

	var payload struct {
		Current struct {
			Temperature2m       float64 `json:"temperature_2m"`
			ApparentTemperature float64 `json:"apparent_temperature"`
			WeatherCode         int     `json:"weather_code"`
		} `json:"current"`
	}
	if err := json.NewDecoder(wResp.Body).Decode(&payload); err != nil {
		return "", err
	}
	desc := weatherCodeToRU(payload.Current.WeatherCode)
	return fmt.Sprintf("%s, %.0f°C (ощущается как %.0f°C)", desc, payload.Current.Temperature2m, payload.Current.ApparentTemperature), nil
}

func weatherCodeToRU(code int) string {
	switch code {
	case 0:
		return "ясно"
	case 1, 2:
		return "переменная облачность"
	case 3:
		return "пасмурно"
	case 45, 48:
		return "туман"
	case 51, 53, 55, 56, 57:
		return "морось"
	case 61, 63, 65, 66, 67, 80, 81, 82:
		return "дождь"
	case 71, 73, 75, 77, 85, 86:
		return "снег"
	case 95, 96, 99:
		return "гроза"
	default:
		return "погода"
	}
}

func resolveWebSearchContext(query string, condition string, compact bool) string {
	query = strings.TrimSpace(query)
	if query == "" {
		return ""
	}
	val, err := fetchWebSearchContext(query, condition, compact)
	if err != nil {
		logWebSearchf("fetch failed query=%q cond=%q compact=%t err=%v", clipText(query, 120), clipText(condition, 120), compact, err)
		return ""
	}
	logWebSearchf("fetch ok query=%q cond=%q compact=%t len=%d", clipText(query, 120), clipText(condition, 120), compact, len(val))
	return val
}

func fetchWebSearchContext(query string, condition string, compact bool) (string, error) {
	apiKey := strings.TrimSpace(os.Getenv("OPENAI_API_KEY"))
	if apiKey == "" {
		return "", fmt.Errorf("OPENAI_API_KEY is required")
	}
	model := strings.TrimSpace(os.Getenv("OPENAI_WEB_SEARCH_MODEL"))
	if model == "" {
		model = strings.TrimSpace(os.Getenv("OPENAI_MODEL"))
	}
	if model == "" {
		model = "gpt-4.1"
	}
	condition = strings.TrimSpace(condition)
	logWebSearchf("start query=%q cond=%q compact=%t", clipText(query, 180), clipText(condition, 180), compact)
	if condition != "" {
		shouldSearch, err := shouldRunWebSearch(apiKey, model, query, condition)
		if err != nil {
			return "", err
		}
		if !shouldSearch {
			logWebSearchf("condition rejected query=%q cond=%q", clipText(query, 180), clipText(condition, 180))
			return "", nil
		}
		logWebSearchf("condition accepted query=%q cond=%q", clipText(query, 180), clipText(condition, 180))
	}

	prompt := buildWebSearchPrompt(query, compact)
	reqPayload := map[string]interface{}{
		"model": model,
		"input": []map[string]interface{}{
			{
				"role": "user",
				"content": []map[string]interface{}{
					{"type": "input_text", "text": prompt},
				},
			},
		},
		"tools":       []map[string]interface{}{{"type": "web_search"}},
		"tool_choice": "auto",
	}
	raw, err := callOpenAIResponsesOutputText(apiKey, reqPayload, 12*time.Second)
	if err != nil {
		return "", err
	}
	logWebSearchf("raw output len=%d query=%q", len(raw), clipText(query, 180))
	raw = sanitizeWebSearchOutput(raw)
	if raw == "" {
		logWebSearchf("sanitized empty query=%q", clipText(query, 180))
		return "", nil
	}
	logWebSearchf("sanitized output len=%d query=%q", len(raw), clipText(query, 180))
	return clipText(raw, 3800), nil
}

func buildWebSearchPrompt(query string, compact bool) string {
	if compact {
		return fmt.Sprintf(
			"Сделай веб-поиск по запросу: %q.\n"+
				"Верни короткие пункты на русском в формате:\n"+
				"1) заголовок — краткая суть\n"+
				"Без URL, без markdown-ссылок, без отдельного списка источников.\n"+
				"Нужна только суть, компактно и по делу.",
			query,
		)
	}
	return fmt.Sprintf(
		"Сделай веб-поиск по запросу: %q.\n"+
			"Верни умеренно подробную выдачу на русском: 4–6 пунктов с ключевой сутью.\n"+
			"Сохрани полезные детали и формулировки, но без лишней воды и без чрезмерной длины.\n"+
			"Без URL, без markdown-ссылок, без отдельного списка источников.",
		query,
	)
}

func shouldRunWebSearch(apiKey, model, query, condition string) (bool, error) {
	prompt := fmt.Sprintf(
		"Определи, нужно ли запускать веб-поиск по условию.\n"+
			"Условие: %s\n"+
			"Запрос: %q\n"+
			"Ответь строго одним словом: SEARCH или NO_SEARCH.",
		condition,
		query,
	)
	reqPayload := map[string]interface{}{
		"model": model,
		"input": []map[string]interface{}{
			{
				"role": "user",
				"content": []map[string]interface{}{
					{"type": "input_text", "text": prompt},
				},
			},
		},
		"temperature":       0,
		"max_output_tokens": 16,
	}
	raw, err := callOpenAIResponsesOutputText(apiKey, reqPayload, 10*time.Second)
	if err != nil {
		return false, err
	}
	decision := strings.ToUpper(strings.TrimSpace(raw))
	logWebSearchf("condition decision raw=%q normalized=%q query=%q cond=%q", clipText(raw, 80), decision, clipText(query, 120), clipText(condition, 120))
	switch decision {
	case "SEARCH":
		return true, nil
	case "NO_SEARCH":
		return false, nil
	default:
		// Fallback to SEARCH so we don't silently drop useful results.
		logWebSearchf("condition decision fallback->SEARCH query=%q cond=%q", clipText(query, 120), clipText(condition, 120))
		return true, nil
	}
}

func webSearchDebugEnabled() bool {
	return debugTriggerLogEnabled || debugGPTLogEnabled || envBool("DEBUG_WEB_SEARCH_LOG", false)
}

func logWebSearchf(format string, args ...interface{}) {
	if !webSearchDebugEnabled() {
		return
	}
	log.Printf("web_search: "+format, args...)
}

func callOpenAIResponsesOutputText(apiKey string, reqPayload map[string]interface{}, timeout time.Duration) (string, error) {
	body, _ := json.Marshal(reqPayload)
	client := &http.Client{Timeout: timeout}
	req, err := http.NewRequest(http.MethodPost, "https://api.openai.com/v1/responses", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("openai responses status=%d body=%s", resp.StatusCode, clipText(string(bodyBytes), 500))
	}
	var respPayload struct {
		Error      string `json:"error"`
		OutputText string `json:"output_text"`
		Output     []struct {
			Type    string `json:"type"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
	}
	if err := json.Unmarshal(bodyBytes, &respPayload); err != nil {
		return "", err
	}
	if strings.TrimSpace(respPayload.Error) != "" {
		return "", errors.New(strings.TrimSpace(respPayload.Error))
	}
	raw := strings.TrimSpace(respPayload.OutputText)
	if raw != "" {
		return raw, nil
	}
	var b strings.Builder
	for _, item := range respPayload.Output {
		if item.Type != "message" {
			continue
		}
		for _, part := range item.Content {
			if part.Type != "output_text" && part.Type != "text" {
				continue
			}
			txt := strings.TrimSpace(part.Text)
			if txt == "" {
				continue
			}
			if b.Len() > 0 {
				b.WriteString("\n")
			}
			b.WriteString(txt)
		}
	}
	return strings.TrimSpace(b.String()), nil
}

func sanitizeWebSearchOutput(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	raw = webSnippetMarkdownLinkRe.ReplaceAllString(raw, "$1")
	raw = webSnippetAngleURLRe.ReplaceAllString(raw, "")
	raw = webSnippetURLRe.ReplaceAllString(raw, "")

	lines := strings.Split(raw, "\n")
	const maxLineRunes = 520
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if webSearchLineDropRe.MatchString(strings.ToLower(line)) {
			continue
		}
		line = strings.Join(strings.Fields(line), " ")
		if line == "" {
			continue
		}
		out = append(out, clipText(line, maxLineRunes))
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

func loadTemplateLocation(tz string) *time.Location {
	name := strings.TrimSpace(tz)
	if name == "" {
		return time.Local
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return time.Local
	}
	return loc
}

func describeTimeOfDay(hour int) string {
	switch {
	case hour >= 5 && hour < 12:
		return "утро"
	case hour >= 12 && hour < 18:
		return "день"
	case hour >= 18 && hour < 23:
		return "вечер"
	default:
		return "ночь"
	}
}

func russianWeekdayName(day time.Weekday) string {
	switch day {
	case time.Monday:
		return "понедельник"
	case time.Tuesday:
		return "вторник"
	case time.Wednesday:
		return "среда"
	case time.Thursday:
		return "четверг"
	case time.Friday:
		return "пятница"
	case time.Saturday:
		return "суббота"
	case time.Sunday:
		return "воскресенье"
	default:
		return ""
	}
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
	rewrittenTemplate, chatContextLimits := rewriteChatContextTemplateActions(template)
	vars := buildTemplateVars(ctx)
	if ctx.Msg != nil && ctx.Msg.Chat != nil {
		chatID := ctx.Msg.Chat.ID
		vars["chat_context"] = strings.TrimSpace(resolveChatContext(chatID, 12))
		vars["summary"] = strings.TrimSpace(resolveChatSummary(chatID))
		for _, n := range chatContextLimits {
			key := fmt.Sprintf("__chat_context_%d", n)
			vars[key] = strings.TrimSpace(resolveChatContext(chatID, n))
		}
	}
	out, err := renderResponseTemplate(rewrittenTemplate, vars, ctx.TemplateLookup)
	if err != nil {
		return restoreTrustedTemplateFragments(applySimpleTemplateVars(rewrittenTemplate, vars), vars)
	}
	return out
}

func rewriteChatContextTemplateActions(input string) (string, []int) {
	if strings.TrimSpace(input) == "" {
		return input, nil
	}
	seen := make(map[int]struct{}, 2)
	limits := make([]int, 0, 2)
	out := chatContextActionRe.ReplaceAllStringFunc(input, func(full string) string {
		m := chatContextActionRe.FindStringSubmatch(full)
		limit := 12
		if len(m) > 1 {
			if n, err := strconv.Atoi(strings.TrimSpace(m[1])); err == nil && n > 0 {
				limit = n
			}
		}
		if _, ok := seen[limit]; !ok {
			seen[limit] = struct{}{}
			limits = append(limits, limit)
		}
		return fmt.Sprintf("{{ .__chat_context_%d }}", limit)
	})
	return out, limits
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
				vars[name] = strings.TrimSpace(value)
			}
		}
		prepareTrustedTemplateFragment(vars, "user_link", "__trusted_user_link_html", "__TRUSTED_USER_LINK_HTML_99DBF0C9__")
		prepareTrustedTemplateFragment(vars, "reply_user_link", "__trusted_reply_user_link_html", "__TRUSTED_REPLY_USER_LINK_HTML_1C4C4B0A__")
		if ctx.Bot != nil && ctx.Msg.Chat != nil {
			vars["admins"] = resolveChatAdmins(ctx.Bot, ctx.Msg.Chat.ID)
		}
	}
	cleanCapture := strings.TrimSpace(ctx.CapturingText)
	choice := deriveCapturingChoice(ctx.MatchText, cleanCapture, ctx.CaseSensitive)
	vars["capturing_text"] = cleanCapture
	vars["capturing_choice"] = choice
	vars["capturing_option"] = choice
	return vars
}

func prepareTrustedTemplateFragment(vars map[string]interface{}, fieldName, stashName, token string) {
	raw, ok := vars[fieldName]
	if !ok {
		return
	}
	s, ok := raw.(string)
	if !ok {
		return
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return
	}
	vars[stashName] = htmltmpl.HTML(s)
	vars[fieldName] = token
}

func restoreTrustedTemplateFragments(input string, vars map[string]interface{}) string {
	out := strings.TrimSpace(input)
	if out == "" {
		return out
	}
	if v, ok := vars["__trusted_user_link_html"]; ok {
		if s, ok2 := v.(htmltmpl.HTML); ok2 {
			out = strings.ReplaceAll(out, "__TRUSTED_USER_LINK_HTML_99DBF0C9__", string(s))
		}
	}
	if v, ok := vars["__trusted_reply_user_link_html"]; ok {
		if s, ok2 := v.(htmltmpl.HTML); ok2 {
			out = strings.ReplaceAll(out, "__TRUSTED_REPLY_USER_LINK_HTML_1C4C4B0A__", string(s))
		}
	}
	return out
}

func resolveChatAdmins(bot *tgbotapi.BotAPI, chatID int64) []map[string]interface{} {
	if bot == nil || chatID == 0 {
		return nil
	}
	now := time.Now()
	chatAdminsCache.mu.RLock()
	if cached, ok := chatAdminsCache.items[chatID]; ok && now.Before(cached.expiresAt) {
		chatAdminsCache.mu.RUnlock()
		return cached.value
	}
	chatAdminsCache.mu.RUnlock()

	admins, err := bot.GetChatAdministrators(tgbotapi.ChatAdministratorsConfig{
		ChatConfig: tgbotapi.ChatConfig{ChatID: chatID},
	})
	if err != nil {
		return nil
	}

	out := make([]map[string]interface{}, 0, len(admins))
	for _, member := range admins {
		u := member.User
		if u == nil || u.IsBot {
			continue
		}
		nickname := strings.TrimSpace(buildUserDisplayName(u))
		if nickname == "" {
			if username := strings.TrimSpace(u.UserName); username != "" {
				nickname = "@" + username
			}
		}
		fullName := strings.TrimSpace(strings.TrimSpace(u.FirstName) + " " + strings.TrimSpace(u.LastName))
		if fullName == "" {
			fullName = nickname
		}
		item := map[string]interface{}{
			"id":         u.ID,
			"nickname":   nickname,
			"full_name":  fullName,
			"first_name": strings.TrimSpace(u.FirstName),
			"last_name":  strings.TrimSpace(u.LastName),
			"username":   strings.TrimSpace(u.UserName),
		}
		out = append(out, item)
	}

	chatAdminsCache.mu.Lock()
	if len(chatAdminsCache.items) > 4096 {
		chatAdminsCache.items = make(map[int64]chatAdminsCacheEntry)
	}
	chatAdminsCache.items[chatID] = chatAdminsCacheEntry{
		value:     out,
		expiresAt: now.Add(45 * time.Second),
	}
	chatAdminsCache.mu.Unlock()
	return out
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
	return restoreTrustedTemplateFragments(buf.String(), vars), nil
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

func extractObjectField(v interface{}, field string) string {
	if strings.TrimSpace(field) == "" || v == nil {
		return ""
	}
	switch t := v.(type) {
	case map[string]interface{}:
		if raw, ok := t[field]; ok {
			return toTemplateString(raw)
		}
		for k, raw := range t {
			if strings.EqualFold(strings.TrimSpace(k), field) {
				return toTemplateString(raw)
			}
		}
		return ""
	case map[string]string:
		if raw, ok := t[field]; ok {
			return strings.TrimSpace(raw)
		}
		for k, raw := range t {
			if strings.EqualFold(strings.TrimSpace(k), field) {
				return strings.TrimSpace(raw)
			}
		}
		return ""
	}
	rv := reflect.ValueOf(v)
	for rv.Kind() == reflect.Ptr {
		if rv.IsNil() {
			return ""
		}
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Struct {
		return ""
	}
	rt := rv.Type()
	for i := 0; i < rv.NumField(); i++ {
		sf := rt.Field(i)
		if strings.EqualFold(strings.TrimSpace(sf.Name), field) {
			return toTemplateString(rv.Field(i).Interface())
		}
	}
	return ""
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
	case "coub":
		return mediadl.ModeAuto, interactive
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

func firstNonEmptyMessageContent(msg *tgbotapi.Message) string {
	if msg == nil {
		return ""
	}
	if v := strings.TrimSpace(msg.Text); v != "" {
		return v
	}
	if msg.Audio != nil {
		title := strings.TrimSpace(msg.Audio.Title)
		performer := strings.TrimSpace(msg.Audio.Performer)
		switch {
		case title != "" && performer != "":
			return performer + " - " + title
		case title != "":
			return title
		case performer != "":
			return performer
		}
		if v := strings.TrimSpace(msg.Audio.FileName); v != "" {
			return v
		}
		if v := strings.TrimSpace(msg.Caption); v != "" {
			return v
		}
		return "audio message"
	}
	if v := strings.TrimSpace(msg.Caption); v != "" {
		return v
	}
	if msg.Animation != nil {
		if v := strings.TrimSpace(msg.Animation.FileName); v != "" {
			return v
		}
	}
	if msg.Document != nil {
		if v := strings.TrimSpace(msg.Document.FileName); v != "" {
			return v
		}
	}
	if msg.Video != nil {
		if v := strings.TrimSpace(msg.Video.FileName); v != "" {
			return v
		}
	}
	if len(msg.Photo) > 0 {
		return "photo"
	}
	if msg.Sticker != nil {
		if v := strings.TrimSpace(msg.Sticker.Emoji); v != "" {
			return "sticker " + v
		}
		return "sticker"
	}
	if msg.Voice != nil {
		return "voice message"
	}
	if msg.VideoNote != nil {
		return "video message"
	}
	return ""
}

func sanitizeReplyAudioTagValue(v string, max int) string {
	v = strings.ToValidUTF8(strings.TrimSpace(v), "")
	v = strings.ReplaceAll(v, "\x00", "")
	v = strings.ReplaceAll(v, "\u00A0", " ")
	v = strings.ReplaceAll(v, "\r\n", "\n")
	v = strings.ReplaceAll(v, "\r", "\n")
	if max > 0 {
		r := []rune(v)
		if len(r) > max {
			v = string(r[:max])
		}
	}
	return strings.TrimSpace(v)
}

func buildReplyAudioText(details replyAudioDetails) string {
	title := strings.TrimSpace(details.Title)
	artist := strings.TrimSpace(details.Artist)
	album := strings.TrimSpace(details.Album)
	year := strings.TrimSpace(details.Year)
	track := strings.TrimSpace(details.Track)
	text := strings.TrimSpace(details.Text)

	head := ""
	switch {
	case artist != "" && title != "":
		head = artist + " - " + title
	case title != "":
		head = title
	case artist != "":
		head = artist
	}
	metaBits := make([]string, 0, 3)
	if album != "" {
		metaBits = append(metaBits, "album: "+album)
	}
	if year != "" {
		metaBits = append(metaBits, "year: "+year)
	}
	if track != "" {
		metaBits = append(metaBits, "track: "+track)
	}

	lines := make([]string, 0, 3)
	if head != "" {
		lines = append(lines, head)
	}
	if len(metaBits) > 0 {
		lines = append(lines, strings.Join(metaBits, " | "))
	}
	if text != "" {
		lines = append(lines, clipText(text, 1600))
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func getCachedReplyAudioDetails(cacheKey string) (replyAudioDetails, bool) {
	replyAudioDetailsCache.mu.RLock()
	item, ok := replyAudioDetailsCache.items[cacheKey]
	replyAudioDetailsCache.mu.RUnlock()
	if !ok || time.Now().After(item.expiresAt) {
		return replyAudioDetails{}, false
	}
	return item.details, true
}

func setCachedReplyAudioDetails(cacheKey string, details replyAudioDetails) {
	replyAudioDetailsCache.mu.Lock()
	replyAudioDetailsCache.items[cacheKey] = cachedReplyAudioDetails{
		details:   details,
		expiresAt: time.Now().Add(6 * time.Hour),
	}
	replyAudioDetailsCache.mu.Unlock()
}

func fetchReplyAudioDetails(bot *tgbotapi.BotAPI, msg *tgbotapi.Message) replyAudioDetails {
	var out replyAudioDetails
	if msg == nil || msg.Audio == nil {
		return out
	}
	out.Title = sanitizeReplyAudioTagValue(msg.Audio.Title, 256)
	out.Artist = sanitizeReplyAudioTagValue(msg.Audio.Performer, 256)
	if out.Title == "" {
		out.Title = sanitizeReplyAudioTagValue(msg.Audio.FileName, 256)
	}
	if bot == nil || !envBool("REPLY_AUDIO_FETCH_TAGS", true) {
		return out
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		return out
	}
	cacheKey := strings.TrimSpace(msg.Audio.FileUniqueID)
	if cacheKey == "" {
		cacheKey = strings.TrimSpace(msg.Audio.FileID)
	}
	if cacheKey != "" {
		if cached, ok := getCachedReplyAudioDetails(cacheKey); ok {
			mergeReplyAudioDetails(&out, cached)
			return out
		}
	}
	fileURL, err := bot.GetFileDirectURL(strings.TrimSpace(msg.Audio.FileID))
	if err != nil || strings.TrimSpace(fileURL) == "" {
		return out
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	probed, err := probeReplyAudioDetailsWithFFprobe(ctx, strings.TrimSpace(fileURL))
	if err == nil {
		mergeReplyAudioDetails(&out, probed)
		if cacheKey != "" {
			setCachedReplyAudioDetails(cacheKey, out)
		}
	}
	return out
}

func mergeReplyAudioDetails(dst *replyAudioDetails, src replyAudioDetails) {
	if dst == nil {
		return
	}
	if strings.TrimSpace(dst.Title) == "" {
		dst.Title = strings.TrimSpace(src.Title)
	}
	if strings.TrimSpace(dst.Artist) == "" {
		dst.Artist = strings.TrimSpace(src.Artist)
	}
	if strings.TrimSpace(dst.Album) == "" {
		dst.Album = strings.TrimSpace(src.Album)
	}
	if strings.TrimSpace(dst.Year) == "" {
		dst.Year = strings.TrimSpace(src.Year)
	}
	if strings.TrimSpace(dst.Track) == "" {
		dst.Track = strings.TrimSpace(src.Track)
	}
	if strings.TrimSpace(dst.Text) == "" {
		dst.Text = strings.TrimSpace(src.Text)
	}
}

func probeReplyAudioDetailsWithFFprobe(ctx context.Context, input string) (replyAudioDetails, error) {
	cmd := exec.CommandContext(
		ctx,
		"ffprobe",
		"-v", "error",
		"-show_entries", "format_tags=title,artist,album,date,track,text,lyrics,comment",
		"-of", "json",
		input,
	)
	var outBytes bytes.Buffer
	var errBytes bytes.Buffer
	cmd.Stdout = &outBytes
	cmd.Stderr = &errBytes
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errBytes.String())
		if msg == "" {
			msg = err.Error()
		}
		return replyAudioDetails{}, errors.New(msg)
	}
	var payload struct {
		Format struct {
			Tags map[string]string `json:"tags"`
		} `json:"format"`
	}
	if err := json.Unmarshal(outBytes.Bytes(), &payload); err != nil {
		return replyAudioDetails{}, err
	}
	get := func(keys ...string) string {
		for _, key := range keys {
			for k, v := range payload.Format.Tags {
				if strings.EqualFold(strings.TrimSpace(k), strings.TrimSpace(key)) {
					if s := sanitizeReplyAudioTagValue(v, 6000); s != "" {
						return s
					}
				}
			}
		}
		return ""
	}
	return replyAudioDetails{
		Title:  get("title"),
		Artist: get("artist"),
		Album:  get("album"),
		Year:   get("date"),
		Track:  get("track"),
		Text:   pickReplyAudioTextFromTags(payload.Format.Tags),
	}, nil
}

func pickReplyAudioTextFromTags(tags map[string]string) string {
	if len(tags) == 0 {
		return ""
	}
	get := func(keys ...string) string {
		for _, key := range keys {
			for k, v := range tags {
				if strings.EqualFold(strings.TrimSpace(k), strings.TrimSpace(key)) {
					if s := sanitizeReplyAudioTagValue(v, 6000); s != "" {
						return s
					}
				}
			}
		}
		return ""
	}
	if v := get("text", "lyrics", "unsyncedlyrics", "uslt", "comment", "lyricist"); v != "" {
		return v
	}
	for k, v := range tags {
		key := strings.ToLower(strings.TrimSpace(k))
		if strings.HasPrefix(key, "lyrics-") || strings.HasPrefix(key, "uslt") {
			if s := sanitizeReplyAudioTagValue(v, 6000); s != "" {
				return s
			}
		}
	}
	return ""
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

	replyText := ""
	replyUserID := ""
	replyFirstName := ""
	replyUsername := ""
	replyDisplayName := ""
	replyLabel := ""
	replyUserLink := ""
	replySenderTag := ""
	replyAudioTitle := ""
	replyAudioArtist := ""
	replyAudioAlbum := ""
	replyAudioYear := ""
	replyAudioTrack := ""
	replyAudioText := ""
	if msg.ReplyToMessage != nil {
		replyText = firstNonEmptyMessageContent(msg.ReplyToMessage)
		if msg.ReplyToMessage.Audio != nil {
			audioDetails := fetchReplyAudioDetails(bot, msg.ReplyToMessage)
			replyAudioTitle = strings.TrimSpace(audioDetails.Title)
			replyAudioArtist = strings.TrimSpace(audioDetails.Artist)
			replyAudioAlbum = strings.TrimSpace(audioDetails.Album)
			replyAudioYear = strings.TrimSpace(audioDetails.Year)
			replyAudioTrack = strings.TrimSpace(audioDetails.Track)
			replyAudioText = strings.TrimSpace(audioDetails.Text)
			if formatted := strings.TrimSpace(buildReplyAudioText(audioDetails)); formatted != "" {
				replyText = formatted
			}
		}
		if strings.TrimSpace(replyText) == "" {
			log.Printf("reply_text empty chat=%d msg=%d reply_msg=%d has_text=%v has_caption=%v has_audio=%v has_document=%v has_video=%v has_photo=%v has_voice=%v has_sticker=%v",
				msg.Chat.ID,
				msg.MessageID,
				msg.ReplyToMessage.MessageID,
				strings.TrimSpace(msg.ReplyToMessage.Text) != "",
				strings.TrimSpace(msg.ReplyToMessage.Caption) != "",
				msg.ReplyToMessage.Audio != nil,
				msg.ReplyToMessage.Document != nil,
				msg.ReplyToMessage.Video != nil,
				len(msg.ReplyToMessage.Photo) > 0,
				msg.ReplyToMessage.Voice != nil,
				msg.ReplyToMessage.Sticker != nil,
			)
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
	userPortrait := resolveParticipantPortrait(msg.Chat.ID, msg.From.ID)
	userPortraitRemaining := strconv.Itoa(resolveParticipantPortraitRemaining(msg.Chat.ID, msg.From.ID))

	return map[string]string{
		"{{message}}":                 userText,
		"{{user_text}}":               userText,
		"{{user_id}}":                 strconv.FormatInt(msg.From.ID, 10),
		"{{user_first_name}}":         strings.TrimSpace(msg.From.FirstName),
		"{{user_username}}":           userUsername,
		"{{user_display_name}}":       userDisplayName,
		"{{user_label}}":              userLabel,
		"{{sender_tag}}":              senderTagDisplay,
		"{{user_portrait}}":           userPortrait,
		"{{user_portrait_remaining}}": userPortraitRemaining,
		"{{user_link}}":               buildUserLink(msg.From),
		"{{chat_id}}":                 strconv.FormatInt(msg.Chat.ID, 10),
		"{{chat_title}}":              chatTitle,
		"{{reply_text}}":              replyText,
		"{{reply_user_id}}":           replyUserID,
		"{{reply_first_name}}":        replyFirstName,
		"{{reply_username}}":          replyUsername,
		"{{reply_display_name}}":      replyDisplayName,
		"{{reply_label}}":             replyLabel,
		"{{reply_user_link}}":         replyUserLink,
		"{{reply_sender_tag}}":        replySenderTagDisplay,
		"{{reply_audio_title}}":       replyAudioTitle,
		"{{reply_audio_artist}}":      replyAudioArtist,
		"{{reply_audio_album}}":       replyAudioAlbum,
		"{{reply_audio_year}}":        replyAudioYear,
		"{{reply_audio_track}}":       replyAudioTrack,
		"{{reply_audio_text}}":        replyAudioText,
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
	return fmt.Sprintf("<a href=\"tg://user?id=%d\">%s</a>", u.ID, htmlstd.EscapeString(name))
}
