package transport

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/gin-gonic/gin"

	"github.com/cbl/cbl-kit/ginmw"
	"github.com/cbl/cbl-kit/grpcmw"

	"github.com/cbl/cbl-backoffice/internal/domain"
	"github.com/cbl/cbl-backoffice/internal/transport/httpmw"
	"github.com/cbl/cbl-backoffice/internal/usecase"
)

// Bounds of the routing and decision payloads (CBL-12.2 FR-3; mirrors the
// connector rule bounds so obvious violations fail fast).
const (
	maxRoutingRules   = 1000
	maxProviderName   = 64
	maxAdminReason    = 255
	maxPriority       = 1_000_000
	maxWeight         = 1_000_000
	maxListValueBytes = 64
	maxListValues     = 64
	maxBodyBytes      = 1 << 20
)

// payoutDecisionRequest is the body of approve/reject.
type payoutDecisionRequest struct {
	OperatorID string `json:"operator_id"`
	Reason     string `json:"reason"`
}

// payoutAdminJSON is the decision response (PayoutAdmin of the contract).
type payoutAdminJSON struct {
	ID         string   `json:"id"`
	Status     string   `json:"status"`
	ApprovedBy []string `json:"approved_by"`
}

// disableProviderRequest is the optional body of the disable route.
type disableProviderRequest struct {
	Reason string `json:"reason"`
}

// routingRuleJSON is one routing rule of the contract shape.
type routingRuleJSON struct {
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

// routingRuleSetJSON is the routing configuration of the contract shape.
type routingRuleSetJSON struct {
	Revision int64             `json:"revision"`
	Items    []routingRuleJSON `json:"items"`
}

// ActionsHandler serves the mutating admin routes of CBL-12.2.
type ActionsHandler struct {
	actions *usecase.AdminActions
}

// NewActionsHandler creates the handler.
func NewActionsHandler(actions *usecase.AdminActions) *ActionsHandler {
	return &ActionsHandler{actions: actions}
}

// ApprovePayout handles POST /admin/v1/payouts/{id}/approve.
func (h *ActionsHandler) ApprovePayout(c *gin.Context) {
	h.decidePayout(c, true)
}

// RejectPayout handles POST /admin/v1/payouts/{id}/reject.
func (h *ActionsHandler) RejectPayout(c *gin.Context) {
	h.decidePayout(c, false)
}

// decidePayout shares the approval and rejection flow (FR-2).
func (h *ActionsHandler) decidePayout(c *gin.Context, approve bool) {
	operator := httpmw.OperatorFrom(c)
	var body payoutDecisionRequest
	if !bindStrict(c, &body) {
		return
	}
	subject := strings.TrimSpace(body.OperatorID)
	if subject == "" || subject != operator.Subject {
		ginmw.AbortFromError(c, grpcmw.E(grpcmw.KindPermissionDenied, "operator_mismatch",
			"operator_id must match the token subject", nil))
		return
	}
	reason := strings.TrimSpace(body.Reason)
	if !approve && reason == "" {
		ginmw.AbortWithProblem(c, http.StatusUnprocessableEntity, "validation_failed",
			"Unprocessable Entity", "reason is required for a rejection")
		return
	}
	if err := validateReason(reason); err != nil {
		AbortValidation(c, err.Error())
		return
	}
	view, err := h.actions.DecidePayout(c.Request.Context(), operator.Subject, ginmw.RequestIDFrom(c),
		c.Param("id"), approve, reason)
	if err != nil {
		abortActionError(c, err)
		return
	}
	approvedBy := view.ApprovedBy
	if approvedBy == nil {
		// The contract types approved_by as an array: never null.
		approvedBy = []string{}
	}
	c.JSON(http.StatusOK, payoutAdminJSON{ID: view.ID, Status: view.Status, ApprovedBy: approvedBy})
}

// GetRoutingRules handles GET /admin/v1/routing-rules.
func (h *ActionsHandler) GetRoutingRules(c *gin.Context) {
	revision, rules, err := h.actions.GetRouting(c.Request.Context())
	if err != nil {
		abortActionError(c, err)
		return
	}
	c.JSON(http.StatusOK, routingRuleSetJSON{Revision: revision, Items: routingItemsJSON(rules)})
}

// PutRoutingRules handles PUT /admin/v1/routing-rules.
func (h *ActionsHandler) PutRoutingRules(c *gin.Context) {
	operator := httpmw.OperatorFrom(c)
	var body routingRuleSetJSON
	if !bindStrict(c, &body) {
		return
	}
	rules, err := validateRoutingSet(body)
	if err != nil {
		AbortValidation(c, err.Error())
		return
	}
	revision, stored, err := h.actions.PutRouting(c.Request.Context(), operator.Subject, ginmw.RequestIDFrom(c),
		body.Revision, rules)
	if err != nil {
		abortActionError(c, err)
		return
	}
	c.JSON(http.StatusOK, routingRuleSetJSON{Revision: revision, Items: routingItemsJSON(stored)})
}

// DisableProvider handles POST /admin/v1/providers/{id}/disable.
func (h *ActionsHandler) DisableProvider(c *gin.Context) {
	operator := httpmw.OperatorFrom(c)
	var body disableProviderRequest
	if c.Request.ContentLength != 0 {
		if !bindStrict(c, &body) {
			return
		}
	}
	reason := strings.TrimSpace(body.Reason)
	if err := validateReason(reason); err != nil {
		AbortValidation(c, err.Error())
		return
	}
	if _, err := h.actions.DisableProvider(c.Request.Context(), operator.Subject, ginmw.RequestIDFrom(c),
		c.Param("id"), reason); err != nil {
		abortActionError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// bindStrict decodes the body rejecting unknown fields; a failure responds 400.
// The body is capped at 1 MiB, matching the gRPC receive limit of the connector
// (a larger payload could never be applied).
func bindStrict(c *gin.Context, dst any) bool {
	if c.Request.Body == nil {
		AbortValidation(c, "request body is required")
		return false
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBodyBytes)
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		AbortValidation(c, "request body is invalid")
		return false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		AbortValidation(c, "request body must be a single JSON object")
		return false
	}
	return true
}

// validateReason checks an optional reason: up to 255 characters without
// control characters.
func validateReason(reason string) error {
	if utf8.RuneCountInString(reason) > maxAdminReason {
		return errors.New("reason must be at most 255 characters")
	}
	for _, r := range reason {
		if r < 0x20 || r == 0x7F {
			return errors.New("reason must not contain control characters")
		}
	}
	return nil
}

// validateRoutingSet validates the PUT payload and converts it to the domain
// form; violations are plain validation messages (HTTP 400).
func validateRoutingSet(set routingRuleSetJSON) ([]domain.RoutingRule, error) {
	if set.Revision < 0 {
		return nil, errors.New("revision must not be negative")
	}
	if len(set.Items) == 0 || len(set.Items) > maxRoutingRules {
		return nil, errors.New("items must contain between 1 and 1000 rules")
	}
	rules := make([]domain.RoutingRule, 0, len(set.Items))
	for i := range set.Items {
		item := set.Items[i]
		provider := strings.TrimSpace(item.Provider)
		if provider == "" || utf8.RuneCountInString(provider) > maxProviderName {
			return nil, errors.New("provider must be 1..64 characters")
		}
		for _, r := range provider {
			if r < 0x21 || r > 0x7E {
				return nil, errors.New("provider must be visible ASCII")
			}
		}
		switch item.Operation {
		case "", "invoice", "payout":
		default:
			return nil, errors.New("operation must be empty, invoice or payout")
		}
		if item.Priority < 0 || item.Priority > maxPriority {
			return nil, errors.New("priority must be between 0 and 1000000")
		}
		if item.Weight < 1 || item.Weight > maxWeight {
			return nil, errors.New("weight must be between 1 and 1000000")
		}
		if item.MinAmount < 0 || item.MaxAmount < 0 {
			return nil, errors.New("amount bounds must not be negative")
		}
		if item.MinAmount > 0 && item.MaxAmount > 0 && item.MinAmount > item.MaxAmount {
			return nil, errors.New("min_amount must not exceed max_amount")
		}
		if err := validateListValues(item.Networks); err != nil {
			return nil, err
		}
		if err := validateListValues(item.Assets); err != nil {
			return nil, err
		}
		rules = append(rules, domain.RoutingRule{
			Provider:  provider,
			Operation: item.Operation,
			Networks:  item.Networks,
			Assets:    item.Assets,
			Priority:  item.Priority,
			Weight:    item.Weight,
			MinAmount: item.MinAmount,
			MaxAmount: item.MaxAmount,
			Enabled:   item.Enabled,
		})
	}
	return rules, nil
}

// validateListValues bounds the networks/assets entries: count and shape.
func validateListValues(values []string) error {
	if len(values) > maxListValues {
		return errors.New("network and asset lists must contain at most 64 values")
	}
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" || len(trimmed) > maxListValueBytes {
			return errors.New("network and asset values must be 1..64 characters")
		}
		for _, r := range trimmed {
			if r < 0x21 || r > 0x7E {
				return errors.New("network and asset values must be visible ASCII")
			}
		}
	}
	return nil
}

