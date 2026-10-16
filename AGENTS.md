# AGENTS.md - cbl-backoffice

Admin API for operators: payout approval, provider routing, discrepancy handling, audit.
Task CBL-12.1 delivered the foundations (operator bearer tokens, the RBAC matrix, the
append-only action journal with its read API); CBL-12.2 added the business routes
(payout decisions, routing rules, provider disable); the discrepancies arrive with 12.3.

Mandatory global rules: `../../AGENTS.md`, `../../.specs/CBL-001-development-process.md`,
`../../.specs/CBL-002-repos-and-docker.md`, contracts `../../.specs/CBL-003-contracts.md`.
The em dash (U+2014) is forbidden, only the hyphen is used.

## 1. Boundaries

In scope: `/admin/v1`; operator authentication (stateless HS256 JWT, `ADMIN_JWT_SECRET`);
RBAC (role-permission matrix in code, deny by default); the append-only `audit_log`
(migration with UPDATE/DELETE/TRUNCATE triggers, writer with the `ExistsForResource`
compensation, keyset reader) and the `GET /admin/v1/audit-log` read route; the business
routes of CBL-12.2 (payout approve/reject via the PayoutService client, routing
get/put and provider disable via the ConnectorAdminService client, all audited);
service foundation (`/healthz`, `/readyz`, `/metrics` placeholder, problem+json,
request-id/access-log/recovery via `cbl-kit`, graceful shutdown); migrations at startup
(goose via `storagex`).

Out of scope: the discrepancies of 12.3 (the `recon.alerts` consumer and
`manual_adjustment`); the operator UI (12.4); token issuance, an operator directory,
rotation and revocation (dev stand: the mint utility and the shared secret; production:
identity provider/ops - KI-backoffice-02); provider enable and disabled-set retention
(KI-backoffice-05, KI-connector-09); rate limiting and mTLS (0.6); real metrics
and dashboards (0.7/14.4); Redis and Kafka (not needed until 12.3).

## 2. Contracts

- REST (`openapi/admin.v1.yaml` 1.0.0, tags `cbl-protos@cf-3.5..cf-3.10`):
  `GET /admin/v1/audit-log` (filters `operator_id`, `action`, `period_from`/`period_to`,
  `limit` 1-100 default 20, opaque `cursor`); `POST /admin/v1/payouts/{id}/approve|reject`
  (the body operator must match the token subject; a missing reject reason is 422);
  `GET/PUT /admin/v1/routing-rules` (revision-based optimistic concurrency, 409
  `revision_conflict`; the RoutingRule shape matches the connector source of truth);
  `POST /admin/v1/providers/{id}/disable` (idempotent 204). Errors: 400
  `validation_failed`, 401 `unauthenticated`, 403 `permission_denied`/`operator_mismatch`,
  404 `payout_not_found`/`provider_not_found`, 409 conflict, 503 `service_unavailable`
  + `Retry-After`. The discrepancies routes are frozen but unimplemented until 12.3.
- gRPC clients: `billing.payout.v1.PayoutService` (ApprovePayout, `Payout.approved_by`
  cf-3.9) and `billing.connector.v1.ConnectorAdminService` (routing and provider
  disable, cf-3.7); plaintext inside the trusted network (mTLS - 0.6).
- Kafka: none; `recon.alerts` consumption arrives with 12.3.
- Storage: PostgreSQL `backoffice` (own migrations; no shared tables).

## 3. Architecture

```
cmd/backoffice/          # main: config, migrations, servers (API :8020, metrics :9220)
internal/config/         # env parsing and validation (fail-fast)
internal/auth/           # HS256 operator token verification (MinSecretLength = 32)
internal/payoutclient/   # PayoutService gRPC client (decisions; code mapping)
internal/connectorclient/# ConnectorAdminService gRPC client (routing, disable)
internal/domain/         # roles, the RBAC matrix, audit types, routing rule, payout view
internal/usecase/        # audit reader/writer, the 12.2 admin actions (clients + audit)
internal/storage/        # Open/Migrate/Ready, the audit_log store (WithTx, ExistsForResource)
internal/transport/      # Gin router, audit and action handlers, problem+json mapping
internal/transport/httpmw/ # operator authn (strict Bearer) and RBAC Require middleware
internal/testsupport/    # throwaway PostgreSQL for integration tests (tag integration)
migrations/              # goose migrations (forward only)
deploy/                  # service compose stand (postgres, stubs, backoffice image)
test/e2e/                # E2E.md, .additionals (mint, stubs), .runs (artifacts)
```

