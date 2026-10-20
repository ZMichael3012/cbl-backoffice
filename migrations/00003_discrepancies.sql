-- +goose Up
-- Reconciliation discrepancies consumed from recon.alerts (CBL-12.3). The id is
-- the alert id (the envelope event id): the inbox deduplicates consumption, the
-- primary key protects against a replay after an inbox cleanup. The resolution
-- is one-way (open -> resolved) and commits with its audit record.
CREATE TABLE IF NOT EXISTS discrepancies (
    id          uuid PRIMARY KEY,
    type        text NOT NULL,
    ref_id      text NOT NULL,
    merchant_id text NOT NULL DEFAULT '',
    severity    text NOT NULL,
    expected    text NOT NULL DEFAULT '',
    actual      text NOT NULL DEFAULT '',
    status      text NOT NULL DEFAULT 'open',
    created_at  timestamptz NOT NULL,
    received_at timestamptz NOT NULL DEFAULT now(),
    resolved_at timestamptz,
    resolved_by text NOT NULL DEFAULT '',
    resolution  text NOT NULL DEFAULT '',
    comment     text NOT NULL DEFAULT '',
    CONSTRAINT discrepancies_status_check
        CHECK (status IN ('open', 'resolved')),
    CONSTRAINT discrepancies_resolution_check
        CHECK ((status = 'open' AND resolution = '' AND resolved_by = '' AND resolved_at IS NULL)
            OR (status = 'resolved'
                AND resolution IN ('confirm_platform', 'confirm_gateway', 'manual_adjustment')
                AND resolved_by <> '' AND resolved_at IS NOT NULL))
);

CREATE INDEX IF NOT EXISTS discrepancies_listing_idx
    ON discrepancies (created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS discrepancies_severity_idx
    ON discrepancies (severity, created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS discrepancies_merchant_idx
    ON discrepancies (merchant_id, created_at DESC, id DESC);

-- +goose Down
DROP TABLE IF EXISTS discrepancies;
