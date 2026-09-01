// Package config loads and validates application configuration from the
// environment.
//
// This is the only package permitted to read os.Getenv. Every other package
// receives what it needs through explicit struct fields, which keeps them
// testable and makes the full set of knobs discoverable in one place.
package config

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	_ "github.com/joho/godotenv/autoload"
)

// Config is the fully resolved configuration for one process.
type Config struct {
	App           App
	HTTP          HTTP
	DB            DB
	Observability Observability
}

// App holds identity and environment metadata.
type App struct {
	Name    string
	Env     string
	Version string
}

// IsProduction reports whether the process runs with production semantics
// (stricter logging, no debug routes, no pretty printing).
func (a App) IsProduction() bool { return a.Env == "production" }

// HTTP holds the inbound HTTP server settings.
type HTTP struct {
	Port            int
	ReadTimeout     time.Duration
	WriteTimeout    time.Duration
	IdleTimeout     time.Duration
	ShutdownTimeout time.Duration
	CORSOrigins     []string
}

// Addr returns the listen address for the HTTP server.
func (h HTTP) Addr() string { return fmt.Sprintf(":%d", h.Port) }

// DB holds PostgreSQL connection and pool settings.
type DB struct {
	Host     string
	Port     string
	Name     string
	User     string
	Password string
	Schema   string
	SSLMode  string

	MaxConns        int32
	MinConns        int32
	MaxConnLifetime time.Duration
	MaxConnIdleTime time.Duration
	ConnectTimeout  time.Duration
}

// DSN renders a pgx-compatible connection string. Credentials are escaped so
// that special characters in a password cannot corrupt the DSN.
func (d DB) DSN() string {
	return fmt.Sprintf(
		"postgres://%s:%s@%s/%s?sslmode=%s&search_path=%s&connect_timeout=%d",
		url.QueryEscape(d.User),
		url.QueryEscape(d.Password),
		net.JoinHostPort(d.Host, d.Port),
		url.PathEscape(d.Name),
		url.QueryEscape(d.SSLMode),
		url.QueryEscape(d.Schema),
		int(d.ConnectTimeout.Seconds()),
	)
}

// Observability holds logging, tracing and metrics settings.
type Observability struct {
	LogLevel     string // debug | info | warn | error
	LogFormat    string // json | text
	OTLPEndpoint string // host:port; empty disables span and metric export
	OTLPInsecure bool
	SampleRatio  float64
	ServiceName  string
}

// ExportEnabled reports whether telemetry should be shipped over OTLP.
// When false the application still creates spans, so trace IDs keep appearing
// in logs; they are simply not exported anywhere.
func (o Observability) ExportEnabled() bool { return o.OTLPEndpoint != "" }

