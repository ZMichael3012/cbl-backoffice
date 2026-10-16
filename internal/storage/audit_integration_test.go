//go:build integration

package storage_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/cbl/cbl-backoffice/internal/domain"
	"github.com/cbl/cbl-backoffice/internal/storage"
	"github.com/cbl/cbl-backoffice/internal/testsupport"
	"github.com/cbl/cbl-backoffice/internal/usecase"
)

// TestMigrationsAndImmutability verifies the journal schema invariants.
func TestMigrationsAndImmutability(t *testing.T) {
	ctx := context.Background()
	db := testsupport.OpenMigrated(t)
	require.NoError(t, storage.Migrate(ctx, db, "../../migrations"), "migrations are tracked and a second run is a no-op")
	require.NoError(t, storage.Ready(ctx, db))

	store := storage.NewAuditStore(db)
	record := domain.AuditRecord{
		ID:           "018f0000-0000-7000-8000-000000000001",
		OccurredAt:   time.Date(2026, 10, 16, 10, 0, 0, 0, time.UTC),
		OperatorID:   "op-1",
		Action:       "payout.approve",
		ResourceType: "payout",
		ResourceID:   "po-1",
		Before:       nil,
		After:        nil,
	}
	require.NoError(t, store.Insert(ctx, record))

	require.Error(t, db.Exec("UPDATE audit_log SET action = 'tampered'").Error,
		"the trigger forbids updates")
	require.Error(t, db.Exec("DELETE FROM audit_log").Error,
		"the trigger forbids deletes")
	require.Error(t, db.Exec("TRUNCATE audit_log").Error,
		"the statement trigger forbids truncate")

	// nil states must be stored as SQL NULL, not as jsonb 'null'.
	var beforeNull, afterNull bool
	row := db.Raw("SELECT before IS NULL AS b, after IS NULL AS a FROM audit_log WHERE id = ?", record.ID).Row()
	require.NoError(t, row.Scan(&beforeNull, &afterNull))
	assert.True(t, beforeNull)
	assert.True(t, afterNull)

	page, err := store.List(ctx, domain.AuditFilter{}, 10, nil)
	require.NoError(t, err)
	require.Len(t, page, 1)
	assert.Equal(t, "payout.approve", page[0].Action)
}

// TestAuditWithTx verifies the transactional seam of the write path (12.2):
// a rolled back business transaction discards the journal record as well.
func TestAuditWithTx(t *testing.T) {
	ctx := context.Background()
	db := testsupport.OpenMigrated(t)
	store := storage.NewAuditStore(db)
	entry := usecase.AuditEntry{OperatorID: "op-1", Action: "payout.approve"}

	err := db.Transaction(func(tx *gorm.DB) error {
		if recordErr := usecase.NewAuditWriter(store.WithTx(tx)).Record(ctx, entry); recordErr != nil {
			return recordErr
		}
		return assert.AnError
	})
	require.Error(t, err, "the rollback surfaces as the transaction error")

	rows, err := store.List(ctx, domain.AuditFilter{}, 10, nil)
	require.NoError(t, err)
	assert.Empty(t, rows, "a rolled back transaction must not leave a record")

	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return usecase.NewAuditWriter(store.WithTx(tx)).Record(ctx, entry)
	}))
	rows, err = store.List(ctx, domain.AuditFilter{}, 10, nil)
	require.NoError(t, err)
	assert.Len(t, rows, 1, "a committed transaction keeps the record")
}

// TestAuditListFiltersAndPagination verifies ordering, filters and keyset pages.
func TestAuditListFiltersAndPagination(t *testing.T) {
	ctx := context.Background()
	db := testsupport.OpenMigrated(t)
	store := storage.NewAuditStore(db)

	base := time.Date(2026, 10, 16, 10, 0, 0, 0, time.UTC)
	seed := []domain.AuditRecord{
		{ID: "018f0000-0000-7000-8000-000000000001", OccurredAt: base, OperatorID: "op-1", Action: "payout.approve"},
		{ID: "018f0000-0000-7000-8000-000000000002", OccurredAt: base.Add(time.Minute), OperatorID: "op-2", Action: "routing.update"},
		{ID: "018f0000-0000-7000-8000-000000000003", OccurredAt: base.Add(2 * time.Minute), OperatorID: "op-1", Action: "routing.update"},
		{ID: "018f0000-0000-7000-8000-000000000004", OccurredAt: base.Add(3 * time.Minute), OperatorID: "op-1", Action: "payout.reject"},
		{ID: "018f0000-0000-7000-8000-000000000005", OccurredAt: base.Add(3 * time.Minute), OperatorID: "op-1", Action: "payout.reject"},
	}
	for _, record := range seed {
		require.NoError(t, store.Insert(ctx, record))
	}

	// Newest first with id as the tiebreaker inside the same instant.
	rows, err := store.List(ctx, domain.AuditFilter{}, 3, nil)
	require.NoError(t, err)
	require.Len(t, rows, 3)
	assert.Equal(t, "018f0000-0000-7000-8000-000000000005", rows[0].ID)
	assert.Equal(t, "018f0000-0000-7000-8000-000000000004", rows[1].ID)
	assert.Equal(t, "018f0000-0000-7000-8000-000000000003", rows[2].ID)

	// Keyset continuation has no gaps and no repeats.
	after := &domain.AuditKey{OccurredAt: rows[2].OccurredAt, ID: rows[2].ID}
	rows2, err := store.List(ctx, domain.AuditFilter{}, 3, after)
	require.NoError(t, err)
	require.Len(t, rows2, 2)
	assert.Equal(t, "018f0000-0000-7000-8000-000000000002", rows2[0].ID)
	assert.Equal(t, "018f0000-0000-7000-8000-000000000001", rows2[1].ID)

	// Exact-match filters.
	byOperator, err := store.List(ctx, domain.AuditFilter{OperatorID: "op-1"}, 10, nil)
	require.NoError(t, err)
	assert.Len(t, byOperator, 4)
	byAction, err := store.List(ctx, domain.AuditFilter{OperatorID: "op-1", Action: "payout.reject"}, 10, nil)
	require.NoError(t, err)
	assert.Len(t, byAction, 2)

	// Inclusive period bounds.
	from := base.Add(time.Minute)
	to := base.Add(2 * time.Minute)
	inPeriod, err := store.List(ctx, domain.AuditFilter{PeriodFrom: &from, PeriodTo: &to}, 10, nil)
	require.NoError(t, err)
	assert.Len(t, inPeriod, 2)
}
