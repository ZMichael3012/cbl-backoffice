package domain_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/cbl/cbl-backoffice/internal/domain"
)

func TestAuditFilterFingerprint(t *testing.T) {
	t.Parallel()

	from := time.Date(2026, 10, 16, 10, 0, 0, 0, time.UTC)
	base := domain.AuditFilter{OperatorID: "op-1", Action: "payout.approve", PeriodFrom: &from}

	assert.Equal(t, base.Fingerprint(), base.Fingerprint(), "fingerprint is stable")

	other := domain.AuditFilter{OperatorID: "op-1", Action: "payout.reject", PeriodFrom: &from}
	assert.NotEqual(t, base.Fingerprint(), other.Fingerprint(), "action affects the fingerprint")

	empty := domain.AuditFilter{}
	assert.NotEqual(t, base.Fingerprint(), empty.Fingerprint())

	// The same instant in another location formats identically (canonical UTC).
	shifted := time.Date(2026, 10, 16, 13, 0, 0, 0, time.FixedZone("MSK", 3*3600))
	equivalent := domain.AuditFilter{OperatorID: "op-1", Action: "payout.approve", PeriodFrom: &shifted}
	assert.Equal(t, base.Fingerprint(), equivalent.Fingerprint())
}

func TestAuditFilterFingerprintNoSeparatorCollision(t *testing.T) {
	t.Parallel()

	// A separator-joined encoding would collide these two filter sets; the
	// canonical JSON form must keep them distinct.
	first := domain.AuditFilter{OperatorID: "a", Action: "b"}
	second := domain.AuditFilter{OperatorID: "a\x1fb", Action: ""}
	assert.NotEqual(t, first.Fingerprint(), second.Fingerprint())
}

func TestAuditKeyString(t *testing.T) {
	t.Parallel()

	key := domain.AuditKey{
		OccurredAt: time.Date(2026, 10, 16, 10, 0, 0, 0, time.UTC),
		ID:         "018f0000-0000-7000-8000-000000000001",
	}
	assert.Equal(t, "2026-10-16T10:00:00Z/018f0000-0000-7000-8000-000000000001", key.String())
}
