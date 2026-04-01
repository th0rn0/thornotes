package security

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"sync"

	"github.com/gin-gonic/gin"
)

const csrfTokenBytes = 32

// csrfStore maps session token → CSRF token.
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
	csrfStore.Store(sessionToken, token)
	return token, nil
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
			http.Error(w, `{"error":"csrf token required"}`, http.StatusForbidden)
			return
		}

		stored, ok := csrfStore.Load(cookie.Value)
		if !ok {
			http.Error(w, `{"error":"csrf token not found"}`, http.StatusForbidden)
			return
		}

		storedStr, _ := stored.(string)
		if subtle.ConstantTimeCompare([]byte(provided), []byte(storedStr)) != 1 {
			http.Error(w, `{"error":"invalid csrf token"}`, http.StatusForbidden)
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
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "csrf token required"})
			return
		}

		stored, ok := csrfStore.Load(cookie)
		if !ok {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "csrf token not found"})
			return
		}

		storedStr, _ := stored.(string)
		if subtle.ConstantTimeCompare([]byte(provided), []byte(storedStr)) != 1 {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "invalid csrf token"})
			return
		}

		c.Next()
	}
}

// InvalidateCSRFToken removes the CSRF token for a session (call on logout).
func InvalidateCSRFToken(sessionToken string) {
	csrfStore.Delete(sessionToken)
}
