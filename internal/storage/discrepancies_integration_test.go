//go:build integration

package storage_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cbl/cbl-backoffice/internal/domain"
	"github.com/cbl/cbl-backoffice/internal/storage"
	"github.com/cbl/cbl-backoffice/internal/testsupport"
)

// uuidN builds a valid UUID for the fixtures.
func uuidN(n int) string {
	return fmt.Sprintf("01a154e0-0000-7000-8000-%012d", n)
}

// discrepancyRow builds an open discrepancy with a minute offset.
func discrepancyRow(n int, severity, merchant string) domain.Discrepancy {
	return domain.Discrepancy{
		ID: uuidN(n), Type: "amount_mismatch", RefID: uuidN(n + 100),
		MerchantID: merchant, Severity: severity, Expected: "55.000000", Actual: "60.000000",
		Status:    domain.DiscrepancyStatusOpen,
		CreatedAt: time.Date(2026, 10, 19, 12, n, 0, 0, time.UTC),
	}
}

func TestIntegrationDiscrepancyInsertDedupe(t *testing.T) {
	db := testsupport.OpenMigrated(t)
	store := storage.NewDiscrepancyStore(db)
	ctx := context.Background()

	row := discrepancyRow(1, "critical", "0199d6c2-8e3f-7a1b-9c4d-2e5f6a7b8c9d")
	require.NoError(t, store.Insert(ctx, row))
	require.NoError(t, store.Insert(ctx, row), "a replay is skipped")

	var count int64
	require.NoError(t, db.Raw("SELECT count(*) FROM discrepancies").Scan(&count).Error)
	assert.Equal(t, int64(1), count)

	var receivedAt time.Time
	require.NoError(t, db.Raw("SELECT received_at FROM discrepancies WHERE id = ?", row.ID).Row().Scan(&receivedAt))
	assert.False(t, receivedAt.IsZero(), "the ingestion time is written")
	assert.True(t, receivedAt.After(row.CreatedAt.Add(-time.Minute)), "the ingestion time is current, not the zero time")
}

func TestIntegrationDiscrepancyListing(t *testing.T) {
	db := testsupport.OpenMigrated(t)
	store := storage.NewDiscrepancyStore(db)
	ctx := context.Background()

	merchant := "0199d6c2-8e3f-7a1b-9c4d-2e5f6a7b8c9d"
	for i := 1; i <= 5; i++ {
		require.NoError(t, store.Insert(ctx, discrepancyRow(i, "critical", merchant)))
	}
	require.NoError(t, store.Insert(ctx, discrepancyRow(9, "info", "")))

	var seen []string
	var after *domain.DiscrepancyKey
	pageSize := 2
	for page := 0; page < 5; page++ {
		rows, err := store.ListDiscrepancies(ctx, domain.DiscrepancyFilter{}, pageSize+1, after)
		require.NoError(t, err)
		more := len(rows) > pageSize
		if more {
			rows = rows[:pageSize]
		}
		for _, row := range rows {
			seen = append(seen, row.ID)
		}
		if !more || len(rows) == 0 {
			break
		}
		last := rows[len(rows)-1]
		after = &domain.DiscrepancyKey{CreatedAt: last.CreatedAt, ID: last.ID}
	}
	require.Len(t, seen, 6)
	assert.Equal(t, uuidN(9), seen[0], "newest first, including the system row")
	assert.Equal(t, uuidN(1), seen[5])

	critical, err := store.ListDiscrepancies(ctx, domain.DiscrepancyFilter{Severity: "critical"}, 10, nil)
	require.NoError(t, err)
	assert.Len(t, critical, 5)

	system, err := store.ListDiscrepancies(ctx, domain.DiscrepancyFilter{MerchantID: ""}, 10, nil)
	require.NoError(t, err)
	assert.Len(t, system, 6, "an empty merchant filter means no filter")

	onlyMerchant, err := store.ListDiscrepancies(ctx, domain.DiscrepancyFilter{MerchantID: merchant, Severity: "critical"}, 10, nil)
	require.NoError(t, err)
	assert.Len(t, onlyMerchant, 5)
}

