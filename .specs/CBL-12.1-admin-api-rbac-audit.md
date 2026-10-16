# CBL-12.1. Admin API foundations: operator auth, RBAC and audit log

| Field | Value |
| :-- | :-- |
| Epic | E12, `Back-office` |
| Repositories | `cbl-backoffice` (main part), `cbl-protos` (admin.v1.yaml finalization, `cf-3.5`), `cbl-kit` (401/403 error kinds) |
| Size | M |
| Wave and parallel group | W4-ADMIN |
| Depends on | CF-3 (Admin API v1 composition frozen), `CBL-1.5` (ginmw), `CBL-1.4` (grpcmw kinds) |
| Contract version | `openapi/admin.v1.yaml` 1.0.0 (`cbl-protos@cf-3.5`, `cf-3.6`: the 503 response); no `.proto` changes |
| Status | done, review closed |

## 1. Context and goal

`CBL-000`: the operator perimeter is the Back-office `/admin/v1` with RBAC and audit
(`CBL-003` section 3). CF-3 froze the route composition (`openapi/admin.v1.yaml`,
version 1.0.0-draft, "finalized by task 12.1") and stated: "Authentication is a bearer
token of the operator; RBAC and audit are on the cbl-backoffice side (12.1): every action
writes an audit log record (who, what, before/after)". Task 12.1 delivers those
foundations; the business routes land in 12.2 (payout approval, routing, providers) and
12.3 (discrepancies).

## 2. Boundaries

In scope (cbl-backoffice): fail-fast configuration; the operator bearer-token
authentication (HS256 JWT); the RBAC role-permission matrix and middleware over the
`/admin/v1` group; the append-only `audit_log` (migration, trigger immutability, writer
with transaction joining, keyset reader); `GET /admin/v1/audit-log`; the service
foundation (`/healthz`, `/readyz`, `/metrics` placeholder, problem+json everywhere,
request-id/access-log/recovery from `cbl-kit`, graceful shutdown); migrations at
startup (goose, `storagex`); the e2e scenario `E2E_Backoffice_RBACAudit` with a token
minting utility; AGENTS/README.

In scope (cbl-protos): admin.v1.yaml finalization 1.0.0 - the additive `GET /audit-log`
route and the `AuditRecord` schema; tag `cf-3.5`.

In scope (cbl-kit): the additive error kinds `KindUnauthenticated`/`KindPermissionDenied`
(gRPC codes and HTTP 401/403 mapping in `ginmw.AbortFromError`).

Out of scope: the business endpoints of 12.2/12.3 (approve/reject, routing-rules,
providers disable, discrepancies and the `recon.alerts` consumer); the operator UI
(12.4); operator directory, token issuance, rotation and revocation (dev stand: the
mint utility and a shared secret; production - 0.6/ops, KI-backoffice-02); rate
limiting (0.6); mTLS and perimeter hardening (0.6); metrics and dashboards (0.7/14.4);
Redis (not needed in 12.1).

## 3. Functional requirements

FR-1. Configuration (fail-fast): `HTTP_ADDR` (:8020), `METRICS_ADDR` (:9220), `PG_DSN`
(required), `MIGRATIONS_DIR` (./migrations; /app/migrations in the image),
`ADMIN_JWT_SECRET` (required, at least 32 characters - RFC 7518 requires 256 bits for
HMAC-SHA256; the single constant `auth.MinSecretLength` is reused by the config check).
Invalid or missing values - startup failure with a clear error; the secret never appears
in logs.

FR-2. Operator authentication (`internal/auth`, `transport/httpmw.Authn`):

- exactly one `Authorization` header, a strict `Bearer <token>` scheme (case-insensitive
  scheme per RFC 7235, no spaces inside the token), otherwise 401;
