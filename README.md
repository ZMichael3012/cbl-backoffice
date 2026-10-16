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
`E2E_Backoffice_RBACAudit` scenario); artifacts go to `test/e2e/.runs/<UTC mark>/`,
auxiliary files to `test/e2e/.additionals/`.

## Admin API (12.1)

`/admin/v1` is reached with an operator bearer token: an HS256 JWT (`ADMIN_JWT_SECRET`,
at least 32 characters, `exp` required, algorithm pinned) whose `roles` claim drives the
RBAC matrix (`admin`, `payout_operator`, `routing_manager`, `recon_operator`,
`provider_manager`, `auditor`; deny by default). `GET /admin/v1/audit-log` serves the
append-only action journal (`operator_id`/`action`/`period` filters, keyset cursor,
`limit` up to 100). The dev stand mints tokens with
`go run ./test/e2e/.additionals/mint -sub op-1 -roles auditor -secret dev-admin-jwt-secret-change-me-please`.

Every mutating action (12.2+) writes one `audit_log` record (who, what, before/after)
in the same transaction; the table rejects UPDATE/DELETE/TRUNCATE at the database level.
The remaining frozen routes (payout decisions, routing rules, providers, discrepancies)
arrive with tasks 12.2 and 12.3; the operator UI is 12.4.

## Environment

`PG_DSN` (required), `ADMIN_JWT_SECRET` (required, >= 32 characters), `HTTP_ADDR`
(:8020), `METRICS_ADDR` (:9220), `MIGRATIONS_DIR` (./migrations; /app/migrations in the
image). Migrations are applied at startup (goose, forward only).
