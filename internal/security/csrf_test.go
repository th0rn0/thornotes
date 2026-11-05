package security

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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

func TestSweepExpiredCSRFTokens_RemovesExpired(t *testing.T) {
	live := "sweep-live-session"
	stale := "sweep-stale-session"

	_, err := GenerateCSRFToken(live)
	require.NoError(t, err)
	// Inject a stale entry directly (cannot rely on time.Sleep for a week).
	csrfStore.Store(stale, csrfEntry{token: "old", expires: time.Now().Add(-time.Hour)})

	SweepExpiredCSRFTokens()

	_, liveOK := loadValidCSRFEntry(live)
	assert.True(t, liveOK, "live entry must survive sweep")

	_, staleOK := loadValidCSRFEntry(stale)
	assert.False(t, staleOK, "stale entry must be evicted")
}

func TestLoadValidCSRFEntry_ExpiredEntryIsRejected(t *testing.T) {
	expired := "expired-session"
	csrfStore.Store(expired, csrfEntry{token: "anything", expires: time.Now().Add(-time.Second)})

	_, ok := loadValidCSRFEntry(expired)
	assert.False(t, ok)

	// And expiry sweep should have removed the entry on read.
	_, raw := csrfStore.Load(expired)
	assert.False(t, raw, "expired entry must be deleted on load")
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

// ── Rejection shape (H-06) ────────────────────────────────────────────────────
//
// The SPA tells "another tab rotated the token, refetch and replay" from "you
// are not allowed, stop" by the `code` field alone. A bare 403 cannot carry
// that distinction, so these tests pin the marker as part of the API contract.

// decodeErrorBody parses a JSON error body written by the CSRF middleware.
func decodeErrorBody(t *testing.T, rr *httptest.ResponseRecorder) map[string]string {
	t.Helper()
	var body map[string]string
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &body),
		"403 body must be valid JSON, got %q", rr.Body.String())
	return body
}

// TestCSRFGin_StaleTokenIsDistinguishableAndRefreshRecovers is the multi-tab
// failure mode end to end: the tab holds a token, another tab's page load
// rotates it, the stale write is rejected with a code the client can act on,
// and the token that /api/v1/csrf hands back works on the replay.
func TestCSRFGin_StaleTokenIsDistinguishableAndRefreshRecovers(t *testing.T) {
	const session = "gin-multitab-session"

	stale, err := GenerateCSRFToken(session)
	require.NoError(t, err)
	// Tab A reloads: its GET /api/v1/csrf issues a new token for this session,
	// which is exactly what invalidates the token tab B is holding.
	fresh, err := GenerateCSRFToken(session)
	require.NoError(t, err)
	require.NotEqual(t, stale, fresh, "rotation must actually change the token")

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("{}"))
	req.AddCookie(&http.Cookie{Name: "session", Value: session})
	req.Header.Set("X-CSRF-Token", stale)
	rr := serveGinCSRF(req)

	require.Equal(t, http.StatusForbidden, rr.Code)
	body := decodeErrorBody(t, rr)
	assert.Equal(t, "csrf_invalid", body["code"], "the SPA retries on this literal")
	assert.Equal(t, CSRFErrorCode, body["code"])
	assert.Equal(t, "invalid csrf token", body["error"])
	assert.Equal(t, "application/json; charset=utf-8", rr.Header().Get("Content-Type"))

	// The refetched token succeeds when the client replays the request.
	replay := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("{}"))
	replay.AddCookie(&http.Cookie{Name: "session", Value: session})
	replay.Header.Set("X-CSRF-Token", fresh)
	assert.Equal(t, http.StatusOK, serveGinCSRF(replay).Code)
}

// TestCSRFGin_MissingOrGarbageTokenHasSameShape: every token-value rejection
// carries the marker, so the client's retry condition does not depend on which
// branch tripped.
func TestCSRFGin_MissingOrGarbageTokenHasSameShape(t *testing.T) {
	const session = "gin-rejection-shape"
	_, err := GenerateCSRFToken(session)
	require.NoError(t, err)

	cases := []struct {
		name    string
		header  string
		wantErr string
	}{
		{"missing header", "", "csrf token required"},
		{"garbage header", strings.Repeat("f", 64), "invalid csrf token"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("{}"))
			req.AddCookie(&http.Cookie{Name: "session", Value: session})
			if tc.header != "" {
				req.Header.Set("X-CSRF-Token", tc.header)
			}
			rr := serveGinCSRF(req)

			require.Equal(t, http.StatusForbidden, rr.Code)
			body := decodeErrorBody(t, rr)
			assert.Equal(t, CSRFErrorCode, body["code"])
			assert.Equal(t, tc.wantErr, body["error"])
		})
	}
}

// TestCSRFGin_NoSessionCookieHasNoCode: an absent session is not a stale-token
// case. It must NOT carry the code, because refetching a CSRF token without a
// session fails too — a code here would make the client replay a request that
// cannot succeed and would mask a dead session as a token problem.
func TestCSRFGin_NoSessionCookieHasNoCode(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("{}"))
	req.Header.Set("X-CSRF-Token", "sometoken")
	rr := serveGinCSRF(req)

	require.Equal(t, http.StatusForbidden, rr.Code)
	body := decodeErrorBody(t, rr)
	assert.NotContains(t, body, "code")
	assert.Equal(t, "forbidden", body["error"])
}

// TestCSRF_HandlerVariantKeepsSameShape guards the http.Handler twin used by
// the tests and any non-gin wiring: `http.Error` used to append a newline and
// force a text/plain content type, which broke JSON parsing on the client.
func TestCSRF_HandlerVariantKeepsSameShape(t *testing.T) {
	const session = "handler-rejection-shape"
	_, err := GenerateCSRFToken(session)
	require.NoError(t, err)

	handler := CSRFMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("{}"))
	req.AddCookie(&http.Cookie{Name: "session", Value: session})
	req.Header.Set("X-CSRF-Token", "wrongtoken")

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	require.Equal(t, http.StatusForbidden, rr.Code)
	body := decodeErrorBody(t, rr)
	assert.Equal(t, CSRFErrorCode, body["code"])
	assert.Equal(t, "invalid csrf token", body["error"])
	assert.Equal(t, "application/json; charset=utf-8", rr.Header().Get("Content-Type"))
}
