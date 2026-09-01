// Package observability wires the three signals — logs, traces and metrics —
// onto one shared configuration and one shared resource description.
//
// The rule the rest of the service relies on: a logger pulled off the request
// context already carries the request ID and the active trace ID, so a log line
// and a span can always be correlated in a backend.
package observability

import (
	"context"
	"log/slog"
	"os"

	"go.opentelemetry.io/otel/trace"

	"order-service/internal/config"
)

// ctxKey is unexported so no other package can collide with our context keys.
type ctxKey struct{ name string }

var (
	loggerKey    = ctxKey{"logger"}
	requestIDKey = ctxKey{"request_id"}
)

// NewLogger builds the root logger from configuration.
func NewLogger(cfg config.Config) *slog.Logger {
	opts := &slog.HandlerOptions{Level: parseLevel(cfg.Observability.LogLevel)}

	var handler slog.Handler
	if cfg.Observability.LogFormat == "text" {
		handler = slog.NewTextHandler(os.Stdout, opts)
	} else {
		handler = slog.NewJSONHandler(os.Stdout, opts)
	}

	return slog.New(handler).With(
		slog.String("service", cfg.App.Name),
		slog.String("env", cfg.App.Env),
		slog.String("version", cfg.App.Version),
	)
}

func parseLevel(level string) slog.Level {
	switch level {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// WithLogger stores a logger on the context.
func WithLogger(ctx context.Context, logger *slog.Logger) context.Context {
	return context.WithValue(ctx, loggerKey, logger)
}

// LoggerFrom returns the context logger, falling back to slog.Default() so a
// caller never has to nil-check.
func LoggerFrom(ctx context.Context) *slog.Logger {
	if logger, ok := ctx.Value(loggerKey).(*slog.Logger); ok && logger != nil {
		return logger
	}
	return slog.Default()
}

// WithRequestID stores the request ID on the context.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey, id)
}

// RequestIDFrom returns the request ID, or "" when none was set.
func RequestIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey).(string)
	return id
}

// TraceFields returns the trace and span IDs of the active span, ready to be
// attached to a logger. It returns nothing when no span is recording, which
// keeps log lines free of useless all-zero IDs.
func TraceFields(ctx context.Context) []any {
	sc := trace.SpanContextFromContext(ctx)
	if !sc.IsValid() {
		return nil
	}
	return []any{
		slog.String("trace_id", sc.TraceID().String()),
		slog.String("span_id", sc.SpanID().String()),
	}
}
