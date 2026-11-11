# Privo checkpoint: 2026-11-11

Read this first in a new session. It replaces every earlier checkpoint
(`docs/checkpoint-2026-11-03.md`, `docs/checkpoint-2026-11-01.md`, the root
`privo-checkpoint-2026-10-29.md` and the ones in the Claude Project); those are
history. Open work is in `docs/todo.md`; the audit that produced this file is
`docs/reviews/2026-11-11-project-audit.md`.

## Where things stand (checked 2026-11-11)

- **Production:** https://app.privo.club runs **0.6.0** (`/version`), `/readyz`
  is ok. GitHub `main` is at `7e434a2` (2026-11-04) and CI is green on it (run
  41882716507). No release tags exist.
- **Shipped and deployed:** GitHub sign-in with the setup link (F05, 0.3.0),
  each person's own account on OAuth servers and invites (F07, 0.4.0), the
  Preact admin app (F14, 0.5.0), teams, the server catalog and the Start page
  (F26, 0.6.0). The owner signed in with real GitHub on 2026-11-03 and walked
  through F26 on app.privo.club on 2026-11-04.
- **Checks on `7e434a2` (cloud copy, Go 1.27.3, 2026-11-11):** `gofmt -l`
  clean; `go test -race ./...` all ok; acceptance suite ok under `-race`;
  golangci-lint 2.15.0: 0 issues; govulncheck: none; `go mod tidy` changes
  nothing; the admin app: `tsc` ok, Vitest 30/30, the build equals the
  committed `web/dist`.
- **Scheduled backups are off in production** (owner's choice, 2026-10-29).
  The restore has never been drilled on the server.

## Open, most important first

1. **Review findings that were parked on 2026-11-03** (now listed in
   `docs/todo.md` under F05, F07, F14 and F24). The one that matters most:
   **F07 R-01 / F14 R-02 (High)**: an owner or admin can issue a gateway token
   with another member's `person` and use that member's upstream grant; team
   membership follows the same string. Fix by binding tokens to a user id.
   Then the Mediums: the sign-in state cap can be filled by anyone (F05 R-01,
   F07 R-03), a re-registration is not seen by the running upstream manager
   (F07 R-02), the tool review shows only the first line of a description
   (F14 R-01), a bootstrap token installs on a browser-only org (F24 R-01),
   `privo token create` writes no audit row (F24 R-02).
2. **Owner's walkthrough left open** (`docs/manual-test.md`): steps 4 to 6 and
   9.2, 9.5, 9.6 need a second GitHub account; connecting a real OAuth server
   (Linear, Notion…) needs an account there. No real provider grant has been
   through the broker in production yet.
3. **F26 follow-ups** (todo, F26): errors of a catalog add are shown out of
   view; a slow provider ends in "context deadline exceeded"; token names are
   unique per org, not per person; R-03 (audit of out-of-team attempts).
4. **Before any real customer:** scheduled backups on with an off-host copy
   and one restore drill; the independent review of the backup and restore
   code (ten questions in the Project's 2026-10-29 checkpoint, section "To do:
   independent review of the backup and restore code").
5. **Decisions waiting for the owner:** see "For the owner to decide" in the
   audit report (ADRs 0016, 0017, 0020 still Proposed; release tags; the
   landing page still sells "Privo Club"; files to delete).
6. **Backlog, unchanged:** sign-in through the company's identity provider
   (owner is thinking about it; the Obot comparison is in the Project's
   2026-10-30 checkpoint), F08 toolsets, F17 context cost, F21 client checks,
   F06 OAuth server, F13 stdio runner, the rest of F15 (WAL shipping,
   `privo migrate`), the two go-sdk reports in `docs/upstream/` (owner files
   them).

## How to work (still true)

- The owner commits and pushes from the Mac (`~/Downloads/code/privoclub`); a
  session never pushes. A push to `main` that touches the gateway's watch
  paths deploys production (a few seconds of downtime).
- On the Mac: no `go vet`, no `gofmt -w` (check with `gofmt -l`), no deletes;
  edit files in place. CI runs vet.
- Independent reviews run only when the owner says go.
- Files for the owner to read go into this repo (root or `docs/`).