- the token is a JWT signed with HS256 (`admin_jwt_secret`); only HS256 is accepted
  (alg pinning: `none`/RS/ES/wrong signing key are rejected); `exp` is required and
  validated; `sub` must be a non-empty operator identifier; `roles` is an optional
  string array, every entry must be a known role name (an unknown role is a token
  error, strict by design);
- a valid token puts `Operator{Subject, Roles}` into the request context;
- rejected tokens: 401 problem+json `unauthenticated` with `WWW-Authenticate: Bearer
  realm="admin"`; the response never explains the failure reason.

FR-3. RBAC (`internal/domain` matrix, `transport/httpmw.Require`):

| Permission | admin | payout_operator | routing_manager | recon_operator | provider_manager | auditor |
| :-- | :-- | :-- | :-- | :-- | :-- | :-- |
| `payouts:approve` | x | x | | | | |
| `routing:read` | x | | x | | | |
| `routing:manage` | x | | x | | | |
| `reconciliation:read` | x | | | x | | x |
| `reconciliation:resolve` | x | | | x | | |
| `providers:disable` | x | | | | x | |
| `audit:read` | x | | | | | x |

Roles are the union of permissions; `admin` grants everything; an authenticated operator
without the route permission gets 403 problem+json `permission_denied` (no details).
Deny by default: any future route must declare its permission explicitly.

FR-4. Audit log storage (`migrations/00001_audit_log.sql`):

```sql
CREATE TABLE audit_log (
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
CREATE INDEX audit_log_occurred_at_id_idx ON audit_log (occurred_at DESC, id DESC);
```

append-only is enforced by a trigger (`BEFORE UPDATE OR DELETE` raises an exception):
neither the service nor direct SQL can modify or delete records; `id` is UUIDv7 (time
ordered), `occurred_at` is UTC. The migration is goose, forward only.

FR-5. Audit read API `GET /admin/v1/audit-log` (permission `audit:read`):

- filters: `operator_id` and `action` (exact), `period_from`/`period_to` (RFC 3339,
  inclusive on `occurred_at`, `period_to < period_from` - 400);
- `limit` 1 - 100 (default 20), `cursor` - opaque `base64url(JSON)` with the keys
  `v` (version), `t`/`i` (last `occurred_at`/`id`), `f` (filter fingerprint: a short
  hash of the canonical JSON filter set); a tampered, malformed, oversized (over
  512 bytes), trailing-bytes or filter-mismatched cursor, or a position id that is not a
  valid UUID - 400 `validation_failed` without reaching the database;
- ordering newest first `(occurred_at DESC, id DESC)`, `LIMIT limit+1`, `next_cursor`
  from the last returned row, empty at the end;
- response: `{"items":[AuditRecord...],"next_cursor":"..."}`; `before`/`after` are
  embedded JSON objects (null when absent); `occurred_at` is RFC 3339 UTC with
  milliseconds;
- invalid parameters - 400 `validation_failed`; a storage failure - 503
  `service_unavailable` with `Retry-After: 1`.

FR-6. Audit writer (`internal/usecase`): `Record(ctx, AuditEntry)` - the entry carries
the operator id, the request id, the action, the resource and the `before`/`after` state
snapshots; the writer marshals the snapshots, fills `id` (UUIDv7) and `occurred_at` (UTC
now) and inserts one row. `request_id` is passed explicitly by the caller (12.2 handlers
take it from `ginmw.RequestIDFrom`): the usecase layer stays free of gin context
coupling. The transactional seam lives in storage:
`storage.AuditStore.WithTx(tx)` returns a tx-scoped store which is wrapped by a new
writer (`usecase.NewAuditWriter(store.WithTx(tx))`), so the journal record commits
atomically with the business change and the usecase never imports gorm. Marshaling or
storage errors are returned wrapped, never swallowed.

