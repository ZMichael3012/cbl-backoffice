# cbl-backoffice

Admin API of the CBL platform for operators: payout approval, provider routing,
discrepancy handling and the append-only operator action journal.

The service role and contracts: `AGENTS.md`. Platform-wide rules: `../../AGENTS.md`,
`../../.specs/CBL-000-system-description.md`, `../../.specs/CBL-003-contracts.md`.

## Commands

```powershell
make build             # build
make test              # unit tests
make test-integration  # integration (testcontainers: PostgreSQL)
make cover             # merged unit+integration coverage
make vet               # go vet (+ integration e2e tags)
make lint              # golangci-lint in Docker
make up                # stand: postgres, backoffice (app profile)
make down
```

E2E: the run is performed by an agent following `test/e2e/E2E.md` (the
`E2E_Backoffice_RBACAudit`, `E2E_Backoffice_AdminActions` and `E2E_Backoffice_Reconciliation` scenarios); artifacts go
to `test/e2e/.runs/<UTC mark>/`, auxiliary files to `test/e2e/.additionals/`.

## Admin API (12.1-12.3)

`/admin/v1` is reached with an operator bearer token: an HS256 JWT (`ADMIN_JWT_SECRET`,
at least 32 characters, `exp` required, algorithm pinned) whose `roles` claim drives the
RBAC matrix (`admin`, `payout_operator`, `routing_manager`, `recon_operator`,
`provider_manager`, `auditor`; deny by default). Routes: `GET /admin/v1/audit-log`
(the append-only action journal: `operator_id`/`action`/`period` filters, keyset
cursor, `limit` up to 100); `POST /payouts/{id}/approve|reject` (the PayoutService
client; the body operator must match the token subject); `GET/PUT /routing-rules`
(the ConnectorAdminService client; the revision-based optimistic concurrency returns
409 on a stale revision), `POST /providers/{id}/disable` (idempotent 204), `GET /reconciliation/discrepancies` (severity/merchant filters, filter-bound keyset cursor) and `POST /reconciliation/discrepancies/{id}/resolve` (one-way open -> resolved, the audited operator decision).

Every successful mutating action writes one `audit_log` record (who, what, before/after,
whitelisted contract DTOs) after the external call (T-BO6: an audit failure answers
503 and the retry heals); the table rejects UPDATE/DELETE/TRUNCATE at the database
level. The `recon.alerts` inbox consumer stores the discrepancies (Kafka group `cbl-backoffice-recon`); the operator UI is 12.4. The
dev stand mints tokens with
`go run ./test/e2e/.additionals/mint -sub op-1 -roles auditor -secret dev-admin-jwt-secret-change-me-please`.

## Environment

`PG_DSN` (required), `ADMIN_JWT_SECRET` (required, >= 32 characters),
`PAYOUT_GRPC_ADDR` and `CONNECTOR_GRPC_ADDR` (required), `PAYOUT_TIMEOUT` (2s,
1s - 10s), `CONNECTOR_TIMEOUT` (5s, 1s - 10s), `HTTP_ADDR` (:8020), `METRICS_ADDR`
(:9220), `MIGRATIONS_DIR` (./migrations; /app/migrations in the image), `KAFKA_BROKERS` (optional; empty disables the consumer) and `BACKOFFICE_RECON_GROUP` (default `cbl-backoffice-recon`). Migrations
are applied at startup (goose, forward only).
