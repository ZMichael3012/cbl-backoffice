package transport_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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

// actionPayout substitutes the payout port.
type actionPayout struct {
	view domain.PayoutView
	err  error
	got  struct {
		payoutID string
		operator string
		approve  bool
		reason   string
	}
}

// Approve implements usecase.PayoutDecider.
func (f *actionPayout) Approve(_ context.Context, payoutID, operatorID string, approve bool, reason string) (domain.PayoutView, error) {
	f.got.payoutID = payoutID
	f.got.operator = operatorID
	f.got.approve = approve
	f.got.reason = reason
	return f.view, f.err
}

// actionRouting substitutes the connector admin port.
type actionRouting struct {
	revision    int64
	rules       []domain.RoutingRule
	putRevision int64
	putRules    []domain.RoutingRule
	putErr      error
	already     bool
	disableErr  error
}

// GetRules implements usecase.RoutingAdmin.
func (f *actionRouting) GetRules(context.Context) (int64, []domain.RoutingRule, error) {
	return f.revision, f.rules, nil
}

// PutRules implements usecase.RoutingAdmin.
func (f *actionRouting) PutRules(_ context.Context, _ int64, _ []domain.RoutingRule) (int64, []domain.RoutingRule, error) {
	if f.putErr != nil {
		return 0, nil, f.putErr
	}
	return f.putRevision, f.putRules, nil
}

// DisableProvider implements usecase.RoutingAdmin.
func (f *actionRouting) DisableProvider(context.Context, string, string) (bool, error) {
	if f.disableErr != nil {
		return false, f.disableErr
	}
	return f.already, nil
}

// actionJournal captures the audit records.
type actionJournal struct {
	records []domain.AuditRecord
	err     error
}

// Insert implements usecase.JournalWriter.
func (j *actionJournal) Insert(_ context.Context, record domain.AuditRecord) error {
	if j.err != nil {
		return j.err
	}
	j.records = append(j.records, record)
	return nil
}

// ExistsForResource implements usecase.JournalWriter by scanning the captures.
func (j *actionJournal) ExistsForResource(_ context.Context, action, resourceType, resourceID string) (bool, error) {
	if j.err != nil {
		return false, j.err
	}
	for _, record := range j.records {
		if record.Action == action && record.ResourceType == resourceType && record.ResourceID == resourceID {
			return true, nil
		}
	}
	return false, nil
}

// actionsRouter builds the router with the 12.2 routes over fakes.
func actionsRouter(payout *actionPayout, routing *actionRouting, journal *actionJournal) (*gin.Engine, *usecase.AdminActions) {
	actions := usecase.NewAdminActions(payout, routing, usecase.NewAuditWriter(journal))
	router := transport.NewRouter(transport.Options{
		Log:     zap.NewNop(),
		Verify:  verifier,
		Audit:   usecase.NewAuditReader(&fakeStore{}),
		Actions: actions,
	})
	return router, actions
}

// doAction performs one request with an optional token and body.
func doAction(router *gin.Engine, method, path, token, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func TestPayoutDecisionAuthorization(t *testing.T) {
	t.Parallel()

	router, _ := actionsRouter(&actionPayout{}, &actionRouting{}, &actionJournal{})

	rec := doAction(router, http.MethodPost, "/admin/v1/payouts/po-1/approve", "", `{"operator_id":"op-1"}`)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)

	rec = doAction(router, http.MethodPost, "/admin/v1/payouts/po-1/approve", "auditor", `{"operator_id":"op-aud"}`)
	assert.Equal(t, http.StatusForbidden, rec.Code)

	rec = doAction(router, http.MethodPost, "/admin/v1/payouts/po-1/approve", "admin", `{"operator_id":"someone-else"}`)
	require.Equal(t, http.StatusForbidden, rec.Code)
	var problem ginmw.Problem
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &problem))
	assert.Equal(t, "operator_mismatch", problem.Code)
}

