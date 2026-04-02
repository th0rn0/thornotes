package security

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

func TestRateLimit_AllowsUnderLimit(t *testing.T) {
	rl := NewAuthRateLimiter(nil)
	t.Cleanup(rl.Stop)
	handler := rl.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	for i := 0; i < rateLimitRequests; i++ {
		req := httptest.NewRequest(http.MethodPost, "/", nil)
		req.RemoteAddr = "10.0.0.1:1234"
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		assert.Equal(t, http.StatusOK, rr.Code, "request %d should be allowed", i+1)
	}
}

func TestRateLimit_BlocksAfterLimit(t *testing.T) {
	rl := NewAuthRateLimiter(nil)
	t.Cleanup(rl.Stop)
	handler := rl.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// Exhaust the burst.
	for i := 0; i < rateLimitRequests; i++ {
		req := httptest.NewRequest(http.MethodPost, "/", nil)
		req.RemoteAddr = "10.0.0.2:1234"
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
	}

	// Next request should be rate-limited.
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.RemoteAddr = "10.0.0.2:1234"
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusTooManyRequests, rr.Code)
	assert.Equal(t, "900", rr.Header().Get("Retry-After"))
}

func TestRateLimit_DifferentIPsAreIndependent(t *testing.T) {
	rl := NewAuthRateLimiter(nil)
	t.Cleanup(rl.Stop)
	handler := rl.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// Exhaust IP1.
	for i := 0; i <= rateLimitRequests; i++ {
		req := httptest.NewRequest(http.MethodPost, "/", nil)
		req.RemoteAddr = "10.0.0.3:1234"
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
	}

	// IP2 should still be allowed.
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.RemoteAddr = "10.0.0.4:1234"
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code)
}

func TestRateLimit_TrustedProxy_ExtractsRealIP(t *testing.T) {
	_, proxyNet, _ := net.ParseCIDR("10.0.0.0/8")
	rl := NewAuthRateLimiter(proxyNet)
	t.Cleanup(rl.Stop)
	handler := rl.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// Exhaust using requests that appear from 203.0.113.1 via XFF.
	for i := 0; i <= rateLimitRequests; i++ {
		req := httptest.NewRequest(http.MethodPost, "/", nil)
		req.RemoteAddr = "10.0.0.5:1234" // trusted proxy
		req.Header.Set("X-Forwarded-For", "203.0.113.1")
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
	}

	// Next request from same real IP (203.0.113.1) should be blocked.
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.RemoteAddr = "10.0.0.5:1234"
	req.Header.Set("X-Forwarded-For", "203.0.113.1")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusTooManyRequests, rr.Code)

	// But a different IP through the same proxy should still be allowed.
	req2 := httptest.NewRequest(http.MethodPost, "/", nil)
	req2.RemoteAddr = "10.0.0.5:1234"
	req2.Header.Set("X-Forwarded-For", "203.0.113.99")
	rr2 := httptest.NewRecorder()
	handler.ServeHTTP(rr2, req2)
	assert.Equal(t, http.StatusOK, rr2.Code)
}

func TestRateLimit_UntrustedProxy_IgnoresXFF(t *testing.T) {
	rl := NewAuthRateLimiter(nil) // no trusted proxy
	t.Cleanup(rl.Stop)
	handler := rl.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// Exhaust using RemoteAddr.
	for i := 0; i <= rateLimitRequests; i++ {
		req := httptest.NewRequest(http.MethodPost, "/", nil)
		req.RemoteAddr = "192.168.1.1:1234"
		req.Header.Set("X-Forwarded-For", "1.2.3.4") // attacker tries to spoof
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
	}

	// Even with different XFF, same RemoteAddr should be blocked.
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.RemoteAddr = "192.168.1.1:1234"
	req.Header.Set("X-Forwarded-For", "5.6.7.8") // different spoofed IP
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusTooManyRequests, rr.Code)
}

func TestRateLimit_EvictStale(t *testing.T) {
	rl := NewAuthRateLimiter(nil)
	t.Cleanup(rl.Stop)

	// Add an entry via getLimiter.
	rl.getLimiter("10.1.1.1")

	// Backdate its lastSeen.
	rl.mu.Lock()
	rl.limiters["10.1.1.1"].lastSeen = time.Now().Add(-2 * time.Hour)
	rl.mu.Unlock()

	// Evict with time.Now() as cutoff (entries older than now are evicted).
	rl.evictStale(time.Now())

	rl.mu.Lock()
	_, exists := rl.limiters["10.1.1.1"]
	rl.mu.Unlock()

	assert.False(t, exists, "stale entry should have been evicted")
}

