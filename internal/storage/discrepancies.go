package storage

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/cbl/cbl-backoffice/internal/domain"
)

// DiscrepancyStore persists the reconciliation discrepancies and resolves them
// atomically with the audit record (CBL-12.3).
type DiscrepancyStore struct {
	db    *gorm.DB
	audit *AuditStore
}

// discrepancyRow maps one discrepancies row.
type discrepancyRow struct {
	ID         string     `gorm:"column:id;primaryKey"`
	Type       string     `gorm:"column:type"`
	RefID      string     `gorm:"column:ref_id"`
	MerchantID string     `gorm:"column:merchant_id"`
	Severity   string     `gorm:"column:severity"`
	Expected   string     `gorm:"column:expected"`
	Actual     string     `gorm:"column:actual"`
	Status     string     `gorm:"column:status"`
	CreatedAt  time.Time  `gorm:"column:created_at"`
	ReceivedAt time.Time  `gorm:"column:received_at"`
	ResolvedAt *time.Time `gorm:"column:resolved_at"`
	ResolvedBy string     `gorm:"column:resolved_by"`
	Resolution string     `gorm:"column:resolution"`
	Comment    string     `gorm:"column:comment"`
}

// TableName binds the row to the discrepancies table.
func (discrepancyRow) TableName() string { return "discrepancies" }

// toDomain maps the row onto the domain model.
func (r discrepancyRow) toDomain() domain.Discrepancy {
	return domain.Discrepancy{
		ID: r.ID, Type: r.Type, RefID: r.RefID, MerchantID: r.MerchantID,
		Severity: r.Severity, Expected: r.Expected, Actual: r.Actual,
		Status: r.Status, CreatedAt: r.CreatedAt.UTC(), ReceivedAt: r.ReceivedAt.UTC(),
		ResolvedAt: r.ResolvedAt, ResolvedBy: r.ResolvedBy,
		Resolution: r.Resolution, Comment: r.Comment,
	}
}

// NewDiscrepancyStore creates the store over the database handle.
func NewDiscrepancyStore(db *gorm.DB) *DiscrepancyStore {
	return &DiscrepancyStore{db: db, audit: NewAuditStore(db)}
}

// Insert stores one discrepancy; a replay of the same alert id is skipped.
func (s *DiscrepancyStore) Insert(ctx context.Context, record domain.Discrepancy) error {
	_, err := s.InsertTx(ctx, s.db, record)
	return err
}

// InsertTx stores one discrepancy in the caller transaction (the inbox
// consumption) and reports whether the row was inserted; a replay of the same
// alert id is skipped.
func (s *DiscrepancyStore) InsertTx(ctx context.Context, tx *gorm.DB, record domain.Discrepancy) (bool, error) {
	if tx == nil {
		tx = s.db
	}
	row := discrepancyRow{
		ID: record.ID, Type: record.Type, RefID: record.RefID, MerchantID: record.MerchantID,
		Severity: record.Severity, Expected: record.Expected, Actual: record.Actual,
		Status: domain.DiscrepancyStatusOpen, CreatedAt: record.CreatedAt.UTC(),
		ReceivedAt: time.Now().UTC(),
	}
	result := tx.WithContext(ctx).
		Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "id"}}, DoNothing: true}).
		Create(&row)
	if result.Error != nil {
		return false, fmt.Errorf("storage: insert discrepancy: %w", result.Error)
	}
	return result.RowsAffected > 0, nil
}

// ListDiscrepancies returns the filtered rows ordered created_at DESC, id DESC.
func (s *DiscrepancyStore) ListDiscrepancies(ctx context.Context, filter domain.DiscrepancyFilter, limit int, after *domain.DiscrepancyKey) ([]domain.Discrepancy, error) {
	query := s.db.WithContext(ctx).Model(&discrepancyRow{})
	if filter.Severity != "" {
		query = query.Where("severity = ?", filter.Severity)
	}
	if filter.MerchantID != "" {
		query = query.Where("merchant_id = ?", filter.MerchantID)
	}
	if after != nil {
		query = query.Where("(created_at, id) < (?, ?)", after.CreatedAt.UTC(), after.ID)
	}
	if limit <= 0 {
		limit = 20
	}
	var rows []discrepancyRow
	if err := query.Order("created_at DESC").Order("id DESC").Limit(limit).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("storage: list discrepancies: %w", err)
	}
	records := make([]domain.Discrepancy, 0, len(rows))
	for _, row := range rows {
		records = append(records, row.toDomain())
	}
	return records, nil
}

// ResolveDiscrepancy applies one resolution and appends its audit record in
// one transaction; buildAudit renders the record from the open state. A row
// that is not open yields domain.ErrDiscrepancyResolved, an unknown id yields
// domain.ErrDiscrepancyNotFound.
func (s *DiscrepancyStore) ResolveDiscrepancy(ctx context.Context, id string, decision domain.ResolveDecision, buildAudit func(before domain.Discrepancy) (domain.AuditRecord, error)) (domain.Discrepancy, error) {
	var resolved domain.Discrepancy
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row discrepancyRow
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", id).Take(&row).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return domain.ErrDiscrepancyNotFound
		}
		if err != nil {
			return fmt.Errorf("storage: load discrepancy: %w", err)
		}
		if row.Status != domain.DiscrepancyStatusOpen {
			return domain.ErrDiscrepancyResolved
		}
		before := row.toDomain()
		at := decision.At.UTC()
		updates := map[string]any{
			"status":      domain.DiscrepancyStatusResolved,
			"resolution":  decision.Resolution,
			"comment":     decision.Comment,
			"resolved_by": decision.OperatorID,
			"resolved_at": at,
		}
		result := tx.Model(&discrepancyRow{}).Where("id = ? AND status = ?", id, domain.DiscrepancyStatusOpen).
			Updates(updates)
		if result.Error != nil {
			return fmt.Errorf("storage: resolve discrepancy: %w", result.Error)
		}
		if result.RowsAffected != 1 {
			return domain.ErrDiscrepancyResolved
		}
		record, err := buildAudit(before)
		if err != nil {
			return err
		}
		if err := s.audit.WithTx(tx).Insert(ctx, record); err != nil {
			return err
		}
		after := before
		after.Status = domain.DiscrepancyStatusResolved
		after.Resolution = decision.Resolution
		after.Comment = decision.Comment
		after.ResolvedBy = decision.OperatorID
		after.ResolvedAt = &at
		resolved = after
		return nil
	})
	if err != nil {
		return domain.Discrepancy{}, err
	}
	return resolved, nil
}
