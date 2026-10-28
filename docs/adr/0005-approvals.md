# 0005. Approvals

- Status: Accepted, 2026-10-28
- Date: 2026-10-28

## Context

"Ask a person before risky actions" is Privo's headline feature. Approvals can
take minutes, must survive restarts, must approve exactly what runs, and must
not be approvable by the AI agent itself.

## Decision

### Binding

An approval is a durable row bound to (org, requester, client id, server,
upstream tool, `args_digest`). It is **single use**: the call that executes
consumes it atomically
(`UPDATE ... SET consumed_at = now() WHERE id = $1 AND status = 'approved' AND consumed_at IS NULL`).
Any change to arguments after approval voids it. Default lifetime 10 minutes.

### Flow

1. The call blocks for up to 25 seconds (configurable), sending progress
   notifications when the client supplied a progress token, and wakes on the
   decision (Postgres `LISTEN/NOTIFY`, or in process on SQLite).
2. If undecided, the client gets:
   - 2026-07-28 clients: input-required with a URL elicitation to the approval
     page and an encrypted `requestState`;
   - others: an error result with the approval id, a view link and "retry the
     same call once approved".
3. The retry finds the approval by `requestState`, or by (requester, tool,
   args digest) within its lifetime, and consumes it.
4. Client timeouts or retries never create a second approval for the same
   binding; they reuse the pending one.

### Who decides, and how

- The link given to the client is **view-only**. Deciding requires an
  authenticated admin-site session, a POST with a CSRF token, and step-up
  re-authentication for high-risk tools. An agent with a browser or fetch tool
  cannot approve its own call.
- Approver differs from requester by default. Small teams may allow
  self-approval per org; it still requires the person's own browser session.
- Slack: requests verified with Slack's signing secret and a 5-minute timestamp
  window; the Slack user is mapped to a Privo user through an explicit,
  verified link, never by email or display name. High-risk approvals in Slack
  say "approve in Privo" instead of offering a button.
- Approvers see the **full** arguments; the audit stores a redacted copy.

### Fatigue controls

- Scoped grants: "allow this tool for this person for 1 hour", recorded as a
  policy with expiry.
- Batching of similar pending requests; per-org notification rate limits.
- An approver dashboard flagging people who approve everything.

### States

`pending` → `approved` | `denied` | `expired` | `withdrawn` → (`approved` only)
`consumed`. Every transition is an audit event.

## Consequences

- Clients without input-required support see an error and a retry; the
  experience is acceptable and works everywhere today.
- Durable approvals add a table and notifier workers, and remove every failure
  mode tied to an open connection.