Layers: `transport` -> `usecase` (ports `JournalStore`/`JournalWriter`,
`PayoutDecider`, `RoutingAdmin`) -> `domain`; the clients and `storage` implement the
ports (the usecase layer never imports gin or gorm).
Libraries: the standard `CBL-002` set (Gin, GORM + pgx driver, goose via `storagex`,
zap), `golang-jwt/v5` (root `AGENTS.md` section 2.1) and `cbl-kit` (ginmw middlewares,
grpcmw error taxonomy). `AutoMigrate` is forbidden, migrations are goose only and
forward only.

RBAC matrix (single source: `internal/domain/identity.go`; deny by default):

| Permission | admin | payout_operator | routing_manager | recon_operator | provider_manager | auditor |
| :-- | :-- | :-- | :-- | :-- | :-- | :-- |
| `payouts:approve` | x | x | | | | |
| `routing:read` | x | | x | | | |
| `routing:manage` | x | | x | | | |
| `reconciliation:read` | x | | | x | | x |
| `reconciliation:resolve` | x | | | x | | |
| `providers:disable` | x | | | | x | |
| `audit:read` | x | | | | | x |

## 4. Commands

```powershell
make build             # go build ./...
make test              # go test ./...
make test-integration  # integration (testcontainers: PostgreSQL)
make cover             # merged unit+integration coverage (cover.out)
make vet               # go vet ./... (+ integration e2e tags)
make lint              # golangci-lint in Docker (services/ context for cbl-kit)
make up                # stand: postgres, backoffice (app profile)
make down              # stand + volume
```

E2E: the run is performed by an agent following `test/e2e/E2E.md` (platform-independent:
bring up the stand, mint tokens with `.additionals/mint`, execute the
`E2E_Backoffice_RBACAudit` and `E2E_Backoffice_AdminActions` scenarios - the latter
against the in-stand PayoutService/ConnectorAdminService stubs, save artifacts to
`test/e2e/.runs/<UTC mark>/`, mandatory `summary.md`); there are no PowerShell run
scripts (`CBL-002` section 7).

## 5. Ports and environment

HTTP `8020`, metrics `9220`; the e2e stand adds the stub ports 9081 (PayoutService)
and 9082 (ConnectorAdminService), neither published to the host.
Variables: `HTTP_ADDR` (:8020), `METRICS_ADDR` (:9220), `PG_DSN` (required),
`MIGRATIONS_DIR` (./migrations; /app/migrations in the image), `ADMIN_JWT_SECRET`
(required, at least 32 characters; the HS256 key of operator tokens),
`PAYOUT_GRPC_ADDR` (required; the PayoutService address), `CONNECTOR_GRPC_ADDR`
(required; the ConnectorAdminService address), `PAYOUT_TIMEOUT` (2s; 1s - 10s, the
`CBL-003` deadline), `CONNECTOR_TIMEOUT` (5s; 1s - 10s). Invalid values - startup
failure. Dev stand: `admin` tokens are minted by the e2e utility with the shared
dev secret (`dev-admin-jwt-secret-change-me-please`); production secret distribution -
KI-backoffice-02.

## 6. Development rules

- Pipeline: SDD spec in `.specs/` -> contracts -> tests first -> code -> review by 4
  agents (no-context, architecture, with-context, security - blocking) -> AGENTS.md ->
  runs -> linters -> commit `CBL-<epic>.<number>: ...` (the spec in the same commit).
- Unit+integration coverage is at least 80% (currently 85.0%); money scenarios only on
  real PostgreSQL and Kafka (testcontainers); local docker-compose e2e is mandatory.
- Money is int64 in minor units plus decimals only; float is forbidden.
- Every mutating action writes an audit record; `before`/`after` carry whitelisted
  contract DTOs only - never tokens, signing keys, secrets or credentials (the journal
  is immutable and readable by the auditor role).
- The RBAC matrix is the single source of truth; a new route must declare its permission
  (deny by default); tokens are verified with alg pinning (HS256) and `exp` required.
- Comments and docstrings are English; comments explain why; errors are wrapped with
  `%w`; panics in business code are forbidden.

## 7. SLO and metrics

The SLO is defined in task 14.4. Base metrics: latency p95/p99, error rate; the
`/metrics` endpoint is a stub until 0.7. Audit page latency stays in the tens of
milliseconds on the stand; selective-predicate indexes are added only if 14.2 shows the
need (spec section 11).

## 8. Trade-offs

