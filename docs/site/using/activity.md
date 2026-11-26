# Activity and metrics

0.10.0, [ADR 0024](https://github.com/talk2sohail/privoclub/blob/main/docs/adr/0024-call-statistics-and-diagnostic-log.md).

## Overview

**Activity → Overview** shows, for the last hour, day, week or month:

- how many calls there were, how many failed, were tool errors or were
  refused;
- the median and p95 time, and a chart over the window;
- a table by server, tool, member or client with p50/p95/p99, the time spent
  getting the server's session (including a sign-in refresh), the time the
  server itself took, and the average size of what it sent back (what the
  model reads).

## Log

**Log** lists what happened to connections, sign-ins and keys, the audit and
the process: a server that failed to connect and came back, a grant the
provider refused, a person's key the server refused. The same event within a
minute is one line with a count.

**Tool calls** and **Admin changes** are [the audit trail](audit.md).

## What is kept

These statistics are kept apart from the audit: one row per call with its
phases and sizes, never its arguments, result or error text.

- They are best effort: a full queue drops a row and counts it rather than
  slow a call. Counts cover every call; times come from the most recent
  100,000 calls in a window. The time a person takes to approve is kept out
  of latency.
- They are kept `PRIVO_TELEMETRY_RETENTION_DAYS` days (30 by default; `0`
  turns them off). Nothing leaves the server.
- Owners, admins and auditors see them, like the audit.

The same figures come from the admin API under `/api/v1/telemetry/`
(`summary`, `timeseries`, `calls`, `logs`, `storage`).

## Prometheus metrics

For Prometheus and Grafana, set `PRIVO_METRICS_ADDR=127.0.0.1:9464` (or an
address on an internal network). `/metrics` there gives:

- `privo_mcp_calls_total{server,method,outcome}`
- `privo_mcp_call_duration_seconds` (a histogram by server and method)
- `privo_upstream_up{server}`
- `privo_upstream_breaker_state{server}` (`0` closed, `1` open, `2` half-open)
- `privo_audit_events_lost_total`
- `privo_telemetry_dropped_total`
- `privo_build_info`

!!! warning
    `/metrics` has no sign-in, so it never shares the public port. Keep it on
    localhost or an internal network.
