# CBL-12.2. Payout decisions, routing rules and provider disable

| Field | Value |
| :-- | :-- |
| Epic | E12, `Back-office` |
| Repositories | `cbl-backoffice` (main part), `cbl-protos` (cf-3.7/cf-3.8/cf-3.9), `cbl-connector` (ConnectorAdminService server), `cbl-payout` (Payout.approved_by) |
| Size | M |
| Wave and parallel group | W4-ADMIN |
| Depends on | CBL-12.1 (authN, RBAC, audit), CBL-10.5 (ApprovePayout semantics), CBL-3.4 (routing rules source of truth) |
| Contract version | `openapi/admin.v1.yaml` 1.0.0 (`cbl-protos@cf-3.5..cf-3.10`), `ConnectorAdminService` (cf-3.7), `Payout.approved_by` (cf-3.9) |
| Status | done, review closed |

## 1. Context and goal

The frozen admin API business routes (`CBL-003` section 3) become operational:
payout approval of large payouts (the votes themselves live in `cbl-payout`,
10.5), provider routing rules and the manual provider disable. KI-connector-08
(the rules cannot be changed in production) is closed by the ConnectorAdminService
server (cbl-connector, 12.2) and the back-office client with RBAC and audit.
The payout decision response needs the approval progress: the additive
`Payout.approved_by` (cf-3.9) carries it from the vote journal.

## 2. Boundaries

In scope: the back-office clients of PayoutService and ConnectorAdminService;
the five frozen routes (`POST /payouts/{id}/approve|reject`,
`GET/PUT /routing-rules`, `POST /providers/{id}/disable`); the RBAC permissions
of 12.1 (`payouts:approve`, `routing:read`, `routing:manage`, `providers:disable`);
the audit records of these actions with whitelisted DTOs; the operator/audit
binding (the body operator must match the token subject); tests and the e2e
scenario `E2E_Backoffice_AdminActions` with stub payout and connector servers;
AGENTS/README.

Out of scope: discrepancy handling and the `recon.alerts` consumer (12.3);
the operator UI (12.4); provider enable (an additive contract follow-up);
automatic failover (3.4 open question); mTLS (0.6); rate limiting (0.6).

## 3. Functional requirements

FR-1. Configuration: `PAYOUT_GRPC_ADDR` and `CONNECTOR_GRPC_ADDR` are required
(fail-fast); `PAYOUT_TIMEOUT` (2 s default, 1 - 10 s) bounds one ApprovePayout call
(`CBL-003` deadline 2 s), `CONNECTOR_TIMEOUT` (5 s default, 1 - 10 s) bounds the
admin calls. Plaintext gRPC inside the trusted network (mTLS - 0.6).

FR-2. Payout decisions (`POST /payouts/{id}/approve|reject`, permission
`payouts:approve`):

- body `{operator_id, reason}` (strict JSON, unknown fields rejected): `operator_id`
  is required and must equal the token subject, otherwise 403 `operator_mismatch`
  (the audit is recorded against the token subject);
- reject requires a non-empty reason: missing/empty - 422 `validation_failed`;
  a reason over 255 characters or with control characters - 400 `validation_failed`;
- the call `PayoutService.ApprovePayout{payout_id, operator_id=subject, approve,
  reason}` with `PAYOUT_TIMEOUT`; mapping: NotFound - 404 `payout_not_found`;
  FailedPrecondition - 409 (the cause text stays in `detail`: vote locked, already
  decided, approval not required); Aborted - 409 `state_conflict`; InvalidArgument -
  400 `validation_failed`; Unavailable/DeadlineExceeded - 503 `service_unavailable`
  with `Retry-After: 1`;
- 200 with `PayoutAdmin{id, status, approved_by}` from the response;
- on success one audit record `payout.approve`/`payout.reject` (resource
  `payout`/`payout_id`, `after` - the whitelisted DTO `{id, status, approved_by}`).
  The action lives in another service: the record cannot share its transaction;
  an audit write failure surfaces 503 `service_unavailable` with `Retry-After` (the failure is retryable; the vote itself is idempotent),
  recorded in section 5.

FR-3. Routing rules (`GET /routing-rules` - `routing:read`; `PUT /routing-rules` -
`routing:manage`):

- GET returns `{revision, items[]}` mapped from the connector configuration
  (shape per the finalized contract: operation, networks/assets arrays, priority,
  weight, minor-unit bounds, enabled);
