package http

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"order-service/internal/config"
)

// Server wraps net/http with this service's timeouts and shutdown semantics.
type Server struct {
	http   *http.Server
	logger *slog.Logger
	cfg    config.HTTP
}

// NewServer builds the listener around a handler.
//
// Every timeout is set explicitly: net/http defaults to none, which lets a slow
// or malicious client hold a connection open indefinitely.
func NewServer(cfg config.HTTP, handler http.Handler, logger *slog.Logger) *Server {
	return &Server{
		http: &http.Server{
			Addr:              cfg.Addr(),
			Handler:           handler,
			ReadTimeout:       cfg.ReadTimeout,
			ReadHeaderTimeout: cfg.ReadTimeout,
			WriteTimeout:      cfg.WriteTimeout,
			IdleTimeout:       cfg.IdleTimeout,
		},
		logger: logger,
		cfg:    cfg,
	}
}

// Start blocks serving requests. A clean shutdown returns nil rather than
// http.ErrServerClosed, so the caller only has to handle real failures.
func (s *Server) Start() error {
	s.logger.Info("http server listening", slog.String("addr", s.http.Addr))

	if err := s.http.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("http server: %w", err)
	}
	return nil
}

// Shutdown stops accepting connections and waits for in-flight requests, up to
// the configured grace period.
func (s *Server) Shutdown(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, s.cfg.ShutdownTimeout)
	defer cancel()

	s.logger.Info("http server shutting down",
		slog.Duration("grace_period", s.cfg.ShutdownTimeout))

	if err := s.http.Shutdown(ctx); err != nil {
		return fmt.Errorf("shutdown http server: %w", err)
	}
	return nil
}
