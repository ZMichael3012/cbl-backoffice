package transport_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/cbl/cbl-kit/ginmw"
	"github.com/cbl/cbl-kit/grpcmw"

	"github.com/cbl/cbl-backoffice/internal/domain"
	"github.com/cbl/cbl-backoffice/internal/transport"
	"github.com/cbl/cbl-backoffice/internal/usecase"
)

// fakeStore serves fixed journal pages.
type fakeStore struct {
	rows []domain.AuditRecord
	err  error
	got  struct {
		filter domain.AuditFilter
		limit  int
		after  *domain.AuditKey
	}
}

// List implements usecase.JournalStore.
func (f *fakeStore) List(_ context.Context, filter domain.AuditFilter, limit int, after *domain.AuditKey) ([]domain.AuditRecord, error) {
	f.got.filter = filter
	f.got.limit = limit
	f.got.after = after
	if f.err != nil {
		return nil, f.err
	}
	return f.rows, nil
}

// verifier maps tokens to fixed identities.
func verifier(_ context.Context, token string) (domain.Operator, error) {
	switch token {
	case "auditor":
		return domain.Operator{Subject: "op-aud", Roles: []domain.Role{domain.RoleAuditor}}, nil
	case "payout":
		return domain.Operator{Subject: "op-pay", Roles: []domain.Role{domain.RolePayoutOperator}}, nil
	case "routing":
		return domain.Operator{Subject: "op-route", Roles: []domain.Role{domain.RoleRoutingManager}}, nil
	case "admin":
		return domain.Operator{Subject: "op-adm", Roles: []domain.Role{domain.RoleAdmin}}, nil
	default:
		return domain.Operator{}, grpcmw.E(grpcmw.KindUnauthenticated, "unauthenticated", "bad token", nil)
	}
}

// newRouter builds the router over a fake store.
func newRouter(store *fakeStore, ping func(ctx context.Context) error) *gin.Engine {
	return transport.NewRouter(transport.Options{
		Log:    zap.NewNop(),
		Verify: verifier,
		Audit:  usecase.NewAuditReader(store),
		Ping:   ping,
	})
}

// get performs one GET request.
func get(router *gin.Engine, path, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func TestHealthEndpoints(t *testing.T) {
	t.Parallel()

	router := newRouter(&fakeStore{}, nil)
	assert.Equal(t, http.StatusOK, get(router, "/healthz", "").Code)
	assert.Equal(t, http.StatusOK, get(router, "/readyz", "").Code)

	router = newRouter(&fakeStore{}, func(context.Context) error { return errors.New("db down") })
	assert.Equal(t, http.StatusServiceUnavailable, get(router, "/readyz", "").Code)
}

func TestAuditLogAuthorization(t *testing.T) {
	t.Parallel()

	router := newRouter(&fakeStore{}, nil)

	rec := get(router, "/admin/v1/audit-log", "")
	require.Equal(t, http.StatusUnauthorized, rec.Code)
	require.Equal(t, "application/problem+json", rec.Header().Get("Content-Type"))
	var problem ginmw.Problem
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &problem))
	assert.Equal(t, "unauthenticated", problem.Code)

	rec = get(router, "/admin/v1/audit-log", "payout")
	require.Equal(t, http.StatusForbidden, rec.Code)
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &problem))
	assert.Equal(t, "permission_denied", problem.Code)
	assert.NotEmpty(t, problem.RequestID)

	rec = get(router, "/admin/v1/audit-log", "auditor")
	require.Equal(t, http.StatusOK, rec.Code)
	rec = get(router, "/admin/v1/audit-log", "admin")
	require.Equal(t, http.StatusOK, rec.Code)
}

func TestAuditLogPage(t *testing.T) {
	t.Parallel()

	occurred := time.Date(2026, 10, 16, 10, 0, 0, 123000000, time.UTC)
	store := &fakeStore{rows: []domain.AuditRecord{
		{
			ID:         "018f0000-0000-7000-8000-000000000001",
			OccurredAt: occurred,
			OperatorID: "op-1",
			Action:     "payout.approve",
			Before:     json.RawMessage(`{"status":"processing"}`),
		},
		{
			ID:         "018f0000-0000-7000-8000-000000000002",
			OccurredAt: occurred.Add(time.Second),
			OperatorID: "op-1",
			Action:     "payout.approve",
		},
	}}
	router := newRouter(store, nil)

	rec := get(router, "/admin/v1/audit-log?operator_id=op-1&action=payout.approve&limit=1", "auditor")
	require.Equal(t, http.StatusOK, rec.Code)

	var page struct {
		Items []struct {
			ID         string          `json:"id"`
			OccurredAt string          `json:"occurred_at"`
			OperatorID string          `json:"operator_id"`
			Action     string          `json:"action"`
			Before     json.RawMessage `json:"before"`
			After      json.RawMessage `json:"after"`
		} `json:"items"`
		NextCursor string `json:"next_cursor"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &page))
	require.Len(t, page.Items, 1)
	assert.Equal(t, "018f0000-0000-7000-8000-000000000001", page.Items[0].ID)
	assert.Equal(t, "2026-10-16T10:00:00.123Z", page.Items[0].OccurredAt)
	assert.JSONEq(t, `{"status":"processing"}`, string(page.Items[0].Before))
	assert.Equal(t, "null", string(page.Items[0].After))
	assert.NotEmpty(t, page.NextCursor)

	assert.Equal(t, "op-1", store.got.filter.OperatorID)
	assert.Equal(t, "payout.approve", store.got.filter.Action)
	assert.Equal(t, 2, store.got.limit, "the store must be asked for limit+1")
}

func TestAuditLogValidation(t *testing.T) {
	t.Parallel()

	router := newRouter(&fakeStore{}, nil)
	cases := []string{
		"/admin/v1/audit-log?limit=0",
		"/admin/v1/audit-log?limit=101",
		"/admin/v1/audit-log?limit=abc",
		"/admin/v1/audit-log?period_from=yesterday",
		"/admin/v1/audit-log?period_to=2026-10-16T10:00:00", // no zone
		"/admin/v1/audit-log?period_from=2026-10-16T10:00:00Z&period_to=2026-10-15T10:00:00Z",
		"/admin/v1/audit-log?cursor=!!!not-base64!!!",
		"/admin/v1/audit-log?operator_id=" + strings.Repeat("x", 300),
	}
	for _, path := range cases {
		rec := get(router, path, "auditor")
		require.Equalf(t, http.StatusBadRequest, rec.Code, "path %s", path)
		var problem ginmw.Problem
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &problem))
		assert.Equal(t, "validation_failed", problem.Code)
	}
}

func TestAuditLogStoreFailure(t *testing.T) {
	t.Parallel()

	router := newRouter(&fakeStore{err: errors.New("db down")}, nil)
	rec := get(router, "/admin/v1/audit-log", "auditor")
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.Equal(t, "1", rec.Header().Get("Retry-After"))

	var problem ginmw.Problem
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &problem))
	assert.Equal(t, "service_unavailable", problem.Code)
}

func TestNotFound(t *testing.T) {
	t.Parallel()

	router := newRouter(&fakeStore{}, nil)
	rec := get(router, "/admin/v1/unknown", "admin")
	require.Equal(t, http.StatusNotFound, rec.Code)
	var problem ginmw.Problem
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &problem))
	assert.Equal(t, "not_found", problem.Code)
}
