# 0019. Admin app: Preact and Vite, embedded in the binary

- Status: Accepted, 2026-11-03 (owner's choice)
- Date: 2026-11-03
- Supersedes: 0018 (server-rendered admin pages). Its points 2 (the admin
  API in process), 4 (members and own-token endpoints) and 5 (setup link)
  stand; points 1 and 3 are replaced below.
- Related: 0011 (roles), 0014 (token kinds), 0016 (sessions, CSRF, CSP), 0017

## Context

The server-rendered pages of 0018 worked, but the owner found them slow: every
click and every button is a full round trip to the server, a form post is a
post, a redirect and a second page load, and the stylesheet was sent with
`no-store`. The server itself answers in milliseconds; the latency is the
network, paid two or three times per action. The owner asked for an
experience like VictoriaLogs' UI.

VictoriaLogs (vmui) is a Preact + Vite + TypeScript single-page app. Its
build output is committed to the repository and embedded with `go:embed`;
Go only serves files, and the app calls the same JSON API as every other
client. Building the Go binary never needs Node.

## Decision

1. **A Preact + Vite + TypeScript single-page app in `web/`**, the
   layout CLAUDE.md always planned. Runtime dependency: `preact` only. No
   router library, no CSS preprocessor, no UI kit: a small history router and
   one stylesheet. Exact versions are pinned with a lockfile.
2. **The build output is committed** (`web/dist`) and embedded by the Go
   package `privo.club/privo/web` (`//go:embed all:dist`). `go build`, the
   Dockerfile and the Go CI jobs need no Node. A CI job runs `npm ci`,
   type-checks, runs the unit tests, rebuilds, and fails if the committed
   build differs from the source (`make ui-check`).
3. **Served by the website** (`internal/web`):
   - `/admin/app/assets/*` (content-hashed file names):
     `Cache-Control: public, max-age=31536000, immutable`.
   - The app's pages (`/admin/servers`, `/admin/servers/{alias}`,
     `/admin/rules`, `/admin/people`, `/admin/tokens`, `/admin/activity`,
     `/admin/approvals`, `/admin/connections`) answer `index.html`
     (`no-store`) to a signed-in person and redirect to sign-in otherwise.
     Inside the app, navigation is client-side: no page load.
   - Sign-in, the setup link, accepting an invite, the upstream connect pages
     and error pages stay server-rendered, without script: they sit in
     GitHub and OAuth redirects and must work without JavaScript.
4. **The app is a client of the admin API through the session.**
   `/admin/api/v1/{path...}` runs `GET|POST|PATCH|DELETE /api/v1/{path...}`
   in process as the signed-in person (`control.API.Serve`, 0018 point 2):
   the same handler, validation, permission check and audit as a token
   request. `/api/v1` itself never accepts the session cookie, so the token
   API keeps no ambient credential. `GET /admin/api/session` returns who is
   signed in, what their role may do, the session's CSRF token and the MCP
   address. Each person's own upstream connections are
   `GET /admin/api/me/connections` and
   `DELETE /admin/api/me/connections/{alias}`.
5. **CSRF and request checks.** Every request under `/admin/api` needs a
   valid session (else 401 JSON, and the app goes to sign-in). Every
   state-changing request also needs a same-origin `Origin` (or
   `Sec-Fetch-Site: same-origin`), `Content-Type: application/json` when it
   has a body, and the session's CSRF token in the `X-Privo-CSRF` header,
   compared in constant time. A cross-site form cannot set the header or the
   content type, and a cross-site script cannot read the token (no CORS).
   Bodies are capped at 64 KiB.
6. **CSP for the app**: `default-src 'none'; script-src 'self';
   style-src 'self'; connect-src 'self'; img-src 'self' data:;
   form-action 'self'; frame-ancestors 'none'; base-uri 'none'`. No inline
   script or style, no `eval`, no third-party origin. Everything the app
   shows that comes from upstream servers, members or GitHub is rendered as
   text by Preact (no `dangerouslySetInnerHTML` anywhere; a unit test
   checks the source for it).
7. **Speed by design**: one request for the app shell, cached and gzipped
   assets after the first visit (about 16 KB of script), a small in-memory
   cache so revisiting a page shows the last data at once while it refreshes,
   and pages that wait on something keep themselves current: a server's page
   while it connects and lists its tools, and Activity every 10 seconds while
   visible. Approvals do not poll, because every read of them is audited
   (the approver sees the arguments); they reload when the window comes back
   to the front and on Refresh.

## Consequences

- One code path for every admin action, as in 0018; the API's tests cover
  the rules. The website's tests cover the bridge (session, origin, header
  token, content type, size) and the serving of the app. The app's unit
  tests (Vitest, jsdom) cover the API client and the pages' behaviour.
- Script is back in the admin site, so XSS is a real class again: the CSP,
  text-only rendering and the source check above are the controls, and a
  security review is due before 1.0.
- A change to the UI needs Node 22 and `make ui` on the developer's
  machine; the Go side does not.
- CLAUDE.md's stack line changes to this ADR.
