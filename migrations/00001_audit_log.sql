-- +goose Up
-- Append-only audit journal of operator actions (CBL-12.1): who did what, with
-- the state before and after. The trigger below makes the append-only property
-- a database invariant: neither the service nor direct SQL can update or
-- delete a record.
CREATE TABLE IF NOT EXISTS audit_log (
    id            uuid PRIMARY KEY,
    occurred_at   timestamptz NOT NULL,
    operator_id   text NOT NULL,
    action        text NOT NULL,
    resource_type text NOT NULL DEFAULT '',
    resource_id   text NOT NULL DEFAULT '',
    request_id    text NOT NULL DEFAULT '',
    before        jsonb,
    after         jsonb
);

CREATE INDEX IF NOT EXISTS audit_log_occurred_at_id_idx ON audit_log (occurred_at DESC, id DESC);

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION audit_log_immutable()
RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'audit_log is append-only';
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS audit_log_no_update_delete ON audit_log;
CREATE TRIGGER audit_log_no_update_delete
    BEFORE UPDATE OR DELETE ON audit_log
    FOR EACH ROW EXECUTE FUNCTION audit_log_immutable();

-- Row triggers do not fire on TRUNCATE: a statement-level trigger closes that
-- gap (DROP TABLE and disabling the triggers stay owner-level acts outside the
-- dev threat model, recorded in the CBL-12.1 spec and AGENTS trade-offs).
DROP TRIGGER IF EXISTS audit_log_no_truncate ON audit_log;
CREATE TRIGGER audit_log_no_truncate
    BEFORE TRUNCATE ON audit_log
    FOR EACH STATEMENT EXECUTE FUNCTION audit_log_immutable();
