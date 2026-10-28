# 0002. Data model and tenancy

- Status: Accepted (revision 2, after the 2026-10-28 review), 2026-10-28
- Date: 2026-10-28
- Related: 0003 secrets, 0006 audit, 0010 operations

## Context

One codebase runs as a single-node install on a customer's Coolify server and
as Privo Cloud with many organisations. Small installs must need no database
server. One customer must never see another's data.

## Decision

### Engines

- **SQLite** (pure-Go driver) for single-node installs; **Postgres** (pgx) for
  HA installs and Privo Cloud.
- One `Store` interface; sqlc per engine; goose migrations embedded.
- Portable SQL subset only: UUIDv7 stored as text on SQLite and `uuid` on
  Postgres behind one Go type; timestamps as UTC. One shared conformance test
  suite runs against both engines in CI. No Postgres-only features except
  row-level security as defence in depth.

### SQLite discipline

- WAL mode, `synchronous=NORMAL`, `busy_timeout=5000`, one dedicated writer
  connection using `BEGIN IMMEDIATE`, a separate read pool, short reads (exports
  page through results).
- A lease row with a heartbeat: a second process refuses to start, so a
  redeploy overlap cannot run two writers.
- Refuse network file systems.

### Tenancy

- Every table carries `org_id`; every index starts with it; every `Store`
  method takes a typed `OrgID`. A CI check fails any table without `org_id`.
- Postgres row-level security:
  - Each transaction runs `set_config('app.org_id', $1, true)` (transaction
    local; a session-level `SET` would leak across pooled connections), sent in
    the same pgx batch as the first query.
  - Policies use `org_id = (SELECT current_setting('app.org_id', true)::uuid)`;
    a missing value matches no rows.
  - `FORCE ROW LEVEL SECURITY`; the application role neither owns tables nor
    has `BYPASSRLS`; migrations, sealing and retention use separate roles.
- SQLite installs with several orgs rely on code-level isolation only. This is
  documented; Privo Cloud always uses Postgres.
- Large or Managed customers get their own install.

### Core tables

| Table | Holds |
| --- | --- |
| `orgs` | id, slug, name, plan, region |
| `users`, `identities`, `memberships` | people; external identities keyed by issuer and stable subject (see 0011); roles per org |
| `groups`, `group_members` | teams |
| `servers` | alias (`^[a-z][a-z0-9-]{1,15}$`), transport, target, protocol era and negotiated version, capabilities, `stateful`, tags (`untrusted_source`, `egress_sink`), trusted flag, enabled, status |
| `tool_aliases` | (server, upstream name) to public name; routing is by lookup, never string splitting |
| `tool_pins` | per tool: canonical hash of name, description, input and output schema, annotations; scope key for tool lists that vary by user; status (pending, accepted, blocked) |
| `server_manifests` | every manifest seen, for diffs and alerts |
| `toolsets`, `toolset_items`, `toolset_grants` | which tools a user or group sees |
| `credentials`, `org_keys` | encrypted secrets and wrapped data keys (see 0003) |
| `policies` | server, tool pattern, subject, action, conditions, priority, version |
| `approvals` | see 0005 |
| `api_tokens`, `oauth_clients`, `oauth_grants`, `oauth_tokens` | see 0003; all secrets stored hashed |
| `audit_events`, `audit_blocks` | see 0006 |
| `settings`, `leases` | per-org settings; single-writer and sealer leases |

### Public tool names

Public names are `<alias>__<tool>`, normalised to `[A-Za-z0-9_-]` and capped
at **46 characters**, so they stay within client limits after the client adds
its own prefix (Claude Code adds `mcp__<server>__`; Cursor caps server plus
tool name at 60). Over-long names are truncated with a deterministic hash
suffix. Policies and pins are keyed on upstream names, so renaming an alias
does not change security decisions.

### Migrations

- Expand and contract only: each migration works with the previous binary;
  removals ship one release later. No downgrades.
- `privo migrate` runs migrations as a separate step, so long migrations do not
  trip health checks. `CREATE INDEX CONCURRENTLY` runs outside transactions.
- SQLite takes a `VACUUM INTO` snapshot before migrating.

### Growth path

- Postgres `audit_events` is partitioned by month (see 0006).
- SQLite to Postgres moves are a supported `privo export` and `privo import`.
- Valkey is not used in v1. Invalidation and approval wake-ups use Postgres
  `LISTEN/NOTIFY` on a dedicated connection.

## Consequences

- Two engines cost a conformance suite in CI; that buys a zero-dependency
  install for small teams.
- Typed `OrgID`, forced row-level security and the CI check make cross-tenant
  leaks unlikely; every new table must follow the rules.
