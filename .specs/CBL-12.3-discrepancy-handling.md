# CBL-12.3 (cbl-backoffice). Reconciliation discrepancy handling

| Field | Value |
| :-- | :-- |
| Epic | E12. Back-office |
| Repository | `cbl-backoffice` |
| Size | M |
| Wave/group | W4-ADMIN |
| Depends on | 12.1, 12.2, 17.13 |
| Contract version | `admin.v1.yaml` 1.0.0 (the frozen routes; additive `cf-3.12`: 400/503 responses, UUID filter format, comment bound), `recon_events.proto` (CF-3) |
| Status | done (20.10.2026), review closed |

## 1. Context and goal

CBL-12.1/12.2 delivered the admin API foundations (tokens, RBAC, append-only audit)
and the payout/routing/provider actions. The frozen admin contract already contains
the reconciliation routes (`GET /admin/v1/reconciliation/discrepancies`,
`POST /admin/v1/reconciliation/discrepancies/{id}/resolve`) with the `recon_operator`
and `auditor` roles prepared. This task implements them: consume `recon.alerts`,
persist the discrepancies, expose the operator list and the manual resolution with the
atomic audit record. It closes the back-office half of GE-5 and the 17.26-D
acceptance.

## 2. Scope

In scope: the `recon.alerts` consumer through the kit inbox (group
`cbl-backoffice-recon`, retry topics and DLQ, dedupe by `event_id`); the discrepancy
validation and mapping; the `discrepancies` table and the inbox reference table; the
list route (filters, keyset cursor, RBAC); the resolve route (operator binding, RBAC,
one-way transition, atomic audit); the service wiring and the stand env.

Out of scope: the discrepancy report and dashboard (9.4); the operator UI (12.4); a
merchant-facing discrepancy notification type (deferred by the CF-3 freeze); an
automated ledger correction for `manual_adjustment` (see 3.5 and KI-backoffice-06);
token issuance/rotation (KI-backoffice-02); real metrics (0.7).

## 3. Architecture of the solution

### 3.1. Consumer

`internal/consumer/recon.go` implements the kit `inbox.Handler`:

- decode `billing.events.v1.ReconAlert`; the envelope `event_id` must be a UUID and
  equal `alert.alert_id` (the topic contract, CF-3);
- `created_at` is taken from the body (authoritative per the proto); a missing body
  value falls back to the envelope `occurred_at` header; both absent is an invalid
  event;
- validation: `ref_id` non-empty and at most 256 characters, `expected`/`actual` at
  most 512 characters, a non-empty `merchant_id` must parse as a UUID;
- enum policy (T-BO-12.3-03): an unspecified or unknown enum value is an invalid
  event, so the inbox moves it to the retry topics and the DLQ; an alert is never
  silently skipped (a hidden alert would hide a money-reconciliation fact and the
  inbox marker would make it unreplayable);
- insert with `ON CONFLICT (id) DO NOTHING` in the inbox transaction: the inbox
  `(consumer, event_id)` is the primary dedupe, the table primary key protects
  against a replay after an inbox cleanup.

The consumer starts only when `KAFKA_BROKERS` is set (the stand and integration
stands configure it; the service remains usable for reads without it). A fresh group
reads the topic from the earliest offset: the alert history is the source of truth for
the operator surface, not a transient queue (T-BO-12.3-04).

### 3.2. Data model (migration 00003 + the kit inbox reference 00002)

```
discrepancies (
  id uuid primary key,                  -- alert_id = the envelope event_id
  type text not null,                   -- missing_in_ledger | amount_mismatch | status_mismatch
  ref_id text not null,                 -- payment_id / payout_id
  merchant_id text not null default '', -- UUID or empty for system discrepancies
  severity text not null,               -- info | warning | critical
  expected text not null default '',
  actual text not null default '',
  status text not null default 'open',  -- open | resolved
  created_at timestamptz not null,      -- the alert time (authoritative)
  received_at timestamptz not null default now(),
  resolved_at timestamptz,
  resolved_by text not null default '',
  resolution text not null default '',  -- confirm_platform | confirm_gateway | manual_adjustment
  comment text not null default '',
  check (status in ('open','resolved')),
  check ((status = 'open' and resolution = '' and resolved_by = '')
      or (status = 'resolved' and resolution in (...) and resolved_by <> ''))
)
index (created_at desc, id desc)                 -- the default listing
index (severity, created_at desc, id desc)       -- the severity filter
index (merchant_id, created_at desc, id desc)    -- the merchant filter
```

