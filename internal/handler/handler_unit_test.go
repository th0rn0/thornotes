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
	"github.com/th0rn0/thornotes/internal/model"
	"github.com/th0rn0/thornotes/internal/notes"
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

// ─── MCP utility function unit tests ─────────────────────────────────────────

func TestItoa_Zero(t *testing.T) {
	assert.Equal(t, "0", itoa(0))
}

func TestItoa_Positive(t *testing.T) {
	assert.Equal(t, "12345", itoa(12345))
}

func TestIsAppErr_Nil(t *testing.T) {
	var target *apperror.AppError
	assert.False(t, isAppErr(nil, &target))
}

func TestIsAppErr_AppError(t *testing.T) {
	e := apperror.NotFound("not found")
	var target *apperror.AppError
	assert.True(t, isAppErr(e, &target))
	assert.Equal(t, e, target)
}

func TestIsAppErr_NonAppError(t *testing.T) {
	var target *apperror.AppError
	assert.False(t, isAppErr(errors.New("plain error"), &target))
}

func TestErrorString_AppError(t *testing.T) {
	e := apperror.NotFound("resource missing")
	assert.Equal(t, "resource missing", errorString(e))
}

func TestErrorString_GenericError(t *testing.T) {
	assert.Equal(t, "internal error", errorString(errors.New("boom")))
}

// ─── HistoryHandler validation unit tests ────────────────────────────────────

func newHistoryRouter(user *model.User, svc *notes.Service) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	h := NewHistoryHandler(svc)
	r.Use(func(c *gin.Context) {
		c.Set("user", user)
		c.Next()
	})
	r.GET("/notes/:id/history", h.List)
	r.GET("/notes/:id/history/:sha", h.At)
	r.POST("/notes/:id/history/:sha/restore", h.Restore)
	return r
}

func TestHistoryList_InvalidID(t *testing.T) {
	user := &model.User{ID: 1}
	r := newHistoryRouter(user, nil)
	req := httptest.NewRequest(http.MethodGet, "/notes/notanid/history", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestHistoryList_InvalidLimit(t *testing.T) {
	user := &model.User{ID: 1}
	r := newHistoryRouter(user, nil)
	req := httptest.NewRequest(http.MethodGet, "/notes/1/history?limit=bad", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestHistoryList_NegativeLimit(t *testing.T) {
	user := &model.User{ID: 1}
	r := newHistoryRouter(user, nil)
	req := httptest.NewRequest(http.MethodGet, "/notes/1/history?limit=-1", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestHistoryAt_InvalidID(t *testing.T) {
	user := &model.User{ID: 1}
	r := newHistoryRouter(user, nil)
	req := httptest.NewRequest(http.MethodGet, "/notes/notanid/history/abc1234", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestHistoryAt_ShortSHA(t *testing.T) {
	user := &model.User{ID: 1}
	r := newHistoryRouter(user, nil)
	req := httptest.NewRequest(http.MethodGet, "/notes/1/history/abc", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestHistoryRestore_InvalidID(t *testing.T) {
	user := &model.User{ID: 1}
	r := newHistoryRouter(user, nil)
	req := httptest.NewRequest(http.MethodPost, "/notes/notanid/history/abc1234/restore",
		strings.NewReader(`{"content_hash":"abc"}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestHistoryRestore_ShortSHA(t *testing.T) {
	user := &model.User{ID: 1}
	r := newHistoryRouter(user, nil)
	req := httptest.NewRequest(http.MethodPost, "/notes/1/history/abc/restore",
		strings.NewReader(`{"content_hash":"abc"}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestHistoryRestore_BadJSON(t *testing.T) {
	user := &model.User{ID: 1}
	r := newHistoryRouter(user, nil)
	req := httptest.NewRequest(http.MethodPost, "/notes/1/history/abc1234/restore",
		strings.NewReader("notjson"))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

// ─── MoveNote handler validation ────────────────────────────────────────────

func newMoveNoteRouter(user *model.User, svc *notes.Service) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	h := NewNotesHandler(svc)
	r.Use(func(c *gin.Context) {
		c.Set("user", user)
		c.Next()
	})
	r.PATCH("/notes/:id/move", h.Move)
	return r
}

func TestMoveNote_InvalidID(t *testing.T) {
	user := &model.User{ID: 1}
	r := newMoveNoteRouter(user, nil)
	req := httptest.NewRequest(http.MethodPatch, "/notes/notanid/move",
		strings.NewReader(`{"folder_id":null}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestMoveNote_BadJSON(t *testing.T) {
	user := &model.User{ID: 1}
	r := newMoveNoteRouter(user, nil)
	req := httptest.NewRequest(http.MethodPatch, "/notes/1/move",
		strings.NewReader("notjson"))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

// ─── MoveFolder handler validation ──────────────────────────────────────────

func newMoveFolderRouter(user *model.User, svc *notes.Service) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	h := NewFoldersHandler(svc)
	r.Use(func(c *gin.Context) {
		c.Set("user", user)
		c.Next()
	})
	r.PATCH("/folders/:id/move", h.Move)
	return r
}

func TestMoveFolder_InvalidID(t *testing.T) {
	user := &model.User{ID: 1}
	r := newMoveFolderRouter(user, nil)
	req := httptest.NewRequest(http.MethodPatch, "/folders/notanid/move",
		strings.NewReader(`{"parent_id":null}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestMoveFolder_BadJSON(t *testing.T) {
	user := &model.User{ID: 1}
	r := newMoveFolderRouter(user, nil)
	req := httptest.NewRequest(http.MethodPatch, "/folders/1/move",
		strings.NewReader("notjson"))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusBadRequest, rr.Code)
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
