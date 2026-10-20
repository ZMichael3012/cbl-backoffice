package transport_test

import (
	"context"
	"encoding/json"
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

// fakeDiscrepancyAPI serves the reconciliation routes.
type fakeDiscrepancyAPI struct {
	page       domain.DiscrepancyPage
	resolved   domain.Discrepancy
	listErr    error
	resolveErr error

	gotFilter  domain.DiscrepancyFilter
	gotLimit   int
	gotCursor  string
	gotResolve struct {
		operatorID string
		requestID  string
		id         string
		resolution string
		comment    string
	}
	calls int
}

// List implements transport.DiscrepancyAPI.
func (f *fakeDiscrepancyAPI) List(_ context.Context, filter domain.DiscrepancyFilter, limit int, cursor string) (domain.DiscrepancyPage, error) {
	f.calls++
	f.gotFilter = filter
	f.gotLimit = limit
	f.gotCursor = cursor
	if f.listErr != nil {
		return domain.DiscrepancyPage{}, f.listErr
	}
	return f.page, nil
}

// Resolve implements transport.DiscrepancyAPI.
func (f *fakeDiscrepancyAPI) Resolve(_ context.Context, operatorID, requestID, id, resolution, comment string) (domain.Discrepancy, error) {
	f.calls++
	f.gotResolve.operatorID = operatorID
	f.gotResolve.requestID = requestID
	f.gotResolve.id = id
	f.gotResolve.resolution = resolution
	f.gotResolve.comment = comment
	if f.resolveErr != nil {
		return domain.Discrepancy{}, f.resolveErr
	}
	return f.resolved, nil
}

// newDiscrepancyRouter builds the router over the fake discrepancy API.
func newDiscrepancyRouter(api *fakeDiscrepancyAPI) *gin.Engine {
	return transport.NewRouter(transport.Options{
		Log:           zap.NewNop(),
		Verify:        verifier,
		Audit:         usecase.NewAuditReader(&fakeStore{}),
		Discrepancies: api,
	})
}

// postJSON performs a POST with the given body.
func postJSON(router *gin.Engine, path, token, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func TestDiscrepancyAuthorization(t *testing.T) {
	t.Parallel()

	api := &fakeDiscrepancyAPI{}
	router := newDiscrepancyRouter(api)

	assert.Equal(t, http.StatusUnauthorized, get(router, "/admin/v1/reconciliation/discrepancies", "").Code)
	assert.Equal(t, http.StatusForbidden, get(router, "/admin/v1/reconciliation/discrepancies", "payout").Code)
	assert.Equal(t, http.StatusOK, get(router, "/admin/v1/reconciliation/discrepancies", "auditor").Code)
	assert.Equal(t, http.StatusOK, get(router, "/admin/v1/reconciliation/discrepancies", "recon").Code)

	body := `{"operator_id":"op-recon","resolution":"confirm_gateway"}`
	assert.Equal(t, http.StatusForbidden, postJSON(router, "/admin/v1/reconciliation/discrepancies/x/resolve", "auditor", body).Code)
	assert.Equal(t, http.StatusOK, postJSON(router, "/admin/v1/reconciliation/discrepancies/x/resolve", "recon", body).Code)
}

func TestDiscrepancyListMapsThePage(t *testing.T) {
	t.Parallel()

	created := time.Date(2026, 10, 19, 12, 0, 0, 123_000_000, time.UTC)
	api := &fakeDiscrepancyAPI{page: domain.DiscrepancyPage{
		Items: []domain.Discrepancy{{
			ID: "01a154e0-0000-7000-8000-000000000901", Type: "amount_mismatch",
			RefID: "01a154e0-0000-7000-8000-000000000902", MerchantID: "0199d6c2-8e3f-7a1b-9c4d-2e5f6a7b8c9d",
			Severity: "critical", Expected: "55.000000", Actual: "60.000000",
			Status: domain.DiscrepancyStatusOpen, CreatedAt: created,
		}},
		NextCursor: "next-1",
	}}
	router := newDiscrepancyRouter(api)

	rec := get(router, "/admin/v1/reconciliation/discrepancies?severity=critical&merchant_id=0199d6c2-8e3f-7a1b-9c4d-2e5f6a7b8c9d&limit=10&cursor=abc", "recon")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, domain.DiscrepancyFilter{Severity: "critical", MerchantID: "0199d6c2-8e3f-7a1b-9c4d-2e5f6a7b8c9d"}, api.gotFilter)
	assert.Equal(t, 10, api.gotLimit)
	assert.Equal(t, "abc", api.gotCursor)

	var page struct {
		Items []struct {
			ID        string `json:"id"`
			Type      string `json:"type"`
			RefID     string `json:"ref_id"`
			Severity  string `json:"severity"`
			Status    string `json:"status"`
			CreatedAt string `json:"created_at"`
		} `json:"items"`
		NextCursor string `json:"next_cursor"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &page))
	require.Len(t, page.Items, 1)
	assert.Equal(t, "01a154e0-0000-7000-8000-000000000901", page.Items[0].ID)
	assert.Equal(t, "amount_mismatch", page.Items[0].Type)
	assert.Equal(t, "critical", page.Items[0].Severity)
	assert.Equal(t, "open", page.Items[0].Status)
	assert.Equal(t, "2026-10-19T12:00:00.123Z", page.Items[0].CreatedAt)
	assert.Equal(t, "next-1", page.NextCursor)
}

func TestDiscrepancyListValidation(t *testing.T) {
	t.Parallel()

	api := &fakeDiscrepancyAPI{}
	router := newDiscrepancyRouter(api)

	for _, path := range []string{
		"/admin/v1/reconciliation/discrepancies?limit=abc",
		"/admin/v1/reconciliation/discrepancies?limit=101",
		"/admin/v1/reconciliation/discrepancies?limit=-1",
	} {
		rec := get(router, path, "recon")
		require.Equal(t, http.StatusBadRequest, rec.Code, path)
		var problem ginmw.Problem
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &problem))
		assert.Equal(t, "validation_failed", problem.Code)
	}
	assert.Zero(t, api.calls)

	api.listErr = grpcmw.E(grpcmw.KindInvalidArgument, "validation_failed", "cursor is invalid", nil)
	rec := get(router, "/admin/v1/reconciliation/discrepancies?cursor=bad", "recon")
	require.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestDiscrepancyResolveValidation(t *testing.T) {
	t.Parallel()

	api := &fakeDiscrepancyAPI{resolved: domain.Discrepancy{ID: "01a154e0-0000-7000-8000-000000000901", Status: domain.DiscrepancyStatusResolved}}
	router := newDiscrepancyRouter(api)

	rec := postJSON(router, "/admin/v1/reconciliation/discrepancies/01a154e0-0000-7000-8000-000000000901/resolve", "recon",
		`{"operator_id":"other","resolution":"confirm_gateway"}`)
	require.Equal(t, http.StatusForbidden, rec.Code)
	var problem ginmw.Problem
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &problem))
	assert.Equal(t, "operator_mismatch", problem.Code)
	assert.Zero(t, api.calls)

	rec = postJSON(router, "/admin/v1/reconciliation/discrepancies/01a154e0-0000-7000-8000-000000000901/resolve", "recon",
		`{"operator_id":"op-recon","resolution":"confirm_gateway","unknown":true}`)
	require.Equal(t, http.StatusBadRequest, rec.Code)

	rec = postJSON(router, "/admin/v1/reconciliation/discrepancies/01a154e0-0000-7000-8000-000000000901/resolve", "recon",
		`{"operator_id":"op-recon","resolution":"confirm_gateway"}`)
	require.Equal(t, http.StatusOK, rec.Code)
	var resolvedBody struct {
		Status string `json:"status"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resolvedBody))
	assert.Equal(t, "resolved", resolvedBody.Status)
	assert.Equal(t, "op-recon", api.gotResolve.operatorID)
	assert.Equal(t, "01a154e0-0000-7000-8000-000000000901", api.gotResolve.id)
	assert.Equal(t, "confirm_gateway", api.gotResolve.resolution)
}

