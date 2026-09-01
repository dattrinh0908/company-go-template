package config

import (
	"strings"
	"testing"
	"time"
)

// setRequired sets the minimum needed for Load to succeed.
func setRequired(t *testing.T) {
	t.Helper()
	t.Setenv("DB_DATABASE", "orders")
	t.Setenv("DB_USERNAME", "app")
}

func TestLoadAppliesDefaults(t *testing.T) {
	setRequired(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.HTTP.Port != 8080 {
		t.Errorf("port = %d, want the 8080 default", cfg.HTTP.Port)
	}
	if cfg.HTTP.ShutdownTimeout != 15*time.Second {
		t.Errorf("shutdown timeout = %v, want 15s", cfg.HTTP.ShutdownTimeout)
	}
	if cfg.Observability.LogFormat != "json" {
		t.Errorf("log format = %q, want json", cfg.Observability.LogFormat)
	}
	if cfg.Observability.ExportEnabled() {
		t.Error("telemetry export should be off when no endpoint is configured")
	}
	if cfg.Observability.ServiceName != cfg.App.Name {
		t.Errorf("service name = %q, want it to default to the app name", cfg.Observability.ServiceName)
	}
}

func TestLoadFallsBackToBlueprintNames(t *testing.T) {
	// The original scaffold's variable names must keep working.
	t.Setenv("BLUEPRINT_DB_DATABASE", "legacy")
	t.Setenv("BLUEPRINT_DB_USERNAME", "legacy_user")
	t.Setenv("BLUEPRINT_DB_HOST", "legacy-host")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.DB.Name != "legacy" || cfg.DB.User != "legacy_user" || cfg.DB.Host != "legacy-host" {
		t.Errorf("blueprint fallback not applied: %+v", cfg.DB)
	}
}

func TestDBNamesTakePrecedenceOverBlueprint(t *testing.T) {
	t.Setenv("BLUEPRINT_DB_DATABASE", "legacy")
	t.Setenv("BLUEPRINT_DB_USERNAME", "legacy_user")
	t.Setenv("DB_DATABASE", "current")
	t.Setenv("DB_USERNAME", "current_user")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.DB.Name != "current" || cfg.DB.User != "current_user" {
		t.Errorf("DB_* should win over BLUEPRINT_DB_*: %+v", cfg.DB)
	}
}

func TestLoadReportsEveryProblemAtOnce(t *testing.T) {
	// Both name sets must be cleared: a stray .env on the developer's machine
	// would otherwise satisfy the fallback and make this test flaky.
	t.Setenv("DB_DATABASE", "")
	t.Setenv("DB_USERNAME", "")
	t.Setenv("BLUEPRINT_DB_DATABASE", "")
	t.Setenv("BLUEPRINT_DB_USERNAME", "")
	t.Setenv("PORT", "70000")
	t.Setenv("LOG_LEVEL", "verbose")

	_, err := Load()
	if err == nil {
		t.Fatal("expected a validation error")
	}

	for _, want := range []string{"DB_DATABASE", "DB_USERNAME", "PORT", "LOG_LEVEL"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should mention %s, got: %v", want, err)
		}
	}
}

func TestUnparseableValuesFallBackToDefaults(t *testing.T) {
	setRequired(t)
	t.Setenv("PORT", "not-a-number")
	t.Setenv("HTTP_READ_TIMEOUT", "banana")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.HTTP.Port != 8080 {
		t.Errorf("port = %d, want the default after an unparseable value", cfg.HTTP.Port)
	}
	if cfg.HTTP.ReadTimeout != 10*time.Second {
		t.Errorf("read timeout = %v, want the 10s default", cfg.HTTP.ReadTimeout)
	}
}

func TestDSNEscapesCredentials(t *testing.T) {
	db := DB{
		Host: "localhost", Port: "5432", Name: "orders",
		User: "app", Password: "p@ss:word/with?specials",
		Schema: "public", SSLMode: "disable", ConnectTimeout: 5 * time.Second,
	}

	dsn := db.DSN()

	// The raw password must never appear unescaped, or it would terminate the
	// userinfo section early and corrupt the DSN.
	if strings.Contains(dsn, "p@ss:word/with?specials") {
		t.Errorf("password was not escaped: %s", dsn)
	}
	if !strings.Contains(dsn, "localhost:5432") {
		t.Errorf("host and port missing from dsn: %s", dsn)
	}
	if !strings.Contains(dsn, "connect_timeout=5") {
		t.Errorf("connect timeout missing from dsn: %s", dsn)
	}
}

func TestCORSOriginsParsing(t *testing.T) {
	setRequired(t)
	t.Setenv("HTTP_CORS_ORIGINS", " https://a.example , https://b.example ,")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	want := []string{"https://a.example", "https://b.example"}
	if len(cfg.HTTP.CORSOrigins) != len(want) {
		t.Fatalf("got %v, want %v", cfg.HTTP.CORSOrigins, want)
	}
	for i := range want {
		if cfg.HTTP.CORSOrigins[i] != want[i] {
			t.Errorf("origin[%d] = %q, want %q", i, cfg.HTTP.CORSOrigins[i], want[i])
		}
	}
}

func TestIsProduction(t *testing.T) {
	if (App{Env: "production"}).IsProduction() != true {
		t.Error("production env should report true")
	}
	if (App{Env: "local"}).IsProduction() != false {
		t.Error("local env should report false")
	}
}
