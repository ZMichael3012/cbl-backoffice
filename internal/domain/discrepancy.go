package domain

import (
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"

	eventsv1 "github.com/cbl/cbl-protos/gen/go/billing/events/v1"
)

// Discrepancy contract values (admin.v1.yaml, CF-3).
const (
	// DiscrepancyTypeMissingInLedger marks a gateway operation missing in the ledger.
	DiscrepancyTypeMissingInLedger = "missing_in_ledger"
	// DiscrepancyTypeAmountMismatch marks a mismatch of the expected and actual amounts.
	DiscrepancyTypeAmountMismatch = "amount_mismatch"
	// DiscrepancyTypeStatusMismatch marks a mismatch of the operation states.
	DiscrepancyTypeStatusMismatch = "status_mismatch"

	// DiscrepancySeverityInfo marks an informational discrepancy.
	DiscrepancySeverityInfo = "info"
	// DiscrepancySeverityWarning marks a warning discrepancy.
	DiscrepancySeverityWarning = "warning"
	// DiscrepancySeverityCritical marks a critical discrepancy.
	DiscrepancySeverityCritical = "critical"

	// DiscrepancyStatusOpen marks a discrepancy awaiting a decision.
	DiscrepancyStatusOpen = "open"
	// DiscrepancyStatusResolved marks a resolved discrepancy.
	DiscrepancyStatusResolved = "resolved"

	// DiscrepancyResolutionConfirmPlatform accepts the platform state.
	DiscrepancyResolutionConfirmPlatform = "confirm_platform"
	// DiscrepancyResolutionConfirmGateway accepts the gateway state.
	DiscrepancyResolutionConfirmGateway = "confirm_gateway"
	// DiscrepancyResolutionManualAdjustment attests an out-of-band correction.
	DiscrepancyResolutionManualAdjustment = "manual_adjustment"
)

// Bounds of the discrepancy fields copied from the alert.
const (
	maxDiscrepancyRefID  = 256
	maxDiscrepancyValue  = 512
	maxResolutionComment = 1024
)

// Discrepancy is one reconciliation mismatch of the operator surface.
type Discrepancy struct {
	// ID is the alert id (the envelope event id).
	ID string
	// Type is one of the contract types.
	Type string
	// RefID is the mismatched entity id (payment or payout).
	RefID string
	// MerchantID is the owning merchant; empty for system discrepancies.
	MerchantID string
	// Severity is info, warning or critical.
	Severity string
	// Expected is the expected value (decimal amount or state name).
	Expected string
	// Actual is the actual value.
	Actual string
	// Status is open or resolved.
	Status string
	// CreatedAt is the alert time (authoritative).
	CreatedAt time.Time
	// ReceivedAt is the ingestion time.
	ReceivedAt time.Time
	// ResolvedAt is the resolution time.
	ResolvedAt *time.Time
	// ResolvedBy is the token subject of the resolving operator.
	ResolvedBy string
	// Resolution is the operator decision.
	Resolution string
	// Comment is the optional operator comment.
	Comment string
}

// DiscrepancyFilter narrows a listing page; empty fields do not filter.
type DiscrepancyFilter struct {
	// Severity filters by the severity.
	Severity string
	// MerchantID filters by the merchant; empty means no filter.
	MerchantID string
}

// DiscrepancyKey is the keyset position of a listing page.
type DiscrepancyKey struct {
	// CreatedAt is the alert time of the last row.
	CreatedAt time.Time
	// ID is the id of the last row.
	ID string
}

// DiscrepancyPage is one listing page.
type DiscrepancyPage struct {
	// Items are the rows, newest first.
	Items []Discrepancy
	// NextCursor is the opaque cursor of the next page; empty at the end.
	NextCursor string
}

// ResolveDecision is one operator resolution.
type ResolveDecision struct {
	// OperatorID is the token subject of the acting operator.
	OperatorID string
	// Resolution is the operator decision.
	Resolution string
	// Comment is the optional operator comment.
	Comment string
	// At is the decision time.
	At time.Time
}

// DiscrepancyAuditView is the whitelisted state snapshot of the audit journal.
type DiscrepancyAuditView struct {
	// ID is the discrepancy identifier.
	ID string `json:"id"`
	// Type is the contract type.
	Type string `json:"type"`
	// RefID is the mismatched entity id.
	RefID string `json:"ref_id"`
	// MerchantID is the owning merchant (empty for system rows).
	MerchantID string `json:"merchant_id"`
	// Severity is the contract severity.
	Severity string `json:"severity"`
	// Status is open or resolved.
	Status string `json:"status"`
	// Resolution is the operator decision (empty while open).
	Resolution string `json:"resolution"`
	// Comment is the optional operator comment.
	Comment string `json:"comment"`
}

// Errors of the discrepancy store.
var (
	// ErrDiscrepancyNotFound reports an unknown discrepancy id.
	ErrDiscrepancyNotFound = errors.New("discrepancy not found")
	// ErrDiscrepancyResolved reports an already resolved discrepancy.
	ErrDiscrepancyResolved = errors.New("discrepancy is already resolved")
)

// discrepancyTypes maps the alert enum onto the contract types.
var discrepancyTypes = map[eventsv1.ReconAlertType]string{
	eventsv1.ReconAlertType_RECON_ALERT_TYPE_MISSING_IN_LEDGER: DiscrepancyTypeMissingInLedger,
	eventsv1.ReconAlertType_RECON_ALERT_TYPE_AMOUNT_MISMATCH:   DiscrepancyTypeAmountMismatch,
	eventsv1.ReconAlertType_RECON_ALERT_TYPE_STATUS_MISMATCH:   DiscrepancyTypeStatusMismatch,
}

