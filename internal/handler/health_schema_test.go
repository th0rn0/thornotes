package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubDBHealthWithSchema satisfies both DBHealthChecker and SchemaReporter, the
// way db.Pool does in production.
type stubDBHealthWithSchema struct {
	version      int
	maxSupported int
}

func (s *stubDBHealthWithSchema) HealthCheck() map[string]string {
	return map[string]string{"db_read": "ok", "db_write": "ok"}
}

func (s *stubDBHealthWithSchema) SchemaStatus() (version, maxSupported int) {
	return s.version, s.maxSupported
}

// stubDBHealthOnly deliberately does not implement SchemaReporter: callers that
// have no schema information keep the old response shape.
type stubDBHealthOnly struct{}

func (stubDBHealthOnly) HealthCheck() map[string]string {
	return map[string]string{"db_read": "ok", "db_write": "ok"}
}

func healthBody(t *testing.T, h *HealthHandler) (int, map[string]any) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/healthz", nil)
	h.Check(c)

	var body map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body), "response must be JSON: %s", w.Body.String())
	return w.Code, body
}

// TestHealthHandler_SchemaBlock pins the shape an operator reads off a running
// instance before deploying an image: the applied version and the build's floor.
func TestHealthHandler_SchemaBlock(t *testing.T) {
	db := &stubDBHealthWithSchema{version: 8, maxSupported: 8}
	h := NewHealthHandler(db, WithSchemaReporter(db))

	code, body := healthBody(t, h)
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, "ok", body["status"])

	schema, ok := body["schema"].(map[string]any)
	require.True(t, ok, "healthz must carry a schema block, got %v", body)
	assert.Equal(t, float64(8), schema["version"])
	assert.Equal(t, float64(8), schema["max_supported"])
}

// TestHealthHandler_SchemaBlock_ReportsDrift covers the operator's actual use:
// a running instance whose database is behind its build must show both numbers
// so the two can be compared before an image is deployed.
func TestHealthHandler_SchemaBlock_ReportsDrift(t *testing.T) {
	db := &stubDBHealthWithSchema{version: 5, maxSupported: 8}
	h := NewHealthHandler(db, WithSchemaReporter(db))

	_, body := healthBody(t, h)
	schema, ok := body["schema"].(map[string]any)
	require.True(t, ok, "healthz must carry a schema block, got %v", body)
	assert.Equal(t, float64(5), schema["version"])
	assert.Equal(t, float64(8), schema["max_supported"])
}

// TestHealthHandler_NoSchemaReporter_OmitsBlock keeps every existing caller and
// the pre-existing response contract working: without a reporter there is no
// schema block, and the status is unchanged.
func TestHealthHandler_NoSchemaReporter_OmitsBlock(t *testing.T) {
	h := NewHealthHandler(stubDBHealthOnly{})

	code, body := healthBody(t, h)
	require.Equal(t, http.StatusOK, code)
	assert.NotContains(t, body, "schema")
	assert.Equal(t, "ok", body["status"])
}
