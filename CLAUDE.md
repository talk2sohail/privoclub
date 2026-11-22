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
- No feature merges without its section in `docs/todo.md` updated and its
  page in `docs/site/` (the documentation site) written or updated.

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
6. Update the feature's page in `docs/site/` (say the version it arrived in;
   `make docs` must pass) and tick the feature's to-do items.

## Layout

```
cmd/privo/            entry point and CLI (serve, token, backup, restore, config, admin)
internal/gateway/     MCP data plane: routing, pipeline stages inline (ADR 0001), result checks
internal/upstream/    connections to upstream MCP servers, per-person sessions (ADR 0017)
internal/upstreamauth/ OAuth discovery, registration and token requests for upstreams
internal/grants/      each person's upstream grants: seal, refresh, disconnect
internal/control/     admin API, also run in process for the admin app (ADR 0019)
internal/web/         admin website: GitHub sign-in, invite and connect pages, app bridge
internal/githubauth/  GitHub OAuth client for sign-in (ADR 0016)
internal/auth/        principals, roles and permissions, token verification
internal/token/       personal token format
internal/vault/       envelope encryption, key providers (ADR 0003)
internal/keyring/     per-org keys
internal/store/       SQLite store, migrations, backup and restore (ADR 0002)
internal/policy/      rules and evaluation
internal/approval/    approval requests
internal/audit/       audit writer
internal/catalog/     built-in server catalog and team profiles (ADR 0020)
internal/config/      settings registry, environment and file (ADR 0015)
internal/egress/      SSRF-safe outbound client (ADR 0008)
internal/runner/      stdio runner (local, development only; ADR 0007)
internal/server/      HTTP server, root page, health endpoints
internal/health/ internal/safe/ internal/ids/ internal/buildinfo/   small helpers
web/                  admin app source (Preact); web/dist is committed and embedded
site/                 landing page for privo.club (separate Coolify app, Node)
test/acceptance/      black-box suite against the built binary (build tag `acceptance`)
docs/site/            the documentation site's pages (MkDocs; mkdocs.yml at the root)
docs/adr/             architecture decisions
docs/reviews/         independent review reports
docs/todo.md          per-feature to-do tracker
```

Planned, not built yet: `internal/pipeline/` (pluggable stages and hooks, ADR
0001), `internal/protocol/` (version handling, ADR 0004), a Postgres store
(ADR 0002), `deploy/coolify/` (Compose file; the guide is
`docs/site/installation/coolify.md`).
