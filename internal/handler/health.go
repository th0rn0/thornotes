package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// DBHealthChecker reports whether database connections are alive.
type DBHealthChecker interface {
	HealthCheck() map[string]string
}

// NotesRootHealthChecker reports whether the on-disk notes root can be trusted:
// present, readable, and free of an aborted reconcile pass. It is deliberately
// expressed with primitives so the notes service can satisfy it without
// importing this package.
type NotesRootHealthChecker interface {
	NotesRootHealthy() bool
	NotesRootHealthError() string
}

type HealthHandler struct {
	db    DBHealthChecker
	notes NotesRootHealthChecker
}

// HealthOption configures optional readiness checks.
type HealthOption func(*HealthHandler)

// WithNotesRootChecker adds the notes-root readiness check. Without it the
// endpoint reports database health only, which keeps every existing caller
// working unchanged.
func WithNotesRootChecker(c NotesRootHealthChecker) HealthOption {
	return func(h *HealthHandler) { h.notes = c }
}

func NewHealthHandler(db DBHealthChecker, opts ...HealthOption) *HealthHandler {
	h := &HealthHandler{db: db}
	for _, opt := range opts {
		opt(h)
	}
	return h
}

// Check handles GET /healthz. Returns 200 when all checks pass, 503 otherwise.
// The endpoint is unauthenticated so load balancers and monitoring can reach it.
//
// An unhealthy notes root deliberately fails readiness: an instance whose
// reconciler refused a pass has an intact but stale index, and an operator must
// see that before trusting it with writes.
func (h *HealthHandler) Check(c *gin.Context) {
	checks := h.db.HealthCheck()
	if h.notes != nil {
		if msg := h.notes.NotesRootHealthError(); msg != "" {
			checks["notes_root"] = msg
		} else {
			checks["notes_root"] = "ok"
		}
	}
	status := "ok"
	if len(checks) == 0 {
		status = "error"
	}
	for _, v := range checks {
		if v != "ok" {
			status = "error"
			break
		}
	}
	code := http.StatusOK
	if status == "error" {
		code = http.StatusServiceUnavailable
	}
	c.JSON(code, gin.H{
		"status": status,
		"checks": checks,
	})
}
