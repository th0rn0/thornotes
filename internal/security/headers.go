package security

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// contentSecurityPolicy is the CSP applied to every response.
//
//   - default-src 'self'              everything from same origin
//   - script-src   'self'             no inline / no eval — blocks XSS even
//     if note content slips past DOMPurify
//   - style-src    'self' 'unsafe-inline' CodeMirror / highlight.js inject
//     inline styles; can't avoid
//   - img-src      'self' data:       icons embed as data URIs
//   - font-src     'self'             self-hosted fonts only
//   - object-src   'none'             no <object>/<embed>/<applet>
//   - base-uri     'self'             prevent <base href> injection that
//     would re-target every relative URL
//   - form-action  'self'             prevent form-action redirect XSS
//   - frame-ancestors 'none'          superset of X-Frame-Options: DENY,
//     blocks clickjacking
const contentSecurityPolicy = "default-src 'self'; " +
	"script-src 'self'; " +
	"style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' data:; " +
	"font-src 'self'; " +
	"object-src 'none'; " +
	"base-uri 'self'; " +
	"form-action 'self'; " +
	"frame-ancestors 'none';"

// SecureHeaders sets recommended security headers on every response.
// Kept for backward compat with tests.
func SecureHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("Content-Security-Policy", contentSecurityPolicy)
		next.ServeHTTP(w, r)
	})
}

// SecureHeadersMiddleware sets recommended security headers for use with gin.
func SecureHeadersMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("X-Content-Type-Options", "nosniff")
		c.Header("X-Frame-Options", "DENY")
		c.Header("Referrer-Policy", "same-origin")
		c.Header("Content-Security-Policy", contentSecurityPolicy)
		c.Next()
	}
}
