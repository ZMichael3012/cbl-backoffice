//go:build integration

// Package testsupport contains shared helpers of the service integration tests:
// a throwaway PostgreSQL with the service migrations applied.
package testsupport

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
	"gorm.io/gorm"

	"github.com/cbl/cbl-backoffice/internal/storage"
)

// StartPostgres runs a throwaway PostgreSQL and returns its DSN.
func StartPostgres(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	container, err := postgres.Run(ctx, "postgres:16-alpine",
		postgres.WithDatabase("backoffice"),
		postgres.WithUsername("cbl"),
		postgres.WithPassword("cbl"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).WithStartupTimeout(90*time.Second)),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = testcontainers.TerminateContainer(container) })
	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)
	return dsn
}

// OpenMigrated opens a throwaway database with the service migrations applied;
// the migrations path is resolved from the calling test package directory.
func OpenMigrated(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := storage.Open(StartPostgres(t))
	require.NoError(t, err)
	require.NoError(t, storage.Migrate(context.Background(), db, filepath.Join("..", "..", "migrations")))
	return db
}
