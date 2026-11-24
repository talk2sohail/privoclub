# Privo checkpoint: 2026-11-01, about 04:30 IST

> **Superseded.** Kept for history; do not start from this file. The current state is in `docs/checkpoint-2026-11-11.md`.

Read this first in a new session. Details: `docs/todo.md` (F05, F07),
`docs/adr/0016-admin-sign-in-with-github.md`, `docs/adr/0017-upstream-oauth-broker.md`.
Older context: `docs/checkpoint-2026-10-29.md`.

## Where things stand

- **Production unchanged:** 0.2.0 (`1a4ecab`), CI green on it.
- **F05 (GitHub sign-in, web approvals page) is committed locally as `509c3ec`
  ("partial github sigin") but NOT on GitHub yet:** `git status` says
  `main...origin/main [ahead 1]`, and GitHub's `main` and CI still show `1a4ecab`.
  Run `git push` (a push to `main` deploys; with no GitHub OAuth App configured,
  `/admin` and `/auth` answer 503, so nothing visible changes).
- **F07 (each person connects their own account to OAuth servers) is in the
  working tree, uncommitted**, version 0.4.0. Checked on this Mac on 2026-11-01:
  `gofmt -l` clean, `go test -race ./...` all ok, acceptance suite 51/51 under
  `-race` (3 new F07 tests). In the cloud copy also: `go vet`, `go mod tidy`
  unchanged, golangci-lint 0 issues, and a real Chromium run of invite, connect
  and disconnect.

## Decisions (user)

- Company IdP sign-in (Okta, Entra, Google Workspace, SCIM): backlog; the user
  is thinking about it. The Obot comparison is in the Project checkpoint of
  2026-10-30.
- Order of work: F05 GitHub sign-in as a stopgap, then F07, then a thin admin
  UI (F14), with F08 toolsets, F17 context cost and F21 client checks as quick
  wins.
- GitHub sign-in does not need to be enterprise-scale now.
- Files the user should read go into the repo (this folder), not only the
  Project.

## F07 in one screen

- `POST /api/v1/servers` with `"oauth": {}` (optional `client_id`,
  `client_secret`, `scopes`, `issuer`) on an http/sse server: Privo discovers
  the authorization server and registers (admin client id, else its client ID
  metadata document at `/oauth/client-metadata.json`, else dynamic
  registration).
- `privo admin invite --person ada [--role ...]` or `POST /api/v1/invites`:
  one-time link, 72 h; claimed with GitHub sign-in.
- People connect at `/connect/<alias>` (or from Connections on the admin
  site). The first owner/admin grant lists the tools; every call runs with the
  caller's own grant; no grant gives the connect link in the tool error.
- Disconnect from the Connections page, or an admin with
  `DELETE /api/v1/servers/<alias>/connections/<person>`.
- Found while building: the AI client's MCP protocol version leaked into
  Privo's upstream connections through the request context (fixed for the
  per-person connections).

## Next steps

1. User: `git push` F05; review and commit F07, then push.
2. Try it for real: GitHub OAuth App for sign-in (callback
   `https://app.privo.club/auth/github/callback`), the secret as a Coolify file
   mount, `privo admin setup-link`, add Linear with `"oauth": {}`, connect,
   invite someone. GitHub's MCP server needs a second OAuth App with callback
   `https://app.privo.club/connect/callback`. Real GitHub and real providers
   are untested.
3. Independent reviews of F05 and F07 when the user says go.
4. Then F14 thin admin UI, F08/F17, F21.
