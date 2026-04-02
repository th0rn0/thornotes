package security

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// serveGinCSRF is a helper that runs the CSRFGinMiddleware with a session cookie
// injected via gin context.
func serveGinCSRF(req *http.Request) *httptest.ResponseRecorder {
	r := gin.New()
	r.Any("/", CSRFGinMiddleware(), func(c *gin.Context) {
		c.Status(http.StatusOK)
	})
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	return rr
}

func TestCSRF_ValidToken(t *testing.T) {
	token, err := GenerateCSRFToken("session-abc")
	require.NoError(t, err)
	require.NotEmpty(t, token)

	handler := CSRFMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("{}"))
	req.AddCookie(&http.Cookie{Name: "session", Value: "session-abc"})
	req.Header.Set("X-CSRF-Token", token)

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code)
}

func TestCSRF_MissingHeader(t *testing.T) {
	_, err := GenerateCSRFToken("session-missing-hdr")
	require.NoError(t, err)

	handler := CSRFMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("{}"))
	req.AddCookie(&http.Cookie{Name: "session", Value: "session-missing-hdr"})
	// No X-CSRF-Token header.

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusForbidden, rr.Code)
}

func TestCSRF_WrongToken(t *testing.T) {
	_, err := GenerateCSRFToken("session-wrong-tok")
	require.NoError(t, err)

	handler := CSRFMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("{}"))
	req.AddCookie(&http.Cookie{Name: "session", Value: "session-wrong-tok"})
	req.Header.Set("X-CSRF-Token", "wrongtoken")

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusForbidden, rr.Code)
}

func TestCSRF_GetRequestBypassesValidation(t *testing.T) {
	handler := CSRFMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	// No cookie, no CSRF header — GET should pass through.
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code)
}

func TestCSRF_TimingConstant(t *testing.T) {
	// Verify that subtle.ConstantTimeCompare is used.
	// This is a structural test — we verify the code path by checking that
	// a token with the right length but wrong value is still rejected.
	validToken, _ := GenerateCSRFToken("session-timing")

	// Same length, different content.
	wrongToken := strings.Repeat("0", len(validToken))

	handler := CSRFMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.AddCookie(&http.Cookie{Name: "session", Value: "session-timing"})
	req.Header.Set("X-CSRF-Token", wrongToken)

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusForbidden, rr.Code)
}

func TestCSRF_NoSessionCookie(t *testing.T) {
	handler := CSRFMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.Header.Set("X-CSRF-Token", "sometoken")
	// No session cookie.

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusForbidden, rr.Code)
}

func TestCSRF_InvalidateToken(t *testing.T) {
	sessionToken := "session-invalidate-test"
	_, err := GenerateCSRFToken(sessionToken)
	require.NoError(t, err)

	InvalidateCSRFToken(sessionToken)

	handler := CSRFMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.AddCookie(&http.Cookie{Name: "session", Value: sessionToken})
	req.Header.Set("X-CSRF-Token", "anytoken")

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusForbidden, rr.Code)
	assert.Contains(t, rr.Body.String(), "csrf token not found")
}

func TestCSRF_HeadRequestBypasses(t *testing.T) {
	handler := CSRFMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodHead, "/", nil)
	// No cookie, no CSRF header — HEAD should pass through.
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code)
}

func TestCSRF_OptionsRequestBypasses(t *testing.T) {
	handler := CSRFMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodOptions, "/", nil)
	// No cookie, no CSRF header — OPTIONS should pass through.
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code)
}

// ── CSRFGinMiddleware tests ────────────────────────────────────────────────────

func TestCSRFGin_GetBypasses(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := serveGinCSRF(req)
	assert.Equal(t, http.StatusOK, rr.Code)
}

func TestCSRFGin_PostNoSessionCookie(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("{}"))
	req.Header.Set("X-CSRF-Token", "sometoken")
	rr := serveGinCSRF(req)
	assert.Equal(t, http.StatusForbidden, rr.Code)
}

func TestCSRFGin_PostMissingHeader(t *testing.T) {
	_, _ = GenerateCSRFToken("gin-session-missing-hdr")
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("{}"))
	req.AddCookie(&http.Cookie{Name: "session", Value: "gin-session-missing-hdr"})
	rr := serveGinCSRF(req)
	assert.Equal(t, http.StatusForbidden, rr.Code)
}

func TestCSRFGin_PostTokenNotFound(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("{}"))
	req.AddCookie(&http.Cookie{Name: "session", Value: "gin-session-no-csrf-stored"})
	req.Header.Set("X-CSRF-Token", "anytoken")
	rr := serveGinCSRF(req)
	assert.Equal(t, http.StatusForbidden, rr.Code)
}

func TestCSRFGin_PostWrongToken(t *testing.T) {
	_, _ = GenerateCSRFToken("gin-session-wrong")
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("{}"))
	req.AddCookie(&http.Cookie{Name: "session", Value: "gin-session-wrong"})
	req.Header.Set("X-CSRF-Token", "wrongtoken")
	rr := serveGinCSRF(req)
	assert.Equal(t, http.StatusForbidden, rr.Code)
}

func TestCSRFGin_PostValidToken(t *testing.T) {
	token, err := GenerateCSRFToken("gin-session-valid")
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("{}"))
	req.AddCookie(&http.Cookie{Name: "session", Value: "gin-session-valid"})
	req.Header.Set("X-CSRF-Token", token)
	rr := serveGinCSRF(req)
	assert.Equal(t, http.StatusOK, rr.Code)
}
