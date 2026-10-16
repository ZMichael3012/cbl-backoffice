# E2E cbl-backoffice: admin API, RBAC and audit

Run instruction for the local e2e scenarios of the service. A run is performed by an agent
platform-independently: docker, a command shell and Go utilities from `.additionals/`.
There are no PowerShell run scripts (customer decision of 16.10.2026, `CBL-002` section 7).

Repository: `cbl-backoffice`. Stand: `deploy/docker-compose.yml` (postgres, backoffice in
the `app` profile).

## 1. Scenarios

| Scenario | Checks | Contracts |
| :-- | :-- | :-- |
| `E2E_Backoffice_RBACAudit` | migrations, operator authN (missing/expired/wrong-secret tokens), RBAC (403 without `audit:read`, 200 for auditor/admin), problem+json with request_id, audit-log pagination and filters, append-only immutability against direct SQL | admin.v1.yaml 1.0.0 (`GET /admin/v1/audit-log`), PostgreSQL |

Minimum for a task run: the scenario affected by the task (the full set - before release
runs). The write path of business actions (10.5/12.2) grows the scenario when those routes
land; in 12.1 the writer is covered by integration tests and the scenario seeds rows with
psql to exercise the read API. An idempotent-repeat check applies to mutating routes and
is deferred to 12.2 together with the first mutating endpoint.

## 2. Isolation and parallelism

- Run mark: UTC lowercase `yyyymmddthhmmssz` (example: `20261016t070916z`).
- Artifact directory: `test/e2e/.runs/<mark>/` (create before the start).
- Compose project: `cbl-backoffice-e2e-<mark>`; its network is `<project>_cbl-net`.
- Parallel runs of different services are allowed; parallel runs of this service only with
  different projects and a port override file (host ports: 5432, 8020, 9220).
- All commands run from the repository root unless stated otherwise.

## 3. Common run order

1. `<MARK>` = current UTC time in lowercase `yyyymmddthhmmssz`; create `test/e2e/.runs/<MARK>/`.
2. Stop the previous stand: `docker compose -p cbl-backoffice-e2e-<MARK> -f deploy/docker-compose.yml --profile app down -v`
   (output to `compose-down.log`).
3. Start the stand: `... --profile app up -d --build` (`compose-up.log`); the builder's
   context is the services directory (the neighboring `cbl-kit` module).
4. Wait for `postgres` healthy and `GET http://localhost:8020/readyz` = 200 (migrations are
   applied at startup; `compose-ps.txt`, `prepare.log`).
5. After the scenario: logs of all stand services -
   `... --profile app logs --no-color > compose-logs.txt` (the `--profile app` flag is
   required: without it the backoffice container is not included).
6. `summary.md` by the `.additionals/summary.template.md` template; finish with `down -v`
   (`compose-down.log`) and make sure the run containers are removed.

Common utilities:

- tokens: `go run ./test/e2e/.additionals/mint -sub <id> -roles <role1,role2> -ttl 1h -secret dev-admin-jwt-secret-change-me-please`
  (pass `-secret` explicitly: the shell of an agent run does not export
  `ADMIN_JWT_SECRET`; an expired token: `-ttl=-1m`);
- HTTP: `curl.exe -s -D <headers file> -o <body file> -w "%{http_code}" -H "Authorization: Bearer <token>" http://localhost:8020/admin/v1/audit-log?...`
  (save headers when the scenario checks `WWW-Authenticate`/`X-Request-Id`);
- SQL: `docker compose -p <project> -f deploy/docker-compose.yml exec -T postgres psql -U cbl -d backoffice -c "<SQL>"`
  (for expected failures do not pass `ON_ERROR_STOP` and grep the error text).

## 4. Waits and timeouts

Async checks run in a polling loop up to 120 s (2 s interval): the stand is healthy
usually within seconds after postgres. On a timeout, record the last actual value in
`*-checks.txt` and collect emergency dumps (section 6).

## 5. E2E_Backoffice_RBACAudit

1. Full order (sections 3-4) until `readyz` = 200.
2. Health: `GET http://localhost:8020/healthz` -> 200 `{"status":"ok"}`; readyz body
   contains `"status":"ok"`.