The API shape is the frozen `Discrepancy` schema: `id`, `type`, `ref_id`,
`merchant_id`, `severity`, `expected`, `actual`, `status`, `created_at`.

### 3.3. Listing

`GET /admin/v1/reconciliation/discrepancies` (`reconciliation:read`): optional
`severity` (`info|warning|critical`), `merchant_id` (UUID; empty means no filter),
`limit` 1..100 (default 20), opaque `cursor`. Order `created_at DESC, id DESC`; the
cursor is a canonical-JSON keyset position `(created_at, id)` bound to the filter
fingerprint `{severity, merchant_id}` (the CBL-12.1 T-BO3 pattern: a cursor cannot
change the filter set). Invalid values and forged cursors are 400
`validation_failed`.

### 3.4. Resolution

`POST /admin/v1/reconciliation/discrepancies/{id}/resolve`
(`reconciliation:resolve`): strict JSON `{operator_id, resolution, comment}`.

- `operator_id` must equal the token subject, otherwise 403 `operator_mismatch`
  (the acting identity is always the token subject, the CBL-12.2 rule);
- `resolution` in `confirm_platform|confirm_gateway|manual_adjustment`, otherwise 400;
- `comment` optional, at most 1024 characters, no control characters;
- unknown id -> 404 `discrepancy_not_found`; already resolved -> 409
  `discrepancy_resolved`; the transition is one-way (`open -> resolved`);
- the update and the audit record commit in one PostgreSQL transaction
  (`storage.DiscrepancyStore.ResolveDiscrepancy` with the `AuditStore.WithTx` seam,
  T-BO4): the audit entry `discrepancy.resolve` / `discrepancy` / id carries the
  whitelisted snapshots `{id, type, ref_id, merchant_id, severity, status,
  resolution, comment}` before and after; a build or insert failure rolls back the
  resolution.

### 3.5. `manual_adjustment`

The frozen contract carries no adjustment amount, direction or accounts, so an
automatic ledger posting is impossible without new contract input; money mutations
must go through the owning service (CBL-003, money invariants). In 12.3
`manual_adjustment` is the operator attestation of an out-of-band correction: the
decision, the acting operator and the comment are journaled atomically, and the
ledger is not touched by the back-office. A future automated adjustment needs an
additive contract (amount, debit/credit accounts, reference) and a dedicated
money-path task (KI-backoffice-06).

### 3.6. Wiring

- `internal/config`: `KAFKA_BROKERS` (comma-separated, optional) and
  `BACKOFFICE_RECON_GROUP` (default `cbl-backoffice-recon`).
- `cmd/backoffice`: `storage.NewDiscrepancyStore`, `usecase.NewDiscrepancyService`,
  `startReconConsumer` (publisher + processor + runner, closed on shutdown, ordered
  before the DB close).
- `internal/transport`: two routes behind the existing authN/RBAC middleware.
- `go.mod`: the Kafka client arrives through `cbl-kit` (franz-go v1.18.0); the
  service imports only the kit consumer/publisher packages.

## 4. Error taxonomy

| Condition | HTTP |
| :-- | :-- |
| bad severity/merchant/limit/cursor/resolution/comment/id | 400 `validation_failed` |
| body operator_id differs from the token subject | 403 `operator_mismatch` |
| missing permission | 403 `permission_denied` |
| unknown discrepancy id | 404 `discrepancy_not_found` |
| already resolved | 409 `discrepancy_resolved` |
| storage failure | 503 `service_unavailable` + `Retry-After: 1` |

## 5. Test design

Red run (step 5): the first `go test ./...` fails to build on the new symbols; the
output is recorded in the task session before the implementation.

