// Package repository implements the persistence ports declared in
// internal/domain, backed by PostgreSQL through pgx.
//
// Everything SQL-shaped is confined here. Handlers and services deal in domain
// types and domain errors; the translation from pgx errors and column values
// happens at this boundary and nowhere else.
package repository

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"order-service/internal/config"
	"order-service/internal/domain"
)

// PostgreSQL SQLSTATE codes we translate into domain errors.
const (
	sqlStateUniqueViolation     = "23505"
	sqlStateForeignKeyViolation = "23503"
)

// Postgres owns the connection pool and is shared by every repository.
type Postgres struct {
	pool *pgxpool.Pool
}

// NewPostgres opens and verifies a connection pool. It pings before returning
// so that a misconfigured database fails at startup rather than on the first
// request.
func NewPostgres(ctx context.Context, cfg config.DB) (*Postgres, error) {
	poolCfg, err := pgxpool.ParseConfig(cfg.DSN())
	if err != nil {
		return nil, fmt.Errorf("parse database dsn: %w", err)
	}

	poolCfg.MaxConns = cfg.MaxConns
	poolCfg.MinConns = cfg.MinConns
	poolCfg.MaxConnLifetime = cfg.MaxConnLifetime
	poolCfg.MaxConnIdleTime = cfg.MaxConnIdleTime

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("create connection pool: %w", err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, cfg.ConnectTimeout)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}

	return &Postgres{pool: pool}, nil
}

// Pool exposes the underlying pool for migrations and tests.
func (p *Postgres) Pool() *pgxpool.Pool { return p.pool }

// Close releases every pooled connection.
func (p *Postgres) Close() { p.pool.Close() }

// Health implements domain.HealthChecker. It reports pool saturation alongside
// reachability, because "the database answers a ping" and "the service can get
// a connection" are different failures with different remedies.
func (p *Postgres) Health(ctx context.Context) map[string]string {
	stats := map[string]string{}

	if err := p.pool.Ping(ctx); err != nil {
		stats["status"] = "down"
		stats["error"] = err.Error()
		return stats
	}
	stats["status"] = "up"

	s := p.pool.Stat()
	stats["total_conns"] = strconv.Itoa(int(s.TotalConns()))
	stats["acquired_conns"] = strconv.Itoa(int(s.AcquiredConns()))
	stats["idle_conns"] = strconv.Itoa(int(s.IdleConns()))
	stats["max_conns"] = strconv.Itoa(int(s.MaxConns()))
	stats["acquire_count"] = strconv.FormatInt(s.AcquireCount(), 10)
	stats["canceled_acquire_count"] = strconv.FormatInt(s.CanceledAcquireCount(), 10)
	stats["empty_acquire_count"] = strconv.FormatInt(s.EmptyAcquireCount(), 10)

	switch {
	case s.MaxConns() > 0 && float64(s.AcquiredConns())/float64(s.MaxConns()) > 0.8:
		stats["message"] = "connection pool is above 80% utilisation"
	case s.EmptyAcquireCount() > 0:
		stats["message"] = "some acquisitions had to wait for a free connection"
	default:
		stats["message"] = "healthy"
	}

	return stats
}

// classify maps a driver error onto a domain error class. Callers wrap the
// result with context; this only decides which sentinel it unwraps to.
func classify(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case sqlStateUniqueViolation, sqlStateForeignKeyViolation:
			return fmt.Errorf("%s: %w", pgErr.Message, domain.ErrConflict)
		}
	}
	return err
}
