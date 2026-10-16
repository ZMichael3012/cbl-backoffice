package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"
)

// AuditRecord is one immutable entry of the operator action journal
// (who did what, with the state before and after).
type AuditRecord struct {
	// ID is the UUIDv7 identifier of the record.
	ID string
	// OccurredAt is the UTC time of the action.
	OccurredAt time.Time
	// OperatorID is the token subject of the acting operator.
	OperatorID string
	// Action names the operation, for example payout.approve.
	Action string
	// ResourceType names the resource class, for example payout.
	ResourceType string
	// ResourceID identifies the resource (may be empty for global actions).
	ResourceID string
	// RequestID links the record to the access log of the request.
	RequestID string
	// Before is the state before the action (nil for creations).
	Before json.RawMessage
	// After is the state after the action (nil for deletions).
	After json.RawMessage
}

// AuditFilter narrows an audit journal page; empty fields do not filter.
type AuditFilter struct {
	// OperatorID filters by the acting operator (exact match).
	OperatorID string
	// Action filters by the action name (exact match).
	Action string
	// PeriodFrom is the inclusive lower bound of OccurredAt (UTC).
	PeriodFrom *time.Time
	// PeriodTo is the inclusive upper bound of OccurredAt (UTC).
	PeriodTo *time.Time
}

// Fingerprint returns a short stable hash of the canonical filter set; it binds
// a pagination cursor to one filter set, so a cursor cannot silently change it.
// The canonical form is JSON, so field values cannot collide through separators.
func (f AuditFilter) Fingerprint() string {
	canonical, err := json.Marshal(struct {
		OperatorID string `json:"operator_id"`
		Action     string `json:"action"`
		From       string `json:"from"`
		To         string `json:"to"`
	}{f.OperatorID, f.Action, bound(f.PeriodFrom), bound(f.PeriodTo)})
	if err != nil {
		// Plain strings cannot fail to marshal; keep the contract defensive.
		return ""
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:])[:16]
}

// bound formats a period bound canonically for the fingerprint.
func bound(t *time.Time) string {
	if t == nil {
		return "-"
	}
	return t.UTC().Format(time.RFC3339Nano)
}

// AuditKey is the keyset position of a journal page: the (OccurredAt, ID) pair
// of the last returned row.
type AuditKey struct {
	// OccurredAt is the time of the last returned row.
	OccurredAt time.Time
	// ID is the identifier of the last returned row.
	ID string
}

// String returns the position in a log-friendly form.
func (k AuditKey) String() string {
	return fmt.Sprintf("%s/%s", k.OccurredAt.UTC().Format(time.RFC3339Nano), k.ID)
}

// AuditPage is one page of the operator action journal.
type AuditPage struct {
	// Items are the records of the page, newest first.
	Items []AuditRecord
	// NextCursor is the opaque cursor of the next page; empty at the end.
	NextCursor string
}