// discrepancySeverities maps the alert enum onto the contract severities.
var discrepancySeverities = map[eventsv1.ReconAlertSeverity]string{
	eventsv1.ReconAlertSeverity_RECON_ALERT_SEVERITY_INFO:     DiscrepancySeverityInfo,
	eventsv1.ReconAlertSeverity_RECON_ALERT_SEVERITY_WARNING:  DiscrepancySeverityWarning,
	eventsv1.ReconAlertSeverity_RECON_ALERT_SEVERITY_CRITICAL: DiscrepancySeverityCritical,
}

// ReconAlertRecord validates one recon.alert and maps it onto a discrepancy.
// An invalid or unsupported event (including an unspecified or unknown enum)
// returns an error so the inbox moves it to the retry topics and the DLQ; a
// silently skipped alert would hide a money-reconciliation fact.
func ReconAlertRecord(alert *eventsv1.ReconAlert, eventID string, envelopeOccurredAt time.Time) (Discrepancy, error) {
	if alert == nil {
		return Discrepancy{}, errors.New("recon alert is empty")
	}
	eventUUID, err := uuid.Parse(strings.TrimSpace(eventID))
	if err != nil {
		return Discrepancy{}, errors.New("event_id is not a valid UUID")
	}
	alertUUID, err := uuid.Parse(strings.TrimSpace(alert.GetAlertId()))
	if err != nil || alertUUID != eventUUID {
		return Discrepancy{}, errors.New("alert_id must equal the envelope event_id")
	}
	discrepancyType, ok := discrepancyTypes[alert.GetType()]
	if !ok {
		return Discrepancy{}, errors.New("recon alert type is unsupported")
	}
	severity, ok := discrepancySeverities[alert.GetSeverity()]
	if !ok {
		return Discrepancy{}, errors.New("recon alert severity is unsupported")
	}
	refID := strings.TrimSpace(alert.GetRefId())
	if refID == "" {
		return Discrepancy{}, errors.New("ref_id is empty")
	}
	if err := validateAlertText(refID, maxDiscrepancyRefID, "ref_id"); err != nil {
		return Discrepancy{}, err
	}
	if err := validateAlertText(alert.GetExpected(), maxDiscrepancyValue, "expected"); err != nil {
		return Discrepancy{}, err
	}
	if err := validateAlertText(alert.GetActual(), maxDiscrepancyValue, "actual"); err != nil {
		return Discrepancy{}, err
	}
	merchantID := strings.TrimSpace(alert.GetMerchantId())
	if merchantID != "" {
		parsed, err := uuid.Parse(merchantID)
		if err != nil {
			return Discrepancy{}, errors.New("merchant_id is not a valid UUID")
		}
		merchantID = parsed.String()
	}
	createdAt := envelopeOccurredAt
	if stamp := alert.GetCreatedAt(); stamp != nil {
		if err := stamp.CheckValid(); err != nil {
			return Discrepancy{}, errors.New("created_at is not a valid timestamp")
		}
		createdAt = stamp.AsTime()
	}
	if createdAt.IsZero() {
		return Discrepancy{}, errors.New("the alert time is missing")
	}
	return Discrepancy{
		ID:         eventUUID.String(),
		Type:       discrepancyType,
		RefID:      refID,
		MerchantID: merchantID,
		Severity:   severity,
		Expected:   alert.GetExpected(),
		Actual:     alert.GetActual(),
		Status:     DiscrepancyStatusOpen,
		CreatedAt:  createdAt.UTC(),
	}, nil
}

// ParseDiscrepancySeverity validates a severity filter value.
func ParseDiscrepancySeverity(name string) (string, bool) {
	switch name {
	case DiscrepancySeverityInfo, DiscrepancySeverityWarning, DiscrepancySeverityCritical:
		return name, true
	default:
		return "", false
	}
}

// ParseDiscrepancyResolution validates a resolution value.
func ParseDiscrepancyResolution(name string) (string, bool) {
	switch name {
	case DiscrepancyResolutionConfirmPlatform, DiscrepancyResolutionConfirmGateway,
		DiscrepancyResolutionManualAdjustment:
		return name, true
	default:
		return "", false
	}
}

// ValidateResolutionComment checks the optional operator comment.
func ValidateResolutionComment(comment string) error {
	if utf8.RuneCountInString(comment) > maxResolutionComment {
		return errors.New("comment must be at most 1024 characters")
	}
	for _, r := range comment {
		if unicode.IsControl(r) {
			return errors.New("comment must not contain control characters")
		}
	}
	return nil
}

// AuditView renders the whitelisted journal snapshot.
func (d Discrepancy) AuditView() DiscrepancyAuditView {
	return DiscrepancyAuditView{
		ID: d.ID, Type: d.Type, RefID: d.RefID, MerchantID: d.MerchantID,
		Severity: d.Severity, Status: d.Status, Resolution: d.Resolution, Comment: d.Comment,
	}
}

// validateAlertText bounds one copied alert field (empty values are allowed).
func validateAlertText(value string, max int, field string) error {
	if utf8.RuneCountInString(value) > max {
		return errors.New(field + " is too long")
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return errors.New(field + " must not contain control characters")
		}
	}
	return nil
}