FR-7. Service foundation: `ginmw.RequestID/AccessLog/Recovery` on every route;
`/healthz` 200; `/readyz` pings PostgreSQL (3 s timeout) - 503 `unavailable` on
failure; `/metrics` placeholder on :9220 (0.7); unknown routes and methods - 404
problem+json `not_found`; the probe endpoints keep the plain `{"status":...}` shape
(probe convention, not problem+json); JSON body limits and server timeouts as in
`cbl-reporting` (15 s read/write); the service name and version (ldflags
`-X main.version`) are logged at startup; graceful shutdown of both servers on
SIGINT/SIGTERM; migrations applied before the servers start.

FR-8. Tests: unit (config, token verification table, RBAC matrix, cursor codec,
handlers over fakes); integration (testcontainers PostgreSQL: migrations, trigger
immutability against UPDATE/DELETE, insert/list filters/pagination/order, the full HTTP
flow through the real router with a real database including the write path and
problem+json shapes); e2e `E2E_Backoffice_RBACAudit` (compose stand: postgres +
backoffice; the mint utility from `.additionals/` signs tokens; curl checks for
healthz/readyz, 401 scenarios, 403/200 RBAC, seeded rows pagination, immutability
against direct SQL via psql); coverage ≥ 80% of `internal/`.

## 4. Contracts

- REST: `openapi/admin.v1.yaml` 1.0.0 (`cbl-protos@cf-3.5`): the additive `GET
  /audit-log` route and `AuditRecord`; all other routes unchanged (implemented later).
- No gRPC/Kafka changes in 12.1; `recon.alerts` consumption - 12.3.
- `cbl-kit` error kinds: `KindUnauthenticated` -> gRPC `Unauthenticated`, HTTP 401
  `unauthenticated`; `KindPermissionDenied` -> gRPC `PermissionDenied`, HTTP 403
  `permission_denied`.

## 5. Decisions

Stateless HS256 JWT (no session storage, no operator table in 12.1) with a key of at
least 32 bytes (RFC 7518): the token issuer is out of band (dev: the mint utility with
the shared compose secret; prod: the identity provider/ops - KI-backoffice-02). Keyset
cursor with a filter fingerprint (canonical JSON hash) instead of offset: stable under
inserts, a cursor cannot silently change the filter set (the same approach as 11.4).
Audit immutability is a database trigger covering UPDATE, DELETE and TRUNCATE: the
strongest boundary available without an external WORM store; DROP TABLE, DISABLE TRIGGER
and `session_replication_role` remain owner-level acts outside the dev threat model
(recorded in AGENTS trade-offs). The writer joins the business transaction to keep
"action and record" atomic (12.2+). The audit read route is added to the frozen
composition by 12.1 itself, exactly as CF-3 prescribes ("finalized by task 12.1"); the
other routes stay unimplemented until 12.2/12.3.

## 6. Edge cases

- Empty journal: 200 with `items: []` and an empty `next_cursor`.
- Last page: the `limit+1`-th row is not returned; `next_cursor` empty.
- Cursor: malformed base64/JSON, wrong version, wrong key set, filter mismatch, over
  512 bytes - all 400 without a database call; `occurred_at` at page boundaries with
  equal timestamps does not skip or repeat rows (id is the tiebreaker).
- Token: multiple `Authorization` headers, empty token, spaces inside, `Bearer` with
  different case, expired exactly at `exp`, missing `exp`, missing `sub`, empty roles,
  duplicate roles, unknown role - all 401 (except non-empty roles semantics: a token
  without roles authenticates but every permission check fails with 403).
- Period bounds inclusive; the milliseconds precision of the response matches the
  storage; `period_to` earlier than `period_from` - 400.
- Database unavailable: `/readyz` 503; the audit read - 503 `service_unavailable`
  with `Retry-After: 1`; audit writes fail the enclosing operation (no silent loss).
- The triggers fire on UPDATE/DELETE/TRUNCATE for every role, including the service
  user and direct SQL (verified by the integration test and the e2e psql check); DROP
  TABLE, DISABLE TRIGGER and session_replication_role stay owner-level acts outside the
  dev threat model (trade-off, production WORM - 0.6).
