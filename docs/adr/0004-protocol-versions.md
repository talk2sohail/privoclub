# 0004. Protocol versions, statelessness and notifications

- Status: Accepted, 2026-10-28
- Date: 2026-10-28

## Context

MCP 2026-07-28 removed protocol sessions, the `initialize` handshake, SSE
resumability and server-initiated requests. It added `server/discover`,
`subscriptions/listen`, multi round-trip requests (MRTR: the server answers
"input required" with an opaque `requestState` and the client retries) and
moved tasks to an extension. Clients and upstream servers will speak both the
new version and 2025-11-25 for at least a year. The Go SDK supports 2026-07-28
from v1.7 when its Streamable HTTP handler runs stateless.

## Decision

### Versions

- **2026-07-28 is primary; 2025-11-25 is supported for compatibility** on both
  sides (clients and upstreams). Older versions are rejected with a clear
  error. Support for a version ends no sooner than the spec's deprecation
  window, announced in release notes.
- Client transport: Streamable HTTP only. The deprecated HTTP+SSE transport is
  kept only as an upstream client.

### Stateless gateway

- Modern clients: no session state at all.
- 2025-11-25 clients: Privo issues a **self-contained session ID**, an
  encrypted blob holding principal, org, negotiated version, client
  capabilities and expiry. Any replica can decode it; nothing is stored.
  Authorisation still happens on every request.
- No SSE replay. A broken response stream loses that response; the client
  re-issues the request, and audit marks a re-issue (same args digest within
  60 seconds).

### Server-to-client requests from upstreams

| Upstream asks for | Privo does |
| --- | --- |
| Elicitation (form or URL) | Forwards to the client after redaction: as MRTR input-required to modern clients, as `elicitation/create` to 2025-11-25 clients. Rejected if not tied to an in-flight request |
| Sampling | Not advertised; refused (deprecated in 2026-07-28) |
| Roots | Answered from the server's configuration; client laptop paths mean nothing in the runner |

An upstream call waiting on a client answer is **parked on the replica that
started it**. `requestState` is encrypted and bound to principal, args digest,
replica and expiry; it is not proof of single use (approvals add their own
single-use check, 0005).

### Notifications

- Privo sends `tools/list_changed` (and the 2026 equivalents through
  `subscriptions/listen`) when an upstream list changes, a pin is decided, or a
  toolset or policy changes.
- Some clients do not refresh reliably, so a call to a removed or blocked tool
  returns a clear error asking the user to refresh.
- Upstream `resources/updated` subscriptions are reference-counted per (org,
  server, URI) and fanned out only to subscribers whose toolset includes them.
- Upstream log and progress messages are redacted before reaching clients.
- Cancellation is translated per upstream: `notifications/cancelled` for stdio
  and 2025 servers, closing the stream for 2026 servers. Cancelling during an
  approval wait withdraws the approval.

### Upstream connections

- One shared HTTP transport per upstream host (HTTP/2, connection caps). The
  per-(org, server, credential owner) object is a cheap, lazily created client
  with an LRU cache and idle eviction after about 5 minutes. No standing
  upstream streams.
- Servers marked `stateful` (and every stdio server by default) get one
  connection or process per (org, server, end user), so one user never sees
  another's browser tab or cursor.
- Mirrored HTTP headers (`Mcp-Name`, `Mcp-Param-*`) are computed from the final
  arguments and upstream names; `x-mcp-header` is stripped from schemas shown
  to clients.

### v1 topology consequence

Parked calls and in-memory caches mean **v1 runs one gateway replica**.
Multi-replica routing of parked calls (sticky on `requestState`) arrives with
the HA topology in 0010.

## Consequences

- Dual-era support is real work on both sides, tested by a client and server
  compatibility suite in CI (0010).
- The tasks extension is not built in v1; approvals use hold-then-retry (0005).
  It is added behind a capability check once major clients support it.