func TestPayoutDecisionFlow(t *testing.T) {
	t.Parallel()

	payout := &actionPayout{view: domain.PayoutView{ID: "po-1", Status: "approved", ApprovedBy: []string{"op-adm"}}}
	journal := &actionJournal{}
	router, _ := actionsRouter(payout, &actionRouting{}, journal)

	rec := doAction(router, http.MethodPost, "/admin/v1/payouts/po-1/approve", "admin", `{"operator_id":"op-adm","reason":"checked"}`)
	require.Equal(t, http.StatusOK, rec.Code)
	var response struct {
		ID         string   `json:"id"`
		Status     string   `json:"status"`
		ApprovedBy []string `json:"approved_by"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response))
	assert.Equal(t, "po-1", response.ID)
	assert.Equal(t, []string{"op-adm"}, response.ApprovedBy)
	assert.Equal(t, "op-adm", payout.got.operator, "the subject comes from the token")
	assert.True(t, payout.got.approve)
	require.Len(t, journal.records, 1)
	assert.Equal(t, "payout.approve", journal.records[0].Action)

	// Rejection without a reason is business validation.
	rec = doAction(router, http.MethodPost, "/admin/v1/payouts/po-1/reject", "admin", `{"operator_id":"op-adm"}`)
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	var problem ginmw.Problem
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &problem))
	assert.Equal(t, "validation_failed", problem.Code)

	// Unknown fields are rejected.
	rec = doAction(router, http.MethodPost, "/admin/v1/payouts/po-1/approve", "admin", `{"operator_id":"op-adm","extra":1}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestPayoutDecisionErrorMapping(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		err  error
		want int
		code string
	}{
		{name: "not found", err: grpcmw.E(grpcmw.KindNotFound, "payout_not_found", "payout not found", nil), want: http.StatusNotFound, code: "payout_not_found"},
		{name: "vote locked", err: grpcmw.E(grpcmw.KindFailedPrecondition, "conflict", "operator vote is immutable", nil), want: http.StatusConflict, code: "conflict"},
		{name: "state conflict", err: grpcmw.E(grpcmw.KindFailedPrecondition, "state_conflict", "state conflict, retry", nil), want: http.StatusConflict, code: "state_conflict"},
		{name: "unavailable", err: grpcmw.E(grpcmw.KindUnavailable, "service_unavailable", "payout service is unavailable", nil), want: http.StatusServiceUnavailable, code: "service_unavailable"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			router, _ := actionsRouter(&actionPayout{err: tc.err}, &actionRouting{}, &actionJournal{})
			rec := doAction(router, http.MethodPost, "/admin/v1/payouts/po-1/approve", "admin", `{"operator_id":"op-adm"}`)
			require.Equal(t, tc.want, rec.Code)
			var problem ginmw.Problem
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &problem))
			assert.Equal(t, tc.code, problem.Code)
		})
	}
}

