package http

import (
	"log/slog"
	"net/http"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"

	"order-service/internal/config"
	"order-service/internal/domain"
	"order-service/internal/observability"
)

// Deps is everything the HTTP layer needs from the rest of the process.
// Grouping it in one struct keeps NewRouter's signature stable as the service
// grows and makes the layer's dependencies explicit at a glance.
type Deps struct {
	Config  config.Config
	Logger  *slog.Logger
	Metrics *observability.HTTPMetrics
	Orders  *domain.OrderService
	DB      domain.HealthChecker
}

// NewRouter builds the fully configured Gin engine.
func NewRouter(deps Deps) *gin.Engine {
	if deps.Config.App.IsProduction() {
		gin.SetMode(gin.ReleaseMode)
	}

	// gin.New rather than gin.Default: the default logger writes unstructured
	// text, and this service logs through slog.
	r := gin.New()

	// Order matters. RequestID and Tracing run first so every later middleware
	// and handler can read the IDs; Recovery sits after Logging so a panic is
	// logged with the request already on the context.
	r.Use(
		RequestID(),
		Tracing(),
		Logging(deps.Logger),
		Metrics(deps.Metrics),
		Recovery(),
		cors.New(cors.Config{
			AllowOrigins:     deps.Config.HTTP.CORSOrigins,
			AllowMethods:     []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodOptions},
			AllowHeaders:     []string{"Accept", "Authorization", "Content-Type", requestIDHeader},
			ExposeHeaders:    []string{requestIDHeader},
			AllowCredentials: true,
		}),
	)

	NewHealthHandler(deps.Config.App, deps.DB).Register(r)

	// Versioned from day one: adding /api/v2 later must not require moving v1.
	v1 := r.Group("/api/v1")
	NewOrderHandler(deps.Orders).Register(v1)

	r.NoRoute(func(c *gin.Context) {
		c.JSON(http.StatusNotFound, errorResponse{Error: ErrorBody{
			Code:      "not_found",
			Message:   "no route matches " + c.Request.Method + " " + c.Request.URL.Path,
			RequestID: observability.RequestIDFrom(c.Request.Context()),
		}})
	})

	return r
}