- `before`/`after` are arbitrary JSON objects (null for creates/deletes); the 12.2
  writers must pass whitelisted contract DTOs only - never tokens, signing keys,
  secrets or credentials (the table is immutable and readable by the auditor role).
  No size limit in 12.1 (actions are operator decisions, bounded by the contracts of
  12.2/12.3).

## 7. Non-functional requirements

Security: token and secret never logged (access log carries only the request id and
status); 401/403 responses carry no reason details; JWT algorithm pinning; deny by
default RBAC; audit append-only in the database. Latency: audit page < 50 ms on the
stand. Concurrency: inserts are independent; the reader is page-scoped. Coverage:
unit+integration at least 80% (measured 94.8%). No new libraries beyond the platform
set (`gin`, `golang-jwt/v5` - sanctioned by the root `AGENTS.md` section 2.1,
`gorm`+pgx, goose via `storagex`, `zap`, `cbl-kit`); float is not used anywhere.

## 8. Test plan

| Level | Scenarios |
| :-- | :-- |
| Unit | config validation (required vars, secret length, defaults); token table (valid, expired, wrong secret, wrong alg, missing sub, unknown role, duplicate roles, empty roles); RBAC matrix (admin all, unions, auditor/recon pairs, deny by default); cursor codec (round-trip, tampering, version, fingerprint mismatch, size bound); handlers over fakes (400/401/403/503, problem+json, request_id, NoRoute) |
| Integration | testcontainers PostgreSQL: migrations apply; trigger blocks UPDATE/DELETE; insert + list (filters, period inclusivity, order, pagination by pages without gaps/repeats); HTTP flow over the real router with a real DB: minted tokens, 401/403/200, seeded rows, write path, problem+json |
| E2E local | `E2E_Backoffice_RBACAudit` (compose: postgres + backoffice; mint utility; curl + psql checks) |
| E2E global | GE-5 not affected (12.3); no end-to-end flow change |
| Load | not in this scope (14.2) |

## 9. Work plan and commits

Tests first (unit tables) -> `internal/config`, `internal/domain`,
`internal/auth`, `internal/transport/httpmw`, `internal/usecase`,
`internal/storage`, `internal/transport`, `cmd/backoffice` -> migration + Dockerfile
(migrations in the image) + compose + `.env.example` -> integration and e2e (scenario
+ mint utility) -> 4 reviews -> AGENTS/README -> battery -> commits:
`CBL-12.1: admin.v1.yaml finalization: audit-log route` (`cbl-protos`, tag `cf-3.5`),
`CBL-12.1: unauthenticated and permission_denied error kinds` (`cbl-kit`),
`CBL-12.1: Admin API foundations: operator auth, RBAC and audit log` (`cbl-backoffice`).
The umbrella `CBL-004` row and the `CBL-003` section 3 audit-log bullet are updated
with the umbrella (committed on instruction).

## 10. Reviews

Four independent passes (16.10.2026) and closure verification; the validation run after
the fixes: `test/e2e/.runs/20261016t133751z/` PASS 25/25 (two earlier aborted harness
attempts are documented as superseded).

