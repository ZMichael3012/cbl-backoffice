// Package storage owns the PostgreSQL access of the back-office service:
// connection, migrations and the append-only audit journal store.
package storage

import (
	"context"
	"fmt"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/cbl/cbl-kit/storagex"
)

// Open connects to PostgreSQL with bounded pooling.
func Open(dsn string) (*gorm.DB, error) {
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		return nil, fmt.Errorf("storage: open: %w", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("storage: sql db: %w", err)
	}
	sqlDB.SetMaxOpenConns(20)
	sqlDB.SetMaxIdleConns(10)
	sqlDB.SetConnMaxLifetime(30 * time.Minute)
	return db, nil
}

// Migrate applies the goose migrations of the service (forward only).
func Migrate(ctx context.Context, db *gorm.DB, dir string) error {
	return storagex.Migrate(ctx, db, dir)
}

// Ready reports whether the database answers a ping.
func Ready(ctx context.Context, db *gorm.DB) error {
	sqlDB, err := db.DB()
	if err != nil {
		return fmt.Errorf("storage: sql db: %w", err)
	}
	if err := sqlDB.PingContext(ctx); err != nil {
		return fmt.Errorf("storage: ping: %w", err)
	}
	return nil
}
