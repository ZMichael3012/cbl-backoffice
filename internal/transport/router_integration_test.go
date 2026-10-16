//go:build integration

package transport_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/cbl/cbl-kit/ginmw"

	"github.com/cbl/cbl-backoffice/internal/auth"
	"github.com/cbl/cbl-backoffice/internal/storage"
	"github.com/cbl/cbl-backoffice/internal/testsupport"
	"github.com/cbl/cbl-backoffice/internal/transport"
	"github.com/cbl/cbl-backoffice/internal/usecase"
)

const integrationSecret = "integration-admin-jwt-secret-0123456789"

// mint signs an operator token with the integration secret.
func mint(t *testing.T, roles []string, ttl time.Duration) string {
	t.Helper()
	claims := jwt.MapClaims{
		"sub":   "op-1",
		"roles": roles,
		"exp":   time.Now().Add(ttl).Unix(),
		"iat":   time.Now().Unix(),
	}
	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(integrationSecret))
	require.NoError(t, err)
	return signed
}

// request performs one GET with an optional token.
func request(router http.Handler, path, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

// TestAuditLogFlowOverDatabase exercises auth, RBAC, the write path and
// pagination against a real PostgreSQL through the real router.
func TestAuditLogFlowOverDatabase(t *testing.T) {
	ctx := context.Background()
	db := testsupport.OpenMigrated(t)

	store := storage.NewAuditStore(db)
	writer := usecase.NewAuditWriter(store)
	reader := usecase.NewAuditReader(store)
	verifier, err := auth.NewVerifier(integrationSecret)
	require.NoError(t, err)

	router := transport.NewRouter(transport.Options{
		Log:    zap.NewNop(),
		Verify: verifier.Verify,
		Audit:  reader,
		Ping:   func(pingCtx context.Context) error { return storage.Ready(pingCtx, db) },
	})

	// Seed through the real write path (what 12.2 actions will do).
	for i, action := range []string{"payout.approve", "routing.update", "audit.export"} {
		require.NoError(t, writer.Record(ctx, usecase.AuditEntry{
			OperatorID:   "op-1",
			RequestID:    "req-seed",
			Action:       action,
			ResourceType: "payout",
			ResourceID:   "po-1",
			After:        map[string]int{"version": i},
		}))
	}

	// 401 without a token.
	rec := request(router, "/admin/v1/audit-log", "")
	require.Equal(t, http.StatusUnauthorized, rec.Code)

	// 401 with an expired token.
	expired := mint(t, []string{"auditor"}, -time.Minute)
	rec = request(router, "/admin/v1/audit-log", expired)
	require.Equal(t, http.StatusUnauthorized, rec.Code)

	// 403 without the audit:read permission.
	rec = request(router, "/admin/v1/audit-log", mint(t, []string{"payout_operator"}, time.Hour))
	require.Equal(t, http.StatusForbidden, rec.Code)

	// Auditor pages: limit 2 -> 2 items with a cursor, then the last item.
	auditor := mint(t, []string{"auditor"}, time.Hour)
	rec = request(router, "/admin/v1/audit-log?limit=2", auditor)
	require.Equal(t, http.StatusOK, rec.Code)
	var page struct {
		Items []struct {
			ID       string `json:"id"`
			Action   string `json:"action"`
			AfterRaw string `json:"-"`
		} `json:"items"`
		NextCursor string `json:"next_cursor"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &page))
	require.Len(t, page.Items, 2)
	require.NotEmpty(t, page.NextCursor)

	rec = request(router, "/admin/v1/audit-log?limit=2&cursor="+page.NextCursor, auditor)
	require.Equal(t, http.StatusOK, rec.Code)
	var page2 struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
		NextCursor string `json:"next_cursor"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &page2))
	require.Len(t, page2.Items, 1)
	assert.Empty(t, page2.NextCursor)

	// Filter by operator and action; the cursor of another filter set is rejected.
	rec = request(router, "/admin/v1/audit-log?operator_id=op-1&action=payout.approve", auditor)
	require.Equal(t, http.StatusOK, rec.Code)
	rec = request(router, "/admin/v1/audit-log?operator_id=op-2&cursor="+page.NextCursor, auditor)
	require.Equal(t, http.StatusBadRequest, rec.Code)

	// A forged cursor stays a 400 without touching the journal rows.
	rec = request(router, "/admin/v1/audit-log?cursor="+page.NextCursor[:10]+"zz", auditor)
	require.Equal(t, http.StatusBadRequest, rec.Code)

	// The admin role also reads; problem+json carries the request id.
	rec = request(router, "/admin/v1/audit-log", mint(t, []string{"admin"}, time.Hour))
	require.Equal(t, http.StatusOK, rec.Code)
	var problem ginmw.Problem
	rec = request(router, "/admin/v1/audit-log?limit=0", auditor)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &problem))
	assert.NotEmpty(t, problem.RequestID)
}
