# Privo checkpoint: 2026-11-03

> **Superseded.** Kept for history; do not start from this file. The current state is in `docs/checkpoint-2026-11-11.md`.

Read this first in a new session; the previous one is `docs/checkpoint-2026-11-01.md`.

## Where things stand

- **Production:** 0.3.0 (`509c3ec`, F05 GitHub sign-in), CI green. The GitHub
  OAuth App is configured on Coolify and the owner signed in on 2026-11-03:
  real GitHub sign-in works.
- **Working tree on the Mac, uncommitted, two slices on top of `509c3ec`:**
  - **F07** (0.4.0): each person connects their own account to OAuth MCP
    servers; invites. ADR 0017.
  - **F14 first slice** (0.5.0): the admin UI. Every step after the setup link
    is in the browser: Servers (add, review tools, turn off, delete, connected
    people), Rules, People (invite, roles, remove), Tokens (each person makes
    their AI-client token with a ready Claude Code command; admins see all and
    make API tokens), Activity. ADR 0018: server-rendered pages, no JavaScript,
    calling the admin API in process as the signed-in person
    (`control.API.Serve`), so every rule and audit record is the API's own.
    New API: `/api/v1/members` (list, role, remove), `/api/v1/me/tokens`.
- **Checked on this Mac 2026-11-03:** `gofmt -l` clean, `go test -race ./...`
  all ok, acceptance 52/52 under `-race` (new `TestAdminUIFromScratch`). In
  the cloud copy also vet, `go mod tidy` unchanged, golangci-lint 0 issues and
  a real-Chromium walkthrough from an empty install (no console or CSP errors).

## Later the same day: the Preact admin app (ADR 0019)

The owner committed F07 + F14 (`c7801b0`, not deployed yet) and then found the
server-rendered pages slow. Every click was a page load and every button a
post, a redirect and a reload. VictoriaLogs' UI is a Preact + Vite + TypeScript
app whose build is committed and embedded with `go:embed`, so Privo now does
the same:

- `web/`: the app's source (Preact 10.29.8, Vite 8.3.5, TypeScript 6.0.3, no
  router or UI library). `web/dist` is committed and embedded by package
  `privo.club/privo/web`. The script is about 16 KB gzipped, cached for a
  year; moving between pages loads no page (40–70 ms in a local Chromium).
- `internal/web/app.go`: serves the app pages and assets,
  `/admin/api/session`, the bridge `/admin/api/v1/...` (runs the admin API
  in process as the signed-in person; needs the session, the `X-Privo-CSRF`
  header, same origin for changes, JSON bodies of at most 64 KiB) and
  `/admin/api/me/connections`. `/api/v1` still never takes the cookie.
- The server-rendered admin, approvals and connections pages are gone.
  Sign-in, setup link, invite and connect pages stay server-rendered.
- A refused self-approval is now audited by the API.
- `make ui` rebuilds the app (Node 22); `make ui-check` is the new CI job
  `ui`. Building Privo and the image need no Node.
- Checked: cloud (gofmt, vet, `go test -race ./...`, acceptance, golangci-lint
  0 issues, Vitest 19/19, `tsc`, reproducible build, real-Chromium walkthrough
  with no console or CSP errors); this Mac (`go test -race ./...` all ok,
  acceptance 52/52 under `-race`).
- **To delete by hand before committing** (the session cannot delete files
  here; they are stubs so the build passes): `internal/web/ui.go`,
  `internal/web/ui_test.go`, `internal/web/approvals.go`, and
  `internal/web/templates/{activity,approvals,connections,people,rules,server,servers,tokens}.html`.

## Decisions (owner, 2026-11-03)

- No curl in the normal flow: build the UI first, then test from scratch.
- UI is server-rendered HTML (not React/Vite); CLAUDE.md updated.
  **Reversed the same day:** a Preact + Vite app like VictoriaLogs', without
  Playwright tests (ADR 0019).
- The first owner still claims a setup link printed by `docker exec … privo
  admin setup-link` (not an env var naming the owner).
- Use an OAuth App (not a GitHub App) for sign-in.

## Next steps

1. Owner: delete the stub files above, commit the admin app, push; Coolify
   deploys 0.5.0 (F07 + the admin app).
2. Owner: follow `docs/manual-test.md` (browser only). `docs/manual-test-f05-f07.md`
   is replaced by it and can be deleted.
3. Independent reviews of F05, F07, F14 when the owner says go.
4. Later in F14: toolsets (F08), context cost (F17), presets, editing servers,
   re-enabling removed members.
