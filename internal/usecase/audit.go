// Package usecase contains the business logic of the back-office service:
// the operator action journal (CBL-12.1) and later the admin actions (12.2/12.3).
package usecase

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/cbl/cbl-kit/grpcmw"

	"github.com/cbl/cbl-backoffice/internal/domain"
)

// Journal limits and the cursor version of CBL-12.1 FR-5.
const (
	defaultAuditLimit = 20
	maxAuditLimit     = 100
	auditCursorMaxLen = 512
	auditCursorVer    = 1
)

// JournalStore lists pages of the audit journal.
type JournalStore interface {
	// List returns up to limit records after the keyset position, newest first.
	List(ctx context.Context, filter domain.AuditFilter, limit int, after *domain.AuditKey) ([]domain.AuditRecord, error)
}

// JournalWriter appends one record to the audit journal.
type JournalWriter interface {
	// Insert stores one record.
	Insert(ctx context.Context, record domain.AuditRecord) error
	// ExistsForResource reports whether a record for the action and resource
	// already exists (the compensation check of CBL-12.2).
	ExistsForResource(ctx context.Context, action, resourceType, resourceID string) (bool, error)
}

// AuditReader serves pages of the operator action journal.
type AuditReader struct {
	store JournalStore
}

// NewAuditReader creates the reader over the given store.
func NewAuditReader(store JournalStore) *AuditReader {
	return &AuditReader{store: store}
}

// List returns one page of the journal: newest first, keyset pagination, the
// cursor is bound to the filter set. Validation errors are invalid_argument.
func (r *AuditReader) List(ctx context.Context, filter domain.AuditFilter, limit int, cursor string) (domain.AuditPage, error) {
	if limit == 0 {
		limit = defaultAuditLimit
	}
	if limit < 1 || limit > maxAuditLimit {
		return domain.AuditPage{}, invalid("limit must be between 1 and 100")
	}
	after, err := decodeAuditCursor(cursor, filter)
	if err != nil {
		return domain.AuditPage{}, err
	}
	rows, err := r.store.List(ctx, filter, limit+1, after)
	if err != nil {
		return domain.AuditPage{}, grpcmw.E(grpcmw.KindUnavailable, "service_unavailable",
			"audit journal is temporarily unavailable", err)
	}
	page := domain.AuditPage{Items: rows}
	if len(rows) > limit {
		page.Items = rows[:limit]
		last := page.Items[len(page.Items)-1]
		page.NextCursor = encodeAuditCursor(filter, domain.AuditKey{OccurredAt: last.OccurredAt, ID: last.ID})
	}
	return page, nil
}

// AuditEntry describes one action to append to the journal.
type AuditEntry struct {
	// OperatorID is the token subject of the acting operator.
	OperatorID string
	// RequestID links the record to the access log.
	RequestID string
	// Action names the operation, for example payout.approve.
	Action string
	// ResourceType names the resource class.
	ResourceType string
	// ResourceID identifies the resource (may be empty).
	ResourceID string
	// Before is the state before the action (nil for creations).
	Before any
	// After is the state after the action (nil for deletions).
	After any
}

// AuditWriter appends records to the operator action journal.
type AuditWriter struct {
	store JournalWriter
	now   func() time.Time
	newID func() (string, error)
}

// NewAuditWriter creates the writer over the given store.
func NewAuditWriter(store JournalWriter) *AuditWriter {
	return &AuditWriter{
		store: store,
		now:   func() time.Time { return time.Now().UTC() },
		newID: func() (string, error) {
			id, err := uuid.NewV7()
			if err != nil {
				return "", err
			}
			return id.String(), nil
		},
	}
}

// Record appends one journal entry: identifier, time and marshaled states are
// filled here; a marshaling or storage failure is returned, never swallowed.
func (w *AuditWriter) Record(ctx context.Context, entry AuditEntry) error {
	record, err := w.Build(entry)
	if err != nil {
		return err
	}
	if err := w.store.Insert(ctx, record); err != nil {
		return grpcmw.E(grpcmw.KindUnavailable, "service_unavailable",
			"audit journal is temporarily unavailable", err)
	}
	return nil
}

// Build renders one journal entry without storing it; the discrepancy resolve
// commits the record with the business change in one transaction (CBL-12.3).
func (w *AuditWriter) Build(entry AuditEntry) (domain.AuditRecord, error) {
	id, err := w.newID()
	if err != nil {
		return domain.AuditRecord{}, fmt.Errorf("audit record id: %w", err)
	}
	before, err := marshalState(entry.Before)
	if err != nil {
		return domain.AuditRecord{}, fmt.Errorf("audit before state: %w", err)
	}
	after, err := marshalState(entry.After)
	if err != nil {
		return domain.AuditRecord{}, fmt.Errorf("audit after state: %w", err)
	}
	return domain.AuditRecord{
		ID:           id,
		OccurredAt:   w.now().UTC(),
		OperatorID:   entry.OperatorID,
		Action:       entry.Action,
		ResourceType: entry.ResourceType,
		ResourceID:   entry.ResourceID,
		RequestID:    entry.RequestID,
		Before:       before,
		After:        after,
	}, nil
}

// ExistsForResource reports whether the journal already contains a record for
// the action and resource (used by the CBL-12.2 compensation of a lost record).
func (w *AuditWriter) ExistsForResource(ctx context.Context, action, resourceType, resourceID string) (bool, error) {
	exists, err := w.store.ExistsForResource(ctx, action, resourceType, resourceID)
	if err != nil {
		return false, grpcmw.E(grpcmw.KindUnavailable, "service_unavailable",
			"audit journal is temporarily unavailable", err)
	}
	return exists, nil
}

// marshalState serializes one state snapshot; nil stays absent (SQL NULL).
func marshalState(state any) (json.RawMessage, error) {
	if state == nil {
		return nil, nil
	}
	raw, err := json.Marshal(state)
	if err != nil {
		return nil, err
	}
	return raw, nil
}

// encodeAuditCursor builds the opaque cursor of the next page.
func encodeAuditCursor(filter domain.AuditFilter, key domain.AuditKey) string {
	return encodeKeysetCursor(auditCursorVer, filter.Fingerprint(), key.OccurredAt, key.ID)
}

// decodeAuditCursor parses and validates a cursor against the filter set; an
// empty cursor is the first page.
func decodeAuditCursor(cursor string, filter domain.AuditFilter) (*domain.AuditKey, error) {
	at, id, err := decodeKeysetCursor(cursor, filter.Fingerprint(), auditCursorVer, auditCursorMaxLen)
	if err != nil || at == nil {
		return nil, err
	}
	return &domain.AuditKey{OccurredAt: *at, ID: id}, nil
}

// invalid builds the uniform validation error (HTTP 400 validation_failed).
func invalid(detail string) error {
	return grpcmw.E(grpcmw.KindInvalidArgument, "validation_failed", detail, nil)
}