- PUT body: `revision >= 0`, 1 - 1000 items; per item provider required (visible
  ASCII, up to 64), priority >= 0, weight >= 1, operation in `{"", invoice, payout}`,
  bounds >= 0, arrays of strings; violations - 400 `validation_failed` without
  calling the connector;
- the connector call `PutRoutingRules(revision, items)` with `CONNECTOR_TIMEOUT`:
  a revision mismatch (FailedPrecondition) - 409 `revision_conflict`; an invalid
  payload from the connector - 400 `validation_failed`; unavailable - 503 with
  `Retry-After: 1`;
- on success 200 with the stored `{revision, items}` and one audit record
  `routing.update` with `before`/`after` `{revision, items}` (the before snapshot is
  read first; a concurrent change between read and write surfaces as a revision
  conflict, so the guarded path never writes a misleading record).

FR-4. Provider disable (`POST /providers/{id}/disable`, permission
`providers:disable`): optional body `{reason}` (up to 255, no control characters,
otherwise 400); an unknown provider - 404 `provider_not_found`; the first disable -
204 and one audit record `provider.disable` (resource `provider`/`id`, `after`
`{disabled: true, reason}`); a repeated disable - 204 without a new record
(idempotent, per the frozen contract).

FR-5. Clients (`internal/payoutclient`, `internal/connectorclient`): thin gRPC
wrappers with per-call deadlines, mapping transport codes to the platform error
kinds (KindNotFound/KindFailedPrecondition/KindInvalidArgument/KindUnavailable);
internal details never leak to responses.

FR-6. Audit: every successful mutating action writes exactly one record through the
12.1 writer (`request_id` from the access log, whitelisted DTOs only - never tokens
or secrets); reads are not audited.

FR-7. Tests: unit - clients' code mapping, usecase (audit records, idempotent
disable, conflict paths), handlers (403 subject mismatch, 422 missing reject reason,
400 validation, 409 mapping, strict JSON); integration (testcontainers PostgreSQL,
fake ports) - the full HTTP flow through the real router: decisions, routing
before/after audit, disable idempotence, the audit journal readable through
`GET /audit-log`; e2e - `E2E_Backoffice_AdminActions` on the compose stand with
stub PayoutService and ConnectorAdminService servers (the stand stays autonomous,
no cross-service dependencies).

FR-8. Documentation: AGENTS (boundaries, contracts, RBAC matrix note, trade-offs,
runbook, journal), README, E2E.md scenario.

## 4. Contracts

`openapi/admin.v1.yaml` 1.0.0: the five routes as frozen (the RoutingRule shape is
finalized by cf-3.8 to the source-of-truth form); `ConnectorAdminService` (cf-3.7);
`PayoutService.ApprovePayout` and the additive `Payout.approved_by` (cf-3.9). No
Kafka or gRPC additions beyond these.

## 5. Decisions

- The audit record follows the successful external action (no distributed
  transaction with payout/connector): an audit failure returns 503 while the action
  stands. Compensation (review): a payout repeat re-runs the decision and writes the
  record again (idempotent vote, heals automatically); a provider-disable repeat
  checks the journal (`ExistsForResource`) and appends the missing record - the
  normal repeat (record present) stays 204 without a new record per the contract; a
  routing update whose record was lost is recovered by the operator re-applying the
  configuration with a fresh revision (the successful replacement writes both
  snapshots). T-BO6, runbook.
- The routing `before` snapshot is best-effort: revision conflicts guard the write,
  so `after` is always the stored configuration; a stale `before` is impossible on
  the successful path (the write would have failed).
- `204` for the repeated provider disable without a new audit record - exactly the
  frozen contract wording ("idempotent: 204 without a new audit record"); the only
  record ever written for a repeat is the compensation of a lost one.
- The operator identity always comes from the token subject; the body mirrors it
  for explicit operator intent and is validated, never trusted.
- Audit snapshots use the contract DTO shapes (snake_case, empty arrays as `[]`,
  never null); `approved_by` is an array in responses and in the journal alike.

## 6. Edge cases

Repeated vote by the same operator (409 via the payout service); a vote after the
decision (409 `already decided`); a payout below the threshold (409 `approval not
required`); a forged body operator (403); a missing reject reason (422); a reason
with control characters (400); an unknown JSON field (400); request bodies over
1 MiB, network/asset lists over 64 entries (400 - matching the connector limits);
revision conflicts under concurrent operators (409, both keep working from a fresh
GET); an empty rule set is rejected by the frozen contract (`minItems: 1`; clearing
stays a connector-internal capability, not an admin action); a disable of a provider
known only from rules or from the disabled set (works, stays idempotent even after
the rules were replaced); a repeated disable (204, no record; a lost record is
compensated on the retry); connector/payout timeouts (503 + Retry-After); a
connector receive-limit breach (`ResourceExhausted` -> 400, not 500); audit storage
failure (503 + Retry-After, the action stands, the retry is safe/healing); the audit
journal stays readable through 12.1.

