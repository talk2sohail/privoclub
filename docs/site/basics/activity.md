# Viewing activity

**Activity** shows how your team's AI calls went and what changed in Privo:
how many calls there were, how fast, which failed or were refused, and who
did what. Owners, admins and auditors see it.

Activity arrived in 0.10.0.

## Overview

**Activity → Overview** shows one window at a time: **Last 1h**, **Last 24h**,
**Last 7d** or **Last 30d**.

- **Calls**: how many tool calls there were.
- **Failed**: calls that could not complete, such as when the server could
  not be reached or took too long.
- **Tool errors**: calls the server answered with an error.
- **Refused**: calls Privo refused, such as by a deny rule.
- **Median** and **p95**: how long the calls that ran took, for the typical
  call and for the slowest one in twenty. Refused calls are quick, so they
  are left out.

A chart shows the calls over the window. Below it, a table breaks them down
by **Server**, **Tool**, **Member** or **Client**, with the same counts and
times, and how long the server itself took. The time a person takes to
approve a call is left out of these figures.

These statistics are kept for `PRIVO_TELEMETRY_RETENTION_DAYS` days (30 by
default; see the [configuration reference](../installation/configuration.md)),
and nothing leaves your server.

## Tool calls

**Activity → Tool calls** lists every call, newest first: when, which token
or AI client, the server and tool, the rule's decision, and how it ended.
Calls a rule denied are here too, so you see what agents tried, not only
what ran.

Under **Filter and export**, narrow the list by who made the call, the
server, the action, the decision and a date range, then choose **Filter**. Export the list as CSV from the same
place; the recorded arguments are never part of the file.

## Admin changes

**Activity → Admin changes** lists what people changed in Privo: servers
added or edited, tools accepted, rules, roles, tokens and more, with who did
it and when. It filters the same way.

## Log

**Activity → Log** lists what happened to connections, sign-ins and keys, and
to Privo itself: a server that failed to connect and came back, or a
person's key that a server refused. It is the first place to look when calls
to one server start failing.

## Going further

- [Metrics, diagnostics and telemetry](../advanced/observability.md): what the
  figures hold, Prometheus metrics and the telemetry API.
- [The tamper-evident audit trail](../advanced/audit.md): checking that no
  record was rewritten, retention, and recorded arguments.