// Load reads configuration from the environment, applies defaults and
// validates the result. It reports every problem at once rather than failing
// on the first, so a misconfigured deployment can be fixed in a single pass.
func Load() (Config, error) {
	cfg := Config{
		App: App{
			Name:    env("APP_NAME", "order-service"),
			Env:     env("APP_ENV", "local"),
			Version: env("APP_VERSION", "dev"),
		},
		HTTP: HTTP{
			Port:            envInt("PORT", 8080),
			ReadTimeout:     envDuration("HTTP_READ_TIMEOUT", 10*time.Second),
			WriteTimeout:    envDuration("HTTP_WRITE_TIMEOUT", 30*time.Second),
			IdleTimeout:     envDuration("HTTP_IDLE_TIMEOUT", time.Minute),
			ShutdownTimeout: envDuration("HTTP_SHUTDOWN_TIMEOUT", 15*time.Second),
			CORSOrigins:     envCSV("HTTP_CORS_ORIGINS", []string{"http://localhost:5173"}),
		},
		DB: DB{
			// The BLUEPRINT_DB_* names are a compatibility fallback for the
			// original scaffold's .env and docker-compose.yml.
			Host:            env("DB_HOST", env("BLUEPRINT_DB_HOST", "localhost")),
			Port:            env("DB_PORT", env("BLUEPRINT_DB_PORT", "5432")),
			Name:            env("DB_DATABASE", env("BLUEPRINT_DB_DATABASE", "")),
			User:            env("DB_USERNAME", env("BLUEPRINT_DB_USERNAME", "")),
			Password:        env("DB_PASSWORD", env("BLUEPRINT_DB_PASSWORD", "")),
			Schema:          env("DB_SCHEMA", env("BLUEPRINT_DB_SCHEMA", "public")),
			SSLMode:         env("DB_SSLMODE", "disable"),
			MaxConns:        int32(envInt("DB_MAX_CONNS", 10)),
			MinConns:        int32(envInt("DB_MIN_CONNS", 2)),
			MaxConnLifetime: envDuration("DB_MAX_CONN_LIFETIME", time.Hour),
			MaxConnIdleTime: envDuration("DB_MAX_CONN_IDLE_TIME", 30*time.Minute),
			ConnectTimeout:  envDuration("DB_CONNECT_TIMEOUT", 5*time.Second),
		},
		Observability: Observability{
			LogLevel:     strings.ToLower(env("LOG_LEVEL", "info")),
			LogFormat:    strings.ToLower(env("LOG_FORMAT", "json")),
			OTLPEndpoint: env("OTEL_EXPORTER_OTLP_ENDPOINT", ""),
			OTLPInsecure: envBool("OTEL_EXPORTER_OTLP_INSECURE", true),
			SampleRatio:  envFloat("OTEL_TRACES_SAMPLER_ARG", 1.0),
		},
	}
	cfg.Observability.ServiceName = env("OTEL_SERVICE_NAME", cfg.App.Name)

	if err := cfg.validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c Config) validate() error {
	var problems []string

	if c.HTTP.Port < 1 || c.HTTP.Port > 65535 {
		problems = append(problems, fmt.Sprintf("PORT must be between 1 and 65535, got %d", c.HTTP.Port))
	}
	if c.DB.Name == "" {
		problems = append(problems, "DB_DATABASE (or BLUEPRINT_DB_DATABASE) is required")
	}
	if c.DB.User == "" {
		problems = append(problems, "DB_USERNAME (or BLUEPRINT_DB_USERNAME) is required")
	}
	if c.DB.MaxConns < 1 {
		problems = append(problems, fmt.Sprintf("DB_MAX_CONNS must be at least 1, got %d", c.DB.MaxConns))
	}
	if c.DB.MinConns > c.DB.MaxConns {
		problems = append(problems, fmt.Sprintf("DB_MIN_CONNS (%d) must not exceed DB_MAX_CONNS (%d)", c.DB.MinConns, c.DB.MaxConns))
	}
	switch c.Observability.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		problems = append(problems, fmt.Sprintf("LOG_LEVEL must be one of debug|info|warn|error, got %q", c.Observability.LogLevel))
	}
	switch c.Observability.LogFormat {
	case "json", "text":
	default:
		problems = append(problems, fmt.Sprintf("LOG_FORMAT must be one of json|text, got %q", c.Observability.LogFormat))
	}
	if r := c.Observability.SampleRatio; r < 0 || r > 1 {
		problems = append(problems, fmt.Sprintf("OTEL_TRACES_SAMPLER_ARG must be between 0 and 1, got %v", r))
	}

	if len(problems) > 0 {
		return fmt.Errorf("invalid configuration:\n  - %s", strings.Join(problems, "\n  - "))
	}
	return nil
}

// --- environment helpers -------------------------------------------------
//
// Each helper falls back to the supplied default when the variable is unset or
// empty. A present-but-unparseable value also falls back rather than erroring:
// these stay total so Load never panics, and validate() is where policy lives.

func env(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) int {
	v, err := strconv.Atoi(env(key, ""))
	if err != nil {
		return fallback
	}
	return v
}

func envFloat(key string, fallback float64) float64 {
	v, err := strconv.ParseFloat(env(key, ""), 64)
	if err != nil {
		return fallback
	}
	return v
}

func envBool(key string, fallback bool) bool {
	v, err := strconv.ParseBool(env(key, ""))
	if err != nil {
		return fallback
	}
	return v
}

func envDuration(key string, fallback time.Duration) time.Duration {
	v, err := time.ParseDuration(env(key, ""))
	if err != nil {
		return fallback
	}
	return v
}

func envCSV(key string, fallback []string) []string {
	raw := env(key, "")
	if raw == "" {
		return fallback
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return fallback
	}
	return out
}
