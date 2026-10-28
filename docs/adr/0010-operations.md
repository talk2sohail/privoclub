# 0010. Operations: topology, upgrades, backup, observability

- Status: Accepted, 2026-10-28
- Date: 2026-10-28

## Context

The first deployment target is Coolify with Docker Compose, which does not do
rolling updates. Customers run Privo on their own servers, so upgrades and
backups must be safe without our hands on the machine.

## Decision

### Topology

- **v1, single node (Coolify):** gateway, runner and SQLite on one host;
  a lease prevents two writers. Optional Postgres for larger teams, still one
  gateway replica (0004).
- **HA (later):** two or more gateway replicas behind a load balancer, managed
  Postgres, sticky routing of parked calls by `requestState`. Delivered through
  an adapter beyond Compose (for example Kubernetes), only when a customer
  needs it.
- **Privo Cloud:** cells. A cell is a full stack (gateways, Postgres, runners)
  in one region; a small global directory maps org slug to cell; an org lives
  in one cell; moving it is export and import. This also gives data residency.

### Health and shutdown

- `/livez` (process alive) and `/readyz` (database reachable, migrations
  applied, master key loaded, caches warm). Upstream outages never fail
  readiness.
- On SIGTERM: stop accepting, drain for up to 30 seconds, convert held
  approval waits to "pending, retry".

### Limits

- Per-org in-flight limits, per-upstream bulkheads and circuit breakers, a
  global in-flight cap with 503 shedding.
- Rate limits with GCRA in memory; on HA, per-replica buckets approximate the
  global limit. Exact distributed limits (Valkey) only if Cloud needs them.
- Monthly quotas computed from audit asynchronously, never on the call path.

### Upgrades

- Pinned image tags; never `latest`.
- Every release works with the previous schema (0002 expand and contract).
- Upgrade = snapshot, `privo migrate`, restart. Downtime on single-node
  Coolify is seconds; it is documented.
- Supported-version window: the current and previous minor release. Opt-in
  version check that sends only the version.

### Backup and disaster recovery

- SQLite: continuous WAL shipping to S3-compatible storage (Litestream-style,
  built in) or scheduled snapshots; `privo restore`; a documented restore drill.
- Targets: RPO 1 minute with continuous shipping, 24 hours with daily
  snapshots; RTO 30 minutes for a single node.
- The master key is backed up separately from data; Coolify volume backups are
  not relied on.

### Observability

- OpenTelemetry: one span per pipeline stage, trace context from `_meta`
  `traceparent`.
- Metrics: stage latency histograms, audit queue depth and lag, pending
  approvals, upstream errors and breaker state, connection pool entries,
  runner memory. No `org_id` label on metrics in Cloud (cardinality); the org
  appears in traces and logs.
- Structured JSON logs with redaction.

### Compatibility testing

A CI suite runs the gateway against reference clients and upstreams for both
protocol versions, and a manual matrix covers Claude Code, Cursor, Codex,
VS Code, Amp and ChatGPT (authentication, timeouts, list size, elicitation,
list refresh).

## Consequences

- v1 has brief downtime on upgrade; acceptable for the first customers and
  stated honestly. HA is designed, not built.
