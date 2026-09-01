// Command api is the HTTP entry point for the order service.
//
// main is the only place where concrete types meet: it loads configuration,
// constructs each layer, hands the dependencies downward and owns the
// lifecycle. Every other package receives interfaces and structs it did not
// build, which is what makes them testable in isolation.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"order-service/internal/config"
	"order-service/internal/domain"
	"order-service/internal/observability"
	"order-service/internal/repository"
	transporthttp "order-service/internal/transport/http"
)

// shutdownGrace bounds the whole teardown sequence, telemetry flush included.
const shutdownGrace = 30 * time.Second

// version is stamped at build time with -ldflags "-X main.version=...".
// An explicit APP_VERSION in the environment still wins over it.
var version = "dev"

func main() {
	if err := run(); err != nil {
		// slog is used even here so startup failures land in the same log
		// stream as everything else.
		slog.Error("fatal", slog.Any("error", err))
		os.Exit(1)
	}
}

// run holds the real body of main so that every failure path can return an
// error and still let deferred cleanup execute. os.Exit in main would skip it.
func run() error {
	// Cancelled on SIGINT/SIGTERM; this context is the shutdown signal for the
	// entire process.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if os.Getenv("APP_VERSION") == "" {
		cfg.App.Version = version
	}

	// The logger already carries service, env and version on every line, so
	// repeating them here would only duplicate the keys.
	logger := observability.NewLogger(cfg)
	slog.SetDefault(logger)
	logger.Info("starting")

	telemetry, err := observability.NewTelemetry(ctx, cfg, logger)
	if err != nil {
		return fmt.Errorf("init telemetry: %w", err)
	}
	defer shutdownTelemetry(telemetry, logger)

	metrics, err := observability.NewHTTPMetrics(telemetry.Meter(cfg.App.Name))
	if err != nil {
		return fmt.Errorf("init http metrics: %w", err)
	}

	db, err := repository.NewPostgres(ctx, cfg.DB)
	if err != nil {
		return fmt.Errorf("connect to database: %w", err)
	}
	defer db.Close()
	logger.Info("database connected", slog.String("host", cfg.DB.Host), slog.String("database", cfg.DB.Name))

	// The dependency direction of the whole service, in four lines:
	// repository implements the domain port, the domain service consumes it,
	// and the transport consumes the domain service.
	orderRepo := repository.NewOrderRepository(db)
	orderService := domain.NewOrderService(orderRepo, logger)

	router := transporthttp.NewRouter(transporthttp.Deps{
		Config:  cfg,
		Logger:  logger,
		Metrics: metrics,
		Orders:  orderService,
		DB:      db,
	})
	server := transporthttp.NewServer(cfg.HTTP, router, logger)

	// The server runs in its own goroutine so main can wait on either a signal
	// or a server failure, whichever comes first.
	serverErr := make(chan error, 1)
	go func() { serverErr <- server.Start() }()

	select {
	case err := <-serverErr:
		if err != nil {
			return err
		}
	case <-ctx.Done():
		logger.Info("shutdown signal received")
		stop() // restore default handling, so a second Ctrl+C forces an exit

		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
		defer cancel()

		if err := server.Shutdown(shutdownCtx); err != nil {
			return err
		}
		// Drain the server goroutine so its error is not lost.
		if err := <-serverErr; err != nil && !errors.Is(err, context.Canceled) {
			return err
		}
	}

	logger.Info("shutdown complete")
	return nil
}

// shutdownTelemetry flushes pending spans and metrics on a fresh context: the
// process context is already cancelled by the time this runs, and a cancelled
// context would abort the flush it is meant to perform.
func shutdownTelemetry(t *observability.Telemetry, logger *slog.Logger) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := t.Shutdown(ctx); err != nil {
		logger.Warn("telemetry shutdown reported errors", slog.Any("error", err))
	}
}
