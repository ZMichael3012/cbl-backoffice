package config_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cbl/cbl-backoffice/internal/config"
)

// env builds a getenv function over a fixed map.
func env(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

func TestLoadDefaults(t *testing.T) {
	t.Parallel()

	cfg, err := config.Load(env(map[string]string{
		"PG_DSN":           "postgres://cbl:cbl@localhost:5432/backoffice",
		"ADMIN_JWT_SECRET": "test-admin-jwt-secret-0123456789",
	}))
	require.NoError(t, err)
	assert.Equal(t, ":8020", cfg.HTTPAddr)
	assert.Equal(t, ":9220", cfg.MetricsAddr)
	assert.Equal(t, "./migrations", cfg.MigrationsDir)
}

func TestLoadOverrides(t *testing.T) {
	t.Parallel()

	cfg, err := config.Load(env(map[string]string{
		"HTTP_ADDR":        ":8080",
		"METRICS_ADDR":     ":9090",
		"MIGRATIONS_DIR":   "/app/migrations",
		"PG_DSN":           "postgres://cbl:cbl@postgres:5432/backoffice",
		"ADMIN_JWT_SECRET": "test-admin-jwt-secret-0123456789",
	}))
	require.NoError(t, err)
	assert.Equal(t, ":8080", cfg.HTTPAddr)
	assert.Equal(t, ":9090", cfg.MetricsAddr)
	assert.Equal(t, "/app/migrations", cfg.MigrationsDir)
}

func TestLoadFailures(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		vars map[string]string
	}{
		{name: "missing dsn", vars: map[string]string{"ADMIN_JWT_SECRET": "test-admin-jwt-secret-0123456789"}},
		{name: "missing secret", vars: map[string]string{"PG_DSN": "postgres://x"}},
		{name: "short secret", vars: map[string]string{"PG_DSN": "postgres://x", "ADMIN_JWT_SECRET": "short"}},
		{name: "31 byte secret", vars: map[string]string{"PG_DSN": "postgres://x", "ADMIN_JWT_SECRET": strings.Repeat("s", 31)}},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := config.Load(env(tc.vars))
			require.Error(t, err)
		})
	}
}