func TestRoutingRulesEndpoints(t *testing.T) {
	t.Parallel()

	routing := &actionRouting{
		revision: 2,
		rules: []domain.RoutingRule{{
			Provider: "mock", Networks: []string{"trc20"}, Assets: []string{"USDT"},
			Priority: 1, Weight: 3, MinAmount: 100, Enabled: true,
		}},
		putRevision: 3,
		putRules:    []domain.RoutingRule{{Provider: "mock", Weight: 1, Enabled: true}},
	}
	journal := &actionJournal{}
	router, _ := actionsRouter(&actionPayout{}, routing, journal)

	// RBAC: payout_operator cannot read routing.
	rec := doAction(router, http.MethodGet, "/admin/v1/routing-rules", "payout", "")
	assert.Equal(t, http.StatusForbidden, rec.Code)

	// The routing_manager reads the current revision.
	rec = doAction(router, http.MethodGet, "/admin/v1/routing-rules", "routing", "")
	require.Equal(t, http.StatusOK, rec.Code)
	var set struct {
		Revision int64 `json:"revision"`
		Items    []struct {
			Provider string   `json:"provider"`
			Networks []string `json:"networks"`
			Assets   []string `json:"assets"`
			Weight   int      `json:"weight"`
		} `json:"items"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &set))
	assert.EqualValues(t, 2, set.Revision)
	require.Len(t, set.Items, 1)
	assert.Equal(t, []string{"trc20"}, set.Items[0].Networks)

	// The auditor cannot manage routing.
	rec = doAction(router, http.MethodPut, "/admin/v1/routing-rules", "auditor", `{"revision":2,"items":[{"provider":"mock","priority":0,"weight":1,"enabled":true}]}`)
	assert.Equal(t, http.StatusForbidden, rec.Code)

	// Validation: an empty rule set, a bad weight and oversized lists are 400.
	rec = doAction(router, http.MethodPut, "/admin/v1/routing-rules", "routing", `{"revision":2,"items":[]}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	rec = doAction(router, http.MethodPut, "/admin/v1/routing-rules", "routing", `{"revision":2,"items":[{"provider":"mock","priority":0,"weight":0,"enabled":true}]}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	longList := make([]string, 65)
	for i := range longList {
		longList[i] = "trc20"
	}
	networksJSON, marshalErr := json.Marshal(longList)
	require.NoError(t, marshalErr)
	rec = doAction(router, http.MethodPut, "/admin/v1/routing-rules", "routing",
		`{"revision":2,"items":[{"provider":"mock","priority":0,"weight":1,"enabled":true,"networks":`+string(networksJSON)+`}]}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code, "list counts are bounded")

	// A successful replacement carries the stored revision and one audit record.
	rec = doAction(router, http.MethodPut, "/admin/v1/routing-rules", "routing", `{"revision":2,"items":[{"provider":"mock","priority":0,"weight":1,"enabled":true}]}`)
	require.Equal(t, http.StatusOK, rec.Code)
	var stored struct {
		Revision int64 `json:"revision"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &stored))
	assert.EqualValues(t, 3, stored.Revision)
	require.Len(t, journal.records, 1)
	assert.Equal(t, "routing.update", journal.records[0].Action)

	// A revision conflict maps to 409 revision_conflict.
	conflict := &actionRouting{putErr: grpcmw.E(grpcmw.KindFailedPrecondition, "revision_conflict", "routing revision conflict", nil)}
	router, _ = actionsRouter(&actionPayout{}, conflict, &actionJournal{})
	rec = doAction(router, http.MethodPut, "/admin/v1/routing-rules", "routing", `{"revision":9,"items":[{"provider":"mock","priority":0,"weight":1,"enabled":true}]}`)
	require.Equal(t, http.StatusConflict, rec.Code)
	var problem ginmw.Problem
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &problem))
	assert.Equal(t, "revision_conflict", problem.Code)
}

func TestDisableProviderEndpoint(t *testing.T) {
	t.Parallel()

	routing := &actionRouting{}
	journal := &actionJournal{}
	router, _ := actionsRouter(&actionPayout{}, routing, journal)

	// RBAC: the routing_manager cannot disable providers.
	rec := doAction(router, http.MethodPost, "/admin/v1/providers/mock/disable", "routing", "")
	assert.Equal(t, http.StatusForbidden, rec.Code)

	// The first disable is 204 with one record.
	rec = doAction(router, http.MethodPost, "/admin/v1/providers/mock/disable", "admin", `{"reason":"maintenance"}`)
	require.Equal(t, http.StatusNoContent, rec.Code)
	require.Len(t, journal.records, 1)
	assert.Equal(t, "provider.disable", journal.records[0].Action)

	// The repeated disable is 204 without a new record.
	routing.already = true
	rec = doAction(router, http.MethodPost, "/admin/v1/providers/mock/disable", "admin", "")
	require.Equal(t, http.StatusNoContent, rec.Code)
	assert.Len(t, journal.records, 1)

	// A repeat whose record was lost (fresh journal + already disabled) is
	// compensated: 204 and the missing record is appended.
	compensated := &actionJournal{}
	router, _ = actionsRouter(&actionPayout{}, &actionRouting{already: true}, compensated)
	rec = doAction(router, http.MethodPost, "/admin/v1/providers/mock/disable", "admin", "")
	require.Equal(t, http.StatusNoContent, rec.Code)
	require.Len(t, compensated.records, 1)
	assert.Equal(t, "provider.disable", compensated.records[0].Action)

	// An unknown provider is 404.
	unknown := &actionRouting{disableErr: grpcmw.E(grpcmw.KindNotFound, "provider_not_found", "provider not found", nil)}
	router, _ = actionsRouter(&actionPayout{}, unknown, &actionJournal{})
	rec = doAction(router, http.MethodPost, "/admin/v1/providers/ghost/disable", "admin", "")
	require.Equal(t, http.StatusNotFound, rec.Code)
	var problem ginmw.Problem
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &problem))
	assert.Equal(t, "provider_not_found", problem.Code)

	// A control character in the reason is 400.
	router, _ = actionsRouter(&actionPayout{}, &actionRouting{}, &actionJournal{})
	rec = doAction(router, http.MethodPost, "/admin/v1/providers/mock/disable", "admin", `{"reason":"bad\u0007reason"}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestActionAuditFailureSurfaces(t *testing.T) {
	t.Parallel()

	journal := &actionJournal{err: errors.New("db down")}
	router, _ := actionsRouter(&actionPayout{view: domain.PayoutView{ID: "po-1", Status: "approved"}}, &actionRouting{}, journal)

	rec := doAction(router, http.MethodPost, "/admin/v1/payouts/po-1/approve", "admin", `{"operator_id":"op-adm"}`)
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code,
		"a lost record must not pass silently; the failure is retryable (the vote is idempotent)")
	assert.Equal(t, "1", rec.Header().Get("Retry-After"))
}
