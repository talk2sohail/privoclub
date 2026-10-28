# 0006. Audit delivery and integrity

- Status: Accepted, 2026-10-28
- Date: 2026-10-28

## Context

Buyers rely on the audit trail. Revision 1 queued success events in memory
after the side effect (lost on crash) and chained every row synchronously
(a per-org lock on every insert).

## Decision

### Delivery

- **Intent record**, synchronous, before the upstream call (0001 stage 10):
  one insert. If it fails, the call is denied. Nothing executes unaudited.
- **Completion record**, asynchronous, batched (every 50 ms or 500 rows),
  through a bounded queue with per-org fairness. When full, a request waits at
  most 250 ms, then gets 503 with `Retry-After`. Queue depth and lag are
  metrics with alerts.
- A crash leaves intents without completions, which is detectable and shown.
- Admin actions (policy, toolset, audit settings, keys, members) are audit
  events too.

### Integrity

- Rows are inserted without hashes. A **sealer** per org (one holder, chosen by
  lease) runs every 1 to 5 seconds, assigns `chain_seq`, and hashes batches in
  seal order into `audit_blocks`; each block hash covers the previous block.
- Hash input is the RFC 8785 canonical form of (seq, org, fields, payload
  digest).
- Each block is signed with Ed25519. The signing key is held outside the
  database (key provider, 0003). Signed checkpoints can be exported to customer
  storage (for example an object-lock bucket) or a webhook, so even a database
  administrator cannot rewrite history undetected.
- The tamper-evidence window equals the seal interval; this is documented.

### Privacy

- Recording of arguments and results is **off by default**, switchable per org
  and per server.
- The chain covers a digest of the payload. Payloads are stored separately,
  encrypted under the org's keys, so deletion for GDPR erases payloads without
  breaking the chain.
- Retention per plan. Postgres `audit_events` is partitioned by month and
  expires by dropping partitions, after a signed checkpoint; SQLite deletes in
  batches.

### Access

Search, filters and CSV export in the admin site; the `auditor` role has
read-only access (0011).

## Consequences

- Tamper evidence lags by seconds, in exchange for no per-call lock.
- Fail-closed intent writes make the database a hard dependency on the call
  path; that is deliberate.