func TestIntegrationDiscrepancyResolveWithAudit(t *testing.T) {
	db := testsupport.OpenMigrated(t)
	store := storage.NewDiscrepancyStore(db)
	ctx := context.Background()

	merchant := "0199d6c2-8e3f-7a1b-9c4d-2e5f6a7b8c9d"
	row := discrepancyRow(1, "critical", merchant)
	require.NoError(t, store.Insert(ctx, row))

	at := time.Date(2026, 10, 19, 13, 0, 0, 0, time.UTC)
	resolved, err := store.ResolveDiscrepancy(ctx, row.ID, domain.ResolveDecision{
		OperatorID: "op-recon", Resolution: "confirm_gateway", Comment: "gateway wins",
		At: at,
	}, func(before domain.Discrepancy) (domain.AuditRecord, error) {
		after := before
		after.Status = domain.DiscrepancyStatusResolved
		after.Resolution = "confirm_gateway"
		after.Comment = "gateway wins"
		return domain.AuditRecord{
			ID: uuidN(500), OccurredAt: at, OperatorID: "op-recon",
			Action: "discrepancy.resolve", ResourceType: "discrepancy", ResourceID: before.ID,
			Before: mustJSON(before.AuditView()), After: mustJSON(after.AuditView()),
		}, nil
	})
	require.NoError(t, err)
	assert.Equal(t, domain.DiscrepancyStatusResolved, resolved.Status)
	assert.Equal(t, "confirm_gateway", resolved.Resolution)
	assert.Equal(t, "gateway wins", resolved.Comment)
	assert.Equal(t, "op-recon", resolved.ResolvedBy)
	require.NotNil(t, resolved.ResolvedAt)
	assert.True(t, at.Equal(*resolved.ResolvedAt))

	var status, resolution, comment, resolvedBy string
	require.NoError(t, db.Raw("SELECT status, resolution, comment, resolved_by FROM discrepancies WHERE id = ?", row.ID).
		Row().Scan(&status, &resolution, &comment, &resolvedBy))
	assert.Equal(t, "resolved", status)
	assert.Equal(t, "confirm_gateway", resolution)
	assert.Equal(t, "gateway wins", comment)
	assert.Equal(t, "op-recon", resolvedBy)

	var auditAction string
	require.NoError(t, db.Raw("SELECT action FROM audit_log WHERE resource_id = ?", row.ID).Row().Scan(&auditAction))
	assert.Equal(t, "discrepancy.resolve", auditAction)

	_, err = store.ResolveDiscrepancy(ctx, row.ID, domain.ResolveDecision{
		OperatorID: "op-2", Resolution: "confirm_platform", At: at,
	}, func(domain.Discrepancy) (domain.AuditRecord, error) { return domain.AuditRecord{}, nil })
	require.ErrorIs(t, err, domain.ErrDiscrepancyResolved)

	_, err = store.ResolveDiscrepancy(ctx, uuidN(999), domain.ResolveDecision{
		OperatorID: "op-2", Resolution: "confirm_platform", At: at,
	}, func(domain.Discrepancy) (domain.AuditRecord, error) { return domain.AuditRecord{}, nil })
	require.ErrorIs(t, err, domain.ErrDiscrepancyNotFound)

	var auditCount int64
	require.NoError(t, db.Raw("SELECT count(*) FROM audit_log").Scan(&auditCount).Error)
	assert.Equal(t, int64(1), auditCount, "a rejected resolve writes no audit record")
}

func TestIntegrationDiscrepancyResolveRollback(t *testing.T) {
	db := testsupport.OpenMigrated(t)
	store := storage.NewDiscrepancyStore(db)
	ctx := context.Background()

	row := discrepancyRow(2, "warning", "")
	require.NoError(t, store.Insert(ctx, row))

	_, err := store.ResolveDiscrepancy(ctx, row.ID, domain.ResolveDecision{
		OperatorID: "op-1", Resolution: "manual_adjustment", At: time.Now().UTC(),
	}, func(domain.Discrepancy) (domain.AuditRecord, error) {
		return domain.AuditRecord{}, fmt.Errorf("audit build failed")
	})
	require.Error(t, err)

	var status string
	require.NoError(t, db.Raw("SELECT status FROM discrepancies WHERE id = ?", row.ID).Row().Scan(&status))
	assert.Equal(t, "open", status, "the resolution rolls back with the audit failure")

	var count int64
	require.NoError(t, db.Raw("SELECT count(*) FROM audit_log").Scan(&count).Error)
	assert.Zero(t, count)
}

// mustJSON marshals a fixture value.
func mustJSON(value any) []byte {
	raw, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return raw
}
