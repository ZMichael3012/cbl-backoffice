// Package config parses and validates the environment of the back-office
// service; invalid values fail startup (CBL-12.1 FR-1).
package config

import (
	"errors"
	"fmt"
	"strings"
	"time"

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
	// PayoutGRPCAddr is the PayoutService address (payout decisions).
	PayoutGRPCAddr string
	// ConnectorGRPCAddr is the ConnectorAdminService address (routing, providers).
	ConnectorGRPCAddr string
	// PayoutTimeout bounds one ApprovePayout call (CBL-003: 2 s).
	PayoutTimeout time.Duration
	// ConnectorTimeout bounds one connector admin call.
	ConnectorTimeout time.Duration
	// KafkaBrokers are the bootstrap brokers of the recon.alerts consumer;
	// empty disables the consumer (the reads stay available).
	KafkaBrokers []string
	// ReconGroup is the consumer group of the reconciliation alerts.
	ReconGroup string
}

// Load reads and validates the configuration from getenv.
func Load(getenv func(string) string) (Config, error) {
	cfg := Config{
		HTTPAddr:          value(getenv, "HTTP_ADDR", ":8020"),
		MetricsAddr:       value(getenv, "METRICS_ADDR", ":9220"),
		PGDSN:             value(getenv, "PG_DSN", ""),
		MigrationsDir:     value(getenv, "MIGRATIONS_DIR", "./migrations"),
		JWTSecret:         value(getenv, "ADMIN_JWT_SECRET", ""),
		PayoutGRPCAddr:    value(getenv, "PAYOUT_GRPC_ADDR", ""),
		ConnectorGRPCAddr: value(getenv, "CONNECTOR_GRPC_ADDR", ""),
		KafkaBrokers:      splitCSV(getenv("KAFKA_BROKERS")),
		ReconGroup:        value(getenv, "BACKOFFICE_RECON_GROUP", "cbl-backoffice-recon"),
	}
	if cfg.PGDSN == "" {
		return Config{}, errors.New("config: PG_DSN is required")
	}
	if len(cfg.JWTSecret) < auth.MinSecretLength {
		return Config{}, fmt.Errorf("config: ADMIN_JWT_SECRET must be at least %d characters", auth.MinSecretLength)
	}
	if cfg.PayoutGRPCAddr == "" {
		return Config{}, errors.New("config: PAYOUT_GRPC_ADDR is required")
	}
	if cfg.ConnectorGRPCAddr == "" {
		return Config{}, errors.New("config: CONNECTOR_GRPC_ADDR is required")
	}
	payoutTimeout, err := durationValue(getenv, "PAYOUT_TIMEOUT", 2*time.Second, time.Second, 10*time.Second)
	if err != nil {
		return Config{}, err
	}
	cfg.PayoutTimeout = payoutTimeout
	connectorTimeout, err := durationValue(getenv, "CONNECTOR_TIMEOUT", 5*time.Second, time.Second, 10*time.Second)
	if err != nil {
		return Config{}, err
	}
	cfg.ConnectorTimeout = connectorTimeout
	return cfg, nil
}

// durationValue parses and bounds an optional duration variable.
func durationValue(getenv func(string) string, key string, fallback, min, max time.Duration) (time.Duration, error) {
	raw := getenv(key)
	if raw == "" {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(raw)
	if err != nil || parsed < min || parsed > max {
		return 0, fmt.Errorf("config: %s must be a duration between %s and %s", key, min, max)
	}
	return parsed, nil
}

// value returns the environment variable value or the fallback.
func value(getenv func(string) string, key, fallback string) string {
	if v := getenv(key); v != "" {
		return v
	}
	return fallback
}

// splitCSV splits a comma-separated list skipping empty elements.
func splitCSV(raw string) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}
