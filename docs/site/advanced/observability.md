# Metrics, diagnostics and telemetry

What sits behind the **Activity** page, and how to send the same figures to
your own monitoring. Reading the page itself is in
[Viewing activity](../basics/activity.md).

Activity statistics, the diagnostic log and Prometheus metrics arrived in
0.10.0
([ADR 0024](https://github.com/talk2sohail/privoclub/blob/main/docs/adr/0024-call-statistics-and-diagnostic-log.md)).

## What the statistics hold

Privo keeps call statistics apart from the audit trail: one row per call with
its phases and sizes, never its arguments, its result or an error's text.

- They are best effort. A full queue drops a row and counts it, rather than
  slow a call down.
- Counts cover every call. Times come from the most recent 100,000 calls in
  a window, and only from calls that ran: refusals are quick and would hide a
  slow server. The time a person takes to approve is left out.
- They are kept for `PRIVO_TELEMETRY_RETENTION_DAYS` days, 30 by default and
  up to 400, and pruned every hour; `0` records none. Nothing leaves your
  server.
- Owners, admins and auditors see them, like the audit.

## The breakdown table

Below the chart on **Activity → Overview**, the table by server, tool, member
or client shows, for each row:

| Column | What it is |
| --- | --- |
| Median, p95, p99 | The call's time, from arrival to answer |
| Connect p95 | The time spent getting the server's session, including a sign-in refresh |
| Server p95 | The time the server itself took |
| Result | The average size of what the server sent back, which is what the model reads |

## The diagnostic log

**Activity → Log** lists what happened to connections, sign-ins and keys, the
audit, and the process: a server that failed to connect and came back, a
grant the provider refused, a person's key a server refused. Each line has a
time, a level, what it is about, the server and the member. The same event
within a minute is one line with a count.

## The telemetry API

The same figures, for scripts: `GET /api/v1/telemetry/summary`, `timeseries`,
`calls`, `logs` and `storage`, for owners, admins and auditors. See
[Admin API and script tokens](api-and-automation.md).

## Prometheus metrics

For Prometheus and Grafana, set `PRIVO_METRICS_ADDR` to an address such as
`127.0.0.1:9464`, or one on an internal network. `/metrics` there gives:

| Metric | What it counts |
| --- | --- |
| `privo_mcp_calls_total{server,method,outcome}` | Calls, by server, method and outcome |
| `privo_mcp_call_duration_seconds` | A histogram of call times, by server and method |
| `privo_upstream_up{server}` | Whether each server is connected |
| `privo_audit_events_lost_total` | Audit events that could not be written |
| `privo_telemetry_dropped_total` | Statistics rows dropped by a full queue |
| `privo_build_info` | The running version |

!!! warning
    `/metrics` has no sign-in, so it must never share the public port. Keep
    it on localhost or an internal network.

In a container, to scrape it from another container on the same Docker
network, set `PRIVO_METRICS_ADDR=0.0.0.0:9464` and do not publish that port
or give it a domain. Every container on that network can then read the
metrics: the server aliases, call counts by outcome, whether each server is
up, and the exact version. Put Prometheus and Privo on a network of their own
if other services share it.

The settings are in the [configuration reference](../installation/configuration.md).
