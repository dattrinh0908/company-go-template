package http

import (
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	oteltrace "go.opentelemetry.io/otel/trace"

	"order-service/internal/observability"
)

// requestIDHeader is echoed back on every response so a client can quote it in
// a bug report and an operator can find the exact request in the logs.
const requestIDHeader = "X-Request-Id"

const tracerName = "order-service/internal/transport/http"

// RequestID assigns or adopts a request ID and puts it on the context.
// An inbound header is trusted so that an ID assigned by an edge proxy survives
// across services.
func RequestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.GetHeader(requestIDHeader)
		if id == "" {
			id = uuid.NewString()
		}

		c.Header(requestIDHeader, id)
		c.Request = c.Request.WithContext(
			observability.WithRequestID(c.Request.Context(), id),
		)
		c.Next()
	}
}

// Tracing starts a server span per request, continuing an upstream trace when
// the caller supplied traceparent headers.
func Tracing() gin.HandlerFunc {
	tracer := otel.Tracer(tracerName)
	propagator := otel.GetTextMapPropagator()

	return func(c *gin.Context) {
		ctx := propagator.Extract(
			c.Request.Context(),
			propagation.HeaderCarrier(c.Request.Header),
		)

		// FullPath is the route template ("/api/v1/orders/:id"). Using it
		// instead of the raw URL keeps span names low-cardinality.
		route := c.FullPath()
		if route == "" {
			route = "unmatched"
		}

		ctx, span := tracer.Start(ctx, c.Request.Method+" "+route,
			oteltrace.WithSpanKind(oteltrace.SpanKindServer),
			oteltrace.WithAttributes(
				attribute.String("http.request.method", c.Request.Method),
				attribute.String("http.route", route),
				attribute.String("url.path", c.Request.URL.Path),
				attribute.String("client.address", c.ClientIP()),
			),
		)
		defer span.End()

		c.Request = c.Request.WithContext(ctx)
		c.Next()

		status := c.Writer.Status()
		span.SetAttributes(attribute.Int("http.response.status_code", status))
		if status >= http.StatusInternalServerError {
			span.SetStatus(codes.Error, http.StatusText(status))
		}
	}
}

// Logging attaches a request-scoped logger and emits one structured line per
// completed request.
//
// The logger it stores already carries the request ID and trace ID, which is
// why handlers can call observability.LoggerFrom(ctx) and get correlated output
// without threading anything themselves.
func Logging(base *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		ctx := c.Request.Context()

		attrs := []any{
			slog.String("request_id", observability.RequestIDFrom(ctx)),
			slog.String("method", c.Request.Method),
			slog.String("path", c.Request.URL.Path),
		}
		attrs = append(attrs, observability.TraceFields(ctx)...)

		logger := base.With(attrs...)
		c.Request = c.Request.WithContext(observability.WithLogger(ctx, logger))

		c.Next()

		status := c.Writer.Status()
		fields := []any{
			slog.Int("status", status),
			slog.Duration("duration", time.Since(start)),
			slog.Int("bytes", c.Writer.Size()),
		}

		switch {
		case status >= http.StatusInternalServerError:
			logger.ErrorContext(ctx, "request completed", fields...)
		case status >= http.StatusBadRequest:
			logger.WarnContext(ctx, "request completed", fields...)
		default:
			logger.InfoContext(ctx, "request completed", fields...)
		}
	}
}

// Metrics records the RED metrics for each request.
func Metrics(m *observability.HTTPMetrics) gin.HandlerFunc {
	return func(c *gin.Context) {
		route := c.FullPath()
		if route == "" {
			route = "unmatched"
		}

		finish := m.RequestStarted(c.Request.Context(), c.Request.Method, route)
		c.Next()
		finish(c.Writer.Status())
	}
}

// Recovery converts a panic into a 500 without taking the process down, and
// records the stack once, where it is useful.
func Recovery() gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if r := recover(); r != nil {
				ctx := c.Request.Context()
				observability.LoggerFrom(ctx).ErrorContext(ctx, "panic recovered",
					slog.Any("panic", r),
					slog.String("stack", string(debug.Stack())),
				)

				if span := oteltrace.SpanFromContext(ctx); span.IsRecording() {
					span.SetStatus(codes.Error, "panic")
				}

				c.AbortWithStatusJSON(http.StatusInternalServerError, errorResponse{
					Error: ErrorBody{
						Code:      "internal_error",
						Message:   "an unexpected error occurred",
						RequestID: observability.RequestIDFrom(ctx),
					},
				})
			}
		}()

		c.Next()
	}
}
