package app

import (
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// adminSessionTTLFromEnv keeps admin sessions short by default. Long-lived
// cookies without rotation are a real risk once the panel is proxied publicly.
func adminSessionTTLFromEnv() time.Duration {
	hours := envInt("ADMIN_SESSION_TTL_HOURS", 12)
	if hours < 1 {
		hours = 1
	}
	if hours > 24*30 {
		hours = 24 * 30
	}
	return time.Duration(hours) * time.Hour
}

// adminCookieSecure: cookies must be Secure even behind a TLS-terminating proxy,
// where r.TLS is always nil.
func adminCookieSecure(r *http.Request) bool {
	if envBool("ADMIN_COOKIE_INSECURE", false) {
		return false
	}
	return true
}

// requestIsDirectLoopback reports whether the request reached the process
// directly from this host (no reverse proxy in front of it).
func requestIsDirectLoopback(r *http.Request) bool {
	if r == nil {
		return false
	}
	if strings.TrimSpace(r.Header.Get("X-Forwarded-For")) != "" ||
		strings.TrimSpace(r.Header.Get("X-Real-IP")) != "" ||
		strings.TrimSpace(r.Header.Get("Forwarded")) != "" {
		return false
	}
	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err != nil {
		host = strings.TrimSpace(r.RemoteAddr)
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// adminSetupAllowed guards the first-run password setup: either a bootstrap
// token from the environment is required, or the request must come from the
// local machine without a proxy in front. Otherwise anyone who can reach the
// panel (nginx proxies it publicly) would own it before the operator does.
func adminSetupAllowed(r *http.Request, providedToken string) bool {
	expected := strings.TrimSpace(os.Getenv("ADMIN_BOOTSTRAP_TOKEN"))
	provided := strings.TrimSpace(providedToken)
	if expected != "" {
		return subtleConstantTimeEqual(expected, provided)
	}
	return requestIsDirectLoopback(r)
}

// originAllowed is a cheap CSRF backstop: state-changing requests must not come
// from a foreign origin.
func originAllowed(r *http.Request) bool {
	if r == nil {
		return false
	}
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		ref := strings.TrimSpace(r.Header.Get("Referer"))
		if ref == "" {
			// Non-browser clients (curl, scripts) send neither header.
			return true
		}
		u, err := url.Parse(ref)
		if err != nil {
			return false
		}
		origin = u.Scheme + "://" + u.Host
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false
	}
	return strings.EqualFold(u.Host, strings.TrimSpace(r.Host))
}

// Failed login throttling: 10 failures per 15 minutes per client address.
const (
	loginFailureWindow = 15 * time.Minute
	loginFailureLimit  = 10
)

var loginFailureState = struct {
	mu    sync.Mutex
	items map[string]loginFailureEntry
}{
	items: make(map[string]loginFailureEntry),
}

type loginFailureEntry struct {
	count     int
	expiresAt time.Time
}

func loginThrottled(r *http.Request) bool {
	key := authRequestAddress(r)
	now := time.Now()
	loginFailureState.mu.Lock()
	defer loginFailureState.mu.Unlock()
	entry, ok := loginFailureState.items[key]
	if !ok || now.After(entry.expiresAt) {
		delete(loginFailureState.items, key)
		return false
	}
	return entry.count >= loginFailureLimit
}

func loginRecordFailure(r *http.Request) {
	key := authRequestAddress(r)
	now := time.Now()
	loginFailureState.mu.Lock()
	defer loginFailureState.mu.Unlock()
	entry := loginFailureState.items[key]
	if now.After(entry.expiresAt) {
		entry = loginFailureEntry{}
	}
	entry.count++
	entry.expiresAt = now.Add(loginFailureWindow)
	loginFailureState.items[key] = entry
	if len(loginFailureState.items) > 4096 {
		for k, v := range loginFailureState.items {
			if now.After(v.expiresAt) {
				delete(loginFailureState.items, k)
			}
		}
	}
	log.Printf("admin login failed addr=%s failures_in_window=%d", key, entry.count)
}

func loginRecordSuccess(r *http.Request) {
	key := authRequestAddress(r)
	loginFailureState.mu.Lock()
	delete(loginFailureState.items, key)
	loginFailureState.mu.Unlock()
}

func authRequestAddress(r *http.Request) string {
	if r == nil {
		return "unknown"
	}
	// X-Real-IP is set by our nginx from $remote_addr and cannot be spoofed by
	// the client. X-Forwarded-For is client-appendable, so only its last element
	// (added by our proxy) is trusted.
	if realIP := strings.TrimSpace(r.Header.Get("X-Real-IP")); realIP != "" {
		return realIP
	}
	if xff := strings.TrimSpace(r.Header.Get("X-Forwarded-For")); xff != "" {
		parts := strings.Split(xff, ",")
		return strings.TrimSpace(parts[len(parts)-1])
	}
	return strings.TrimSpace(r.RemoteAddr)
}

// firstNonEmpty returns the first non-blank string.
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func subtleConstantTimeEqual(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	var diff byte
	for i := 0; i < len(a); i++ {
		diff |= a[i] ^ b[i]
	}
	return diff == 0
}
