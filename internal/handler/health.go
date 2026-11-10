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

// SchemaReporter reports the migration version stamped in the database and the
// newest migration the running build embeds. An operator compares the two
// numbers — and the image's co.thornotes.schema.max label — before deploying,
// so this is observability, not a second gate: a database ahead of the binary
// makes the process exit at startup, it never reaches this endpoint.
type SchemaReporter interface {
	SchemaStatus() (version, maxSupported int)
}

type HealthHandler struct {
	db      DBHealthChecker
	notes   NotesRootHealthChecker
	schema  SchemaReporter
	version string
}

// HealthOption configures optional readiness checks.
type HealthOption func(*HealthHandler)

// WithNotesRootChecker adds the notes-root readiness check. Without it the
// endpoint reports database health only, which keeps every existing caller
// working unchanged.
func WithNotesRootChecker(c NotesRootHealthChecker) HealthOption {
	return func(h *HealthHandler) { h.notes = c }
}

// WithBuildVersion adds the build block to /healthz: the version string injected
// at build time (CI passes the commit SHA, a local build leaves "dev").
//
// It exists because nothing unauthenticated could previously say which commit a
// running instance was serving. The front-end asset digest is a hash of the
// assets, so three consecutive merges that touch no JS serve byte-identical
// digests — it cannot distinguish them, and reading it as proof of a deployment
// is how a verification turns into a guess.
func WithBuildVersion(version string) HealthOption {
	return func(h *HealthHandler) { h.version = version }
}

// WithSchemaReporter adds the schema block to /healthz. Without it the endpoint
// reports health checks only, which keeps every existing caller working
// unchanged.
func WithSchemaReporter(s SchemaReporter) HealthOption {
	return func(h *HealthHandler) { h.schema = s }
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
	resp := gin.H{
		"status": status,
		"checks": checks,
	}
	// The schema block lets an operator compare a running instance against an
	// image before deploying it. It never affects the status code: a database
	// ahead of the binary exits at startup, so it cannot be reported here.
	// The build block is never empty: an unstamped build reports "unknown" rather
	// than an empty string, so a caller cannot mistake "dev" for a real version.
	build := h.version
	if build == "" {
		build = "unknown"
	}
	resp["build"] = gin.H{"version": build}
	if h.schema != nil {
		version, maxSupported := h.schema.SchemaStatus()
		resp["schema"] = gin.H{
			"version":       version,
			"max_supported": maxSupported,
		}
	}
	c.JSON(code, resp)
}
