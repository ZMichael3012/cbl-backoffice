package usecase_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cbl/cbl-kit/grpcmw"

	"github.com/cbl/cbl-backoffice/internal/domain"
	"github.com/cbl/cbl-backoffice/internal/usecase"
)

// fakeStore records the calls and returns a fixed page.
type fakeStore struct {
	rows      []domain.AuditRecord
	err       error
	gotFilter domain.AuditFilter
	gotLimit  int
	gotAfter  *domain.AuditKey
	calls     int
}

// List implements usecase.JournalStore.
func (f *fakeStore) List(_ context.Context, filter domain.AuditFilter, limit int, after *domain.AuditKey) ([]domain.AuditRecord, error) {
	f.calls++
	f.gotFilter = filter
	f.gotLimit = limit
	f.gotAfter = after
	if f.err != nil {
		return nil, f.err
	}
	return f.rows, nil
}

// row builds a journal row with a distinct instant.
func row(id string, minute int) domain.AuditRecord {
	return domain.AuditRecord{
		ID:         id,
		OccurredAt: time.Date(2026, 10, 16, 10, minute, 0, 0, time.UTC),
		OperatorID: "op-1",
		Action:     "payout.approve",
	}
}

// rows builds n rows, newest first, ids unique.
func rows(n int) []domain.AuditRecord {
	out := make([]domain.AuditRecord, 0, n)
	for i := n - 1; i >= 0; i-- {
		out = append(out, row("018f0000-0000-7000-8000-0000000000aa", i))
	}
	return out
}

func TestListDefaultsAndPagination(t *testing.T) {
	t.Parallel()

	store := &fakeStore{rows: rows(21)}
	reader := usecase.NewAuditReader(store)

	page, err := reader.List(context.Background(), domain.AuditFilter{}, 0, "")
	require.NoError(t, err)

	assert.Equal(t, 21, store.gotLimit, "the store must be asked for limit+1")
	assert.Nil(t, store.gotAfter, "the first page has no keyset position")
	require.Len(t, page.Items, 20)
	require.NotEmpty(t, page.NextCursor)
	assert.LessOrEqual(t, len(page.NextCursor), 512)

	// The cursor resumes exactly after the last returned row.
	payload, err := base64.RawURLEncoding.DecodeString(page.NextCursor)
	require.NoError(t, err)
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(payload, &decoded))
	assert.EqualValues(t, 1, decoded["v"])
	assert.Equal(t, page.Items[19].ID, decoded["i"])
}

func TestListLastPageHasNoCursor(t *testing.T) {
	t.Parallel()

	store := &fakeStore{rows: rows(5)}
	reader := usecase.NewAuditReader(store)

	page, err := reader.List(context.Background(), domain.AuditFilter{}, 5, "")
	require.NoError(t, err)
	assert.Len(t, page.Items, 5)
	assert.Empty(t, page.NextCursor)
}

func TestListCursorValidation(t *testing.T) {
	t.Parallel()

	filter := domain.AuditFilter{OperatorID: "op-1"}
	good := usecase.NewAuditReader(&fakeStore{rows: rows(2)})
	page, err := good.List(context.Background(), filter, 1, "")
	require.NoError(t, err)
	require.NotEmpty(t, page.NextCursor)

	cases := []struct {
		name   string
		cursor string
		filter domain.AuditFilter
	}{
		{name: "garbage base64", cursor: "!!!not-base64!!!", filter: filter},
		{name: "not json", cursor: base64.RawURLEncoding.EncodeToString([]byte("hello")), filter: filter},
		{name: "wrong version", cursor: encodeCursor(t, map[string]any{"v": 2, "t": "2026-10-16T10:00:00Z", "i": "x", "f": filter.Fingerprint()}), filter: filter},
		{name: "unknown field", cursor: encodeCursor(t, map[string]any{"v": 1, "t": "2026-10-16T10:00:00Z", "i": "x", "f": filter.Fingerprint(), "z": 1}), filter: filter},
		{name: "filter mismatch", cursor: page.NextCursor, filter: domain.AuditFilter{OperatorID: "op-2"}},
		{name: "empty id", cursor: encodeCursor(t, map[string]any{"v": 1, "t": "2026-10-16T10:00:00Z", "i": "", "f": filter.Fingerprint()}), filter: filter},
		{name: "non-uuid id", cursor: encodeCursor(t, map[string]any{"v": 1, "t": "2026-10-16T10:00:00Z", "i": "zzz", "f": filter.Fingerprint()}), filter: filter},
		{name: "oversize id", cursor: encodeCursor(t, map[string]any{"v": 1, "t": "2026-10-16T10:00:00Z", "i": strings.Repeat("a", 200), "f": filter.Fingerprint()}), filter: filter},
		{name: "bad time", cursor: encodeCursor(t, map[string]any{"v": 1, "t": "yesterday", "i": "x", "f": filter.Fingerprint()}), filter: filter},
		{name: "trailing bytes", cursor: base64.RawURLEncoding.EncodeToString([]byte(`{"v":1,"t":"2026-10-16T10:00:00Z","i":"x","f":"` + filter.Fingerprint() + `"}{"x":1}`)), filter: filter},
		{name: "oversize", cursor: string(make([]byte, 600)), filter: filter},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store := &fakeStore{}
			reader := usecase.NewAuditReader(store)
			_, err := reader.List(context.Background(), tc.filter, 10, tc.cursor)
			require.Error(t, err)
			assert.Equal(t, grpcmw.KindInvalidArgument, grpcmw.KindOf(err))
			assert.Equal(t, "validation_failed", grpcmw.CodeOf(err))
			assert.Zero(t, store.calls, "a rejected cursor must not reach the store")
		})
	}
}