### 5.1. Unit

| ID | Case | Expected |
| :-- | :-- | :-- |
| T-BO-12.3-01 | mapping of all three types and severities | contract strings |
| T-BO-12.3-02 | event id != alert id, empty/bad ref_id, bad merchant UUID, too long fields, missing time | invalid event error |
| T-BO-12.3-03 | UNSPECIFIED and unknown type/severity | invalid event (the retry topics and the DLQ) |
| T-BO-12.3-04 | created_at fallback to the envelope time | used when the body time is nil |
| T-BO-12.3-05 | list validation: severity, merchant, limit 1..100, forged cursor | invalid_argument |
| T-BO-12.3-06 | cursor roundtrip and filter binding | same key; changed filter rejected |
| T-BO-12.3-07 | resolve validation: id, resolution, comment length/control chars, operator | invalid_argument |
| T-BO-12.3-08 | resolve store mapping: not found, resolved, storage failure | 404, 409, 503 |
| T-BO-12.3-09 | the audit callback gets the before state and builds the after snapshot | action/resource/snapshots |
| T-BO-12.3-10 | transport: RBAC, operator mismatch, strict body, 200 mapping, 404/409 mapping | codes and bodies |

### 5.2. Integration (PostgreSQL)

| ID | Case | Expected |
| :-- | :-- | :-- |
| T-BO-12.3-20 | insert dedupe by id; the ingestion time is written | one row, a current received_at |
| T-BO-12.3-21 | listing: order and keyset pages (6 rows, page 2) | 2+2+2, DESC, no duplicates |
| T-BO-12.3-22 | listing filters: severity, merchant, combined | matching rows only |
| T-BO-12.3-23 | resolve: open -> resolved, resolved_by/at, resolution/comment persisted | one row changed |
| T-BO-12.3-24 | resolve atomic audit: record written in the transaction; a build failure rolls back | audit row + status; rollback keeps open |
| T-BO-12.3-25 | resolve not found / already resolved | domain errors, no audit row |
| T-BO-12.3-26 | the handler through the kit Processor: first delivery, replay dedupe, unsupported enum -> a retry publication and no inbox marker | one row, one inbox row, one retry envelope |

### 5.3. E2E

Service stand (`test/e2e/E2E.md`, no Kafka in the service compose): seed
`discrepancies` rows with psql, then `E2E_Backoffice_Reconciliation`: auditor and
recon_operator may list, payout operator 403, filters and cursor work, resolve is
200 and audited, the repeat is 409, a missing permission on resolve is 403, the audit
journal shows `discrepancy.resolve`.

Global stand (GE-5 back-office half, part of the 17.26-D run): the GE-5 amount
divergence publishes `recon.alerts`; the back-office consumer stores it; a
recon_operator token lists the alert (id, type, ref, severity, expected/actual),
resolves it (`confirm_gateway` or `manual_adjustment`), the repeat is 409, and the
audit journal and PostgreSQL rows confirm the transition.

## 6. Decisions

| ID | Decision | Pros | Cons |
| :-- | :-- | :-- | :-- |
| T-BO-12.3-01 | The discrepancy id is the alert id (the envelope event id) | one dedupe key across the inbox, the table and ClickHouse; the operator can correlate | an alert replaces nothing; a re-published alert needs a new id (the producer contract) |
| T-BO-12.3-02 | The update and the audit commit in one transaction | no resolution without its journal record | the callback-in-transaction seam (storage calls the usecase builder) is a small deviation from a pure port |
| T-BO-12.3-03 | Every unspecified or unknown enum is an invalid event (retry/DLQ) | no reconciliation alert is silently hidden; the DLQ is durable and replayable | a future type stays in the DLQ until a contract extension (the ClickHouse record remains) |
| T-BO-12.3-04 | A fresh consumer group reads the alert history from the earliest offset | the operator surface is not blind after a deployment; the alerts are durable facts | the first start ingests the topic backlog (bounded by the topic retention) |
| T-BO-12.3-05 | `manual_adjustment` is an attested decision, not a ledger posting | no money mutation outside the owning service; the frozen contract is respected | an operator cannot correct a ledger balance from the back-office (KI-backoffice-06) |
| T-BO-12.3-06 | The listing cursor is bound to the filter fingerprint | stable pagination; a cursor cannot silently change the filter set | the cursor is opaque but not a MAC (no data leak beyond the caller rights) |

