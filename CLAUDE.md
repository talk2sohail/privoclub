# Privo: working agreement

Privo is the team gateway to AI work and the MCP world: one endpoint per
organisation in front of every MCP server, with policies, human approvals,
encrypted credentials and an audit trail. Built for startups, agencies and
mid-size companies, to enterprise scale and quality.

## Quality bar (non-negotiable)

- Enterprise quality from the first release: every feature ships with tests,
  docs, audit coverage and a threat note.
- Security defaults fail closed. Secrets are never logged, returned by the API,
  or passed to hooks.
- Every table carries `org_id`; every store method takes a typed `OrgID`.
- No feature merges without its section in `docs/todo.md` updated.

## Stack

- Go (one static binary: `cmd/privo`), official MCP Go SDK
  (`github.com/modelcontextprotocol/go-sdk`).
- SQLite (pure Go) on a single node, Postgres for HA and Privo Cloud; sqlc and
  goose. No Valkey in v1.
- MCP 2026-07-28 primary, 2025-11-25 compatible; stateless gateway (ADR 0004).
- Admin UI: Preact + Vite + TypeScript app in `web/`, build output committed
  and embedded with `go:embed`; it calls the admin API through the session
  (ADR 0019). Sign-in and OAuth pages stay server-rendered.
- First deployment target: Coolify (Docker Compose). Other platforms through
  packaged artifacts and the versioned runner protocol (ADR 0007).

## Workflow for every feature

1. Spec: add or update the feature's section in `docs/todo.md` (goal,
   acceptance criteria, tasks).
2. Decide: write an ADR in `docs/adr/` for anything hard to reverse.
3. Test first: name the tests for each task before writing code.
4. Build in small changes; keep `go vet`, `go test -race ./...` and the
   end-to-end MCP client test green.
5. Review: a separate security review pass on every change that touches auth,
   secrets, tenancy or the request pipeline.
6. Update docs and tick the feature's to-do items.

## Layout (planned)

```
cmd/privo/          entry point
internal/gateway/   MCP data plane: transports, pipeline, routing
internal/pipeline/  stages and hooks (ADR 0001)
internal/control/   admin API
internal/auth/      OIDC, OAuth server, tokens, upstream broker (ADR 0003)
internal/vault/     envelope encryption, key providers (ADR 0003)
internal/store/     Store interface, sqlite and postgres (ADR 0002)
internal/policy/    rules and evaluation
internal/approval/  approval requests and notifiers
internal/audit/     events and hash chain
internal/runner/    runner protocol, client and server (ADR 0007)
internal/egress/    SSRF-safe outbound client (ADR 0008)
internal/protocol/  version handling, notifications (ADR 0004)
deploy/coolify/     Compose file and install guide
web/                admin UI
docs/adr/           architecture decisions
docs/reviews/       independent review reports
docs/todo.md        per-feature to-do tracker
```
