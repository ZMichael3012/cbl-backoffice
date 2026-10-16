package domain

// RoutingRule is one provider routing rule as seen by the admin contour; the
// form matches RoutingRuleAdmin of the connector contract (CBL-12.2): empty
// networks/assets mean any, amount bounds are in minor units (0 - no bound).
type RoutingRule struct {
	// Provider is the provider registration name.
	Provider string
	// Operation limits the rule: "", invoice or payout.
	Operation string
	// Networks restricts the rule to the listed networks (empty - any).
	Networks []string
	// Assets restricts the rule to the listed assets (empty - any).
	Assets []string
	// Priority orders the rules, lower is more important.
	Priority int
	// Weight distributes traffic within one priority.
	Weight int
	// MinAmount is the lower bound in minor units (0 - no bound).
	MinAmount int64
	// MaxAmount is the upper bound in minor units (0 - no bound).
	MaxAmount int64
	// Enabled reports whether the router may use the rule.
	Enabled bool
}

// PayoutView is the payout state returned by a decision.
type PayoutView struct {
	// ID is the payout identifier.
	ID string
	// Status is the payout state after the decision.
	Status string
	// ApprovedBy lists the operators whose approval votes were recorded.
	ApprovedBy []string
}
