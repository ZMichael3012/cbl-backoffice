package usecase_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cbl/cbl-backoffice/internal/domain"
	"github.com/cbl/cbl-backoffice/internal/usecase"
)

// fakePayoutDecider substitutes the payout port.
type fakePayoutDecider struct {
	view       domain.PayoutView
	err        error
	gotPayout  string
	gotOp      string
	gotApprove bool
	gotReason  string
}

// Approve implements usecase.PayoutDecider.
func (f *fakePayoutDecider) Approve(_ context.Context, payoutID, operatorID string, approve bool, reason string) (domain.PayoutView, error) {
	f.gotPayout = payoutID
	f.gotOp = operatorID
	f.gotApprove = approve
	f.gotReason = reason
	return f.view, f.err
}

// fakeRoutingAdmin substitutes the connector admin port.
type fakeRoutingAdmin struct {
	revision     int64
	rules        []domain.RoutingRule
	getErr       error
	putRevision  int64
	putRules     []domain.RoutingRule
	putErr       error
	already      bool
	disableErr   error
	enableErr    error
	gotPutRev    int64
	gotDisable   string
	gotEnable    string
	gotReason    string
	disableCalls int
	enableCalls  int
	putCalls     int
}

// GetRules implements usecase.RoutingAdmin.
func (f *fakeRoutingAdmin) GetRules(context.Context) (int64, []domain.RoutingRule, error) {
	if f.getErr != nil {
		return 0, nil, f.getErr
	}
	return f.revision, f.rules, nil
}

// PutRules implements usecase.RoutingAdmin.
func (f *fakeRoutingAdmin) PutRules(_ context.Context, revision int64, rules []domain.RoutingRule) (int64, []domain.RoutingRule, error) {
	f.putCalls++
	f.gotPutRev = revision
	if f.putErr != nil {
		return 0, nil, f.putErr
	}
	return f.putRevision, f.putRules, nil
}

// DisableProvider implements usecase.RoutingAdmin.
func (f *fakeRoutingAdmin) DisableProvider(_ context.Context, provider, reason string) (bool, error) {
	f.disableCalls++
	f.gotDisable = provider
	f.gotReason = reason
	if f.disableErr != nil {
		return false, f.disableErr
	}
	return f.already, nil
}

// EnableProvider implements usecase.RoutingAdmin.
func (f *fakeRoutingAdmin) EnableProvider(_ context.Context, provider string) (bool, error) {
	f.enableCalls++
	f.gotEnable = provider
	if f.enableErr != nil {
		return false, f.enableErr
	}
	return f.already, nil
}

// newActions builds the service over fakes and returns the journal capture.
func newActions(payout *fakePayoutDecider, routing *fakeRoutingAdmin) (*usecase.AdminActions, *fakeWriter) {
	journal := &fakeWriter{}
	return usecase.NewAdminActions(payout, routing, usecase.NewAuditWriter(journal)), journal
}

func TestDecidePayoutAudits(t *testing.T) {
	t.Parallel()

	payout := &fakePayoutDecider{view: domain.PayoutView{ID: "po-1", Status: "approved", ApprovedBy: []string{"op-1", "op-2"}}}
	routing := &fakeRoutingAdmin{}
	actions, journal := newActions(payout, routing)

	view, err := actions.DecidePayout(context.Background(), "op-1", "req-1", "po-1", true, "checked")
	require.NoError(t, err)
	assert.Equal(t, "approved", view.Status)
	assert.Equal(t, "op-1", payout.gotOp)
	assert.Equal(t, "checked", payout.gotReason)

	require.Len(t, journal.records, 1)
	record := journal.records[0]
	assert.Equal(t, "payout.approve", record.Action)
	assert.Equal(t, "payout", record.ResourceType)
	assert.Equal(t, "po-1", record.ResourceID)
	assert.Equal(t, "op-1", record.OperatorID)
	assert.Equal(t, "req-1", record.RequestID)

	var after struct {
		ID         string   `json:"id"`
		Status     string   `json:"status"`
		ApprovedBy []string `json:"approved_by"`
	}
	require.NoError(t, json.Unmarshal(record.After, &after))
	assert.Equal(t, "po-1", after.ID)
	assert.Equal(t, []string{"op-1", "op-2"}, after.ApprovedBy)
}