| ID | Decision | Pros | Cons and compensation |
| :-- | :-- | :-- | :-- |
| T-BO1 | Stateless HS256 operator tokens (no operator table, issuer out of band) | no session storage, no directory to sync in 12.1; the verifier is pure | no revocation list, no per-operator disable; secret rotation is an operational act | 32-byte key (RFC 7518), alg pinning, short TTLs; rotation/revocation - KI-backoffice-02 (0.6/ops) |
| T-BO2 | Append-only journal enforced by DB triggers (UPDATE/DELETE/TRUNCATE) | the invariant holds for the service and direct SQL, not by convention | DROP TABLE, DISABLE TRIGGER, session_replication_role stay owner-level acts; no external WORM store | recorded here and in the spec; production WORM/store separation with the 0.6/ops work |
| T-BO3 | Keyset cursor with a canonical-JSON filter fingerprint | pagination is stable under inserts; a cursor cannot silently change the filter set | the fingerprint is a consistency check, not a MAC; the cursor is opaque but forgeable | the cursor grants nothing beyond the caller's own permission scope; key validation (UUID, time, size) keeps forged cursors out of the database |
| T-BO4 | The audit transactional seam lives in storage (`AuditStore.WithTx`, a tx-scoped store wrapped by a fresh writer) and serves same-service flows (12.3); the 12.2 actions live in other services, so their records follow a successful action (`ExistsForResource` compensation, T-BO6) | the usecase layer never imports gorm; the journal record commits with a local business change when one exists | cross-service actions cannot be atomic with their record (the payout/connector DB is elsewhere) | covered by integration tests (rollback discards the record; the 12.2 compensation appends a lost disable record) |
| T-BO5 | `bearerToken` parsing duplicated from `cbl-billing-api` httpmw | the services stay autonomous; the parser is small and unit-tested | a third copy appears with the Gateway (7.5) | extraction into `cbl-kit/ginmw` is a deliberate later task when the third consumer lands |
| T-BO7 | The routing rule converters duplicate between the connector transport and the back-office connectorclient (two directions), and the e2e stand doubles PayoutService/ConnectorAdminService with in-memory stubs | the services and the stand stay autonomous; each side validates its own boundary | two converters to keep in sync with the contract; the stubs simulate semantics (two-operator rule, revision conflicts) rather than the real services | both sides are covered by unit tests (round-trips, error mapping); the stubs follow the 10.5 semantics (approvals only in `approved_by`) |
| T-BO6 | Audit after a successful external action (the 12.2 decisions on PayoutService/ConnectorAdminService cannot share the back-office transaction): an audit failure returns 503 while the action stands; the retry heals - a payout repeat re-runs the decision and re-records; a disable repeat checks the journal and appends only a missing record (the normal repeat stays record-free per the contract); a routing update is recovered by re-applying with a fresh revision | every applied action ends journaled; the frozen contract semantics stay intact | healing depends on an operator retry after a 503; `before` snapshots of routing are best-effort (a stale snapshot cannot occur on the successful path thanks to revision conflicts) | the unit/integration tests cover all three healing paths; the runbook documents the 503 retry |

## 9. Known issues

| ID | Problem | Impact | Plan | Status |
| :-- | :-- | :-- | :-- | :-- |
| KI-backoffice-01 | CI and pre-commit are not configured | linters run manually | task 0.2 | open |
| KI-backoffice-02 | No operator directory and no token issuance/rotation/revocation path: the stand signs tokens with one shared HS256 secret (the e2e mint utility); TTL and clocks are unmanaged | a leaked key mints any operator until the secret is rotated everywhere; no per-operator disable; NTP skew affects `exp` (leeway 0) | production: identity provider or ops-managed issuance, secret via Vault, rotation runbook, TTL policy, NTP monitoring - 0.6/ops; the dev secret never reaches production | open |
| KI-backoffice-03 | The compose service has no healthcheck (distroless image without a shell) | the orchestrator cannot see container degradation, restart only on crash | `-healthcheck` binary mode after 0.2/0.6 (same as global KI-08) | open |
| KI-backoffice-04 | Selective-filter predicates (`operator_id`, `action`) are unindexed on the append-only journal | an auditor can force sequential scans with repeated filters (latency, not integrity) | add `(operator_id, occurred_at DESC)` when 14.2 measurements show the need; rate limiting - 0.6 | open |
| KI-backoffice-05 | A disabled provider cannot be re-enabled through the admin API (no `EnableProvider` RPC; the disabled set wins over rule edits) and `disabled_providers` has no retention | an erroneous disable requires SQL on the connector DB; the table grows | the additive `EnableProvider` RPC with RBAC and audit (contract task), retention with 14.2 - tracked together with KI-connector-09 | open |

## 10. Task journal