func TestRateLimit_EvictStale_KeepsRecent(t *testing.T) {
	rl := NewAuthRateLimiter(nil)
	t.Cleanup(rl.Stop)

	// Add a fresh entry via getLimiter.
	rl.getLimiter("10.2.2.2")

	// Evict entries older than 1 hour ago — the recent entry should stay.
	rl.evictStale(time.Now().Add(-time.Hour))

	rl.mu.Lock()
	_, exists := rl.limiters["10.2.2.2"]
	rl.mu.Unlock()

	assert.True(t, exists, "recent entry should not be evicted")
}

func TestRateLimit_ExtractIP_NoXFF(t *testing.T) {
	_, proxyNet, _ := net.ParseCIDR("10.0.0.0/8")
	rl := NewAuthRateLimiter(proxyNet)
	t.Cleanup(rl.Stop)

	// Trusted proxy configured, but no XFF header — should use RemoteAddr.
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.RemoteAddr = "10.0.0.1:1234"
	// No X-Forwarded-For header.

	ip := rl.extractIP(req)
	assert.Equal(t, "10.0.0.1", ip)
}

func TestRateLimit_ExtractIP_AllTrustedChain(t *testing.T) {
	_, proxyNet, _ := net.ParseCIDR("10.0.0.0/8")
	rl := NewAuthRateLimiter(proxyNet)
	t.Cleanup(rl.Stop)

	// XFF chain is all trusted IPs — should fall back to RemoteAddr.
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.RemoteAddr = "10.0.0.1:1234"
	req.Header.Set("X-Forwarded-For", "10.0.0.2, 10.0.0.3")

	ip := rl.extractIP(req)
	assert.Equal(t, "10.0.0.1", ip)
}

func TestRateLimit_ExtractIP_UnparsableRemote(t *testing.T) {
	_, proxyNet, _ := net.ParseCIDR("10.0.0.0/8")
	rl := NewAuthRateLimiter(proxyNet)
	t.Cleanup(rl.Stop)

	// RemoteAddr with no host part → SplitHostPort returns "", so net.ParseIP("") == nil.
	// Should fall back to the empty string (remoteIP).
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.RemoteAddr = ":1234" // no host, only port

	ip := rl.extractIP(req)
	// parsedRemote is nil → returns remoteIP (which is "")
	assert.Equal(t, "", ip)
}

func TestRateLimit_ExtractIP_InvalidXFFEntry(t *testing.T) {
	_, proxyNet, _ := net.ParseCIDR("10.0.0.0/8")
	rl := NewAuthRateLimiter(proxyNet)
	t.Cleanup(rl.Stop)

	// XFF header contains an invalid (non-parseable) IP before the real one.
	// The loop should skip it and continue.
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.RemoteAddr = "10.0.0.1:1234" // trusted proxy
	req.Header.Set("X-Forwarded-For", "not-an-ip, 203.0.113.1")

	ip := rl.extractIP(req)
	// "not-an-ip" is skipped; "203.0.113.1" is not trusted → returned.
	assert.Equal(t, "203.0.113.1", ip)
}

func TestRateLimit_ExtractIP_RemoteNotInTrustedRange(t *testing.T) {
	_, proxyNet, _ := net.ParseCIDR("10.0.0.0/8")
	rl := NewAuthRateLimiter(proxyNet)
	t.Cleanup(rl.Stop)

	// RemoteAddr is NOT in the trusted proxy range — XFF must be ignored.
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.RemoteAddr = "192.168.1.1:1234" // not in 10.0.0.0/8
	req.Header.Set("X-Forwarded-For", "1.2.3.4")

	ip := rl.extractIP(req)
	assert.Equal(t, "192.168.1.1", ip)
}

// ── GinMiddleware tests ───────────────────────────────────────────────────────

func TestGinRateLimit_AllowsUnderLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rl := NewAuthRateLimiter(nil)
	t.Cleanup(rl.Stop)

	r := gin.New()
	r.POST("/", rl.GinMiddleware(), func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	for i := 0; i < rateLimitRequests; i++ {
		req := httptest.NewRequest(http.MethodPost, "/", nil)
		req.RemoteAddr = "10.0.1.1:1234"
		rr := httptest.NewRecorder()
		r.ServeHTTP(rr, req)
		assert.Equal(t, http.StatusOK, rr.Code, "request %d should be allowed", i+1)
	}
}

func TestGinRateLimit_BlocksAfterLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rl := NewAuthRateLimiter(nil)
	t.Cleanup(rl.Stop)

	r := gin.New()
	r.POST("/", rl.GinMiddleware(), func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	// Exhaust the burst.
	for i := 0; i < rateLimitRequests; i++ {
		req := httptest.NewRequest(http.MethodPost, "/", nil)
		req.RemoteAddr = "10.0.1.2:1234"
		rr := httptest.NewRecorder()
		r.ServeHTTP(rr, req)
	}

	// Next request should be rate-limited.
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.RemoteAddr = "10.0.1.2:1234"
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusTooManyRequests, rr.Code)
	assert.Equal(t, "900", rr.Header().Get("Retry-After"))
}
