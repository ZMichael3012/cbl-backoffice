package domain_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	eventsv1 "github.com/cbl/cbl-protos/gen/go/billing/events/v1"

	"github.com/cbl/cbl-backoffice/internal/domain"
)

const (
	alertID    = "01a154e0-0000-7000-8000-000000000901"
	merchantID = "0199d6c2-8e3f-7a1b-9c4d-2e5f6a7b8c9d"
)

// reconAlert builds a valid alert.
func reconAlert(alertType eventsv1.ReconAlertType, severity eventsv1.ReconAlertSeverity) *eventsv1.ReconAlert {
	return &eventsv1.ReconAlert{
		AlertId:    alertID,
		Type:       alertType,
		RefId:      "01a154e0-0000-7000-8000-000000000902",
		Severity:   severity,
		Expected:   "55.000000",
		Actual:     "60.000000",
		MerchantId: merchantID,
		CreatedAt:  timestamppb.New(time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)),
	}
}

func TestReconAlertRecordMapping(t *testing.T) {
	t.Parallel()

	types := map[eventsv1.ReconAlertType]string{
		eventsv1.ReconAlertType_RECON_ALERT_TYPE_MISSING_IN_LEDGER: "missing_in_ledger",
		eventsv1.ReconAlertType_RECON_ALERT_TYPE_AMOUNT_MISMATCH:   "amount_mismatch",
		eventsv1.ReconAlertType_RECON_ALERT_TYPE_STATUS_MISMATCH:   "status_mismatch",
	}
	severities := map[eventsv1.ReconAlertSeverity]string{
		eventsv1.ReconAlertSeverity_RECON_ALERT_SEVERITY_INFO:     "info",
		eventsv1.ReconAlertSeverity_RECON_ALERT_SEVERITY_WARNING:  "warning",
		eventsv1.ReconAlertSeverity_RECON_ALERT_SEVERITY_CRITICAL: "critical",
	}
	created := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	for alertType, name := range types {
		for severity, severityName := range severities {
			record, err := domain.ReconAlertRecord(reconAlert(alertType, severity), alertID, time.Time{})
			require.NoError(t, err)
			assert.Equal(t, alertID, record.ID)
			assert.Equal(t, name, record.Type)
			assert.Equal(t, severityName, record.Severity)
			assert.Equal(t, merchantID, record.MerchantID)
			assert.Equal(t, "55.000000", record.Expected)
			assert.Equal(t, "60.000000", record.Actual)
			assert.Equal(t, domain.DiscrepancyStatusOpen, record.Status)
			assert.Equal(t, created, record.CreatedAt.UTC())
		}
	}
}

func TestReconAlertRecordValidation(t *testing.T) {
	t.Parallel()

	valid := reconAlert(eventsv1.ReconAlertType_RECON_ALERT_TYPE_AMOUNT_MISMATCH, eventsv1.ReconAlertSeverity_RECON_ALERT_SEVERITY_CRITICAL)

	mismatch := proto.Clone(valid).(*eventsv1.ReconAlert)
	mismatch.AlertId = "01a154e0-0000-7000-8000-000000000999"
	_, err := domain.ReconAlertRecord(mismatch, alertID, time.Time{})
	require.Error(t, err)

	emptyRef := proto.Clone(valid).(*eventsv1.ReconAlert)
	emptyRef.RefId = "  "
	_, err = domain.ReconAlertRecord(emptyRef, alertID, time.Time{})
	require.Error(t, err)

	badMerchant := proto.Clone(valid).(*eventsv1.ReconAlert)
	badMerchant.MerchantId = "not-a-uuid"
	_, err = domain.ReconAlertRecord(badMerchant, alertID, time.Time{})
	require.Error(t, err)

	longExpected := proto.Clone(valid).(*eventsv1.ReconAlert)
	longExpected.Expected = strings.Repeat("9", 513)
	_, err = domain.ReconAlertRecord(longExpected, alertID, time.Time{})
	require.Error(t, err)

	longRef := proto.Clone(valid).(*eventsv1.ReconAlert)
	longRef.RefId = strings.Repeat("r", 257)
	_, err = domain.ReconAlertRecord(longRef, alertID, time.Time{})
	require.Error(t, err)
}

