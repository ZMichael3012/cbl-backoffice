package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/cbl/cbl-backoffice/internal/domain"
)

// AuditStore reads and appends the append-only operator action journal.
type AuditStore struct {
	db *gorm.DB
}

// auditRow maps one audit_log row.
type auditRow struct {
	ID           string    `gorm:"column:id;primaryKey"`
	OccurredAt   time.Time `gorm:"column:occurred_at"`
	OperatorID   string    `gorm:"column:operator_id"`
	Action       string    `gorm:"column:action"`
	ResourceType string    `gorm:"column:resource_type"`
	ResourceID   string    `gorm:"column:resource_id"`
	RequestID    string    `gorm:"column:request_id"`
	Before       *string   `gorm:"column:before"`
	After        *string   `gorm:"column:after"`
}

// TableName binds the row to the journal table.
func (auditRow) TableName() string { return "audit_log" }

// NewAuditStore creates the store over the database handle.
func NewAuditStore(db *gorm.DB) *AuditStore {
	return &AuditStore{db: db}
}

// WithTx returns a store bound to an existing transaction, so the journal
// record commits atomically with the business change (12.2+).
func (s *AuditStore) WithTx(tx *gorm.DB) *AuditStore {
	return &AuditStore{db: tx}
}

// Insert appends one record; the table trigger rejects any later modification.
func (s *AuditStore) Insert(ctx context.Context, record domain.AuditRecord) error {
	row := auditRow{
		ID:           record.ID,
		OccurredAt:   record.OccurredAt.UTC(),
		OperatorID:   record.OperatorID,
		Action:       record.Action,
		ResourceType: record.ResourceType,
		ResourceID:   record.ResourceID,
		RequestID:    record.RequestID,
		Before:       rawToString(record.Before),
		After:        rawToString(record.After),
	}
	if err := s.db.WithContext(ctx).Create(&row).Error; err != nil {
		return fmt.Errorf("storage: insert audit record: %w", err)
	}
	return nil
}

// ExistsForResource reports whether a record for the action and resource exists
// (the compensation check of CBL-12.2: a lost record after a successful action).
func (s *AuditStore) ExistsForResource(ctx context.Context, action, resourceType, resourceID string) (bool, error) {
	var count int64
	err := s.db.WithContext(ctx).Table("audit_log").
		Where("action = ? AND resource_type = ? AND resource_id = ?", action, resourceType, resourceID).
		Count(&count).Error
	if err != nil {
		return false, fmt.Errorf("storage: check audit record: %w", err)
	}
	return count > 0, nil
}

// List returns up to limit records after the keyset position, newest first.
func (s *AuditStore) List(ctx context.Context, filter domain.AuditFilter, limit int, after *domain.AuditKey) ([]domain.AuditRecord, error) {
	query := s.db.WithContext(ctx).Table("audit_log")
	if filter.OperatorID != "" {
		query = query.Where("operator_id = ?", filter.OperatorID)
	}
	if filter.Action != "" {
		query = query.Where("action = ?", filter.Action)
	}
	if filter.PeriodFrom != nil {
		query = query.Where("occurred_at >= ?", filter.PeriodFrom.UTC())
	}
	if filter.PeriodTo != nil {
		query = query.Where("occurred_at <= ?", filter.PeriodTo.UTC())
	}
	if after != nil {
		query = query.Where("(occurred_at, id) < (?, ?)", after.OccurredAt.UTC(), after.ID)
	}
	var rows []auditRow
	if err := query.Order("occurred_at DESC, id DESC").Limit(limit).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("storage: list audit records: %w", err)
	}
	records := make([]domain.AuditRecord, 0, len(rows))
	for _, row := range rows {
		records = append(records, domain.AuditRecord{
			ID:           row.ID,
			OccurredAt:   row.OccurredAt.UTC(),
			OperatorID:   row.OperatorID,
			Action:       row.Action,
			ResourceType: row.ResourceType,
			ResourceID:   row.ResourceID,
			RequestID:    row.RequestID,
			Before:       stringToRaw(row.Before),
			After:        stringToRaw(row.After),
		})
	}
	return records, nil
}

// rawToString converts a state snapshot for the jsonb column.
func rawToString(raw json.RawMessage) *string {
	if raw == nil {
		return nil
	}
	value := string(raw)
	return &value
}

// stringToRaw converts a jsonb value back into a raw state snapshot.
func stringToRaw(value *string) json.RawMessage {
	if value == nil {
		return nil
	}
	return json.RawMessage(*value)
}
