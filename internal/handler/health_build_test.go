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

// serveHealth runs one request through the handler and returns the decoded body.
func serveHealth(t *testing.T, h *HealthHandler) map[string]any {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/healthz", h.Check)

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	var body map[string]any
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &body), "body: %s", rr.Body.String())
	return body
}

// The build block is what makes "which commit is this?" a curl. It is
// unauthenticated on purpose: knowing the commit of a public-facing notes app
// is not a secret, and requiring a login to answer it means only someone with
// host access can tell whether a deploy landed.
func TestHealth_BuildVersionIsReported(t *testing.T) {
	h := NewHealthHandler(stubDBHealthOnly{}, WithBuildVersion("9c0ffee1234567890"))

	body := serveHealth(t, h)

	build, ok := body["build"].(map[string]any)
	require.True(t, ok, "expected a build block, got %v", body["build"])
	assert.Equal(t, "9c0ffee1234567890", build["version"])
}

// An unstamped build must say so, not render an empty string that reads like a
// missing field and not "dev" that reads like a real answer.
func TestHealth_BuildVersionUnstampedSaysUnknown(t *testing.T) {
	h := NewHealthHandler(stubDBHealthOnly{})

	body := serveHealth(t, h)

	build, ok := body["build"].(map[string]any)
	require.True(t, ok, "the build block is always present")
	assert.Equal(t, "unknown", build["version"])
}

// failingHealthDB reports one failed check, which must still drive a 503.
type failingHealthDB struct{}

func (failingHealthDB) HealthCheck() map[string]string {
	return map[string]string{"db_read": "connection refused"}
}

// The build block is observability: it must never change the status code, or a
// missing stamp would fail readiness and pull an instance out of service.
func TestHealth_BuildVersionDoesNotAffectStatus(t *testing.T) {
	gin.SetMode(gin.TestMode)

	healthy := NewHealthHandler(stubDBHealthOnly{}, WithBuildVersion(""))
	rr := httptest.NewRecorder()
	r := gin.New()
	r.GET("/healthz", healthy.Check)
	r.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	assert.Equal(t, http.StatusOK, rr.Code, "an unstamped build is still a healthy instance")

	broken := NewHealthHandler(failingHealthDB{}, WithBuildVersion("abc123"))
	rr2 := httptest.NewRecorder()
	r2 := gin.New()
	r2.GET("/healthz", broken.Check)
	r2.ServeHTTP(rr2, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	assert.Equal(t, http.StatusServiceUnavailable, rr2.Code, "a real failure still fails")
}
