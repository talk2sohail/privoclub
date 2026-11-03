# 0018. Admin UI: server-rendered pages over the admin API

- Status: Accepted, 2026-11-03 (owner's choice)
- Date: 2026-11-03
- Amends: CLAUDE.md stack line "Admin UI: React and Vite, embedded with go:embed"
- Related: 0011 (roles), 0014 (token kinds), 0016 (sessions, CSRF, CSP), 0017

## Context

Every admin task after sign-in (adding a server, reviewing its tools, rules,
tokens, invites, reading the audit) is a curl call against `/api/v1` with a
control token. The owner found that confusing and asked for a UI that covers
these steps, as simple as possible. The sign-in, approvals and connections
pages (0016, 0017) are already server-rendered `html/template` pages with no
script, under a CSP that allows none.

## Decision

1. **Server-rendered HTML, no JavaScript**, extending the existing pages: the
   same CSP (`default-src 'none'`, no script), one embedded stylesheet, forms
   that post with the session's CSRF token and a same-origin check. No Node
   build in the Dockerfile or CI.
2. **The pages are a client of the admin API, in process.** A page calls
   `control.API.Serve(ctx, principal, method, path, body)`, which runs the very
   same handler, validation, permission check and audit as a token request.
   The principal is the signed-in person (kind `session`, their role and
   person, and their user id as the audit actor id). Nothing the API refuses
   can be done from a page, and nothing is implemented twice. The bridge is
   not reachable over HTTP: it is a Go call.
3. **Pages** (each shown only to roles that can use it):
   - Servers: list, add (remote, with an API-key header or each person's own
     account), a server's page with its status, the tools waiting for review
     (accept them), enable or disable, delete, and who connected.
   - Rules: the default for calls no rule matches, the rules, add and delete.
   - People: members and their roles, invite, change a role, remove.
   - Tokens: "your AI clients" (anyone who uses the gateway makes their own
     gateway tokens, shown once with the MCP address and a ready command), and
     for owners and admins every token, API tokens and revocation.
   - Activity: recent tool calls and admin events.
   - Approvals and Connections stay as they are.
4. **New API endpoints the pages need**, available to tokens too:
   `GET /api/v1/members`, `PATCH /api/v1/members/{person}` (role),
   `POST /api/v1/members/{person}/remove`; `GET|POST /api/v1/me/tokens`,
   `DELETE /api/v1/me/tokens/{id}`.
5. The first owner still claims a setup link printed by `privo admin
   setup-link` (owner's choice); after that, nothing needs a terminal or curl.

## Consequences

- One code path for every admin action; the API's tests cover the pages'
  rules, and the page tests cover rendering, escaping and CSRF.
- No client-side state, no XSS surface from script; pages reload on every
  action. Rich interactions (live updates, search as you type) are out of
  reach without revisiting this ADR.
- CLAUDE.md's stack line changes to server-rendered pages.