func TestReconAlertRecordTimeFallback(t *testing.T) {
	t.Parallel()

	alert := reconAlert(eventsv1.ReconAlertType_RECON_ALERT_TYPE_STATUS_MISMATCH, eventsv1.ReconAlertSeverity_RECON_ALERT_SEVERITY_WARNING)
	alert.CreatedAt = nil
	envelope := time.Date(2026, 10, 19, 12, 30, 0, 0, time.UTC)
	record, err := domain.ReconAlertRecord(alert, alertID, envelope)
	require.NoError(t, err)
	assert.Equal(t, envelope, record.CreatedAt.UTC())

	_, err = domain.ReconAlertRecord(alert, alertID, time.Time{})
	require.Error(t, err, "no time source is an invalid alert")
}

func TestReconAlertRecordUnsupportedEnums(t *testing.T) {
	t.Parallel()

	unspecifiedType := reconAlert(eventsv1.ReconAlertType_RECON_ALERT_TYPE_UNSPECIFIED, eventsv1.ReconAlertSeverity_RECON_ALERT_SEVERITY_INFO)
	_, err := domain.ReconAlertRecord(unspecifiedType, alertID, time.Time{})
	require.Error(t, err, "an unspecified type is a producer bug")

	unspecifiedSeverity := reconAlert(eventsv1.ReconAlertType_RECON_ALERT_TYPE_AMOUNT_MISMATCH, eventsv1.ReconAlertSeverity_RECON_ALERT_SEVERITY_UNSPECIFIED)
	_, err = domain.ReconAlertRecord(unspecifiedSeverity, alertID, time.Time{})
	require.Error(t, err, "an unspecified severity is a producer bug")

	unknownType := reconAlert(eventsv1.ReconAlertType(99), eventsv1.ReconAlertSeverity_RECON_ALERT_SEVERITY_INFO)
	_, err = domain.ReconAlertRecord(unknownType, alertID, time.Time{})
	require.Error(t, err, "an unsupported type goes to the retry topics and the DLQ")

	unknownSeverity := reconAlert(eventsv1.ReconAlertType_RECON_ALERT_TYPE_AMOUNT_MISMATCH, eventsv1.ReconAlertSeverity(99))
	_, err = domain.ReconAlertRecord(unknownSeverity, alertID, time.Time{})
	require.Error(t, err, "an unsupported severity goes to the retry topics and the DLQ")
}

func TestParseDiscrepancyEnums(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"info", "warning", "critical"} {
		parsed, ok := domain.ParseDiscrepancySeverity(name)
		assert.True(t, ok, name)
		assert.Equal(t, name, parsed)
	}
	for _, name := range []string{"", "INFO", "fatal"} {
		_, ok := domain.ParseDiscrepancySeverity(name)
		assert.False(t, ok, name)
	}

	for _, name := range []string{"confirm_platform", "confirm_gateway", "manual_adjustment"} {
		parsed, ok := domain.ParseDiscrepancyResolution(name)
		assert.True(t, ok, name)
		assert.Equal(t, name, parsed)
	}
	for _, name := range []string{"", "confirm", "reject"} {
		_, ok := domain.ParseDiscrepancyResolution(name)
		assert.False(t, ok, name)
	}
}

func TestDiscrepancyAuditView(t *testing.T) {
	t.Parallel()

	view := domain.Discrepancy{
		ID: alertID, Type: "amount_mismatch", RefID: "ref-1", MerchantID: merchantID,
		Severity: "critical", Status: domain.DiscrepancyStatusResolved,
		Resolution: domain.DiscrepancyResolutionManualAdjustment, Comment: "corrected out of band",
	}.AuditView()
	raw, err := json.Marshal(view)
	require.NoError(t, err)
	var decoded map[string]string
	require.NoError(t, json.Unmarshal(raw, &decoded))
	assert.Equal(t, alertID, decoded["id"])
	assert.Equal(t, "amount_mismatch", decoded["type"])
	assert.Equal(t, "ref-1", decoded["ref_id"])
	assert.Equal(t, merchantID, decoded["merchant_id"])
	assert.Equal(t, "critical", decoded["severity"])
	assert.Equal(t, "resolved", decoded["status"])
	assert.Equal(t, "manual_adjustment", decoded["resolution"])
	assert.Equal(t, "corrected out of band", decoded["comment"])
}