func TestDecidePayoutRejectAndErrors(t *testing.T) {
	t.Parallel()

	payout := &fakePayoutDecider{view: domain.PayoutView{ID: "po-2", Status: "rejected"}}
	actions, journal := newActions(payout, &fakeRoutingAdmin{})
	_, err := actions.DecidePayout(context.Background(), "op-1", "req-2", "po-2", false, "sanctions")
	require.NoError(t, err)
	require.Len(t, journal.records, 1)
	assert.Equal(t, "payout.reject", journal.records[0].Action)

	// A failed action writes nothing.
	failing := &fakePayoutDecider{err: errors.New("payout down")}
	actions, journal = newActions(failing, &fakeRoutingAdmin{})
	_, err = actions.DecidePayout(context.Background(), "op-1", "req-3", "po-3", true, "")
	require.Error(t, err)
	assert.Empty(t, journal.records)

	// An audit failure surfaces after the successful action (the vote is
	// idempotent, the retry is safe - spec section 5).
	broken := &failingWriter{err: errors.New("db down")}
	actions = usecase.NewAdminActions(payout, &fakeRoutingAdmin{}, usecase.NewAuditWriter(broken))
	_, err = actions.DecidePayout(context.Background(), "op-1", "req-4", "po-2", false, "why")
	require.Error(t, err)
}

func TestPutRoutingAuditsBeforeAfter(t *testing.T) {
	t.Parallel()

	before := []domain.RoutingRule{{Provider: "mock", Weight: 1, Enabled: true}}
	after := []domain.RoutingRule{{Provider: "mock", Weight: 2, Enabled: true}}
	routing := &fakeRoutingAdmin{
		revision:    3,
		rules:       before,
		putRevision: 4,
		putRules:    after,
	}
	actions, journal := newActions(&fakePayoutDecider{}, routing)

	revision, stored, err := actions.PutRouting(context.Background(), "op-1", "req-1", 3, after)
	require.NoError(t, err)
	assert.EqualValues(t, 4, revision)
	assert.Equal(t, after, stored)
	assert.EqualValues(t, 3, routing.gotPutRev, "the requested revision is passed through")

	require.Len(t, journal.records, 1)
	record := journal.records[0]
	assert.Equal(t, "routing.update", record.Action)
	beforeJSON := string(record.Before)
	afterJSON := string(record.After)
	assert.Contains(t, beforeJSON, `"revision":3`, "the journal snapshot uses the contract shape")
	assert.Contains(t, beforeJSON, `"provider":"mock"`, "snake_case keys, not Go field names")
	assert.Contains(t, beforeJSON, `"networks":[]`, "empty lists are arrays, never null")
	assert.NotContains(t, beforeJSON, `"Provider"`, "no PascalCase keys in the journal")
	assert.Contains(t, afterJSON, `"revision":4`)

	// A revision conflict writes nothing (the successful path only).
	routing.putErr = errors.New("conflict")
	actions, journal = newActions(&fakePayoutDecider{}, &fakeRoutingAdmin{putErr: routing.putErr})
	_, _, err = actions.PutRouting(context.Background(), "op-1", "req-2", 0, before)
	require.Error(t, err)
	assert.Empty(t, journal.records)
}

