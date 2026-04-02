package security

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

func init() {
	gin.SetMode(gin.TestMode)
}

// serveGinMiddleware is a helper that runs a gin.HandlerFunc and returns the recorder.
func serveGinMiddleware(mw gin.HandlerFunc, req *http.Request) *httptest.ResponseRecorder {
	r := gin.New()
	r.Any("/", mw, func(c *gin.Context) {
		c.Status(http.StatusOK)
	})
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	return rr
}

func TestSecureHeaders_SetsExpectedHeaders(t *testing.T) {
	handler := SecureHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, "nosniff", rr.Header().Get("X-Content-Type-Options"))
	assert.Equal(t, "DENY", rr.Header().Get("X-Frame-Options"))
	assert.Equal(t, "same-origin", rr.Header().Get("Referrer-Policy"))

	csp := rr.Header().Get("Content-Security-Policy")
	assert.Contains(t, csp, "default-src 'self'")
	assert.Contains(t, csp, "script-src 'self'")
	assert.NotContains(t, csp, "script-src 'self' 'unsafe-inline'", "script-src must not allow unsafe-inline")
	assert.Contains(t, csp, "style-src 'self' 'unsafe-inline'")
	assert.Contains(t, csp, "img-src 'self' data:")
	assert.Contains(t, csp, "font-src 'self'")
}

func TestSecureHeaders_CallsNextHandler(t *testing.T) {
	called := false
	handler := SecureHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodPost, "/api/test", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	assert.True(t, called, "next handler must be called")
	assert.Equal(t, http.StatusNoContent, rr.Code)
}

func TestSecureHeadersMiddleware_SetsHeaders(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := serveGinMiddleware(SecureHeadersMiddleware(), req)

	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, "nosniff", rr.Header().Get("X-Content-Type-Options"))
	assert.Equal(t, "DENY", rr.Header().Get("X-Frame-Options"))
	assert.Equal(t, "same-origin", rr.Header().Get("Referrer-Policy"))
	csp := rr.Header().Get("Content-Security-Policy")
	assert.Contains(t, csp, "default-src 'self'")
}
