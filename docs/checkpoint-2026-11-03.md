# Privo checkpoint: 2026-11-03

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

## Decisions (owner, 2026-11-03)

- No curl in the normal flow: build the UI first, then test from scratch.
- UI is server-rendered HTML (not React/Vite); CLAUDE.md updated.
- The first owner still claims a setup link printed by `docker exec … privo
  admin setup-link` (not an env var naming the owner).
- Use an OAuth App (not a GitHub App) for sign-in.

## Next steps

1. Owner: commit F07 and F14 (separately if wanted), push; Coolify deploys 0.5.0.
2. Owner: follow `docs/manual-test.md` (browser only). `docs/manual-test-f05-f07.md`
   is replaced by it and can be deleted.
3. Independent reviews of F05, F07, F14 when the owner says go.
4. Later in F14: toolsets (F08), context cost (F17), presets, editing servers,
   re-enabling removed members.
