// Package config parses and validates the environment of the back-office
// service; invalid values fail startup (CBL-12.1 FR-1).
package config

import (
	"errors"
	"fmt"

	"github.com/cbl/cbl-backoffice/internal/auth"
)

// Config collects the environment settings of the service.
type Config struct {
	// HTTPAddr is the admin API listen address.
	HTTPAddr string
	// MetricsAddr is the metrics server listen address.
	MetricsAddr string
	// PGDSN is the PostgreSQL connection string of the service database.
	PGDSN string
	// MigrationsDir is the directory with goose migrations.
	MigrationsDir string
	// JWTSecret is the HS256 signing key of operator tokens.
	JWTSecret string
}

// Load reads and validates the configuration from getenv.
func Load(getenv func(string) string) (Config, error) {
	cfg := Config{
		HTTPAddr:      value(getenv, "HTTP_ADDR", ":8020"),
		MetricsAddr:   value(getenv, "METRICS_ADDR", ":9220"),
		PGDSN:         value(getenv, "PG_DSN", ""),
		MigrationsDir: value(getenv, "MIGRATIONS_DIR", "./migrations"),
		JWTSecret:     value(getenv, "ADMIN_JWT_SECRET", ""),
	}
	if cfg.PGDSN == "" {
		return Config{}, errors.New("config: PG_DSN is required")
	}
	if len(cfg.JWTSecret) < auth.MinSecretLength {
		return Config{}, fmt.Errorf("config: ADMIN_JWT_SECRET must be at least %d characters", auth.MinSecretLength)
	}
	return cfg, nil
}

// value returns the environment variable value or the fallback.
func value(getenv func(string) string, key, fallback string) string {
	if v := getenv(key); v != "" {
		return v
	}
	return fallback
}
