package handler

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/th0rn0/thornotes/internal/apperror"
)

// ─── writeJSON/writeError unit tests ──────────────────────────────────────────

func TestWriteJSON(t *testing.T) {
	rr := httptest.NewRecorder()
	writeJSON(rr, http.StatusOK, map[string]string{"hello": "world"})
	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Contains(t, rr.Body.String(), "hello")
}

func TestWriteError_AppError(t *testing.T) {
	rr := httptest.NewRecorder()
	writeError(rr, apperror.NotFound("not found"))
	assert.Equal(t, http.StatusNotFound, rr.Code)
}

func TestWriteError_SentinelNotFound(t *testing.T) {
	rr := httptest.NewRecorder()
	writeError(rr, apperror.ErrNotFound)
	assert.Equal(t, http.StatusNotFound, rr.Code)
}

func TestWriteError_SentinelConflict(t *testing.T) {
	rr := httptest.NewRecorder()
	writeError(rr, apperror.ErrConflict)
	assert.Equal(t, http.StatusConflict, rr.Code)
}

func TestWriteError_GenericError(t *testing.T) {
	rr := httptest.NewRecorder()
	writeError(rr, errors.New("unexpected error"))
	assert.Equal(t, http.StatusInternalServerError, rr.Code)
}

// ─── Auth handler unit tests ──────────────────────────────────────────────────

func TestMe_NoUserInContext(t *testing.T) {
	h := NewAuthHandler(nil, nil, false)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
	rr := httptest.NewRecorder()
	h.Me(rr, req)
	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}

func TestCSRF_NoSessionCookie_Handler(t *testing.T) {
	h := NewAuthHandler(nil, nil, false)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/csrf", nil)
	rr := httptest.NewRecorder()
	h.CSRF(rr, req)
	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}

// ─── readJSON tests ───────────────────────────────────────────────────────────

func TestReadJSON_Valid(t *testing.T) {
	type payload struct {
		Name string `json:"name"`
	}
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"name":"alice"}`))
	req.Header.Set("Content-Type", "application/json")
	var p payload
	err := readJSON(req, &p)
	require.NoError(t, err)
	assert.Equal(t, "alice", p.Name)
}

func TestReadJSON_UnknownField(t *testing.T) {
	type payload struct {
		Name string `json:"name"`
	}
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"name":"bob","extra":"field"}`))
	req.Header.Set("Content-Type", "application/json")
	var p payload
	err := readJSON(req, &p)
	require.Error(t, err)
}

// errWriter always returns an error from Write so that json.Encoder.Encode fails.
type errWriter struct {
	header http.Header
	code   int
}

func (e *errWriter) Header() http.Header {
	if e.header == nil {
		e.header = make(http.Header)
	}
	return e.header
}
func (e *errWriter) WriteHeader(code int) { e.code = code }
func (e *errWriter) Write(_ []byte) (int, error) {
	return 0, errors.New("write failed")
}

func TestWriteJSON_EncodeError(t *testing.T) {
	ew := &errWriter{}
	// Should not panic even when Encode fails.
	assert.NotPanics(t, func() {
		writeJSON(ew, http.StatusOK, map[string]string{"key": "value"})
	})
}

// ─── ShareHandler unit tests ──────────────────────────────────────────────────

func TestShareHandler_EmptyToken(t *testing.T) {
	// When PathValue("token") is empty, View must return 404.
	// We use a nil service because the empty-token branch returns before any service call.
	h := &ShareHandler{svc: nil, tmpl: nil}
	req := httptest.NewRequest(http.MethodGet, "/s/", nil)
	// PathValue defaults to "" if not set.
	rr := httptest.NewRecorder()
	h.View(rr, req)
	assert.Equal(t, http.StatusNotFound, rr.Code)
}