func TestDisableProviderAuditsOnce(t *testing.T) {
	t.Parallel()

	routing := &fakeRoutingAdmin{}
	actions, journal := newActions(&fakePayoutDecider{}, routing)

	already, err := actions.DisableProvider(context.Background(), "op-1", "req-1", "mock", "maintenance")
	require.NoError(t, err)
	assert.False(t, already)
	require.Len(t, journal.records, 1)
	record := journal.records[0]
	assert.Equal(t, "provider.disable", record.Action)
	assert.Equal(t, "provider", record.ResourceType)
	assert.Equal(t, "mock", record.ResourceID)
	var after struct {
		Disabled bool   `json:"disabled"`
		Reason   string `json:"reason"`
	}
	require.NoError(t, json.Unmarshal(record.After, &after))
	assert.True(t, after.Disabled)
	assert.Equal(t, "maintenance", after.Reason)

	// The repeated disable is idempotent: the record exists, nothing is added.
	routing.already = true
	already, err = actions.DisableProvider(context.Background(), "op-1", "req-2", "mock", "")
	require.NoError(t, err)
	assert.True(t, already)
	assert.Len(t, journal.records, 1)

	// A repeat whose first record was lost (audit failure after a successful
	// action) is compensated: the journal is empty, so the record is appended.
	compensated := &fakeWriter{}
	actions = usecase.NewAdminActions(&fakePayoutDecider{}, &fakeRoutingAdmin{already: true},
		usecase.NewAuditWriter(compensated))
	already, err = actions.DisableProvider(context.Background(), "op-1", "req-3", "mock", "maintenance")
	require.NoError(t, err)
	assert.True(t, already)
	require.Len(t, compensated.records, 1, "the applied disable is finally journaled")

	// Errors pass through without a record.
	routing = &fakeRoutingAdmin{disableErr: errors.New("connector down")}
	actions, journal = newActions(&fakePayoutDecider{}, routing)
	_, err = actions.DisableProvider(context.Background(), "op-1", "req-3", "mock", "")
	require.Error(t, err)
	assert.Empty(t, journal.records)
}

func TestEnableProviderAuditsOnce(t *testing.T) {
	t.Parallel()

	routing := &fakeRoutingAdmin{}
	actions, journal := newActions(&fakePayoutDecider{}, routing)

	already, err := actions.EnableProvider(context.Background(), "op-1", "req-1", "mock", "restored")
	require.NoError(t, err)
	assert.False(t, already)
	require.Len(t, journal.records, 1)
	record := journal.records[0]
	assert.Equal(t, "provider.enable", record.Action)
	assert.Equal(t, "provider", record.ResourceType)
	assert.Equal(t, "mock", record.ResourceID)
	var after struct {
		Enabled bool   `json:"enabled"`
		Reason  string `json:"reason"`
	}
	require.NoError(t, json.Unmarshal(record.After, &after))
	assert.True(t, after.Enabled)
	assert.Equal(t, "restored", after.Reason)

	// The repeated enable is idempotent: the record exists, nothing is added.
	routing.already = true
	already, err = actions.EnableProvider(context.Background(), "op-1", "req-2", "mock", "")
	require.NoError(t, err)
	assert.True(t, already)
	assert.Len(t, journal.records, 1)

	// A repeat whose first record was lost is compensated.
	compensated := &fakeWriter{}
	actions = usecase.NewAdminActions(&fakePayoutDecider{}, &fakeRoutingAdmin{already: true},
		usecase.NewAuditWriter(compensated))
	already, err = actions.EnableProvider(context.Background(), "op-1", "req-3", "mock", "restored")
	require.NoError(t, err)
	assert.True(t, already)
	require.Len(t, compensated.records, 1, "the applied enable is finally journaled")

	// Errors pass through without a record.
	routing = &fakeRoutingAdmin{enableErr: errors.New("connector down")}
	actions, journal = newActions(&fakePayoutDecider{}, routing)
	_, err = actions.EnableProvider(context.Background(), "op-1", "req-3", "mock", "")
	require.Error(t, err)
	assert.Empty(t, journal.records)
}

func TestGetRoutingPassthrough(t *testing.T) {
	t.Parallel()

	rules := []domain.RoutingRule{{Provider: "mock", Weight: 1, Enabled: true}}
	actions, journal := newActions(&fakePayoutDecider{}, &fakeRoutingAdmin{revision: 2, rules: rules})

	revision, got, err := actions.GetRouting(context.Background())
	require.NoError(t, err)
	assert.EqualValues(t, 2, revision)
	assert.Equal(t, rules, got)
	assert.Empty(t, journal.records, "reads are not audited")
}

// failingWriter fails every insert (audit storage failure).
type failingWriter struct {
	err error
}

// Insert implements usecase.JournalWriter.
func (f *failingWriter) Insert(context.Context, domain.AuditRecord) error {
	return f.err
}

// ExistsForResource implements usecase.JournalWriter.
func (f *failingWriter) ExistsForResource(context.Context, string, string, string) (bool, error) {
	return false, f.err
}
