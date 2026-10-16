package domain_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/cbl/cbl-backoffice/internal/domain"
)

func TestParseRole(t *testing.T) {
	t.Parallel()

	role, ok := domain.ParseRole("auditor")
	assert.True(t, ok)
	assert.Equal(t, domain.RoleAuditor, role)

	_, ok = domain.ParseRole("root")
	assert.False(t, ok)

	_, ok = domain.ParseRole("")
	assert.False(t, ok)
}

func TestPermissionsMatrix(t *testing.T) {
	t.Parallel()

	admin := domain.Operator{Subject: "op-1", Roles: []domain.Role{domain.RoleAdmin}}
	for _, permission := range []domain.Permission{
		domain.PermissionPayoutsApprove,
		domain.PermissionRoutingRead,
		domain.PermissionRoutingManage,
		domain.PermissionReconciliationRead,
		domain.PermissionReconciliationResolve,
		domain.PermissionProvidersDisable,
		domain.PermissionAuditRead,
	} {
		assert.True(t, admin.Has(permission), "admin must hold %s", permission)
	}

	cases := []struct {
		role       domain.Role
		granted    []domain.Permission
		notGranted []domain.Permission
	}{
		{
			role:       domain.RolePayoutOperator,
			granted:    []domain.Permission{domain.PermissionPayoutsApprove},
			notGranted: []domain.Permission{domain.PermissionAuditRead, domain.PermissionRoutingManage},
		},
		{
			role:       domain.RoleRoutingManager,
			granted:    []domain.Permission{domain.PermissionRoutingRead, domain.PermissionRoutingManage},
			notGranted: []domain.Permission{domain.PermissionPayoutsApprove, domain.PermissionAuditRead},
		},
		{
			role:       domain.RoleReconOperator,
			granted:    []domain.Permission{domain.PermissionReconciliationRead, domain.PermissionReconciliationResolve},
			notGranted: []domain.Permission{domain.PermissionAuditRead, domain.PermissionPayoutsApprove},
		},
		{
			role:       domain.RoleProviderManager,
			granted:    []domain.Permission{domain.PermissionProvidersDisable},
			notGranted: []domain.Permission{domain.PermissionPayoutsApprove, domain.PermissionAuditRead},
		},
		{
			role:       domain.RoleAuditor,
			granted:    []domain.Permission{domain.PermissionAuditRead, domain.PermissionReconciliationRead},
			notGranted: []domain.Permission{domain.PermissionReconciliationResolve, domain.PermissionRoutingManage},
		},
	}
	for _, tc := range cases {
		operator := domain.Operator{Subject: "op-1", Roles: []domain.Role{tc.role}}
		for _, permission := range tc.granted {
			assert.True(t, operator.Has(permission), "%s must hold %s", tc.role, permission)
		}
		for _, permission := range tc.notGranted {
			assert.False(t, operator.Has(permission), "%s must not hold %s", tc.role, permission)
		}
	}
}

func TestPermissionsUnionAndDenyByDefault(t *testing.T) {
	t.Parallel()

	operator := domain.Operator{
		Subject: "op-2",
		Roles:   []domain.Role{domain.RolePayoutOperator, domain.RoleAuditor},
	}
	assert.True(t, operator.Has(domain.PermissionPayoutsApprove))
	assert.True(t, operator.Has(domain.PermissionAuditRead))
	assert.False(t, operator.Has(domain.PermissionProvidersDisable))

	nobody := domain.Operator{Subject: "op-3"}
	assert.False(t, nobody.Has(domain.PermissionAuditRead), "no roles grant nothing")
}
