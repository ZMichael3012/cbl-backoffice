//go:build integration

package consumer_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	eventsv1 "github.com/cbl/cbl-protos/gen/go/billing/events/v1"

	"github.com/cbl/cbl-kit/inbox"
	"github.com/cbl/cbl-kit/outbox"

	"github.com/cbl/cbl-backoffice/internal/consumer"
	"github.com/cbl/cbl-backoffice/internal/storage"
	"github.com/cbl/cbl-backoffice/internal/testsupport"
)

// fakePublisher records the retry and DLQ publications.
type fakePublisher struct {
	published []outbox.Envelope
}

// Publish implements outbox.Publisher.
func (p *fakePublisher) Publish(_ context.Context, env outbox.Envelope) error {
	p.published = append(p.published, env)
	return nil
}

// reconMessage builds the protobuf message of one alert.
func reconMessage(t *testing.T, alert *eventsv1.ReconAlert, eventID string) inbox.Message {
	t.Helper()
	value, err := proto.Marshal(alert)
	require.NoError(t, err)
	return inbox.Message{Topic: "recon.alerts", Value: value, Headers: map[string]string{
		outbox.HeaderEventID:    eventID,
		outbox.HeaderEventType:  "recon.alert",
		outbox.HeaderOccurredAt: "2026-10-19T12:00:00Z",
	}}
}

func TestIntegrationReconHandlerThroughTheInbox(t *testing.T) {
	db := testsupport.OpenMigrated(t)
	store := storage.NewDiscrepancyStore(db)
	handler := consumer.NewReconHandler(store, nil)
	publisher := &fakePublisher{}
	processor := inbox.NewProcessor("cbl-backoffice-recon", db, inbox.NewGuard(), handler,
		publisher, inbox.NewRouter("recon.alerts", 3), nil)
	ctx := context.Background()

	alert := &eventsv1.ReconAlert{
		AlertId:    alertID,
		Type:       eventsv1.ReconAlertType_RECON_ALERT_TYPE_AMOUNT_MISMATCH,
		RefId:      "01a154e0-0000-7000-8000-000000000902",
		Severity:   eventsv1.ReconAlertSeverity_RECON_ALERT_SEVERITY_CRITICAL,
		Expected:   "55.000000",
		Actual:     "60.000000",
		MerchantId: merchantID,
		CreatedAt:  timestamppb.New(time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)),
	}
	message := reconMessage(t, alert, alertID)

	require.NoError(t, processor.Process(ctx, message))
	var count int64
	require.NoError(t, db.Raw("SELECT count(*) FROM discrepancies").Scan(&count).Error)
	assert.Equal(t, int64(1), count)

	var inboxCount int64
	require.NoError(t, db.Raw("SELECT count(*) FROM inbox WHERE consumer = ?", "cbl-backoffice-recon").Scan(&inboxCount).Error)
	assert.Equal(t, int64(1), inboxCount)

	// The second delivery of the same event is deduplicated by the inbox.
	require.NoError(t, processor.Process(ctx, message))
	require.NoError(t, db.Raw("SELECT count(*) FROM discrepancies").Scan(&count).Error)
	assert.Equal(t, int64(1), count)

	// An unsupported enum is routed to the retry topics/DLQ (never a silent
	// skip) and leaves no inbox marker.
	invalid := &eventsv1.ReconAlert{
		AlertId:   "01a154e0-0000-7000-8000-000000000903",
		Type:      eventsv1.ReconAlertType(99),
		RefId:     "01a154e0-0000-7000-8000-000000000904",
		Severity:  eventsv1.ReconAlertSeverity_RECON_ALERT_SEVERITY_INFO,
		CreatedAt: timestamppb.New(time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)),
	}
	require.NoError(t, processor.Process(ctx, reconMessage(t, invalid, invalid.AlertId)), "the original message is acknowledged after the retry routing")
	assert.Len(t, publisher.published, 1, "the unsupported alert is published to a retry topic")
	require.NoError(t, db.Raw("SELECT count(*) FROM inbox WHERE event_id = ?", invalid.AlertId).Scan(&inboxCount).Error)
	assert.Zero(t, inboxCount)
}