func TestListCursorResume(t *testing.T) {
	t.Parallel()

	first := &fakeStore{rows: rows(3)}
	reader := usecase.NewAuditReader(first)
	page, err := reader.List(context.Background(), domain.AuditFilter{}, 2, "")
	require.NoError(t, err)
	require.NotEmpty(t, page.NextCursor)

	second := &fakeStore{rows: rows(2)}
	reader = usecase.NewAuditReader(second)
	_, err = reader.List(context.Background(), domain.AuditFilter{}, 2, page.NextCursor)
	require.NoError(t, err)
	require.NotNil(t, second.gotAfter)
	assert.Equal(t, page.Items[1].ID, second.gotAfter.ID)
	assert.True(t, page.Items[1].OccurredAt.Equal(second.gotAfter.OccurredAt))
}

func TestListLimitValidation(t *testing.T) {
	t.Parallel()

	reader := usecase.NewAuditReader(&fakeStore{})
	for _, limit := range []int{-1, 101} {
		_, err := reader.List(context.Background(), domain.AuditFilter{}, limit, "")
		require.Error(t, err)
		assert.Equal(t, grpcmw.KindInvalidArgument, grpcmw.KindOf(err))
	}
}

func TestListStoreFailure(t *testing.T) {
	t.Parallel()

	reader := usecase.NewAuditReader(&fakeStore{err: errors.New("db down")})
	_, err := reader.List(context.Background(), domain.AuditFilter{}, 10, "")
	require.Error(t, err)
	assert.Equal(t, grpcmw.KindUnavailable, grpcmw.KindOf(err))
	assert.Equal(t, "service_unavailable", grpcmw.CodeOf(err))
}

// encodeCursor builds a raw cursor payload for negative tests.
func encodeCursor(t *testing.T, payload map[string]any) string {
	t.Helper()
	raw, err := json.Marshal(payload)
	require.NoError(t, err)
	return base64.RawURLEncoding.EncodeToString(raw)
}

// fakeWriter captures inserted records.
type fakeWriter struct {
	records []domain.AuditRecord
	err     error
}

// Insert implements usecase.JournalWriter.
func (f *fakeWriter) Insert(_ context.Context, record domain.AuditRecord) error {
	if f.err != nil {
		return f.err
	}
	f.records = append(f.records, record)
	return nil
}

// ExistsForResource implements usecase.JournalWriter by scanning the captures.
func (f *fakeWriter) ExistsForResource(_ context.Context, action, resourceType, resourceID string) (bool, error) {
	if f.err != nil {
		return false, f.err
	}
	for _, record := range f.records {
		if record.Action == action && record.ResourceType == resourceType && record.ResourceID == resourceID {
			return true, nil
		}
	}
	return false, nil
}

func TestRecord(t *testing.T) {
	t.Parallel()

	writer := &fakeWriter{}
	audit := usecase.NewAuditWriter(writer)

	err := audit.Record(context.Background(), usecase.AuditEntry{
		OperatorID:   "op-1",
		RequestID:    "req-1",
		Action:       "payout.approve",
		ResourceType: "payout",
		ResourceID:   "po-1",
		Before:       map[string]string{"status": "processing"},
		After:        map[string]string{"status": "approved"},
	})
	require.NoError(t, err)
	require.Len(t, writer.records, 1)

	record := writer.records[0]
	assert.NotEmpty(t, record.ID)
	parsed, err := uuid.Parse(record.ID)
	require.NoError(t, err)
	assert.Equal(t, uuid.Version(7), parsed.Version(), "identifiers are time ordered UUIDv7")
	assert.WithinDuration(t, time.Now().UTC(), record.OccurredAt, time.Minute)
	assert.Equal(t, "op-1", record.OperatorID)
	assert.Equal(t, "payout.approve", record.Action)
	assert.JSONEq(t, `{"status":"processing"}`, string(record.Before))
	assert.JSONEq(t, `{"status":"approved"}`, string(record.After))
}

func TestRecordNilStatesAndFailures(t *testing.T) {
	t.Parallel()

	writer := &fakeWriter{}
	audit := usecase.NewAuditWriter(writer)
	require.NoError(t, audit.Record(context.Background(), usecase.AuditEntry{Action: "create"}))
	require.Len(t, writer.records, 1)
	assert.Nil(t, writer.records[0].Before)
	assert.Nil(t, writer.records[0].After)

	broken := &fakeWriter{err: errors.New("db down")}
	audit = usecase.NewAuditWriter(broken)
	err := audit.Record(context.Background(), usecase.AuditEntry{Action: "create"})
	require.Error(t, err)
	assert.Equal(t, grpcmw.KindUnavailable, grpcmw.KindOf(err))

	audit = usecase.NewAuditWriter(&fakeWriter{})
	err = audit.Record(context.Background(), usecase.AuditEntry{Action: "create", After: make(chan int)})
	require.Error(t, err)
}