| Role | Key findings | Closure |
| :-- | :-- | :-- |
| No-context | MAJOR: a crafted cursor with a non-UUID id reached the database and returned 503 instead of 400; MINOR: fingerprint separator ambiguity, FR-6 signature drift, missing startup version log, non-idempotent CREATE TRIGGER, no WithTx test, nil states not asserted as SQL NULL, the contract lacked 503; NITs: UUIDv7 version not asserted, trailing cursor bytes accepted, gin debug mode, probe shape | cursor id parsed as UUID before the store; canonical JSON fingerprint; spec FR-6 aligned to `AuditEntry` and the storage tx seam; version logged; DROP TRIGGER IF EXISTS + TRUNCATE trigger; WithTx commit/rollback test; raw SQL NULL assertion; admin.v1.yaml 503 (`cf-3.6`); UUIDv7 assertion; trailing bytes rejected; spec records the probe shape and gin mode fix |
| Architecture | MAJOR: contract missing the 503 response; AGENTS updates pending; MINOR: cursor id hardening, FR-6/WithTx wording, CBL-002 citation, cover.out not ignored, no compose healthcheck (KI-08), append-only bypass surface | 503 added to admin.v1.yaml with a tag; AGENTS complete (trade-offs, KI-02, runbook); spec aligned; citation fixed; `.gitignore` + `cover.out`; KI-08 referenced; DROP/disable residual recorded as a trade-off with the prod WORM plan |
| With-context | MAJOR: `gofmt` failure; AGENTS/README pending; MINOR: `.gitignore` cover.out, stale duplicate doc.go comments, duplicated testcontainers helper and `bearerToken`, E2E.md mint example without `-secret` (the root cause of the aborted run), section refs, artifact hygiene; NITs: dead test field, README wording, aborted-run notes | gofmt applied; AGENTS/README written; `.gitignore` fixed; scaffold doc.go files removed (authoritative comments live in the package files); `internal/testsupport` extracted; `bearerToken` duplication recorded as a trade-off (kit extraction after 7.5); E2E.md mint example and refs fixed, artifacts listed; aborted-run notes made per-run |
| Security (blocking) | no BLOCKER/MAJOR on the change (umbrella stand wiring flagged: build context + secret - fixed with the umbrella edits); MINOR: 16-byte minimum below RFC 7518 (32 bytes, single constant), KI-backoffice-02 dangling, append-only wording overclaimed, cursor id + fingerprint, filter predicates unindexed, DTO rule for 12.2; NITs: cause not logged, cover.out, `.dockerignore`, lint pin | secret >= 32 bytes via `auth.MinSecretLength` (config reuses it) and the dev secret lengthened; KI-backoffice-02 added with a concrete plan; wording fixed (UPDATE/DELETE/TRUNCATE; DROP residual); cursor hardening; index decision recorded (KI-reporting-style follow-up - `(operator_id, occurred_at)` when 14.2 shows need); DTO rule recorded in section 6/11; `services/.dockerignore` added with the umbrella; lint pin stays under KI-07 |

Accepted intentionally: stateless tokens without a revocation list (KI-backoffice-02);
auditor holding `reconciliation:read` (a read-only audit role); the explicit
`request_id` parameter of the writer (no gin in usecase); probe endpoints without
problem+json; trigger bypasses at the DDL level.

## 11. Open decisions

- Roles are a closed set in code; a new role is an additive change with a spec (the
  matrix is the single source of truth; the OpenAPI does not enumerate roles).
- The audit page is newest-first only; filtering by resource is a later additive
  extension if 12.4 needs it.
- `GET /audit-log` is not itself audited (reads are not actions per `CBL-003`).
- Token TTL/rotation policy, issuer/distribution and revocation are out of the stand
  scope (KI-backoffice-02); the dev mint utility accepts `-sub`, `-roles`, `-ttl`,
  `-secret`.
- The filter fingerprint hashes the canonical JSON filter set (not a separator join), so
  field values cannot collide through separators; the fingerprint is a consistency
  check, not a MAC (the cursor grants no access beyond one's own permission scope).
- The audit read route is not itself audited (reads are not actions per `CBL-003`);
  an audit-export action of 12.4, if it appears, writes its own record.
- Index decision: only `(occurred_at DESC, id DESC)` exists in 12.1; selective
  predicates (`operator_id`, `action`) stay unindexed until measurements (14.2) show the
  need - an additive migration then.
- 12.2 writers pass whitelisted DTOs into `before`/`after` (section 6); a redaction
  test lands with the first business action.
