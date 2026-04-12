package match

import (
	crand "crypto/rand"
	mrand "math/rand"
	"regexp"
	"strings"
	"sync"
	"time"

	"trigger-admin-bot/internal/model"
)

func NormalizeMatchType(v string) string {
	v = strings.TrimSpace(strings.ToLower(v))
	switch v {
	case "full", "equals", "exact":
		return "full"
	case "partial", "contains":
		return "partial"
	case "regex", "regexp":
		return "regex"
	case "starts", "prefix", "startswith":
		return "starts"
	case "ends", "suffix", "endswith":
		return "ends"
	case "idle":
		return "idle"
	case "new_member", "new-member":
		return "new_member"
	default:
		return v
	}
}

func TriggerMatches(t model.Trigger, incoming string) bool {
	ok, _ := TriggerMatchCapture(t, incoming)
	return ok
}

func TriggerMatchCapture(t model.Trigger, incoming string) (bool, string) {
	needle := strings.TrimSpace(t.MatchText)
	hay := strings.TrimSpace(incoming)
	if NormalizeMatchType(t.MatchType) == "new_member" {
		return false, ""
	}
	if hay == "" {
		return false, ""
	}
	// Empty match_text means "match any non-empty message".
	// This is useful for reply-driven GPT triggers.
	if needle == "" {
		return true, ""
	}
	switch NormalizeMatchType(t.MatchType) {
	case "idle":
		// Idle mode is not a text matcher: it is evaluated separately in runtime loop.
		return false, ""
	case "partial":
		if !t.CaseSensitive {
			needle = strings.ToLower(needle)
			hay = strings.ToLower(hay)
		}
		return strings.Contains(hay, needle), ""
	case "starts":
		if !t.CaseSensitive {
			needle = strings.ToLower(needle)
			hay = strings.ToLower(hay)
		}
		return strings.HasPrefix(hay, needle), ""
	case "ends":
		if !t.CaseSensitive {
			needle = strings.ToLower(needle)
			hay = strings.ToLower(hay)
		}
		return strings.HasSuffix(hay, needle), ""
	case "regex":
		needle = stripLeadingCaseInsensitiveFlag(needle)
		if !t.CaseSensitive && !strings.HasPrefix(needle, "(?i)") {
			needle = "(?i)" + needle
		}
		re := CompileRegex(needle)
		if re == nil {
			return false, ""
		}
		sub := re.FindStringSubmatch(hay)
		if len(sub) == 0 {
			return false, ""
		}
		capture := pickBestCapture(sub)
		return true, capture
	default:
		if !t.CaseSensitive {
			needle = strings.ToLower(needle)
			hay = strings.ToLower(hay)
		}
		return hay == needle, ""
	}
}

func stripLeadingCaseInsensitiveFlag(pattern string) string {
	p := strings.TrimSpace(pattern)
	for strings.HasPrefix(p, "(?i)") {
		p = strings.TrimPrefix(p, "(?i)")
		p = strings.TrimSpace(p)
	}
	return p
}

func StripLeadingCaseInsensitiveFlag(pattern string) string {
	return stripLeadingCaseInsensitiveFlag(pattern)
}

func pickBestCapture(sub []string) string {
	if len(sub) <= 1 {
		return strings.TrimSpace(sub[0])
	}
	best := ""
	for i := 1; i < len(sub); i++ {
		v := strings.TrimSpace(sub[i])
		if v == "" {
			continue
		}
		if len(v) > len(best) {
			best = v
		}
	}
	if best != "" {
		return best
	}
	return strings.TrimSpace(sub[0])
}

var regexGlobalCache sync.Map // map[string]*regexp.Regexp

func CompileRegex(pattern string) *regexp.Regexp {
	if v, ok := regexGlobalCache.Load(pattern); ok {
		if re, ok2 := v.(*regexp.Regexp); ok2 {
			return re
		}
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil
	}
	regexGlobalCache.Store(pattern, re)
	return re
}

func BenchmarkRegex100US(re *regexp.Regexp) int64 {
	if re == nil {
		return 0
	}
	const iterations = 1000
	sample := randomBenchmarkText(512)
	start := time.Now()
	for i := 0; i < iterations; i++ {
		_ = re.MatchString(sample)
	}
	return time.Since(start).Microseconds()
}

func randomBenchmarkText(n int) string {
	if n <= 0 {
		n = 64
	}
	const alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_- .,;:!?/\\|[](){}+=*&^%$#@~"
	buf := make([]byte, n)
	if _, err := crand.Read(buf); err != nil {
		for i := range buf {
			buf[i] = alphabet[mrand.Intn(len(alphabet))]
		}
		return string(buf)
	}
	for i := range buf {
		buf[i] = alphabet[int(buf[i])%len(alphabet)]
	}
	return string(buf)
}
