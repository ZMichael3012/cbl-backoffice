//go:build integration

package transport_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/cbl/cbl-backoffice/internal/auth"
	"github.com/cbl/cbl-backoffice/internal/domain"
	"github.com/cbl/cbl-backoffice/internal/storage"
	"github.com/cbl/cbl-backoffice/internal/testsupport"
	"github.com/cbl/cbl-backoffice/internal/transport"
	"github.com/cbl/cbl-backoffice/internal/usecase"
)

// TestAdminActionsFlowOverDatabase exercises the 12.2 routes end to end through
// the real router with a real audit journal and fake payout/connector ports.
func TestAdminActionsFlowOverDatabase(t *testing.T) {
	ctx := context.Background()
	db := testsupport.OpenMigrated(t)
	store := storage.NewAuditStore(db)

	verifier, err := auth.NewVerifier(integrationSecret)
	require.NoError(t, err)

	payout := &actionPayout{view: domain.PayoutView{ID: "po-1", Status: "approved", ApprovedBy: []string{"op-1"}}}
	routing := &actionRouting{
		revision:    1,
		rules:       []domain.RoutingRule{{Provider: "mock", Weight: 1, Enabled: true}},
		putRevision: 2,
		putRules:    []domain.RoutingRule{{Provider: "mock", Weight: 3, Enabled: true}},
	}

	router := transport.NewRouter(transport.Options{
		Log:     zap.NewNop(),
		Verify:  verifier.Verify,
		Audit:   usecase.NewAuditReader(store),
		Actions: usecase.NewAdminActions(payout, routing, usecase.NewAuditWriter(store)),
		Ping:    func(context.Context) error { return nil },
	})

	admin := mint(t, []string{"admin"}, time.Hour)

	// Approve: 200 and one journal row with the whitelisted DTO.
	rec := doAction(router, http.MethodPost, "/admin/v1/payouts/po-1/approve", admin, `{"operator_id":"op-1","reason":"checked"}`)
	assert.Equal(t, http.StatusOK, rec.Code)

	// Routing replacement: 200 and the before/after snapshots.
	rec = doAction(router, http.MethodPut, "/admin/v1/routing-rules", admin,
		`{"revision":1,"items":[{"provider":"mock","priority":0,"weight":3,"enabled":true}]}`)
	require.Equal(t, http.StatusOK, rec.Code)

	// Provider disable: 204 once, then the idempotent repeat.
	rec = doAction(router, http.MethodPost, "/admin/v1/providers/mock/disable", admin, `{"reason":"maintenance"}`)
	assert.Equal(t, http.StatusNoContent, rec.Code)
	routing.already = true
	rec = doAction(router, http.MethodPost, "/admin/v1/providers/mock/disable", admin, "")
	assert.Equal(t, http.StatusNoContent, rec.Code)

	// The journal holds exactly three records (the repeat is not audit-recorded
	// and the audit-log read itself is not an action) and serves them through
	// the 12.1 read API.
	rows, err := store.List(ctx, domain.AuditFilter{}, 10, nil)
	require.NoError(t, err)
	require.Len(t, rows, 3, "approve, routing.update and the first disable only")
	assert.Equal(t, "provider.disable", rows[0].Action)
	assert.Equal(t, "routing.update", rows[1].Action)
	assert.Equal(t, "payout.approve", rows[2].Action, "newest first")

	var after struct {
		Revision int64 `json:"revision"`
	}
	require.NoError(t, json.Unmarshal(rows[1].After, &after))
	assert.EqualValues(t, 2, after.Revision)

	rec = doAction(router, http.MethodGet, "/admin/v1/audit-log?limit=10", admin, "")
	require.Equal(t, http.StatusOK, rec.Code)
	var page struct {
		Items []struct {
			Action string `json:"action"`
		} `json:"items"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &page))
	assert.Len(t, page.Items, 3)
}
