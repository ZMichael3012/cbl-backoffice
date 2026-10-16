package domain

// Role names an operator role carried by the bearer token of the admin API.
type Role string

// Operator roles of the back-office (a closed set; a new role is an additive
// change with a spec, see CBL-12.1 section 11).
const (
	// RoleAdmin grants every permission of the platform.
	RoleAdmin Role = "admin"
	// RolePayoutOperator votes on large payouts (12.2).
	RolePayoutOperator Role = "payout_operator"
	// RoleRoutingManager manages provider routing rules (12.2).
	RoleRoutingManager Role = "routing_manager"
	// RoleReconOperator works through reconciliation discrepancies (12.3).
	RoleReconOperator Role = "recon_operator"
	// RoleProviderManager disables providers manually (12.2).
	RoleProviderManager Role = "provider_manager"
	// RoleAuditor reads the action journal and discrepancies.
	RoleAuditor Role = "auditor"
)

// Permission names one auditable operation of the admin API.
type Permission string

// Permissions of the admin API; routes must declare exactly one and deny by default.
const (
	// PermissionPayoutsApprove approves or rejects large payouts.
	PermissionPayoutsApprove Permission = "payouts:approve"
	// PermissionRoutingRead reads provider routing rules.
	PermissionRoutingRead Permission = "routing:read"
	// PermissionRoutingManage replaces provider routing rules.
	PermissionRoutingManage Permission = "routing:manage"
	// PermissionReconciliationRead reads reconciliation discrepancies.
	PermissionReconciliationRead Permission = "reconciliation:read"
	// PermissionReconciliationResolve resolves discrepancies manually.
	PermissionReconciliationResolve Permission = "reconciliation:resolve"
	// PermissionProvidersDisable disables a provider manually.
	PermissionProvidersDisable Permission = "providers:disable"
	// PermissionAuditRead reads the operator action journal.
	PermissionAuditRead Permission = "audit:read"
)

// rolePermissions is the single source of truth of the RBAC matrix (CBL-12.1 FR-3).
var rolePermissions = map[Role][]Permission{
	RoleAdmin: {
		PermissionPayoutsApprove,
		PermissionRoutingRead,
		PermissionRoutingManage,
		PermissionReconciliationRead,
		PermissionReconciliationResolve,
		PermissionProvidersDisable,
		PermissionAuditRead,
	},
	RolePayoutOperator: {PermissionPayoutsApprove},
	RoleRoutingManager: {PermissionRoutingRead, PermissionRoutingManage},
	RoleReconOperator:  {PermissionReconciliationRead, PermissionReconciliationResolve},
	RoleProviderManager: {
		PermissionProvidersDisable,
	},
	RoleAuditor: {PermissionAuditRead, PermissionReconciliationRead},
}

// Operator is the authenticated subject of an admin API request.
type Operator struct {
	// Subject is the operator identifier from the token subject claim.
	Subject string
	// Roles are the validated roles from the token.
	Roles []Role
}

// ParseRole validates a role name from a token claim.
func ParseRole(name string) (Role, bool) {
	role := Role(name)
	if _, ok := rolePermissions[role]; ok {
		return role, true
	}
	return "", false
}

// Permissions returns the union of permissions granted by the given roles.
func Permissions(roles []Role) map[Permission]bool {
	granted := make(map[Permission]bool)
	for _, role := range roles {
		for _, permission := range rolePermissions[role] {
			granted[permission] = true
		}
	}
	return granted
}

// Has reports whether the operator holds the permission.
func (o Operator) Has(permission Permission) bool {
	return Permissions(o.Roles)[permission]
}