3. Mint tokens: auditor `-sub op-auditor -roles auditor`, payout
   `-sub op-payout -roles payout_operator`, admin `-sub op-admin -roles admin`,
   expired `-sub op-auditor -roles auditor -ttl=-1m`, wrong secret
   `-sub op-auditor -roles auditor -secret wrong-secret-0123456789`.
4. AuthN/RBAC checks (`GET /admin/v1/audit-log`, body into files):
   - no token -> 401, body `code` = `unauthenticated`, `request_id` non-empty,
     `WWW-Authenticate: Bearer realm="admin"`;
   - expired token -> 401; wrong-secret token -> 401;
   - payout token -> 403, `code` = `permission_denied`;
   - auditor token -> 200, body `{"items":[],"next_cursor":""}` (clean stand);
   - admin token -> 200;
   - `GET /admin/v1/unknown` with the admin token -> 404, `code` = `not_found`;
   - `limit=0` with the auditor token -> 400, `code` = `validation_failed`.
5. Seed three journal rows through psql (the write path is integration-covered in 12.1;
   the psql seed exercises the read API against the future actions):
   `INSERT INTO audit_log (id, occurred_at, operator_id, action, resource_type, resource_id, request_id, before, after) VALUES
   ('018f0000-0000-7000-8000-000000000001', '2026-10-15T10:00:00Z', 'op-1', 'payout.approve', 'payout', 'po-1', 'req-1', NULL, '{"status":"approved"}'),
   ('018f0000-0000-7000-8000-000000000002', '2026-10-15T10:01:00Z', 'op-2', 'routing.update', 'routing', '', 'req-2', '{"revision":1}', '{"revision":2}'),
   ('018f0000-0000-7000-8000-000000000003', '2026-10-15T10:02:00Z', 'op-1', 'payout.reject', 'payout', 'po-2', 'req-3', NULL, NULL);`
6. Read API with the auditor token:
   - `limit=2` -> 2 items, newest first (`...0003`, `...0002`), `next_cursor` non-empty;
   - the second page with `next_cursor` -> 1 item (`...0001`), `next_cursor` empty;
   - `operator_id=op-1` -> 2 items; `action=payout.approve` -> 1 item;
   - `period_from=2026-10-15T10:01:00Z` -> 2 items (inclusive bounds);
   - the cursor of the `limit=2` page together with `operator_id=op-2` -> 400
     (the cursor is bound to its filter set).
7. Immutability against direct SQL (expected failures, grep the error text):
   - `UPDATE audit_log SET action = 'tampered'` -> error contains `append-only`;
   - `DELETE FROM audit_log` -> error contains `append-only`;
   - `SELECT count(*)` still 3.
8. Checks go to `rbac-audit-checks.txt`; command outputs to `rbac-audit.log`.

## 6. Mandatory run artifacts

- `summary.md` (template `.additionals/summary.template.md`; PASS/FAIL verdict, steps,
  key values, artifact list, time);
- `compose-ps.txt`, `compose-up.log`, `compose-down.log`, `compose-logs.txt`;
- `prepare.log`;
- `<scenario>.log` and `<scenario>-checks.txt` for every executed scenario; scenario
  helpers (`body.json`, `headers.txt`, `seed.sql`) stay in the run directory;
- `coverage.txt` (`make cover` output of the run);
- on failure: service log fragments with `request_id`, the audit_log snapshot
  (`SELECT * FROM audit_log ORDER BY occurred_at`), the container list.
- an aborted run keeps the artifacts collected so far and the note in `summary.md`
  naming the successor run.

## 7. Auxiliary files (.additionals)

- `mint/` - the token minting utility (HS256, the shared dev secret);
- `summary.template.md` - the `summary.md` template;
- `inventory.md` - the list of auxiliary files and their purpose.

## 8. Cleanup

`docker compose -p cbl-backoffice-e2e-<MARK> -f deploy/docker-compose.yml --profile app down -v`
at the end; `.runs/` is not committed; a repeated run must start from a clean stand.
