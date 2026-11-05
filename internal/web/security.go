package web

import (
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"fedigest/internal/auth"
)

// crossOriginProtect is the CSRF defence: it rejects state-changing
// requests a browser sent from another origin. SameSite=Lax alone doesn't
// stop sibling subdomains. Uses Sec-Fetch-Site, else compares Origin with
// Host; requests with neither aren't from a browser and are allowed. Same
// approach as Go 1.25's http.CrossOriginProtection.
func crossOriginProtect(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}
		if !sameOriginRequest(r) {
			http.Error(w, "cross-origin request blocked", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func sameOriginRequest(r *http.Request) bool {
	switch r.Header.Get("Sec-Fetch-Site") {
	case "same-origin", "none":
		return true
	case "":
		// fall through to the Origin check
	default: // "same-site", "cross-site"
		return false
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return strings.EqualFold(u.Host, r.Host)
}

// peerHost is the direct peer's address without the port.
func peerHost(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// fromProxy reports whether the direct peer is a loopback or private
// address, i.e. the reverse proxy, whose forwarding headers are trusted.
func fromProxy(r *http.Request) bool {
	peer := net.ParseIP(peerHost(r))
	return peer != nil && (peer.IsLoopback() || peer.IsPrivate())
}

// isHTTPS reports whether the visitor reached the site over HTTPS, either
// directly or through the reverse proxy.
func isHTTPS(r *http.Request) bool {
	return r.TLS != nil || (fromProxy(r) && strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https"))
}

// clientIP returns the visitor's IP. Only when the direct peer is a
// private/loopback address (the reverse proxy) is the last X-Forwarded-For
// entry trusted; a public peer can't choose its own IP.
func clientIP(r *http.Request) string {
	host := peerHost(r)
	if fromProxy(r) {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			if ip := strings.TrimSpace(parts[len(parts)-1]); net.ParseIP(ip) != nil {
				return ip
			}
		}
	}
	return host
}

// attemptLimiter counts failed attempts per key (IP or username) within a
// window and locks the key out at its limit. In memory only.
type attemptLimiter struct {
	mu      sync.Mutex
	window  time.Duration
	lockout time.Duration
	now     func() time.Time // tests replace this
	entries map[string]*attemptEntry
}

type attemptEntry struct {
	fails       int
	first       time.Time
	lockedUntil time.Time
}

func newAttemptLimiter(window, lockout time.Duration) *attemptLimiter {
	return &attemptLimiter{window: window, lockout: lockout, now: time.Now, entries: map[string]*attemptEntry{}}
}

// locked reports whether key is locked out, and for how much longer.
func (l *attemptLimiter) locked(key string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.prune(l.now())
	e := l.entries[key]
	if e == nil {
		return false, 0
	}
	if left := e.lockedUntil.Sub(l.now()); left > 0 {
		return true, left
	}
	return false, 0
}

// fail records a failed attempt for key; at limit failures within the
// window the key is locked out.
func (l *attemptLimiter) fail(key string, limit int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.prune(now)
	e := l.entries[key]
	if e == nil || now.Sub(e.first) > l.window {
		e = &attemptEntry{first: now}
		l.entries[key] = e
	}
	e.fails++
	if e.fails >= limit {
		e.lockedUntil = now.Add(l.lockout)
		e.fails = 0
		e.first = now
	}
}

// reset forgets key (after a successful login).
func (l *attemptLimiter) reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.entries, key)
}

// prune drops entries past their window and lockout, so nothing is kept
// longer than needed. Caller holds the lock.
func (l *attemptLimiter) prune(now time.Time) {
	for k, e := range l.entries {
		if now.Sub(e.first) > l.window && now.After(e.lockedUntil) {
			delete(l.entries, k)
		}
	}
}

// Login: 5 failures per IP or 10 per username within 15 minutes lock it
// out for 15 minutes; the per-username limit is higher so a real user
// isn't trivially locked out. Reset emails: 5 per IP per 15 minutes.
const (
	loginIPLimit   = 5
	loginUserLimit = 10
	resetIPLimit   = 5
)

var (
	limiterOnce sync.Once
	limiter     *attemptLimiter
)

func (s *Server) limiter() *attemptLimiter {
	if s.attempts != nil {
		return s.attempts
	}
	limiterOnce.Do(func() { limiter = newAttemptLimiter(15*time.Minute, 15*time.Minute) })
	return limiter
}

// currentSessionID is the session cookie's value, or "".
func currentSessionID(r *http.Request) string {
	if c, err := r.Cookie(auth.CookieName); err == nil {
		return c.Value
	}
	return ""
}

// securityHeaders stops browsers guessing content types and other sites
// framing the pages (the theme editor's preview frames the site itself).
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "SAMEORIGIN")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		next.ServeHTTP(w, r)
	})
}

// uploadsHandler serves uploaded files but never a directory listing.
func uploadsHandler(dir string) http.Handler {
	files := http.FileServer(http.Dir(dir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := path.Clean("/" + r.URL.Path)
		info, err := os.Stat(filepath.Join(dir, filepath.FromSlash(p)))
		if err != nil || info.IsDir() || strings.HasSuffix(r.URL.Path, "/") {
			http.NotFound(w, r)
			return
		}
		files.ServeHTTP(w, r)
	})
}

// localRedirect returns target if it's a path on this site, else "/".
func localRedirect(target string) string {
	if target == "" || strings.ContainsAny(target, "\\\x00\t\r\n") {
		return "/"
	}
	for _, c := range target {
		if c < 0x20 || c == 0x7f {
			return "/"
		}
	}
	u, err := url.Parse(target)
	if err != nil || u.Scheme != "" || u.Host != "" || u.User != nil || !strings.HasPrefix(u.Path, "/") || strings.HasPrefix(u.Path, "//") {
		return "/"
	}
	return target
}
