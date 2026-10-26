package web

import (
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// limiter is an in-memory token bucket keyed by an arbitrary string (e.g. "login|1.2.3.4").
// Each key holds up to `burst` tokens and refills one token every `every`.
type limiter struct {
	mu       sync.Mutex
	now      func() time.Time
	buckets  map[string]*bucket
	lastGC   time.Time
	maxItems int
	off      bool // tests turn limiting off except where they exercise it
}

type bucket struct {
	tokens float64
	last   time.Time
}

func newLimiter() *limiter {
	return &limiter{now: time.Now, buckets: map[string]*bucket{}, maxItems: 50000}
}

// allow takes one token from key's bucket. If none is left it returns how long to wait.
func (l *limiter) allow(key string, burst int, every time.Duration) (bool, time.Duration) {
	if l.off {
		return true, 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.gc(now)
	b := l.buckets[key]
	if b == nil {
		if len(l.buckets) >= l.maxItems {
			return false, every // refuse new keys rather than grow without bound
		}
		b = &bucket{tokens: float64(burst), last: now}
		l.buckets[key] = b
	}
	b.tokens += float64(now.Sub(b.last)) / float64(every)
	if b.tokens > float64(burst) {
		b.tokens = float64(burst)
	}
	b.last = now
	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	return false, time.Duration((1 - b.tokens) * float64(every))
}

// gc drops buckets that have been idle long enough to be full again; at most every few minutes.
func (l *limiter) gc(now time.Time) {
	if now.Sub(l.lastGC) < 5*time.Minute {
		return
	}
	l.lastGC = now
	for k, b := range l.buckets {
		if now.Sub(b.last) > time.Hour {
			delete(l.buckets, k)
		}
	}
}

// rule describes one named limit.
type rule struct {
	name    string
	burst   int
	every   time.Duration
	perUser bool // key by signed-in user when there is one, otherwise by client IP
}

var (
	ruleLogin    = rule{"login", 10, 30 * time.Second, false}
	ruleRegister = rule{"register", 5, 12 * time.Minute, false}
	ruleOAuth    = rule{"oauth", 20, 3 * time.Second, false}
	rulePublish  = rule{"publish", 10, 2 * time.Minute, true}
	ruleWrite    = rule{"write", 60, time.Second, true}
	ruleReport   = rule{"report", 5, 12 * time.Minute, true}
	ruleTokens   = rule{"tokens", 10, 6 * time.Minute, true}
	rulePassword = rule{"password", 10, 6 * time.Minute, true}
	ruleDownload = rule{"download", 60, 500 * time.Millisecond, false}
	ruleAPIRead  = rule{"apiread", 120, 500 * time.Millisecond, false}
	ruleGlobal   = rule{"global", 300, 200 * time.Millisecond, false}
)

// limit wraps h so that requests beyond the rule's rate get 429 with Retry-After.
func (s *Server) limit(rl rule, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.allowRequest(w, r, rl) {
			return
		}
		h(w, r)
	}
}

func (s *Server) allowRequest(w http.ResponseWriter, r *http.Request, rl rule) bool {
	key := rl.name + "|ip:" + s.clientIP(r)
	if u := userFrom(r); rl.perUser && u != nil {
		key = rl.name + "|user:" + strconv.FormatInt(u.ID, 10)
	}
	ok, wait := s.limiter.allow(key, rl.burst, rl.every)
	if ok {
		return true
	}
	secs := int(wait/time.Second) + 1
	w.Header().Set("Retry-After", strconv.Itoa(secs))
	if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/index/") || wantsJSON(r) {
		writeJSON(w, http.StatusTooManyRequests, map[string]any{"error": "too many requests; retry in " + strconv.Itoa(secs) + "s"})
	} else {
		http.Error(w, "Too many requests. Please slow down and try again shortly.", http.StatusTooManyRequests)
	}
	return false
}

// globalLimit applies a generous per-IP cap to every request except static assets and health checks.
func (s *Server) globalLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/static/") || r.URL.Path == "/healthz" || s.allowRequest(w, r, ruleGlobal) {
			next.ServeHTTP(w, r)
		}
	})
}

// clientIP returns the caller's address. X-Forwarded-For is trusted only when the direct peer
// is one of the configured proxies (the reverse proxy sets it; clients can't forge the last hop).
func (s *Server) clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer := net.ParseIP(host)
	if peer != nil && s.trustedProxy(peer) {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			if ip := net.ParseIP(strings.TrimSpace(parts[len(parts)-1])); ip != nil {
				return ip.String()
			}
		}
	}
	return host
}

func (s *Server) trustedProxy(ip net.IP) bool {
	for _, n := range s.cfg.TrustedProxies {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}