## 7. Test plan

| Level | Scenarios |
| :-- | :-- |
| Unit | client code mapping; usecase: audit records for decisions/routing/disable, idempotent disable without a record, conflict passthrough, before/after snapshotting; handlers: 401/403/404/409/422/503 mapping, strict JSON, validation of routing payloads |
| Integration | PostgreSQL: the HTTP flow over the real router and database with fake ports: approve/reject rows in `audit_log` (whitelisted DTOs), routing update audit with before/after, idempotent disable, `GET /audit-log` shows the records |
| E2E | `E2E_Backoffice_AdminActions`: approve by two operators, reject with and without reason, subject mismatch, routing get/put with a revision conflict, disable idempotence and unknown provider, the audit journal and RBAC checks |
| Load | not in this scope |

Target unit+integration coverage: at least 80% (12.1 measured 94.8%).

## 8. Work plan and commit

Clients -> usecase -> handlers and router wiring -> main/compose (stub services) ->
tests -> e2e scenario and run -> shared 12.2 review round (cbl-protos,
cbl-connector, cbl-payout, cbl-backoffice) -> AGENTS/README -> commits per
repository; the back-office commit is
`CBL-12.2: payout decisions, routing rules and provider disable`.

## 9. Reviews

Four independent passes (16.10.2026, shared for the four repositories) and closure
verification; the final validation run: `test/e2e/.runs/20261016t150717z/` PASS 24/24
(earlier attempts superseded: the image build missed the cbl-protos copy, inline JSON
bodies were mangled by the shell, and the audit expectations initially omitted the
idempotent-repeat record). Coverage: backoffice 85.0%, connector 91.6%, payout 82.9%.

| Role | Key findings | Closure |
| :-- | :-- | :-- |
| No-context | MAJOR: `approved_by` serialized as null; the payout vote list included a vetoing operator; the audit journal stored PascalCase Go field names with nulls; E2E.md numbers contradicted the run (2/2 vs 6/3/3) | arrays normalized in responses and the journal; `ListApprovals` filters `approve = true` (+ veto test); the journal uses contract-shaped DTOs (`routingRuleAudit`); E2E.md updated to 6/3/3 with the repeat rationale |
| Architecture | MEDIUM: a repeated disable returned 404 after the rules were replaced; the disable audit gap could not heal; routing retry semantics unstated | the disabled set joined the known-provider union (connector) and the cache is invalidated on every attempt; the `ExistsForResource` compensation appended a lost record; the recovery path (re-apply with a fresh revision) documented in section 5 and the runbook |
| With-context | MAJOR: docs pending (expected at review time); the payout AGENTS journal row was corrupted; E2E.md/artifact mismatches; minor doc drift | AGENTS/README written for both repos; the payout row repaired (c044c7b); E2E.md and specs aligned with the artifacts; the body-file rule recorded after the harness incident |
| Security (blocking) | MAJOR: `approved_by` included rejecters; the provider-disable audit hole | both closed as above; `ResourceExhausted` -> 400, body (1 MiB) and list (64) caps added; the unauthenticated internal contour is the recorded platform posture (mTLS - 0.6, KI-connector-03 extended) |

Accepted intentionally: the duplicate rule converters in the connector transport and
the back-office client (service autonomy, recorded as T-BO7); stub helper duplication
(test scaffold); the audit-after-action model with the documented compensations.

## 10. Open decisions

- `PayoutAdmin.amount`/`currency` of the frozen contract stay empty until a client
  needs them (the decision response carries the state and the votes; the amount is
  available through GetPayout if the UI needs it - 12.4).
- Provider enable is an additive contract follow-up (no `EnableProvider` RPC yet):
  until it lands a disabled provider cannot be re-enabled through the admin API -
  neither by editing rules (the disabled set is separate and wins) nor by any other
  route; SQL on `disabled_providers` is the only way (KI-connector-09, KI-backoffice-05).
- The admin contour keeps the platform posture: no authN inside the trusted network
  (mTLS - 0.6); the back-office is the only client. The connector admin RPCs mutate
  the money routing, so port 9050 must not leave the trusted zone (KI-connector-03).
