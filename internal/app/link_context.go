package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	readability "github.com/go-shiori/go-readability"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

var linkContextURLRe = regexp.MustCompile(`https?://[^\s<>()\[\]{}"']+`)
var linkContextScriptRe = regexp.MustCompile(`(?is)<script[^>]*>.*?</script>`)
var linkContextStyleRe = regexp.MustCompile(`(?is)<style[^>]*>.*?</style>`)
var linkContextTagRe = regexp.MustCompile(`(?is)<[^>]+>`)
var linkContextSpaceRe = regexp.MustCompile(`\s+`)
var linkContextSplitRe = regexp.MustCompile(`[.!?;:,\n\r\t]+`)

type linkContextCacheEntry struct {
	title     string
	text      string
	extra     string
	expiresAt time.Time
}

var linkContextCache = struct {
	mu    sync.RWMutex
	items map[string]linkContextCacheEntry
}{
	items: make(map[string]linkContextCacheEntry),
}

func buildLinkContextForMessage(msg *tgbotapi.Message) string {
	if msg == nil {
		return ""
	}
	if !envBool("GPT_LINK_CONTEXT_ENABLED", true) {
		return ""
	}

	maxURLs := envInt("GPT_LINK_CONTEXT_MAX_URLS", 2)
	if maxURLs <= 0 {
		maxURLs = 2
	}
	maxChars := envInt("GPT_LINK_CONTEXT_MAX_CHARS", 3000)
	if maxChars <= 0 {
		maxChars = 3000
	}

	parts := []string{
		strings.TrimSpace(firstNonEmptyUserText(msg)),
	}
	if msg.ReplyToMessage != nil {
		parts = append(parts, strings.TrimSpace(firstNonEmptyUserText(msg.ReplyToMessage)))
	}
	var joined strings.Builder
	for _, p := range parts {
		if p == "" {
			continue
		}
		if joined.Len() > 0 {
			joined.WriteString("\n")
		}
		joined.WriteString(p)
	}

	urls := extractUniqueHTTPURLs(joined.String())
	if len(urls) == 0 {
		return ""
	}
	if len(urls) > maxURLs {
		urls = urls[:maxURLs]
	}

	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()

	out := make([]string, 0, len(urls))
	for _, raw := range urls {
		title, text, extra, err := fetchLinkContextParts(ctx, raw, maxChars)
		if err != nil {
			if debugTriggerLogEnabled {
				log.Printf("link context skip url=%q err=%v", clipLogText(raw, 160), err)
			}
			continue
		}
		if strings.TrimSpace(text) == "" && strings.TrimSpace(extra) == "" {
			continue
		}

		block := fmt.Sprintf("URL: %s\nЗаголовок: %s", raw, strings.TrimSpace(title))
		if strings.TrimSpace(text) != "" {
			block += "\nТекст:\n" + strings.TrimSpace(text)
		}
		if strings.TrimSpace(extra) != "" {
			block += "\n\nДополнительно из исходника (без дублей):\n" + strings.TrimSpace(extra)
		}
		out = append(out, block)
	}

	return strings.TrimSpace(strings.Join(out, "\n\n---\n\n"))
}

func extractUniqueHTTPURLs(input string) []string {
	if strings.TrimSpace(input) == "" {
		return nil
	}
	matches := linkContextURLRe.FindAllString(input, -1)
	if len(matches) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(matches))
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		u := strings.TrimSpace(strings.TrimRight(m, ".,!?:;"))
		if u == "" {
			continue
		}
		if _, ok := seen[u]; ok {
			continue
		}
		seen[u] = struct{}{}
		out = append(out, u)
	}
	return out
}

func fetchLinkContextParts(ctx context.Context, rawURL string, maxChars int) (string, string, string, error) {
	u, err := sanitizePublicHTTPURL(rawURL)
	if err != nil {
		return "", "", "", err
	}
	key := u.String()
	if t, txt, extra, ok := getLinkContextCache(key); ok {
		return t, txt, extra, nil
	}

	body, err := fetchHTMLBody(ctx, key)
	if err != nil {
		return "", "", "", err
	}

	title, mainText := parseReadableText(body, u)
	rawClean := cleanHTMLToText(string(body))

	if strings.TrimSpace(mainText) == "" {
		mainText = rawClean
		if title == "" {
			title = strings.TrimSpace(u.String())
		}
	}

	extra := buildRemainderWithoutBase(rawClean, title, mainText)
	mainText = clipRunes(mainText, maxChars)
	extra = clipRunes(extra, maxChars)

	setLinkContextCache(key, title, mainText, extra, 30*time.Minute)
	return title, mainText, extra, nil
}

