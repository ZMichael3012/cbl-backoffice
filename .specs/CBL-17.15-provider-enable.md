# CBL-17.15 (back-office increment) - Enable provider route and audit

Task: CBL-17.15 in `CBL-004` (E17), back-office part of "provider reporting and
administration"; depends on 17.10 and the connector `EnableProvider` increment.
Repository: `cbl-backoffice`. Status: in progress.

## 1. Problem and scope

The OpenAPI contract (cf-4) defines `POST /admin/v1/providers/{id}/enable`, but the
back-office exposes only the disable route, so an operator cannot revert a manual disable
without SQL. In scope: the connector client call, the admin action with one audit record
for the first enable, the HTTP route with the existing provider-administration permission,
and unit/integration tests. Out of scope: a new permission (the capability is provider
availability management, the existing `providers:disable` permission is reused and the
spec records it), contract changes (the route exists in cf-4), and connector changes (the
connector increment under the same task).

## 2. Architecture

- `connectorclient.Client.EnableProvider(ctx, provider) (already bool, err)`: calls the
  `EnableProvider` RPC and maps `AlreadyEnabled`.
- `usecase.RoutingAdmin` gains `EnableProvider(ctx, provider) (bool, error)`;
  `AdminActions.EnableProvider(ctx, operatorID, requestID, provider, reason)` audits the
  first enable as `provider.enable` with the whitelisted DTO `{enabled:true, reason}`; a
  repeat is idempotent and leaves the journal untouched; a repeat whose first record was
  lost is compensated (mirror of the disable rule, CBL-12.2 FR-4).
- `POST /admin/v1/providers/{id}/enable` accepts the optional `{reason}` body, validates it
  like the disable reason, answers 204 and maps errors with the existing action taxonomy.

## 3. Test plan

| ID | Case | Expectation |
| :-- | :-- | :-- |
| B1 | first enable | client called, one `provider.enable` record with enabled/reason |
| B2 | repeated enable | no new record |
| B3 | repeat with a lost first record | compensated record appended |
| B4 | connector error | error passes through, no record |
| B5 | endpoint 204 with and without a body | 204, one audit row |
| B6 | invalid reason / unknown provider | 400 / 404 mappings |
| B7 | client mapping | AlreadyEnabled passthrough, NotFound mapping |

Integration: the actions integration test extends the disable scenario with an enable and
checks the audit rows (newest first) and the idempotent repeat.
E2E: no new scenario; the back-office service e2e and the global GE-5 remain green (the
route is additive).

## 4. Red evidence

Recorded 20.10.2026: the usecase and transport tests failed to compile because
`RoutingAdmin.EnableProvider` and the fake method did not exist; the integration
flow expected the enable route before it was registered.

## 5. Results and review closure (20.10.2026)

- Unit B1 - B7 and the integration flow green (disable -> enable, four audit rows, the
  idempotent repeats add none).
- Gates: full unit suite green, back-office integration (PostgreSQL) green;
  golangci-lint 0, go vet clean, em dash check green.
- Review pass 1 (no-context): the route contract (204, optional reason, idempotence)
  matches admin.v1.yaml; the audit action is `provider.enable`.
- Review pass 2 (architecture): client -> AdminActions -> connector mirrors the disable
  path; the interface change is implemented by both fakes; no contract change.
- Review pass 3 (with-context, blocking): the enable must be audited exactly once and
  a lost first record must be compensated. Closed: the `ExistsForResource` compensation
  is covered by B3/B5; the integration test asserts four rows.
- Review pass 4 (security): the route reuses `providers:disable` (provider availability
  management; no new permission leak), validates the reason and relies on the connector
  for the known-provider check; no new input reaches SQL.
