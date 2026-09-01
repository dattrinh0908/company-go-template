package observability

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"

	"order-service/internal/config"
)

// Telemetry owns the tracer and meter providers and knows how to drain them.
type Telemetry struct {
	TracerProvider trace.TracerProvider
	MeterProvider  metric.MeterProvider

	shutdownFuncs []func(context.Context) error
}

// Tracer returns a named tracer from the configured provider.
func (t *Telemetry) Tracer(name string) trace.Tracer { return t.TracerProvider.Tracer(name) }

// Meter returns a named meter from the configured provider.
func (t *Telemetry) Meter(name string) metric.Meter { return t.MeterProvider.Meter(name) }

// Shutdown flushes and stops every provider, joining all errors so one broken
// exporter does not hide another.
func (t *Telemetry) Shutdown(ctx context.Context) error {
	var errs []error
	// Reverse order: providers are torn down before the exporters they feed.
	for i := len(t.shutdownFuncs) - 1; i >= 0; i-- {
		if err := t.shutdownFuncs[i](ctx); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// NewTelemetry configures tracing and metrics.
//
// When no OTLP endpoint is configured the providers are still real, so spans
// are created and trace IDs still reach the logs; they are simply never
// exported. That keeps local development identical in shape to production
// without requiring a collector on a laptop.
func NewTelemetry(ctx context.Context, cfg config.Config, logger *slog.Logger) (*Telemetry, error) {
	res, err := newResource(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("build otel resource: %w", err)
	}

	t := &Telemetry{}

	// Propagators must be set regardless of export, so trace context still
	// flows across service boundaries.
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	tracerProvider, err := newTracerProvider(ctx, cfg, res)
	if err != nil {
		return nil, err
	}
	t.TracerProvider = tracerProvider
	t.shutdownFuncs = append(t.shutdownFuncs, tracerProvider.Shutdown)
	otel.SetTracerProvider(tracerProvider)

	meterProvider, err := newMeterProvider(ctx, cfg, res)
	if err != nil {
		return nil, err
	}
	t.MeterProvider = meterProvider
	t.shutdownFuncs = append(t.shutdownFuncs, meterProvider.Shutdown)
	otel.SetMeterProvider(meterProvider)

	if cfg.Observability.ExportEnabled() {
		logger.Info("telemetry export enabled",
			slog.String("otlp_endpoint", cfg.Observability.OTLPEndpoint),
			slog.Float64("sample_ratio", cfg.Observability.SampleRatio),
		)
	} else {
		logger.Info("telemetry export disabled; spans are created but not exported")
	}

	return t, nil
}

func newResource(ctx context.Context, cfg config.Config) (*resource.Resource, error) {
	return resource.New(ctx,
		resource.WithFromEnv(),
		resource.WithTelemetrySDK(),
		resource.WithProcessRuntimeDescription(),
		resource.WithAttributes(
			semconv.ServiceName(cfg.Observability.ServiceName),
			semconv.ServiceVersion(cfg.App.Version),
			attribute.String("deployment.environment", cfg.App.Env),
		),
	)
}

func newTracerProvider(ctx context.Context, cfg config.Config, res *resource.Resource) (*sdktrace.TracerProvider, error) {
	opts := []sdktrace.TracerProviderOption{
		sdktrace.WithResource(res),
		// ParentBased keeps a sampling decision consistent across a whole
		// distributed trace instead of re-rolling it per service.
		sdktrace.WithSampler(sdktrace.ParentBased(
			sdktrace.TraceIDRatioBased(cfg.Observability.SampleRatio),
		)),
	}

	if cfg.Observability.ExportEnabled() {
		exporterOpts := []otlptracegrpc.Option{
			otlptracegrpc.WithEndpoint(cfg.Observability.OTLPEndpoint),
		}
		if cfg.Observability.OTLPInsecure {
			exporterOpts = append(exporterOpts, otlptracegrpc.WithInsecure())
		}
		exporter, err := otlptracegrpc.New(ctx, exporterOpts...)
		if err != nil {
			return nil, fmt.Errorf("create otlp trace exporter: %w", err)
		}
		opts = append(opts, sdktrace.WithBatcher(exporter))
	}

	return sdktrace.NewTracerProvider(opts...), nil
}

func newMeterProvider(ctx context.Context, cfg config.Config, res *resource.Resource) (*sdkmetric.MeterProvider, error) {
	opts := []sdkmetric.Option{sdkmetric.WithResource(res)}

	if cfg.Observability.ExportEnabled() {
		exporterOpts := []otlpmetricgrpc.Option{
			otlpmetricgrpc.WithEndpoint(cfg.Observability.OTLPEndpoint),
		}
		if cfg.Observability.OTLPInsecure {
			exporterOpts = append(exporterOpts, otlpmetricgrpc.WithInsecure())
		}
		exporter, err := otlpmetricgrpc.New(ctx, exporterOpts...)
		if err != nil {
			return nil, fmt.Errorf("create otlp metric exporter: %w", err)
		}
		opts = append(opts, sdkmetric.WithReader(
			sdkmetric.NewPeriodicReader(exporter, sdkmetric.WithInterval(15*time.Second)),
		))
	}

	return sdkmetric.NewMeterProvider(opts...), nil
}
