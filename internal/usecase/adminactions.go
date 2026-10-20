package usecase

import (
	"context"
	"fmt"

	"github.com/cbl/cbl-backoffice/internal/domain"
)

// PayoutDecider records an operator decision on a payout (implemented by the
// PayoutService client).
type PayoutDecider interface {
	// Approve records a vote; the returned view reflects the payout state.
	Approve(ctx context.Context, payoutID, operatorID string, approve bool, reason string) (domain.PayoutView, error)
}

// RoutingAdmin is the connector administrative port (implemented by the
// ConnectorAdminService client).
type RoutingAdmin interface {
	// GetRules returns the current revision and rules.
	GetRules(ctx context.Context) (int64, []domain.RoutingRule, error)
	// PutRules replaces the configuration with optimistic concurrency.
	PutRules(ctx context.Context, revision int64, rules []domain.RoutingRule) (int64, []domain.RoutingRule, error)
	// DisableProvider disables a provider; already reports a repeat.
	DisableProvider(ctx context.Context, provider, reason string) (bool, error)
	// EnableProvider enables a disabled provider; already reports a repeat.
	EnableProvider(ctx context.Context, provider string) (bool, error)
}

// payoutAuditDTO is the whitelisted snapshot of a payout decision (CBL-12.2
// FR-6: only contract DTOs reach the immutable journal).
type payoutAuditDTO struct {
	ID         string   `json:"id"`
	Status     string   `json:"status"`
	ApprovedBy []string `json:"approved_by"`
}

// routingRuleAudit is one routing rule of the journal snapshot: the contract
// shape (snake_case, empty arrays render as [], never null).
type routingRuleAudit struct {
	Provider  string   `json:"provider"`
	Operation string   `json:"operation"`
	Networks  []string `json:"networks"`
	Assets    []string `json:"assets"`
	Priority  int      `json:"priority"`
	Weight    int      `json:"weight"`
	MinAmount int64    `json:"min_amount"`
	MaxAmount int64    `json:"max_amount"`
	Enabled   bool     `json:"enabled"`
}

// routingAuditDTO is the whitelisted snapshot of the routing configuration.
type routingAuditDTO struct {
	Revision int64              `json:"revision"`
	Items    []routingRuleAudit `json:"items"`
}

// providerAuditDTO is the whitelisted snapshot of a provider availability change.
type providerAuditDTO struct {
	Disabled bool   `json:"disabled"`
	Enabled  bool   `json:"enabled"`
	Reason   string `json:"reason"`
}

// AdminActions serves the mutating admin operations of CBL-12.2: payout
// decisions, routing replacement and provider disable, each followed by one
// audit record (the actions live in other services: no shared transaction, the
// record follows a successful action, see the spec section 5).
type AdminActions struct {
	payout  PayoutDecider
	routing RoutingAdmin
	audit   *AuditWriter
}

// NewAdminActions creates the actions service.
func NewAdminActions(payout PayoutDecider, routing RoutingAdmin, audit *AuditWriter) *AdminActions {
	return &AdminActions{payout: payout, routing: routing, audit: audit}
}

// DecidePayout forwards an operator decision and audits it (CBL-12.2 FR-2).
func (a *AdminActions) DecidePayout(ctx context.Context, operatorID, requestID, payoutID string, approve bool, reason string) (domain.PayoutView, error) {
	view, err := a.payout.Approve(ctx, payoutID, operatorID, approve, reason)
	if err != nil {
		return domain.PayoutView{}, err
	}
	action := "payout.reject"
	if approve {
		action = "payout.approve"
	}
	approvedBy := view.ApprovedBy
	if approvedBy == nil {
		// The contract types approved_by as an array: never null, even in the journal.
		approvedBy = []string{}
	}
	entry := AuditEntry{
		OperatorID:   operatorID,
		RequestID:    requestID,
		Action:       action,
		ResourceType: "payout",
		ResourceID:   payoutID,
		After: payoutAuditDTO{
			ID:         view.ID,
			Status:     view.Status,
			ApprovedBy: approvedBy,
		},
	}
	if err := a.audit.Record(ctx, entry); err != nil {
		return domain.PayoutView{}, fmt.Errorf("admin actions: audit payout decision: %w", err)
	}
	return view, nil
}

// GetRouting returns the current routing configuration (a read, not audited).
func (a *AdminActions) GetRouting(ctx context.Context) (int64, []domain.RoutingRule, error) {
	revision, rules, err := a.routing.GetRules(ctx)
	if err != nil {
		return 0, nil, err
	}
	return revision, rules, nil
}

