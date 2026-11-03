package handler_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/th0rn0/thornotes/internal/handler"
)

type stubHealthDB struct{ checks map[string]string }

func (s stubHealthDB) HealthCheck() map[string]string { return s.checks }

type stubNotesRoot struct {
	healthy bool
	err     string
}

func (s stubNotesRoot) NotesRootHealthy() bool       { return s.healthy }
func (s stubNotesRoot) NotesRootHealthError() string { return s.err }

func doHealthz(t *testing.T, h *handler.HealthHandler) (int, map[string]any) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/healthz", nil)
	h.Check(c)

	var body map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	return w.Code, body
}

// An unreadable or aborted notes root must fail readiness: the index is intact
// but stale, and an operator has to see that before the instance takes writes.
func TestHealthz_UnhealthyNotesRoot_Returns503(t *testing.T) {
	h := handler.NewHealthHandler(
		stubHealthDB{checks: map[string]string{"sqlite": "ok"}},
		handler.WithNotesRootChecker(stubNotesRoot{healthy: false, err: "notes root does not exist"}),
	)
	code, body := doHealthz(t, h)

	assert.Equal(t, http.StatusServiceUnavailable, code)
	assert.Equal(t, "error", body["status"])
	checks, ok := body["checks"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "notes root does not exist", checks["notes_root"])
}

func TestHealthz_HealthyNotesRoot_Returns200(t *testing.T) {
	h := handler.NewHealthHandler(
		stubHealthDB{checks: map[string]string{"sqlite": "ok"}},
		handler.WithNotesRootChecker(stubNotesRoot{healthy: true}),
	)
	code, body := doHealthz(t, h)

	assert.Equal(t, http.StatusOK, code)
	assert.Equal(t, "ok", body["status"])
	checks, ok := body["checks"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "ok", checks["notes_root"])
}

// Without the option the endpoint keeps its previous behaviour exactly.
func TestHealthz_WithoutNotesRootChecker_Unchanged(t *testing.T) {
	h := handler.NewHealthHandler(stubHealthDB{checks: map[string]string{"sqlite": "ok"}})
	code, body := doHealthz(t, h)

	assert.Equal(t, http.StatusOK, code)
	checks, ok := body["checks"].(map[string]any)
	require.True(t, ok)
	assert.NotContains(t, checks, "notes_root")
}