func fetchHTMLBody(ctx context.Context, rawURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "trigger-admin-bot/1.0 (+readability)")
	req.Header.Set("Accept", "text/html,application/xhtml+xml")

	client := &http.Client{
		Timeout: 10 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("too many redirects")
			}
			_, err := sanitizePublicHTTPURL(req.URL.String())
			return err
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("http status %d", resp.StatusCode)
	}
	ct := strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Type")))
	if ct != "" && !strings.Contains(ct, "text/html") && !strings.Contains(ct, "application/xhtml+xml") {
		return nil, fmt.Errorf("unsupported content-type %q", ct)
	}

	const maxBody = 1 << 20 // 1 MiB
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, err
	}
	return body, nil
}

func parseReadableText(body []byte, u *url.URL) (string, string) {
	article, err := readability.FromReader(bytes.NewReader(body), u)
	if err != nil {
		return "", ""
	}
	return strings.TrimSpace(article.Title), strings.TrimSpace(article.TextContent)
}

func buildRemainderWithoutBase(rawClean, title, base string) string {
	if strings.TrimSpace(rawClean) == "" {
		return ""
	}
	exclude := make(map[string]struct{})
	for _, f := range splitByPunct(base) {
		n := normalizeFragment(f)
		if len([]rune(n)) < 20 {
			continue
		}
		exclude[n] = struct{}{}
	}
	for _, f := range splitByPunct(title) {
		n := normalizeFragment(f)
		if len([]rune(n)) < 20 {
			continue
		}
		exclude[n] = struct{}{}
	}

	seen := map[string]struct{}{}
	kept := make([]string, 0)
	for _, f := range splitByPunct(rawClean) {
		n := normalizeFragment(f)
		if len([]rune(n)) < 25 {
			continue
		}
		if _, ok := exclude[n]; ok {
			continue
		}
		if _, ok := seen[n]; ok {
			continue
		}
		seen[n] = struct{}{}
		kept = append(kept, strings.TrimSpace(f))
	}
	return strings.TrimSpace(strings.Join(kept, ". "))
}

func splitByPunct(s string) []string {
	parts := linkContextSplitRe.Split(s, -1)
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		out = append(out, p)
	}
	return out
}

func normalizeFragment(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = linkContextSpaceRe.ReplaceAllString(s, " ")
	return s
}

func cleanHTMLToText(s string) string {
	s = linkContextScriptRe.ReplaceAllString(s, " ")
	s = linkContextStyleRe.ReplaceAllString(s, " ")
	s = linkContextTagRe.ReplaceAllString(s, " ")
	s = strings.ReplaceAll(s, "&nbsp;", " ")
	s = strings.ReplaceAll(s, "&quot;", "\"")
	s = strings.ReplaceAll(s, "&amp;", "&")
	s = strings.ReplaceAll(s, "&lt;", "<")
	s = strings.ReplaceAll(s, "&gt;", ">")
	s = linkContextSpaceRe.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}

func clipRunes(s string, maxChars int) string {
	if maxChars <= 0 {
		return strings.TrimSpace(s)
	}
	r := []rune(strings.TrimSpace(s))
	if len(r) <= maxChars {
		return string(r)
	}
	return string(r[:maxChars]) + "…"
}

func sanitizePublicHTTPURL(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, err
	}
	if u == nil || u.Scheme == "" || u.Host == "" {
		return nil, errors.New("invalid url")
	}
	scheme := strings.ToLower(strings.TrimSpace(u.Scheme))
	if scheme != "http" && scheme != "https" {
		return nil, errors.New("unsupported scheme")
	}
	host := strings.TrimSpace(u.Hostname())
	if host == "" {
		return nil, errors.New("empty host")
	}
	if ip := net.ParseIP(host); ip != nil {
		if isBlockedImageHostIP(ip) {
			return nil, errors.New("blocked host ip")
		}
		return u, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("resolve host: %w", err)
	}
	if len(addrs) == 0 {
		return nil, errors.New("host has no ip")
	}
	for _, addr := range addrs {
		if isBlockedImageHostIP(addr.IP) {
			return nil, errors.New("blocked host ip")
		}
	}
	return u, nil
}

func getLinkContextCache(key string) (string, string, string, bool) {
	now := time.Now()
	linkContextCache.mu.RLock()
	entry, ok := linkContextCache.items[key]
	linkContextCache.mu.RUnlock()
	if !ok || now.After(entry.expiresAt) {
		return "", "", "", false
	}
	return entry.title, entry.text, entry.extra, true
}

func setLinkContextCache(key, title, text, extra string, ttl time.Duration) {
	if ttl <= 0 {
		ttl = 30 * time.Minute
	}
	linkContextCache.mu.Lock()
	linkContextCache.items[key] = linkContextCacheEntry{
		title:     title,
		text:      text,
		extra:     extra,
		expiresAt: time.Now().Add(ttl),
	}
	linkContextCache.mu.Unlock()
}
