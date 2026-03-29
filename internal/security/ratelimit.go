package security

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

const (
	rateLimitRequests = 10           // max requests
	rateLimitWindow   = 15 * 60      // per 15 minutes (in seconds), expressed as token refill
	rateLimitCleanup  = 30 * time.Minute
)

type ipLimiter struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

// AuthRateLimiter rate-limits auth endpoints per IP address.
type AuthRateLimiter struct {
	mu           sync.Mutex
	limiters     map[string]*ipLimiter
	trustedProxy *net.IPNet
}

func NewAuthRateLimiter(trustedProxy *net.IPNet) *AuthRateLimiter {
	rl := &AuthRateLimiter{
		limiters:     make(map[string]*ipLimiter),
		trustedProxy: trustedProxy,
	}
	go rl.cleanupLoop()
	return rl
}

// Middleware wraps an HTTP handler with per-IP rate limiting.
func (rl *AuthRateLimiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := rl.extractIP(r)
		limiter := rl.getLimiter(ip)

		if !limiter.Allow() {
			w.Header().Set("Retry-After", "900")
			http.Error(w, `{"error":"too many requests"}`, http.StatusTooManyRequests)
			return
		}

		next.ServeHTTP(w, r)
	})
}

func (rl *AuthRateLimiter) getLimiter(ip string) *rate.Limiter {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	entry, ok := rl.limiters[ip]
	if !ok {
		// Allow burst of rateLimitRequests, refill at 1 token per 90 seconds.
		entry = &ipLimiter{
			limiter: rate.NewLimiter(rate.Every(90*time.Second), rateLimitRequests),
		}
		rl.limiters[ip] = entry
	}
	entry.lastSeen = time.Now()
	return entry.limiter
}

// extractIP returns the real client IP, respecting X-Forwarded-For when a
// trusted proxy CIDR is configured.
func (rl *AuthRateLimiter) extractIP(r *http.Request) string {
	remoteIP, _, _ := net.SplitHostPort(r.RemoteAddr)

	if rl.trustedProxy == nil {
		return remoteIP
	}

	parsedRemote := net.ParseIP(remoteIP)
	if parsedRemote == nil || !rl.trustedProxy.Contains(parsedRemote) {
		return remoteIP
	}

	// Take the leftmost non-trusted IP from XFF chain.
	xff := r.Header.Get("X-Forwarded-For")
	if xff == "" {
		return remoteIP
	}

	parts := strings.Split(xff, ",")
	for i := len(parts) - 1; i >= 0; i-- {
		candidate := strings.TrimSpace(parts[i])
		ip := net.ParseIP(candidate)
		if ip == nil {
			continue
		}
		if !rl.trustedProxy.Contains(ip) {
			return candidate
		}
	}

	return remoteIP
}

// evictStale removes limiters whose lastSeen is before cutoff.
func (rl *AuthRateLimiter) evictStale(cutoff time.Time) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	for ip, entry := range rl.limiters {
		if entry.lastSeen.Before(cutoff) {
			delete(rl.limiters, ip)
		}
	}
}

// cleanupLoop removes limiters not seen in the last 30 minutes.
func (rl *AuthRateLimiter) cleanupLoop() {
	ticker := time.NewTicker(rateLimitCleanup)
	defer ticker.Stop()
	for range ticker.C {
		rl.evictStale(time.Now().Add(-rateLimitCleanup))
	}
}
