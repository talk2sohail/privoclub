# 0032. Cross-server data-flow rule (`untrusted_source` to `egress_sink`)

- Status: Proposed (pending owner sign-off)
- Date: 2026-11-25
- Related: 0001 (request pipeline, cross-server data flow rule), 0005 (approvals), 0009 (threat model), 0021 (tokens belong to members), 0022 (AI clients sign in with OAuth), 0027 (rules and grants), F09 C

## Context

ADR 0001 planned a built-in cross-server data-flow rule in the decision stage:
when a caller reads content from an `untrusted_source` (for example issues,
email, CRM records, or web pages), a later call by the same caller to an
`egress_sink` (for example Slack, outbound email, or an external webhook) within
a short window escalates to `ask` so a human sees the risk before data leaves.

Prompt injection arrives in untrusted upstream data (`tools/call` results and
`resources/read` contents) and tries to steer the model into calling another
tool that exfiltrates that data. Static per-tool rules cannot express "allow
sending Slack messages normally, but ask if the model just read untrusted
external input."

Several choices must be fixed before building:

1. How a caller's recent untrusted reads are keyed and isolated across tenants,
   users, tokens, and AI clients.
2. How the time window is configured and bounded.
3. How tracker memory is bounded without allowing an attacker or busy tenant to
   silently flush taint out of memory.
4. Which operations record taint (`untrusted_source`) and which operations check
   taint (`egress_sink`), and how escalation interacts with `deny` rules and
   F10 scoped grants.
5. Where the threat boundary lies so operators understand what this rule catches
   and what it does not.

## Decision

1. **Tagging servers and tools in storage.**
   - Table `flow_tags` stores `(org_id, server_id, tool_name, tag, updated_at)`
     with primary key `(org_id, server_id, tool_name, tag)`.
   - `tag` is a closed enum: `untrusted_source` or `egress_sink`.
   - `tool_name == ""` tags the entire server (every tool, prompt, and
     resource it serves); a non-empty `tool_name` tags one specific item by its
     upstream name or resource URI.
   - Deleting a server deletes its `flow_tags` rows in the same transaction.
   - Tags are read via `GET /api/v1/servers/{alias}/flow-tags` and replaced via
     `PUT /api/v1/servers/{alias}/flow-tags` (policy/server managers only;
     audited as `server.flow_tags:<alias>`).

2. **Caller tracker key.**
   - Taint is keyed strictly by `(org_id, caller_id, client)` where:
     - `caller_id` is `"user:" + UserID` when `UserID != ""`, else
       `"token:" + TokenID`;
     - `client` is `Principal.Client` (the OAuth client name when signed in via
       ADR 0022, or `""` for a personal access token).
   - Different organisations, different members, different standalone tokens,
     and different OAuth clients never share or overwrite each other's taint.
   - If a call arrives at an `egress_sink` without a `caller_id` (neither
     `UserID` nor `TokenID`), it fails closed as tainted.

3. **Configurable, bounded window.**
   - Per-org setting `policy.flow_window` (default `10m`) controls how long a
     source read remains active.
   - Valid values are durations in `[10s, 24h]` (checked when saved via
     `PATCH /api/v1/settings/policy`). An unset or corrupt setting falls back
     to `10m`, never to `0`.

4. **In-memory bounded tracker with fail-closed overflow.**
   - Recent source reads live only in an in-memory `policy.FlowTracker` on the
     gateway and are never written to disk or exposed over an API.
   - The tracker enforces hard bounds: `maxTrackedCallers = 10_000` globally
     and `maxPerOrgTracked = 2_048` per organisation.
   - Before evicting active entries, entries older than `24h` (`MaxFlowWindow`)
     are pruned because they are expired under every valid window.
   - If inserting a new caller still exceeds `maxPerOrgTracked` for an org or
     `maxTrackedCallers` globally, the oldest entry is evicted and that
     evicted entry's org gets an overflow taint timestamp (`now`). For the
     active `flow_window`, any `egress_sink` check in that overflowed org
     **fails closed** (treated as tainted) instead of silently dropping taint.
     Other organisations are unaffected.

5. **Source and sink semantics and policy precedence.**
   - **`untrusted_source` reads:** When a `tools/call` or `resources/read`
     targets a server or item tagged `untrusted_source` and passes policy and
     audit intent so the call starts upstream, the gateway records
     `(server_alias, item_name, now)` in the tracker. Calls refused before
     starting upstream (`deny`, `pending_approval`, `not_connected`, invalid
     arguments) do not record taint. `prompts/get` does not record taint.
   - **`egress_sink` calls:** When a `tools/call`, `prompts/get`, or
     `resources/read` targets a server or item tagged `egress_sink`, the
     gateway evaluates policy rules first, then checks `FlowTracker`:
     - `deny` rules (or org default `deny`) still win (`deny > ask > allow`).
     - Any `allow` outcome — including an explicit `allow` rule, an F10
       scoped grant (`grant_of != ""`), or the org default `allow` — escalates
       to `ask` with reason
       `"cross-server flow: read from <source> <age> ago"` (so approvers and
       audit logs record why approval was requested).
     - For `prompts/get` and `resources/read`, existing F09 behavior fails
       closed on `ask` as `denied`.

## Threat boundary

This rule is a **heuristic tripwire that escalates to human approval (`ask`)**,
not a formal information-flow control or cryptographic taint proof:

- **What it stops:** An AI client session that reads untrusted external data
  (`untrusted_source`) and shortly afterwards tries to invoke a tagged
  outbound action (`egress_sink`) must stop for human approval (showing the
  destination tool, its arguments, and which untrusted source triggered the
  escalation). Even an existing F10 scoped grant on the sink cannot waive it.
- **What it does not prove or prevent:**
  - It tracks metadata (`who` called `what` `when`), not data values inside
    model context or tool arguments.
  - Exfiltration through an untagged tool, a single server that reads and leaks
    without tags, or across separate clients/users (for example a user pasting
    output into another app) is outside what the gateway observes.
  - Because state is process-local memory (`FlowTracker`), a gateway restart
    clears active source timestamps (and multi-instance HA deployments will need
    shared or sticky state in a later ADR).

## Consequences

- Admins can tag servers or individual tools on each server's page or via the
  admin API without writing complex rule matrices.
- Approvers see clear context (`cross-server flow: read from <server>/<item> <age> ago`)
  on `/admin/approvals`.
- No sensitive upstream arguments or results are stored in the tracker; only
  `(serverAlias, toolName, timestamp)`.
