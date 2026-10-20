package usecase_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cbl/cbl-kit/grpcmw"

	"github.com/cbl/cbl-backoffice/internal/domain"
	"github.com/cbl/cbl-backoffice/internal/usecase"
)

const discrepancyID = "01a154e0-0000-7000-8000-000000000901"
const discrepancyMerchant = "0199d6c2-8e3f-7a1b-9c4d-2e5f6a7b8c9d"

// fakeDiscrepancyStore records calls and simulates the resolve transaction.
type fakeDiscrepancyStore struct {
	rows       []domain.Discrepancy
	listErr    error
	resolveErr error
	resolved   domain.Discrepancy
	audit      domain.AuditRecord

	gotFilter   domain.DiscrepancyFilter
	gotLimit    int
	gotAfter    *domain.DiscrepancyKey
	gotDecision domain.ResolveDecision
}

// ListDiscrepancies returns the preconfigured rows.
func (s *fakeDiscrepancyStore) ListDiscrepancies(_ context.Context, filter domain.DiscrepancyFilter, limit int, after *domain.DiscrepancyKey) ([]domain.Discrepancy, error) {
	s.gotFilter = filter
	s.gotLimit = limit
	s.gotAfter = after
	if s.listErr != nil {
		return nil, s.listErr
	}
	if len(s.rows) > limit {
		return s.rows[:limit], nil
	}
	return s.rows, nil
}

// ResolveDiscrepancy simulates the atomic resolve and the audit build.
func (s *fakeDiscrepancyStore) ResolveDiscrepancy(_ context.Context, id string, decision domain.ResolveDecision, build func(domain.Discrepancy) (domain.AuditRecord, error)) (domain.Discrepancy, error) {
	s.gotDecision = decision
	if s.resolveErr != nil {
		return domain.Discrepancy{}, s.resolveErr
	}
	before := domain.Discrepancy{ID: id, Type: "amount_mismatch", Status: domain.DiscrepancyStatusOpen}
	record, err := build(before)
	if err != nil {
		return domain.Discrepancy{}, err
	}
	s.audit = record
	return s.resolved, nil
}

// discrepancyRow builds a row for a minute offset.
func discrepancyRow(id string, minute int) domain.Discrepancy {
	return domain.Discrepancy{
		ID: id, Type: "amount_mismatch", RefID: "ref-1", MerchantID: discrepancyMerchant,
		Severity: "critical", Status: domain.DiscrepancyStatusOpen,
		CreatedAt: time.Date(2026, 10, 19, 12, minute, 0, 0, time.UTC),
	}
}

// discrepancyClock fixes the service clock.
func discrepancyClock() func() time.Time {
	return func() time.Time { return time.Date(2026, 10, 19, 13, 0, 0, 0, time.UTC) }
}

// newDiscrepancyService builds the service over the fake store.
func newDiscrepancyService(store *fakeDiscrepancyStore) *usecase.DiscrepancyService {
	writer := usecase.NewAuditWriter(&fakeWriter{})
	return usecase.NewDiscrepancyService(store, writer, usecase.WithDiscrepancyClock(discrepancyClock()))
}

func TestDiscrepancyListDefaultsAndPagination(t *testing.T) {
	t.Parallel()

	store := &fakeDiscrepancyStore{rows: []domain.Discrepancy{
		discrepancyRow(discrepancyID, 3), discrepancyRow(discrepancyID, 2), discrepancyRow(discrepancyID, 1),
	}}
	service := newDiscrepancyService(store)

	page, err := service.List(context.Background(), domain.DiscrepancyFilter{}, 0, "")
	require.NoError(t, err)
	require.Len(t, page.Items, 3)
	assert.Equal(t, 21, store.gotLimit, "the default page size is 20 and the store gets limit+1")
}

func TestDiscrepancyListValidation(t *testing.T) {
	t.Parallel()

	service := newDiscrepancyService(&fakeDiscrepancyStore{})
	cases := []struct {
		name   string
		filter domain.DiscrepancyFilter
		limit  int
		cursor string
	}{
		{"bad severity", domain.DiscrepancyFilter{Severity: "fatal"}, 0, ""},
		{"bad merchant", domain.DiscrepancyFilter{MerchantID: "not-a-uuid"}, 0, ""},
		{"limit too high", domain.DiscrepancyFilter{}, 101, ""},
		{"limit negative", domain.DiscrepancyFilter{}, -1, ""},
		{"bad cursor", domain.DiscrepancyFilter{}, 0, "not-a-cursor"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := service.List(context.Background(), tc.filter, tc.limit, tc.cursor)
			require.Error(t, err)
			assert.Equal(t, "validation_failed", grpcmw.CodeOf(err))
		})
	}
}

