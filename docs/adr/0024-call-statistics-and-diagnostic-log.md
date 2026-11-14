# 0024. Call statistics, a diagnostic log and Prometheus metrics, apart from the audit

- Status: Accepted, 2026-11-14 (the owner's direction of 2026-11-11, "the
  audit reworked with telemetry like Meridian"; built in 0.10.0 under his
  instruction of 2026-11-14 to follow industry practice and ask only what is
  critical)
- Date: 2026-11-14
- Related: 0006 (audit delivery and integrity), 0010 (observability), 0001
  (pipeline stages), 0002 (storage)

## Context

The audit (ADR 0006) answers *who did what*: an intent before every call, a
completion after it, kept, never dropped, later sealed and exportable (F12).
It does not answer *how it went*: which servers are slow, where the time goes
(policy, the audit write, opening the upstream session, the upstream itself),
how often calls fail and why, how large arguments and results are, or what
happened to a server's connection at 3 a.m. Admins asked for that view; the
owner pointed at Meridian (a local bridge between coding clients and model
providers), which keeps one row per request with phase timings, summaries over
a window, a time series, a diagnostic log with categories, bounded retention
and a dashboard, plus Prometheus `/metrics`.

The two records have different guarantees. The audit must not lose a row and
is kept as long as the organisation wants; statistics are many, cheap and
useful only for a while. Putting timings into the audit would make the audit
bigger and slower and tie its retention to theirs; making statistics as
strict as the audit would make a slow disk refuse calls for the sake of a
chart.

## Decision

1. **Two records, one call id.** Each call that reaches the decision stage
   writes one *call statistic* row next to its audit records, under the same
   call id. The audit is unchanged and keeps its guarantees. Statistics are
   best effort: a bounded queue (10,000 rows), written in batches; when it is
   full a row is dropped and counted (`privo_telemetry_dropped_total`, and the
   Overview shows the count). A call never waits for, or fails because of,
   its statistic.
2. **What a row holds.** Time (Unix milliseconds, so ranges and buckets are
   integer comparisons), member (user id), token id, client, server, method
   (`tools/call`, `prompts/get`, `resources/read`), tool or item name,
   outcome (`ok`, `tool_error`, `error`, `refused`), an error class (below),
   the time of each phase in microseconds (decide: rules and approval;
   audit: the intent write; connect: getting the upstream session, including
   a grant refresh; upstream: the call itself; total), and the size in bytes
   of the arguments and of the result relayed to the client. **Never** the
   arguments, the result, a token, a key or an error text: the error class is
   one of a fixed list (`denied`, `pending_approval`, `denied_by_approver`,
   `too_many_pending`, `approvals_unavailable`, `audit_unavailable`,
   `not_connected`, `no_account`, `key_refused`, `unavailable`, `timeout`,
   `upstream_error`, `malformed`, `transport`, `internal`).
3. **Names follow OpenTelemetry.** Method and tool map to the MCP semantic
   conventions (`mcp.method.name`, `gen_ai.tool.name`) and durations are
   kept in a unit the conventions convert from, so an OpenTelemetry export
   (F16) maps rows without renaming.
4. **Reading.** The admin API (permission `audit.read`) offers a summary over
   a window grouped by server, tool, member, client or method (calls, errors,
   tool errors, refusals, p50/p95/p99/min/max/average of the total and p50/p95
   of each phase, average and p95 sizes); a time series (bucket at least one
   minute, at most 1,000 buckets, window at most 31 days); the list of calls
   with filters; the diagnostic log; and what storage holds. Aggregates are
   computed in the process from at most 100,000 rows per request; when more
   rows fall in the window the answer says `truncated`. Latency percentiles
   cover calls that ran (refusals are fast and would hide slow servers);
   counts cover all.
5. **Diagnostic log.** A second best-effort table holds operational events
   with a level (`info`, `warn`, `error`), a category (`upstream`, `grant`,
   `gateway`, `audit`, `lifecycle`), the server and member when there is one,
   and a fixed message with credentials removed (the same redaction as the
   server status). The same event repeated within a minute is written once
   with a repeat count. Sources: upstream connect failure, disconnection and
   recovery; a grant the authorization server refused (the person must
   connect again) and failed refreshes; a person's API key refused; audit
   events lost; start of the process.
6. **Retention.** `PRIVO_TELEMETRY_RETENTION_DAYS`, default 30, from 1 to
   400; `0` records nothing and the pages say so. A clean-up deletes older
   rows every hour in small batches. Nothing leaves the server.
7. **Prometheus metrics, off by default.** `PRIVO_METRICS_ADDR` (such as
   `127.0.0.1:9464`) opens a second listener that serves `/metrics` in the
   Prometheus text format: calls by server, method and outcome; a duration
   histogram by server and method; upstream connection state by server;
   audit events lost; statistics dropped; build information. It has no
   authentication, so it is a separate address an operator keeps private
   (a Docker network, localhost), never the public port. No organisation
   label: on one node there is one organisation, and Privo Cloud will need
   its own answer (F16). No tool or member labels, so the number of series
   stays bounded by the number of servers.
8. **The Activity page** gains an Overview (window, totals, a chart of calls
   and errors over time, the breakdown table) and a Log tab next to the
   existing tool calls and admin changes.

## Consequences

- An admin sees in one place where calls are slow and why they fail, by
  server, tool, member and client, without reading logs.
- The audit stays small and strict; statistics can be pruned or lost without
  touching it.
- Prometheus and Grafana users get standard metrics without exporting the
  database; OpenTelemetry traces stay with F16.
- Grouping by team is left out: a member can be in several teams, so a call
  would count more than once. It can come later from current membership.
- Cost: one queued row per call and one batched insert every second; the
  result size costs one more JSON encoding of the result into a counter (no
  copy kept).

## Alternatives considered

- **Timings in the audit rows.** Rejected: the guarantees and the retention
  differ (see Context).
- **Only Prometheus.** Rejected: most teams running a single Privo have no
  Prometheus, and per-member or per-tool views would explode its series.
- **OpenTelemetry SDK now.** Deferred to F16: it adds a large dependency and
  a collector to run; the names chosen here map onto it.
