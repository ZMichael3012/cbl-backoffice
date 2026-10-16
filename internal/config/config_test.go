package config_test

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cbl/cbl-backoffice/internal/config"
)

// env builds a getenv function over a fixed map.
func env(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

// baseVars returns the minimal valid environment.
func baseVars() map[string]string {
	return map[string]string{
		"PG_DSN":              "postgres://cbl:cbl@localhost:5432/backoffice",
		"ADMIN_JWT_SECRET":    "test-admin-jwt-secret-0123456789",
		"PAYOUT_GRPC_ADDR":    "payout:9080",
		"CONNECTOR_GRPC_ADDR": "connector:9050",
	}
}

func TestLoadDefaults(t *testing.T) {
	t.Parallel()

	cfg, err := config.Load(env(baseVars()))
	require.NoError(t, err)
	assert.Equal(t, ":8020", cfg.HTTPAddr)
	assert.Equal(t, ":9220", cfg.MetricsAddr)
	assert.Equal(t, "./migrations", cfg.MigrationsDir)
	assert.Equal(t, "payout:9080", cfg.PayoutGRPCAddr)
	assert.Equal(t, 2*time.Second, cfg.PayoutTimeout)
	assert.Equal(t, 5*time.Second, cfg.ConnectorTimeout)
}

func TestLoadOverrides(t *testing.T) {
	t.Parallel()

	vars := baseVars()
	vars["HTTP_ADDR"] = ":8080"
	vars["METRICS_ADDR"] = ":9090"
	vars["MIGRATIONS_DIR"] = "/app/migrations"
	vars["PAYOUT_TIMEOUT"] = "3s"
	vars["CONNECTOR_TIMEOUT"] = "8s"
	cfg, err := config.Load(env(vars))
	require.NoError(t, err)
	assert.Equal(t, ":8080", cfg.HTTPAddr)
	assert.Equal(t, ":9090", cfg.MetricsAddr)
	assert.Equal(t, "/app/migrations", cfg.MigrationsDir)
	assert.Equal(t, 3*time.Second, cfg.PayoutTimeout)
	assert.Equal(t, 8*time.Second, cfg.ConnectorTimeout)
}

func TestLoadFailures(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		mutate  func(vars map[string]string)
		message string
	}{
		{name: "missing dsn", mutate: func(v map[string]string) { delete(v, "PG_DSN") }, message: "PG_DSN"},
		{name: "missing secret", mutate: func(v map[string]string) { delete(v, "ADMIN_JWT_SECRET") }, message: "ADMIN_JWT_SECRET"},
		{name: "short secret", mutate: func(v map[string]string) { v["ADMIN_JWT_SECRET"] = "short" }, message: "ADMIN_JWT_SECRET"},
		{name: "31 byte secret", mutate: func(v map[string]string) { v["ADMIN_JWT_SECRET"] = strings.Repeat("s", 31) }, message: "ADMIN_JWT_SECRET"},
		{name: "missing payout addr", mutate: func(v map[string]string) { delete(v, "PAYOUT_GRPC_ADDR") }, message: "PAYOUT_GRPC_ADDR"},
		{name: "missing connector addr", mutate: func(v map[string]string) { delete(v, "CONNECTOR_GRPC_ADDR") }, message: "CONNECTOR_GRPC_ADDR"},
		{name: "bad payout timeout", mutate: func(v map[string]string) { v["PAYOUT_TIMEOUT"] = "15s" }, message: "PAYOUT_TIMEOUT"},
		{name: "bad connector timeout", mutate: func(v map[string]string) { v["CONNECTOR_TIMEOUT"] = "soon" }, message: "CONNECTOR_TIMEOUT"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			vars := baseVars()
			tc.mutate(vars)
			_, err := config.Load(env(vars))
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.message)
		})
	}
}
