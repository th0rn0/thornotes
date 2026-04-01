package handler

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/th0rn0/thornotes/internal/apperror"
)

func init() {
	gin.SetMode(gin.TestMode)
}

// newTestGinContext creates a gin.Context backed by an httptest.ResponseRecorder.
func newTestGinContext(req *http.Request) (*gin.Context, *httptest.ResponseRecorder) {
	rr := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rr)
	if req != nil {
		c.Request = req
	} else {
		c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
	}
	return c, rr
}

// ─── writeJSON/writeError unit tests ──────────────────────────────────────────

func TestWriteJSON(t *testing.T) {
	c, rr := newTestGinContext(nil)
	writeJSON(c, http.StatusOK, map[string]string{"hello": "world"})
	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Contains(t, rr.Body.String(), "hello")
}

func TestWriteError_AppError(t *testing.T) {
	c, rr := newTestGinContext(nil)
	writeError(c, apperror.NotFound("not found"))
	assert.Equal(t, http.StatusNotFound, rr.Code)
}

func TestWriteError_SentinelNotFound(t *testing.T) {
	c, rr := newTestGinContext(nil)
	writeError(c, apperror.ErrNotFound)
	assert.Equal(t, http.StatusNotFound, rr.Code)
}

func TestWriteError_SentinelConflict(t *testing.T) {
	c, rr := newTestGinContext(nil)
	writeError(c, apperror.ErrConflict)
	assert.Equal(t, http.StatusConflict, rr.Code)
}

func TestWriteError_GenericError(t *testing.T) {
	c, rr := newTestGinContext(nil)
	writeError(c, errors.New("unexpected error"))
	assert.Equal(t, http.StatusInternalServerError, rr.Code)
}

// ─── Auth handler unit tests ──────────────────────────────────────────────────

func TestMe_NoUserInContext(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := NewAuthHandler(nil, nil, false)
	r := gin.New()
	r.GET("/api/v1/auth/me", h.Me)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}

func TestCSRF_NoSessionCookie_Handler(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := NewAuthHandler(nil, nil, false)
	r := gin.New()
	r.GET("/api/v1/csrf", h.CSRF)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/csrf", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}

// ─── readJSON tests ───────────────────────────────────────────────────────────

func TestReadJSON_Valid(t *testing.T) {
	type payload struct {
		Name string `json:"name"`
	}
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"name":"alice"}`))
	req.Header.Set("Content-Type", "application/json")
	c, _ := newTestGinContext(req)
	var p payload
	err := readJSON(c, &p)
	require.NoError(t, err)
	assert.Equal(t, "alice", p.Name)
}

func TestReadJSON_UnknownField(t *testing.T) {
	type payload struct {
		Name string `json:"name"`
	}
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"name":"bob","extra":"field"}`))
	req.Header.Set("Content-Type", "application/json")
	c, _ := newTestGinContext(req)
	var p payload
	err := readJSON(c, &p)
	require.Error(t, err)
}

func TestWriteJSON_EncodeError(t *testing.T) {
	// writeJSON now just calls c.JSON — the response recorder won't error, so
	// we just verify it doesn't panic.
	c, _ := newTestGinContext(nil)
	assert.NotPanics(t, func() {
		writeJSON(c, http.StatusOK, map[string]string{"key": "value"})
	})
}

// ─── ShareHandler unit tests ──────────────────────────────────────────────────

func TestShareHandler_EmptyToken(t *testing.T) {
	// When c.Param("token") is empty, View must return 404.
	// We use a nil service because the empty-token branch returns before any service call.
	gin.SetMode(gin.TestMode)
	h := &ShareHandler{svc: nil, tmpl: nil}

	r := gin.New()
	r.GET("/s/:token", h.View)

	// Use a path that would yield an empty param — but gin requires non-empty
	// path params so we test via a route that calls View with an empty param
	// by directly using a test context.
	req := httptest.NewRequest(http.MethodGet, "/s/", nil)
	rr := httptest.NewRecorder()

	c, _ := gin.CreateTestContext(rr)
	c.Request = req
	// Manually set empty param.
	c.Params = gin.Params{{Key: "token", Value: ""}}
	h.View(c)
	assert.Equal(t, http.StatusNotFound, rr.Code)
}
