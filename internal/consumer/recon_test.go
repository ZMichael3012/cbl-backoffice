package consumer_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gorm.io/gorm"

	eventsv1 "github.com/cbl/cbl-protos/gen/go/billing/events/v1"

	"github.com/cbl/cbl-kit/inbox"
	"github.com/cbl/cbl-kit/outbox"

	"github.com/cbl/cbl-backoffice/internal/consumer"
	"github.com/cbl/cbl-backoffice/internal/domain"
)

const (
	alertID    = "01a154e0-0000-7000-8000-000000000901"
	merchantID = "0199d6c2-8e3f-7a1b-9c4d-2e5f6a7b8c9d"
)

// fakeStore records the inserted discrepancies.
type fakeStore struct {
	inserted []domain.Discrepancy
	err      error
}

// InsertTx records the record of the inbox transaction.
func (s *fakeStore) InsertTx(_ context.Context, _ *gorm.DB, record domain.Discrepancy) (bool, error) {
	if s.err != nil {
		return false, s.err
	}
	s.inserted = append(s.inserted, record)
	return true, nil
}

// message builds an inbox message from the alert.
func message(t *testing.T, alert *eventsv1.ReconAlert, eventID, occurredAt string) inbox.Message {
	t.Helper()
	value, err := proto.Marshal(alert)
	require.NoError(t, err)
	headers := map[string]string{outbox.HeaderEventID: eventID}
	if occurredAt != "" {
		headers[outbox.HeaderOccurredAt] = occurredAt
	}
	return inbox.Message{Value: value, Headers: headers}
}

// validAlert builds a valid alert.
func validAlert() *eventsv1.ReconAlert {
	return &eventsv1.ReconAlert{
		AlertId:    alertID,
		Type:       eventsv1.ReconAlertType_RECON_ALERT_TYPE_AMOUNT_MISMATCH,
		RefId:      "01a154e0-0000-7000-8000-000000000902",
		Severity:   eventsv1.ReconAlertSeverity_RECON_ALERT_SEVERITY_CRITICAL,
		Expected:   "55.000000",
		Actual:     "60.000000",
		MerchantId: merchantID,
		CreatedAt:  timestamppb.New(time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)),
	}
}

func TestReconHandlerStoresTheAlert(t *testing.T) {
	t.Parallel()

	store := &fakeStore{}
	handler := consumer.NewReconHandler(store, nil)
	require.NoError(t, handler.Handle(context.Background(), nil, message(t, validAlert(), alertID, "")))
	require.Len(t, store.inserted, 1)
	row := store.inserted[0]
	assert.Equal(t, alertID, row.ID)
	assert.Equal(t, "amount_mismatch", row.Type)
	assert.Equal(t, "critical", row.Severity)
	assert.Equal(t, "55.000000", row.Expected)
	assert.Equal(t, domain.DiscrepancyStatusOpen, row.Status)
}

func TestReconHandlerUsesTheEnvelopeTime(t *testing.T) {
	t.Parallel()

	alert := validAlert()
	alert.CreatedAt = nil
	store := &fakeStore{}
	handler := consumer.NewReconHandler(store, nil)
	occurred := "2026-10-19T12:30:00Z"
	require.NoError(t, handler.Handle(context.Background(), nil, message(t, alert, alertID, occurred)))
	require.Len(t, store.inserted, 1)
	assert.Equal(t, time.Date(2026, 10, 19, 12, 30, 0, 0, time.UTC), store.inserted[0].CreatedAt.UTC())
}

func TestReconHandlerRejectsInvalidEvents(t *testing.T) {
	t.Parallel()

	store := &fakeStore{}
	handler := consumer.NewReconHandler(store, nil)

	require.Error(t, handler.Handle(context.Background(), nil, inbox.Message{Value: []byte("not-proto"), Headers: map[string]string{outbox.HeaderEventID: alertID}}))

	alert := validAlert()
	alert.AlertId = "01a154e0-0000-7000-8000-000000000999"
	require.Error(t, handler.Handle(context.Background(), nil, message(t, alert, alertID, "")), "an event id mismatch is invalid")

	unspecified := validAlert()
	unspecified.Type = eventsv1.ReconAlertType_RECON_ALERT_TYPE_UNSPECIFIED
	require.Error(t, handler.Handle(context.Background(), nil, message(t, unspecified, alertID, "")))

	assert.Empty(t, store.inserted)
}

func TestReconHandlerRejectsUnsupportedEnums(t *testing.T) {
	t.Parallel()

	store := &fakeStore{}
	handler := consumer.NewReconHandler(store, nil)
	alert := validAlert()
	alert.Type = eventsv1.ReconAlertType(99)
	require.Error(t, handler.Handle(context.Background(), nil, message(t, alert, alertID, "")))
	assert.Empty(t, store.inserted)
}

func TestReconHandlerPropagatesStoreErrors(t *testing.T) {
	t.Parallel()

	store := &fakeStore{err: assert.AnError}
	handler := consumer.NewReconHandler(store, nil)
	require.Error(t, handler.Handle(context.Background(), nil, message(t, validAlert(), alertID, "")))
}
