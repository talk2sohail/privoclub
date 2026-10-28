# 0001. Request pipeline and extension points

- Status: Accepted (revision 2, after the 2026-10-28 review), 2026-10-28
- Date: 2026-10-28
- Related: 0004 protocol versions, 0005 approvals, 0006 audit, 0008 egress

## Context

Every MCP request from an AI client (Claude Code, Cursor, Amp, Codex,
VS Code, ChatGPT) passes through Privo. The gateway must enforce access, ask a
person before risky actions, protect credentials and record what happened,
with low and predictable latency. Built-in features and future customer
add-ons must fit one model.

Revision 1 let hooks rewrite arguments after approval, ignored prompts and
resources, and assumed protocol sessions, which MCP 2026-07-28 removed. This
revision fixes those.

## Decision

### Principles

1. **Stateless per request.** Every request is authenticated and authorised on
   its own. No authorisation decision depends on a session ID (see 0004).
2. **Arguments are frozen before the decision.** Only transform hooks may
   change arguments, and they run before policy. After the decision, any change
   voids the decision.
3. **Every operation is policed,** not only `tools/call`: `resources/read`,
   `prompts/get` and `completion/complete` pass the same decision and after
   stages.
4. **Fail closed** for security stages; nothing executes unaudited.

### Stages for `tools/call`

| # | Stage | Kind | On failure |
| --- | --- | --- | --- |
| 1 | Authenticate (opaque access token or personal token) | Core | 401 |
| 2 | Rate limit and in-flight limits per token, user, org, upstream | Core | 429 or 503 with `Retry-After` |
| 3 | Resolve public name to (server, upstream tool) via the alias table; tool must be in the caller's toolset | Core | Not found |
| 4 | Tool pin check against the cached per-tool pin (never a live `tools/list`) | Built-in | Deny: "tool changed, awaiting review" |
| 5 | Validate arguments against the pinned `inputSchema` | Core | Invalid params |
| 6 | **Transform hooks** (the only stage allowed to rewrite arguments) | Built-in and extension | Deny |
| 7 | Freeze arguments: `args_digest = sha256(JCS(args))` (RFC 8785) | Core | — |
| 8 | **Decision hooks**: policy, cross-server flow rule, custom. Combined as deny > ask > allow | Built-in and extension | Deny |
| 9 | Approval if any decision is "ask", bound to `args_digest` (see 0005) | Built-in | Deny or pending |
| 10 | Write the audit **intent** record synchronously (see 0006) | Core | Deny; nothing runs unaudited |
| 11 | Inject credentials for this caller and server | Core | "Connect your account" (see 0003) |
| 12 | Call upstream through the egress client (see 0008); compute mirrored headers from the final arguments; default timeout 60 s, per-server override; no automatic retry | Core | Error result |
| 13 | **After hooks**: redaction (type-preserving, re-validated against `outputSchema`), size caps enforced while streaming, URI rewriting | Built-in and extension | Result withheld |
| 14 | Audit **completion** record, written asynchronously in batches | Core | See 0006 |

### Other operations

- `tools/list`, `prompts/list`, `resources/list`, `resources/templates/list`:
  authenticate, filter by toolset, drop unpinned or blocked items, run list
  hooks, return a deterministic order, set `cacheScope: "private"` and a short
  TTL because lists differ per person.
- `resources/read`, `prompts/get`, `completion/complete`: stages 1 to 3, 8 to
  14. Prompt and resource content is treated as untrusted input to the model.
- Upstream notifications and server-to-client requests: see 0004.

### Cross-server data flow rule (built-in decision hook)

Servers and tools are tagged `untrusted_source` (reads outside content: issues,
email, web) and `egress_sink` (can send data out: chat, email, HTTP). If the
same user and client called a source within a configurable window (default 10
minutes), a call to a sink escalates to "ask". This blunts prompt injection
that tries to move data from one server to another.

### Tool annotations

`readOnlyHint`, `destructiveHint` and similar annotations are untrusted. They
may make defaults stricter (destructive or missing `readOnlyHint` means "ask"
under the default preset) and never relax policy unless an admin marks the
server trusted. Admins can override annotations sent to clients.

### Hook interface

```go
const HookAPIVersion = 1

type ListHook interface      { FilterList(ctx context.Context, c *ListCtx, items []Item) ([]Item, error) }
type TransformHook interface { Transform(ctx context.Context, c *Call) (json.RawMessage, error) }
type DecisionHook interface  { Decide(ctx context.Context, c *Call) (Decision, error) } // Allow | Ask | Deny + reason
type AfterHook interface     { After(ctx context.Context, c *Call, r *Result) (*Result, error) }
type Observer interface      { Observe(ctx context.Context, e Event) } // async, cannot change results

type Call struct {
    OrgID, RequestID, TraceID string
    Actor        Actor          // user, groups, roles
    Client       ClientInfo     // name, version, protocol version
    Server       ServerRef      // id, alias, tags, trusted
    Tool         ToolDef        // pinned definition and annotations
    PinHash      string
    Args         json.RawMessage // may contain user-pasted secrets; never Privo-held credentials
    ArgsDigest   string          // set after stage 7
}
```

Built-in features are implemented as these hooks. Hook order is configured,
versioned and written to audit with every decision. Customer webhook hooks are
deferred until the Go interface has been stable for one release; when added,
they carry a versioned envelope, HMAC over timestamp and body with a 5-minute
replay window, size caps, a per-org circuit breaker and explicit per-org opt-in
because they send customer data out.

### Implementation notes

- The Go SDK has no proxy layer. Privo implements the gateway as receiving
  middleware with a raw dispatcher per request, not thousands of `AddTool`
  registrations.
- Cached per request path: compiled policies, toolsets, pins, token lookups,
  unwrapped data keys. Invalidated through Postgres `LISTEN/NOTIFY` (or in
  process on SQLite).

## Consequences

- One explainable order: every decision names the stage and rule.
- Performance targets, measured in CI benchmarks including the synchronous
  intent insert: gateway overhead p50 under 2 ms, p99 under 10 ms, excluding
  the upstream call and approval waits.
- Fail-closed stages make a broken database block work. Readiness checks and
  alerts must catch it fast (see 0010).