// PutRouting replaces the routing configuration and audits it with the
// before/after snapshots (CBL-12.2 FR-3, section 5: the conflict guards the
// successful path, so `after` is always the stored configuration).
func (a *AdminActions) PutRouting(ctx context.Context, operatorID, requestID string, revision int64, rules []domain.RoutingRule) (int64, []domain.RoutingRule, error) {
	beforeRevision, beforeRules, err := a.routing.GetRules(ctx)
	if err != nil {
		return 0, nil, err
	}
	newRevision, stored, err := a.routing.PutRules(ctx, revision, rules)
	if err != nil {
		return 0, nil, err
	}
	entry := AuditEntry{
		OperatorID:   operatorID,
		RequestID:    requestID,
		Action:       "routing.update",
		ResourceType: "routing",
		ResourceID:   "",
		Before:       routingAuditDTO{Revision: beforeRevision, Items: routingAuditRules(beforeRules)},
		After:        routingAuditDTO{Revision: newRevision, Items: routingAuditRules(stored)},
	}
	if err := a.audit.Record(ctx, entry); err != nil {
		return 0, nil, fmt.Errorf("admin actions: audit routing update: %w", err)
	}
	return newRevision, stored, nil
}

// routingAuditRules maps rules into the journal snapshot shape, normalizing
// nil lists to empty arrays.
func routingAuditRules(rules []domain.RoutingRule) []routingRuleAudit {
	items := make([]routingRuleAudit, 0, len(rules))
	for i := range rules {
		rule := rules[i]
		networks := rule.Networks
		if networks == nil {
			networks = []string{}
		}
		assets := rule.Assets
		if assets == nil {
			assets = []string{}
		}
		items = append(items, routingRuleAudit{
			Provider:  rule.Provider,
			Operation: rule.Operation,
			Networks:  networks,
			Assets:    assets,
			Priority:  rule.Priority,
			Weight:    rule.Weight,
			MinAmount: rule.MinAmount,
			MaxAmount: rule.MaxAmount,
			Enabled:   rule.Enabled,
		})
	}
	return items
}

// DisableProvider disables a provider and audits the first disable; a repeated
// disable is idempotent and leaves the journal untouched (CBL-12.2 FR-4). A
// repeat whose first record was lost (the audit write failed after a successful
// action) is compensated: the record is appended when the journal lacks it,
// so the applied action is never unjournaled (review finding).
func (a *AdminActions) DisableProvider(ctx context.Context, operatorID, requestID, provider, reason string) (bool, error) {
	already, err := a.routing.DisableProvider(ctx, provider, reason)
	if err != nil {
		return false, err
	}
	if already {
		exists, checkErr := a.audit.ExistsForResource(ctx, "provider.disable", "provider", provider)
		if checkErr != nil {
			return false, fmt.Errorf("admin actions: check audit journal: %w", checkErr)
		}
		if exists {
			return true, nil
		}
	}
	entry := AuditEntry{
		OperatorID:   operatorID,
		RequestID:    requestID,
		Action:       "provider.disable",
		ResourceType: "provider",
		ResourceID:   provider,
		After:        providerAuditDTO{Disabled: true, Reason: reason},
	}
	if err := a.audit.Record(ctx, entry); err != nil {
		return false, fmt.Errorf("admin actions: audit provider disable: %w", err)
	}
	return already, nil
}

// EnableProvider removes a provider disable and audits the first enable; a
// repeated enable is idempotent and leaves the journal untouched. A repeat
// whose first record was lost (the audit write failed after a successful
// action) is compensated, so the applied action is never unjournaled (mirror
// of the disable rule, CBL-17.15).
func (a *AdminActions) EnableProvider(ctx context.Context, operatorID, requestID, provider, reason string) (bool, error) {
	already, err := a.routing.EnableProvider(ctx, provider)
	if err != nil {
		return false, err
	}
	if already {
		exists, checkErr := a.audit.ExistsForResource(ctx, "provider.enable", "provider", provider)
		if checkErr != nil {
			return false, fmt.Errorf("admin actions: check audit journal: %w", checkErr)
		}
		if exists {
			return true, nil
		}
	}
	entry := AuditEntry{
		OperatorID:   operatorID,
		RequestID:    requestID,
		Action:       "provider.enable",
		ResourceType: "provider",
		ResourceID:   provider,
		After:        providerAuditDTO{Enabled: true, Reason: reason},
	}
	if err := a.audit.Record(ctx, entry); err != nil {
		return false, fmt.Errorf("admin actions: audit provider enable: %w", err)
	}
	return already, nil
}