func TestDiscrepancyResolveErrorMapping(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		err  error
		code int
		name string
	}{
		"not found":   {grpcmw.E(grpcmw.KindNotFound, "discrepancy_not_found", "missing", nil), http.StatusNotFound, "discrepancy_not_found"},
		"resolved":    {grpcmw.E(grpcmw.KindFailedPrecondition, "discrepancy_resolved", "already resolved", nil), http.StatusConflict, "discrepancy_resolved"},
		"invalid":     {grpcmw.E(grpcmw.KindInvalidArgument, "validation_failed", "bad", nil), http.StatusBadRequest, "validation_failed"},
		"unavailable": {grpcmw.E(grpcmw.KindUnavailable, "service_unavailable", "down", nil), http.StatusServiceUnavailable, "service_unavailable"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			api := &fakeDiscrepancyAPI{resolveErr: tc.err}
			router := newDiscrepancyRouter(api)
			rec := postJSON(router, "/admin/v1/reconciliation/discrepancies/01a154e0-0000-7000-8000-000000000901/resolve", "recon",
				`{"operator_id":"op-recon","resolution":"confirm_gateway"}`)
			require.Equal(t, tc.code, rec.Code)
			var problem ginmw.Problem
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &problem))
			assert.Equal(t, tc.name, problem.Code)
		})
	}
}
