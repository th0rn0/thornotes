package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// DBHealthChecker reports whether database connections are alive.
type DBHealthChecker interface {
	HealthCheck() map[string]string
}

type HealthHandler struct {
	db DBHealthChecker
}

func NewHealthHandler(db DBHealthChecker) *HealthHandler {
	return &HealthHandler{db: db}
}

// Check handles GET /healthz. Returns 200 when all checks pass, 503 otherwise.
// The endpoint is unauthenticated so load balancers and monitoring can reach it.
func (h *HealthHandler) Check(c *gin.Context) {
	checks := h.db.HealthCheck()
	status := "ok"
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