| CBL | Date | Status | Spec | Commit |
| :-- | :-- | :-- | :-- | :-- |
| CBL-16.14 | 2026-10-16 | done (English comments and documentation; e2e run instruction `test/e2e/E2E.md` added as a plan, scenarios arrive with CBL-12.x; no code changes) | `.specs/CBL-16.14-conventions.md` | this commit |
| CBL-12.1 | 2026-10-16 | done, review closed (Admin API foundations: HS256 operator tokens (alg-pinned, exp required, key >= 32 bytes), the RBAC matrix and middleware (deny by default, 401/403 via cbl-kit kinds), the append-only `audit_log` (UPDATE/DELETE/TRUNCATE triggers, tx seam) with the keyset reader and `GET /admin/v1/audit-log`; unit+integration 94.8%, e2e `E2E_Backoffice_RBACAudit` PASS; contracts `admin.v1.yaml` 1.0.0 cf-3.5/cf-3.6) | `.specs/CBL-12.1-admin-api-rbac-audit.md` | this commit; cbl-protos 3b67ee2/86d3bf4 (cf-3.5/cf-3.6), cbl-kit 7144d74 |
| CBL-12.2 | 2026-10-16 | done, review closed (payout decisions via the PayoutService client (subject binding, 422 reject reason, 409/404/503 mapping); routing get/put with the revision conflict and provider disable through the ConnectorAdminService client; audit records of every action with the compensation of a lost disable record; strict JSON and size limits; stubs and the e2e scenario `E2E_Backoffice_AdminActions` PASS 24/24; unit+integration 85.0%) | `.specs/CBL-12.2-payout-routing-providers.md` | this commit; contracts `cbl-protos@cf-3.7..cf-3.10`, connector/payout commits |

## 11. Runbook

Authentication and authorization:

1. 401 on every call: the token is missing, malformed (a strict single `Bearer` header is
   required), expired or signed with a wrong key/algorithm; mint a fresh token with
   `go run ./test/e2e/.additionals/mint -sub <id> -roles <roles> -secret <key>`; the
   service never explains the reason (by design) - compare the key with
   `ADMIN_JWT_SECRET`.
2. 403 `permission_denied`: the token is valid but the roles lack the route permission;
   check the RBAC matrix above (`audit:read` for the journal).
3. 400 `validation_failed` on the journal: `limit` outside 1-100, a non-RFC3339 period,
   `period_to` earlier than `period_from`, or an invalid/forged cursor (the cursor is
   bound to its filter set - do not edit query parameters between pages).
4. 503 `service_unavailable` with `Retry-After: 1`: PostgreSQL is down or restarting;
   check `/readyz` and the container logs; the record is never partially written.

Audit journal:

1. Rows missing after an action: same-service flows commit the record with the action
   (T-BO4); cross-service 12.2 actions record after the successful call (T-BO6).
   A 503 on a decision means the record was not written while the action stands:
   repeat the call - a payout repeat re-runs the idempotent decision and re-records;
   a disable repeat appends only the missing record; a routing update is recovered by
   re-applying with the revision from a fresh GET. Check the access log by
   `request_id` and the service error logs.
2. UPDATE/DELETE/TRUNCATE on `audit_log` fails with `audit_log is append-only`: this is
   the invariant; correct records by appending a new action, never by editing history.
3. Growing journal: reads are keyset-paginated; retention/archival is a later
   operational decision (no TTL by design - the journal is evidence).

Admin actions (12.2):

1. 403 `operator_mismatch`: the body `operator_id` differs from the token subject;
   the acting identity is always the token subject.
2. 409 `conflict` on a decision: the payout service refused (vote immutable, already
   decided, approval not required) - the `detail` carries the cause; `state_conflict`
   means a concurrent transition, repeat the call.
3. 409 `revision_conflict` on a routing PUT: another operator changed the rules; GET
   the fresh revision and re-apply.
4. Disable a provider: 204; the repeat is idempotent (no new record); to re-enable -
   SQL on `disabled_providers` until the `EnableProvider` RPC (KI-backoffice-05).
5. 503 with `Retry-After` on any action: the payout/connector service or the journal
   storage is down; the action may already stand - retry is safe (T-BO6).

Operational:

1. Migrations run at startup (`goose_db_version`); forward only; a failed migration
   stops the service before the servers start.
2. Rotating the dev secret: update `ADMIN_JWT_SECRET` in compose (and the umbrella
   stand), restart the service and re-mint tokens; production rotation - KI-backoffice-02.
3. The build context of the image is the services directory (the neighboring `cbl-kit`
   module); a plain `docker build .` from the repository root fails by design.
