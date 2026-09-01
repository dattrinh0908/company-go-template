package http

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"order-service/internal/config"
	"order-service/internal/domain"
)

// healthTimeout bounds a dependency check so a hung database cannot make the
// probe itself hang; an orchestrator would then kill the pod for the wrong
// reason.
const healthTimeout = 2 * time.Second

// HealthHandler serves the liveness and readiness probes.
type HealthHandler struct {
	app config.App
	db  domain.HealthChecker
}

// NewHealthHandler wires the probes to their dependencies.
func NewHealthHandler(app config.App, db domain.HealthChecker) *HealthHandler {
	return &HealthHandler{app: app, db: db}
}

// Register mounts the probes at the router root. They sit outside /api/v1
// because probes are infrastructure, not a versioned public API.
func (h *HealthHandler) Register(r gin.IRouter) {
	r.GET("/livez", h.live)
	r.GET("/readyz", h.ready)
	// /health is kept as an alias for the original scaffold's route.
	r.GET("/health", h.ready)
}

// live reports that the process is running. It deliberately checks nothing
// else: a failing dependency must not cause a restart loop.
func (h *HealthHandler) live(c *gin.Context) {
	respond(c, http.StatusOK, gin.H{
		"status":  "ok",
		"service": h.app.Name,
		"version": h.app.Version,
	})
}

// ready reports whether the service can serve traffic, which means every
// dependency it cannot work without is reachable.
func (h *HealthHandler) ready(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), healthTimeout)
	defer cancel()

	db := h.db.Health(ctx)

	status := http.StatusOK
	overall := "ok"
	if db["status"] != "up" {
		status = http.StatusServiceUnavailable
		overall = "degraded"
	}

	respond(c, status, gin.H{
		"status":  overall,
		"service": h.app.Name,
		"version": h.app.Version,
		"checks":  gin.H{"database": db},
	})
}