## 7. Open questions

- The service stand has no Kafka: the consumer is covered by unit/integration tests
  and the global GE-5 run; a service-stand Kafka is not introduced in this task.
- The 9.4 report/dashboard consumes the same table; its filter needs are not added
  here.

## 8. Results and review closure

### 8.1. Results (20.10.2026)

- Unit: the alert mapping and validation (ids, bounds, merchant UUID, time fallback,
  unspecified/unknown enums as invalid events, canonical UUIDs), the list validation
  and filter-bound cursor, the resolve validation and store-error mapping, the audit
  snapshots; transport RBAC, operator mismatch, strict body, error mapping.
- Integration (PostgreSQL, migrations 00001..00003): insert dedupe with a current
  `received_at`, keyset pages (2+2+2 over 6 rows, DESC), severity/merchant filters,
  the resolve transition with the atomic audit record, the rollback on an audit build
  failure, not found/already resolved without a record, and the handler through the
  kit inbox Processor (first delivery, replay dedupe, unsupported enum -> a retry
  publication and no inbox marker).
- Coverage 87.2% with `-coverpkg`, golangci-lint 0, go vet clean, em dash check green.
- Service e2e `E2E_Backoffice_Reconciliation` (run `20261020t0700z`, artifacts in
  `test/e2e/.runs/20261020t0700z/`): 18/18 checks PASS (listing RBAC, filters, pages,
  cursor binding, resolve one-way transition, 404/409, the audit record).
- Global GE-5 back-office half (run `20261019t130356z`, artifacts
  `ge-5-backoffice.log`/`ge-5-backoffice-checks.txt`): the alert
  `01a154a7-72d7-7ea4-956f-953c681e5b64` (amount_mismatch, 55 vs 60 USDT) was consumed
  from `recon.alerts`, listed with the matching fields, resolved with
  `confirm_gateway`, the repeat returned 409, and the audit record carries the
  transition; 5/5 PASS.

### 8.2. Review closure

Four passes (no-context, architecture, with-context, security) on the staged diff;
the findings closed:

- BLOCKING (security): an unknown non-zero enum was acknowledged and the inbox marker
  committed, permanently hiding the alert. Now every unspecified/unknown enum is an
  invalid event (the retry topics and the DLQ), the handler never silently skips, and
  the integration test asserts the retry publication and the absent inbox marker
  (T-BO-12.3-03);
- MAJOR: `received_at` was written as the Go zero time (GORM includes the field and
  bypasses the `DEFAULT now()`); the ingestion time is now set explicitly and asserted;
- MAJOR: the OpenAPI declared neither 400 on the listing nor 503 on either route and
  omitted the UUID filter format and the comment bound; the additive `cf-3.12`
  contract update (`cbl-protos@76b00d1`) records them;
- the conditional resolve update now checks `RowsAffected` (the audit record is
  written only for the winning transition) and uses the documented
  `AuditStore.WithTx` seam;
- UUIDs are canonicalized at ingestion for the alert/event ids, the merchant and the
  resolve id, so the keyset order, the row ids and the audit `resource_id` agree;
- the cursor codec is a shared helper (`usecase/cursor.go`) for the audit and the
  discrepancies (no drift), and the list limit parsing is shared
  (`transport/parseLimitValue`);
- test gaps closed: the list storage failure -> 503, the resolve 200 body, the config
  Kafka parsing/default group, the inbox integration case;
- documentation: the spec header table, the enum policy and the test matrix (6 rows,
  2+2+2) are aligned; the README, AGENTS and the e2e scenario are updated;
- NIT (accepted): the client-supplied `X-Request-Id` is persisted unvalidated in the
  immutable journal; it is bounded by the access-log middleware and recorded as
  KI-backoffice-07 for the request-id policy of 0.6/0.7.