// routingItemsJSON maps domain rules for the response; empty lists render as
// empty arrays (never null) per the contract.
func routingItemsJSON(rules []domain.RoutingRule) []routingRuleJSON {
	items := make([]routingRuleJSON, 0, len(rules))
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
		items = append(items, routingRuleJSON{
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

// abortActionError maps the action errors to the contract codes (FR-2/FR-3/FR-4).
func abortActionError(c *gin.Context, err error) {
	switch grpcmw.KindOf(err) {
	case grpcmw.KindNotFound:
		code := grpcmw.CodeOf(err)
		if code == "" {
			code = "not_found"
		}
		ginmw.AbortWithProblem(c, http.StatusNotFound, code, "Not Found", problemMessage(err))
	case grpcmw.KindFailedPrecondition:
		code := grpcmw.CodeOf(err)
		if code == "" {
			code = "conflict"
		}
		ginmw.AbortWithProblem(c, http.StatusConflict, code, "Conflict", problemMessage(err))
	case grpcmw.KindInvalidArgument:
		AbortValidation(c, problemMessage(err))
	case grpcmw.KindUnavailable:
		c.Header("Retry-After", "1")
		ginmw.AbortWithProblem(c, http.StatusServiceUnavailable, "service_unavailable",
			"Service Unavailable", problemMessage(err))
	default:
		ginmw.AbortFromError(c, err)
	}
}
