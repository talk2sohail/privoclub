# The audit trail

Every call and every admin change is recorded before it runs
([ADR 0006](https://github.com/talk2sohail/privoclub/blob/main/docs/adr/0006-audit.md)).

## Tamper evidence

0.16.0, [ADR 0028](https://github.com/talk2sohail/privoclub/blob/main/docs/adr/0028-audit-signing-key-and-checkpoints.md).
Every few seconds Privo seals new records into blocks, each linked to the one
before and signed with a key derived from the master key, which never touches
the database. A changed, deleted or reordered row, or a forged block, is found
by:

- **Activity → Check the record** (or `GET /api/v1/audit/integrity`);
- `privo audit verify` on the host (exit 1 on a problem).

Keep the signed checkpoints outside Privo (`GET /api/v1/audit/checkpoints`,
public keys at `GET /api/v1/audit/keys`) to prove later that nothing was
rewritten.

## Search and export

**Activity** filters by person, server, action, decision and date, loads
more, and exports CSV (`GET /api/v1/audit/export`, at most 10,000 rows,
without recorded arguments).

## Retention and erasure

Those who manage the audit:

- set a retention in days (`PATCH /api/v1/settings/audit` with
  `retention_days`); whole sealed blocks go, and the rest still checks;
- erase a person's recorded arguments (`POST /api/v1/audit/erase`), which
  keeps the record checkable.
