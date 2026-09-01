package observability

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// HTTPMetrics is the RED set (Rate, Errors, Duration) for inbound requests.
// Instruments are created once at startup rather than per request, which is
// what the OTel API expects.
type HTTPMetrics struct {
	requests metric.Int64Counter
	duration metric.Float64Histogram
	inFlight metric.Int64UpDownCounter
}

// NewHTTPMetrics registers the inbound HTTP instruments.
func NewHTTPMetrics(meter metric.Meter) (*HTTPMetrics, error) {
	requests, err := meter.Int64Counter(
		"http.server.request.count",
		metric.WithDescription("Number of inbound HTTP requests"),
		metric.WithUnit("{request}"),
	)
	if err != nil {
		return nil, fmt.Errorf("create request counter: %w", err)
	}

	duration, err := meter.Float64Histogram(
		"http.server.request.duration",
		metric.WithDescription("Duration of inbound HTTP requests"),
		metric.WithUnit("s"),
	)
	if err != nil {
		return nil, fmt.Errorf("create duration histogram: %w", err)
	}

	inFlight, err := meter.Int64UpDownCounter(
		"http.server.active_requests",
		metric.WithDescription("Number of in-flight inbound HTTP requests"),
		metric.WithUnit("{request}"),
	)
	if err != nil {
		return nil, fmt.Errorf("create in-flight counter: %w", err)
	}

	return &HTTPMetrics{requests: requests, duration: duration, inFlight: inFlight}, nil
}

// RequestStarted increments the in-flight gauge and returns the function that
// records the completed request. Attributes use the route template rather than
// the raw path so that /orders/:id does not explode cardinality.
func (m *HTTPMetrics) RequestStarted(ctx context.Context, method, route string) func(status int) {
	if m == nil {
		return func(int) {}
	}

	base := metric.WithAttributes(
		attribute.String("http.request.method", method),
		attribute.String("http.route", route),
	)
	m.inFlight.Add(ctx, 1, base)
	start := time.Now()

	return func(status int) {
		attrs := metric.WithAttributes(
			attribute.String("http.request.method", method),
			attribute.String("http.route", route),
			attribute.Int("http.response.status_code", status),
		)
		m.inFlight.Add(ctx, -1, base)
		m.requests.Add(ctx, 1, attrs)
		m.duration.Record(ctx, time.Since(start).Seconds(), attrs)
	}
}
