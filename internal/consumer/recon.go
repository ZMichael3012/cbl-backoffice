package consumer

import (
	"context"
	"fmt"
	"time"

	"go.uber.org/zap"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"

	eventsv1 "github.com/cbl/cbl-protos/gen/go/billing/events/v1"

	"github.com/cbl/cbl-kit/inbox"
	"github.com/cbl/cbl-kit/outbox"

	"github.com/cbl/cbl-backoffice/internal/domain"
)

// Store persists one discrepancy in the inbox transaction (implemented by the
// storage).
type Store interface {
	// InsertTx stores the record and reports whether the row was inserted; a
	// replay of the same alert id reports false.
	InsertTx(ctx context.Context, tx *gorm.DB, record domain.Discrepancy) (bool, error)
}

// ReconHandler consumes recon.alerts and stores the discrepancies (CBL-12.3).
type ReconHandler struct {
	store Store
	log   *zap.Logger
}

// NewReconHandler creates the inbox handler.
func NewReconHandler(store Store, log *zap.Logger) *ReconHandler {
	if log == nil {
		log = zap.NewNop()
	}
	return &ReconHandler{store: store, log: log}
}

// Handle validates the alert and stores it in the inbox transaction. An
// invalid or unsupported alert returns an error so the inbox moves it to the
// retry topics and the DLQ; an alert is never silently skipped (a hidden alert
// would hide a money-reconciliation fact).
func (h *ReconHandler) Handle(ctx context.Context, tx *gorm.DB, msg inbox.Message) error {
	var alert eventsv1.ReconAlert
	if err := proto.Unmarshal(msg.Value, &alert); err != nil {
		return fmt.Errorf("consumer: decode recon alert: %w", err)
	}
	occurredAt := parseOccurredAt(msg.Headers[outbox.HeaderOccurredAt])
	record, err := domain.ReconAlertRecord(&alert, msg.Headers[outbox.HeaderEventID], occurredAt)
	if err != nil {
		return fmt.Errorf("consumer: invalid recon alert: %w", err)
	}
	inserted, err := h.store.InsertTx(ctx, tx, record)
	if err != nil {
		return fmt.Errorf("consumer: store recon alert: %w", err)
	}
	if !inserted {
		h.log.Warn("recon alert already stored", zap.String("alert_id", record.ID))
		return nil
	}
	h.log.Info("recon alert stored",
		zap.String("alert_id", record.ID),
		zap.String("type", record.Type),
		zap.String("severity", record.Severity))
	return nil
}

// parseOccurredAt parses the optional envelope time header.
func parseOccurredAt(raw string) time.Time {
	if raw == "" {
		return time.Time{}
	}
	parsed, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return time.Time{}
	}
	return parsed.UTC()
}
