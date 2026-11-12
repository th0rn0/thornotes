package handler_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/th0rn0/thornotes/internal/handler"
)

// stubStartup satisfies handler.StartupProbe: reconciling is the state a booting
// instance reports while the startup reconcile is still running.
type stubStartup struct{ reconciling bool }

func (s stubStartup) StartupReconciling() bool { return s.reconciling }

// TestHealthz_StartupPhase_Reconciling covers the readiness signal a load
// balancer and an operator both read during a boot: 503 (not in rotation) and a
// status that says *why* — the index is being built, not the database is down.
func TestHealthz_StartupPhase_Reconciling(t *testing.T) {
	h := handler.NewHealthHandler(
		stubHealthDB{checks: map[string]string{"sqlite": "ok"}},
		handler.WithNotesRootChecker(stubNotesRoot{healthy: true}),
		handler.WithStartupProbe(stubStartup{reconciling: true}),
	)
	code, body := doHealthz(t, h)

	assert.Equal(t, http.StatusServiceUnavailable, code,
		"a reconciling instance must not be reported ready to serve traffic")
	assert.Equal(t, "reconciling", body["status"])

	phase, ok := body["startup"].(map[string]any)
	require.True(t, ok, "healthz must carry a startup block while reconciling, got %v", body)
	assert.Equal(t, "reconciling", phase["phase"])
}

// TestHealthz_StartupPhase_Ready is the steady state: 200, "ok", and the phase
// stated explicitly rather than left to inference.
func TestHealthz_StartupPhase_Ready(t *testing.T) {
	h := handler.NewHealthHandler(
		stubHealthDB{checks: map[string]string{"sqlite": "ok"}},
		handler.WithStartupProbe(stubStartup{reconciling: false}),
	)
	code, body := doHealthz(t, h)

	assert.Equal(t, http.StatusOK, code)
	assert.Equal(t, "ok", body["status"])

	phase, ok := body["startup"].(map[string]any)
	require.True(t, ok, "healthz must carry a startup block once the probe is wired, got %v", body)
	assert.Equal(t, "ready", phase["phase"])
}

// TestHealthz_FailureOutranksReconciling: when the volume is unreadable *and* the
// instance is still reconciling, the answer is "error". "Reconciling" only asks an
// operator to wait, so letting it mask a refused pass would hide the one thing
// that needs a human.
func TestHealthz_FailureOutranksReconciling(t *testing.T) {
	h := handler.NewHealthHandler(
		stubHealthDB{checks: map[string]string{"sqlite": "ok"}},
		handler.WithNotesRootChecker(stubNotesRoot{healthy: false, err: "last reconcile aborted: notes root is missing"}),
		handler.WithStartupProbe(stubStartup{reconciling: true}),
	)
	code, body := doHealthz(t, h)

	assert.Equal(t, http.StatusServiceUnavailable, code)
	assert.Equal(t, "error", body["status"])

	phase, ok := body["startup"].(map[string]any)
	require.True(t, ok, "the phase must still be reported next to the failure, got %v", body)
	assert.Equal(t, "reconciling", phase["phase"])
}

// TestHealthz_NoStartupProbe_OmitsBlock keeps the response shape of every caller
// that has no startup pass to report unchanged.
func TestHealthz_NoStartupProbe_OmitsBlock(t *testing.T) {
	h := handler.NewHealthHandler(stubHealthDB{checks: map[string]string{"sqlite": "ok"}})
	code, body := doHealthz(t, h)

	assert.Equal(t, http.StatusOK, code)
	assert.NotContains(t, body, "startup")
}
