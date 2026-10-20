package usecase

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/cbl/cbl-kit/grpcmw"

	"github.com/cbl/cbl-backoffice/internal/domain"
)

// Discrepancy listing limits and the cursor version (CBL-12.3).
const (
	defaultDiscrepancyLimit = 20
	maxDiscrepancyLimit     = 100
	discrepancyCursorMaxLen = 512
	discrepancyCursorVer    = 1
)

// DiscrepancyStore reads the discrepancies and resolves them atomically with
// the audit record (implemented by the storage).
type DiscrepancyStore interface {
	// ListDiscrepancies returns up to limit rows after the keyset position.
	ListDiscrepancies(ctx context.Context, filter domain.DiscrepancyFilter, limit int, after *domain.DiscrepancyKey) ([]domain.Discrepancy, error)
	// ResolveDiscrepancy applies the decision and appends the audit record in
	// one transaction; buildAudit renders the record from the open state.
	ResolveDiscrepancy(ctx context.Context, id string, decision domain.ResolveDecision, buildAudit func(before domain.Discrepancy) (domain.AuditRecord, error)) (domain.Discrepancy, error)
}

// DiscrepancyService serves the reconciliation operator surface (CBL-12.3).
type DiscrepancyService struct {
	store DiscrepancyStore
	audit *AuditWriter
	now   func() time.Time
}

// DiscrepancyOption configures the service.
type DiscrepancyOption func(*DiscrepancyService)

// WithDiscrepancyClock sets the time source.
func WithDiscrepancyClock(clock func() time.Time) DiscrepancyOption {
	return func(s *DiscrepancyService) {
		if clock != nil {
			s.now = clock
		}
	}
}

// NewDiscrepancyService creates the operator service.
func NewDiscrepancyService(store DiscrepancyStore, audit *AuditWriter, opts ...DiscrepancyOption) *DiscrepancyService {
	service := &DiscrepancyService{store: store, audit: audit, now: func() time.Time { return time.Now().UTC() }}
	for _, opt := range opts {
		opt(service)
	}
	return service
}

// List returns one page of the discrepancies: newest first, keyset pagination,
// the cursor is bound to the filter set.
func (s *DiscrepancyService) List(ctx context.Context, filter domain.DiscrepancyFilter, limit int, cursor string) (domain.DiscrepancyPage, error) {
	if filter.Severity != "" {
		if _, ok := domain.ParseDiscrepancySeverity(filter.Severity); !ok {
			return domain.DiscrepancyPage{}, invalid("severity must be info, warning or critical")
		}
	}
	if filter.MerchantID != "" {
		if _, err := uuid.Parse(filter.MerchantID); err != nil {
			return domain.DiscrepancyPage{}, invalid("merchant_id must be a UUID")
		}
	}
	if limit == 0 {
		limit = defaultDiscrepancyLimit
	}
	if limit < 1 || limit > maxDiscrepancyLimit {
		return domain.DiscrepancyPage{}, invalid("limit must be between 1 and 100")
	}
	after, err := DecodeDiscrepancyCursor(cursor, filter)
	if err != nil {
		return domain.DiscrepancyPage{}, err
	}
	rows, err := s.store.ListDiscrepancies(ctx, filter, limit+1, after)
	if err != nil {
		return domain.DiscrepancyPage{}, grpcmw.E(grpcmw.KindUnavailable, "service_unavailable",
			"discrepancies are temporarily unavailable", err)
	}
	page := domain.DiscrepancyPage{Items: rows}
	if len(rows) > limit {
		page.Items = rows[:limit]
		last := page.Items[len(page.Items)-1]
		page.NextCursor = EncodeDiscrepancyCursor(filter, domain.DiscrepancyKey{CreatedAt: last.CreatedAt, ID: last.ID})
	}
	return page, nil
}

// Resolve applies one operator resolution and its audit record atomically.
func (s *DiscrepancyService) Resolve(ctx context.Context, operatorID, requestID, id, resolution, comment string) (domain.Discrepancy, error) {
	if _, err := uuid.Parse(strings.TrimSpace(id)); err != nil {
		return domain.Discrepancy{}, invalid("id must be a UUID")
	}
	if strings.TrimSpace(operatorID) == "" {
		return domain.Discrepancy{}, invalid("operator_id is required")
	}
	resolution, ok := domain.ParseDiscrepancyResolution(resolution)
	if !ok {
		return domain.Discrepancy{}, invalid("resolution must be confirm_platform, confirm_gateway or manual_adjustment")
	}
	comment = strings.TrimSpace(comment)
	if err := domain.ValidateResolutionComment(comment); err != nil {
		return domain.Discrepancy{}, invalid(err.Error())
	}
	decision := domain.ResolveDecision{
		OperatorID: strings.TrimSpace(operatorID),
		Resolution: resolution,
		Comment:    comment,
		At:         s.now().UTC(),
	}
	resolved, err := s.store.ResolveDiscrepancy(ctx, id, decision, func(before domain.Discrepancy) (domain.AuditRecord, error) {
		return s.audit.Build(AuditEntry{
			OperatorID:   decision.OperatorID,
			RequestID:    requestID,
			Action:       "discrepancy.resolve",
			ResourceType: "discrepancy",
			ResourceID:   id,
			Before:       before.AuditView(),
			After:        resolvedView(before, decision),
		})
	})
	if err != nil {
		switch {
		case errors.Is(err, domain.ErrDiscrepancyNotFound):
			return domain.Discrepancy{}, grpcmw.E(grpcmw.KindNotFound, "discrepancy_not_found",
				"the discrepancy does not exist", nil)
		case errors.Is(err, domain.ErrDiscrepancyResolved):
			return domain.Discrepancy{}, grpcmw.E(grpcmw.KindFailedPrecondition, "discrepancy_resolved",
				"the discrepancy is already resolved", nil)
		default:
			return domain.Discrepancy{}, grpcmw.E(grpcmw.KindUnavailable, "service_unavailable",
				"the discrepancy is temporarily unavailable", err)
		}
	}
	return resolved, nil
}

// resolvedView renders the journal snapshot after the decision.
func resolvedView(before domain.Discrepancy, decision domain.ResolveDecision) domain.DiscrepancyAuditView {
	after := before
	after.Status = domain.DiscrepancyStatusResolved
	after.Resolution = decision.Resolution
	after.Comment = decision.Comment
	return after.AuditView()
}

// EncodeDiscrepancyCursor builds the opaque cursor of the next page.
func EncodeDiscrepancyCursor(filter domain.DiscrepancyFilter, key domain.DiscrepancyKey) string {
	return encodeKeysetCursor(discrepancyCursorVer, discrepancyFingerprint(filter), key.CreatedAt, key.ID)
}

// DecodeDiscrepancyCursor parses and validates a cursor against the filter set;
// an empty cursor is the first page.
func DecodeDiscrepancyCursor(cursor string, filter domain.DiscrepancyFilter) (*domain.DiscrepancyKey, error) {
	at, id, err := decodeKeysetCursor(cursor, discrepancyFingerprint(filter), discrepancyCursorVer, discrepancyCursorMaxLen)
	if err != nil || at == nil {
		return nil, err
	}
	return &domain.DiscrepancyKey{CreatedAt: *at, ID: id}, nil
}

// discrepancyFingerprint binds a cursor to one filter set.
func discrepancyFingerprint(filter domain.DiscrepancyFilter) string {
	return filter.Severity + "\x00" + filter.MerchantID
}