func TestDiscrepancyCursorRoundtripAndBinding(t *testing.T) {
	t.Parallel()

	filter := domain.DiscrepancyFilter{Severity: "critical", MerchantID: discrepancyMerchant}
	key := domain.DiscrepancyKey{CreatedAt: time.Date(2026, 10, 19, 12, 34, 56, 789, time.UTC), ID: discrepancyID}
	cursor := usecase.EncodeDiscrepancyCursor(filter, key)
	decoded, err := usecase.DecodeDiscrepancyCursor(cursor, filter)
	require.NoError(t, err)
	require.NotNil(t, decoded)
	assert.True(t, key.CreatedAt.Equal(decoded.CreatedAt))
	assert.Equal(t, key.ID, decoded.ID)

	_, err = usecase.DecodeDiscrepancyCursor(cursor, domain.DiscrepancyFilter{Severity: "info"})
	require.Error(t, err, "a cursor is bound to its filter set")

	store := &fakeDiscrepancyStore{}
	service := newDiscrepancyService(store)
	_, err = service.List(context.Background(), filter, 2, cursor)
	require.NoError(t, err)
	require.NotNil(t, store.gotAfter)
	assert.Equal(t, key.ID, store.gotAfter.ID)
	assert.Equal(t, 3, store.gotLimit)
}

func TestDiscrepancyResolveValidation(t *testing.T) {
	t.Parallel()

	service := newDiscrepancyService(&fakeDiscrepancyStore{})
	cases := []struct {
		name       string
		id         string
		operatorID string
		resolution string
		comment    string
	}{
		{"bad id", "not-a-uuid", "op-1", "confirm_gateway", ""},
		{"empty operator", discrepancyID, "", "confirm_gateway", ""},
		{"bad resolution", discrepancyID, "op-1", "revert", ""},
		{"control comment", discrepancyID, "op-1", "confirm_gateway", "bad\x00comment"},
		{"long comment", discrepancyID, "op-1", "confirm_gateway", strings.Repeat("c", 1025)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := service.Resolve(context.Background(), tc.operatorID, "req-1", tc.id, tc.resolution, tc.comment)
			require.Error(t, err)
			assert.Equal(t, "validation_failed", grpcmw.CodeOf(err))
		})
	}
}

func TestDiscrepancyResolveMapsStoreErrors(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		err  error
		code string
		kind grpcmw.Kind
	}{
		{"not found", domain.ErrDiscrepancyNotFound, "discrepancy_not_found", grpcmw.KindNotFound},
		{"already resolved", domain.ErrDiscrepancyResolved, "discrepancy_resolved", grpcmw.KindFailedPrecondition},
		{"storage", errors.New("db down"), "service_unavailable", grpcmw.KindUnavailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := &fakeDiscrepancyStore{resolveErr: tc.err}
			service := newDiscrepancyService(store)
			_, err := service.Resolve(context.Background(), "op-1", "req-1", discrepancyID, "confirm_gateway", "checked")
			require.Error(t, err)
			assert.Equal(t, tc.kind, grpcmw.KindOf(err))
			assert.Equal(t, tc.code, grpcmw.CodeOf(err))
		})
	}
}

func TestDiscrepancyResolveBuildsTheAudit(t *testing.T) {
	t.Parallel()

	store := &fakeDiscrepancyStore{resolved: domain.Discrepancy{
		ID: discrepancyID, Type: "amount_mismatch", Status: domain.DiscrepancyStatusResolved,
		Resolution: "manual_adjustment", Comment: "corrected", ResolvedBy: "op-1",
	}}
	service := newDiscrepancyService(store)

	_, err := service.Resolve(context.Background(), "op-1", "req-1", discrepancyID, "manual_adjustment", "corrected")
	require.NoError(t, err)
	assert.Equal(t, "discrepancy.resolve", store.audit.Action)
	assert.Equal(t, "discrepancy", store.audit.ResourceType)
	assert.Equal(t, discrepancyID, store.audit.ResourceID)
	assert.Equal(t, "op-1", store.audit.OperatorID)
	assert.Equal(t, "req-1", store.audit.RequestID)
	assert.True(t, discrepancyClock()().Equal(store.gotDecision.At))

	var before map[string]string
	require.NoError(t, json.Unmarshal(store.audit.Before, &before))
	assert.Equal(t, "open", before["status"])

	var after map[string]string
	require.NoError(t, json.Unmarshal(store.audit.After, &after))
	assert.Equal(t, "resolved", after["status"])
	assert.Equal(t, "manual_adjustment", after["resolution"])
	assert.Equal(t, "corrected", after["comment"])
}

func TestDiscrepancyListStoreFailure(t *testing.T) {
	t.Parallel()

	store := &fakeDiscrepancyStore{listErr: errors.New("db down")}
	service := newDiscrepancyService(store)
	_, err := service.List(context.Background(), domain.DiscrepancyFilter{}, 0, "")
	require.Error(t, err)
	assert.Equal(t, grpcmw.KindUnavailable, grpcmw.KindOf(err))
}
