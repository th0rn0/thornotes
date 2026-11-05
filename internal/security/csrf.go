package security

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

const (
	csrfTokenBytes = 32
	// csrfTokenTTL bounds how long a CSRF entry stays in memory after its
	// last refresh. It matches the session TTL in internal/auth (7 days) so
	// that a CSRF entry can never outlive the session it protects. Without
	// this bound the in-memory map grows unboundedly when sessions expire
	// without an explicit logout (which is the common case).
	csrfTokenTTL = 7 * 24 * time.Hour

	// CSRFErrorCode is the machine-readable marker returned alongside the 403
	// that rejects a mutating request whose CSRF token is missing, unknown or
	// stale. A bare 403 is ambiguous: the SPA cannot tell "another tab rotated
	// the token, refetch /api/v1/csrf and replay this write" from "you are not
	// allowed, stop". The client matches on this exact string, so it is part of
	// the API contract — change it and web/static/js/app.js must change too.
	//
	// It is attached only to token-value rejections. The missing-session-cookie
	// branch keeps the plain {"error":"forbidden"} body without a code: no
	// token refresh can rescue an absent session, and giving that case a code
	// would make the client replay a request that is guaranteed to fail again.
	CSRFErrorCode = "csrf_invalid"
)

type csrfEntry struct {
	token   string
	expires time.Time
}

// csrfStore maps session token → csrfEntry.
// A real multi-instance deployment would use the DB, but for single-binary
// self-hosted deployment this is sufficient.
var csrfStore sync.Map

// GenerateCSRFToken creates a new CSRF token for the given session token.
func GenerateCSRFToken(sessionToken string) (string, error) {
	b := make([]byte, csrfTokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	token := hex.EncodeToString(b)
	csrfStore.Store(sessionToken, csrfEntry{token: token, expires: time.Now().Add(csrfTokenTTL)})
	return token, nil
}

// loadValidCSRFEntry returns the live CSRF token for sessionToken, or
// ("", false) if the entry is missing or expired. Expired entries are
// removed in-line so they don't accumulate.
func loadValidCSRFEntry(sessionToken string) (string, bool) {
	raw, ok := csrfStore.Load(sessionToken)
	if !ok {
		return "", false
	}
	entry, ok := raw.(csrfEntry)
	if !ok {
		return "", false
	}
	if !entry.expires.IsZero() && time.Now().After(entry.expires) {
		csrfStore.Delete(sessionToken)
		return "", false
	}
	return entry.token, true
}

// writeCSRFRejection writes the 403 body for a rejected token: the human
// message plus the machine-readable CSRFErrorCode. One helper for both
// middlewares so the two shapes cannot drift apart.
func writeCSRFRejection(w http.ResponseWriter, msg string) {
	body, err := json.Marshal(gin.H{"error": msg, "code": CSRFErrorCode})
	if err != nil {
		http.Error(w, `{"error":"forbidden"}`, http.StatusForbidden)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusForbidden)
	_, _ = w.Write(body)
}

// abortCSRFGin is the gin twin of writeCSRFRejection.
func abortCSRFGin(c *gin.Context, msg string) {
	c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": msg, "code": CSRFErrorCode})
}

// CSRFMiddleware validates the X-CSRF-Token header against the session's stored token.
// Must run AFTER SessionMiddleware (needs session cookie to look up token).
// This version wraps an http.Handler for backward compat with tests.
func CSRFMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Only mutating methods need CSRF protection.
		if r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions {
			next.ServeHTTP(w, r)
			return
		}

		cookie, err := r.Cookie("session")
		if err != nil || cookie.Value == "" {
			http.Error(w, `{"error":"forbidden"}`, http.StatusForbidden)
			return
		}

		provided := r.Header.Get("X-CSRF-Token")
		if provided == "" {
			writeCSRFRejection(w, "csrf token required")
			return
		}

		stored, ok := loadValidCSRFEntry(cookie.Value)
		if !ok {
			writeCSRFRejection(w, "csrf token not found")
			return
		}

		if subtle.ConstantTimeCompare([]byte(provided), []byte(stored)) != 1 {
			writeCSRFRejection(w, "invalid csrf token")
			return
		}

		next.ServeHTTP(w, r)
	})
}

// CSRFGinMiddleware validates the X-CSRF-Token header for use with gin.
// Must run AFTER SessionMiddleware (needs session cookie to look up token).
func CSRFGinMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		// Only mutating methods need CSRF protection.
		if c.Request.Method == http.MethodGet || c.Request.Method == http.MethodHead || c.Request.Method == http.MethodOptions {
			c.Next()
			return
		}

		cookie, err := c.Cookie("session")
		if err != nil || cookie == "" {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "forbidden"})
			return
		}

		provided := c.Request.Header.Get("X-CSRF-Token")
		if provided == "" {
			abortCSRFGin(c, "csrf token required")
			return
		}

		stored, ok := loadValidCSRFEntry(cookie)
		if !ok {
			abortCSRFGin(c, "csrf token not found")
			return
		}

		if subtle.ConstantTimeCompare([]byte(provided), []byte(stored)) != 1 {
			abortCSRFGin(c, "invalid csrf token")
			return
		}

		c.Next()
	}
}

// InvalidateCSRFToken removes the CSRF token for a session (call on logout).
func InvalidateCSRFToken(sessionToken string) {
	csrfStore.Delete(sessionToken)
}

// SweepExpiredCSRFTokens removes every entry whose expiry has passed. Intended
// to be called on a ticker by long-running processes so that the in-memory map
// reclaims slots from sessions that timed out without an explicit logout.
func SweepExpiredCSRFTokens() {
	now := time.Now()
	csrfStore.Range(func(k, v any) bool {
		entry, ok := v.(csrfEntry)
		if !ok {
			csrfStore.Delete(k)
			return true
		}
		if !entry.expires.IsZero() && now.After(entry.expires) {
			csrfStore.Delete(k)
		}
		return true
	})
}
