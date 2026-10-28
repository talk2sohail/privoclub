# Privo checkpoint: 2026-10-29, 03:52 IST (22:22 UTC)

Read this first in a new session. It is self-contained; details live in the repo (`docs/todo.md`, `docs/reviews/`, `docs/upstream/`, `CLAUDE.md`).

## Where things stand

- **Privo gateway is live** at https://app.privo.club (Coolify app `privo-gateway`, uuid `f46equvu3pb9kmq0shusvojo`), running commit `582fa0c` (deployed 21:59 UTC). Commit `10a0362` is docs only, so it did not deploy (watch paths work as intended). The landing page (https://privo.club, Coolify app uuid `ekc8ocwowwcoc0c80swo08gk`) is separate and untouched.
- **Verified end to end today:** Claude Code → Privo → DeepWiki `tools/call` works (the user confirmed 03:36 IST). The audit log shows it (`GET /api/v1/audit/events`). Admin API works with the owner control token (user's `$PRIVO_ADMIN`, copied from `$OWNER`, returned 200).
- **Gateway env keys in Coolify:** `PRIVO_DATA_DIR`, `PRIVO_LISTEN`, `PRIVO_MASTER_KEY`, `PRIVO_PUBLIC_URL`. The one-time `PRIVO_BOOTSTRAP_TOKEN` is already removed.

## Commits this stretch (all pushed by the user)

| Commit | What |
|---|---|
| `778c56b` | F24: bootstrap owner token (24 h), root page, Coolify webhook + watch-path docs |
| `7f84b65` | Private cache hints (`cacheScope`/`ttlMs`) on list and read results; fixed Claude Code "tools fetch failed" |
| `582fa0c` | `resultType` wrapper (`internal/gateway/resulttype.go`): 2026-07-28 clients get `"resultType":"complete"` on tools/call, prompts/get, resources/read; older clients get none. Tests: unit + `test/acceptance/resulttype_test.go` (raw JSON, both a new-protocol and a `-mode legacy` upstream). Cache-hint test now runs at both protocol versions |
| `10a0362` | Draft Go SDK issue: `docs/upstream/go-sdk-resulttype-from-middleware.md` (standalone repro, run against go-sdk v1.8.0) |

Root cause of the last bug: the SDK sets `resultType` only inside its own tool/prompt/resource handlers; Privo answers those methods from receiving middleware. Results from a 2026-07-28 upstream carried the field by accident (relayed); results from older upstreams (DeepWiki) and Privo's own refusals did not.

## Open items, in priority order

1. **Admin token may expire soon. Check first.** The bootstrap token lasts 24 h from first boot (first bootstrap-capable deploy was about 20:54 UTC on 2026-10-28, so roughly 02:25 IST on 2026-10-30; an estimate). It is not known whether the user's `$OWNER` is that token or a longer-lived one. Check `expires_at` with `GET /api/v1/tokens` (control token). If it is the bootstrap token, mint a 90-day owner control token: `POST /api/v1/tokens` with `{"name":"sohail-owner","role":"owner","kind":"control"}`; keep the result in a variable, never print it, store it in a password manager (shown once). Lockout recovery if lost: `privo token create` inside the container from the Coolify host (`docker exec`); bootstrap does not reinstall once consumed.
2. **GitHub CI run failed; cause unknown.** Workflow `.github/workflows/ci.yml` has jobs `test` (vet, `go test -race`, `make build`), `lint` (golangci-lint v-latest), `vuln` (govulncheck), `acceptance` (allowed to fail), `image` (docker build, needs `test`). Reproduced locally and passing: `go vet`, `go test -race ./...`, build, `govulncheck` (no vulnerabilities), the full acceptance suite. **Not reproduced:** `lint` and `image`. Could not read the run: GitHub is not linked to the session (`add_repo` says to link the account), the built-in browser pane is not signed in to GitHub (private repo gives 404), and the Claude-in-Chrome extension was not connected. Next: user pastes the failing job's last ~30 log lines, or reconnects the Chrome extension, or signs in to GitHub in the built-in pane (never enter credentials for them). The user declined installing golangci-lint in the VM; ask before doing so.
3. **Independent security review has NOT happened.** A reviewer subagent was started at about 03:24 IST and disappeared by 03:52 without writing its report (probably stopped when the user interrupted a request; unconfirmed). No report exists in `docs/reviews/`. `CLAUDE.md` requires it for anything touching auth, secrets, tenancy or the request pipeline. Re-run it with the brief in the project doc `privo-review-brief.md`, then fix findings rated medium and above. Only the author's self-review (`docs/reviews/2026-10-29-f24-bootstrap-self-review.md`, with two addenda) exists. The reviewer's partial scratch work lived in the old VM session and is gone.
4. **Go SDK issue:** draft is ready, not filed. The tracker could not be searched from the session; search it for "resultType" first, then file. `typedResult` stays until an SDK release sets the field for middleware results.
5. **Rest of the user's test plan** (run it with a working control token in `$PRIVO_ADMIN`, gateway token in `$PRIVO_TOKEN`, Claude Code server name `privo`, DeepWiki alias `deepwiki` with tools `read_wiki_structure`, `read_wiki_contents`, `ask_question`):
   - Deny: `POST /api/v1/policies {"server":"deepwiki","tool":"ask_question","action":"deny"}`; ask Claude again; expect a "Denied" refusal; remove with `DELETE /api/v1/policies/<id>`.
   - Ask/approval: same with `"action":"ask"`; Claude is told "Approval required: request <id> ..." and to retry the same call after approval; `GET /api/v1/approvals?status=pending`, then `POST /api/v1/approvals/<id>/approve` (or `/deny`). Solo testing hits 403 "requesters cannot decide their own calls" (same token or same person): `PATCH /api/v1/settings/approvals {"allow_self":true}` for the test only, then set it back to `false`.
   - Revocation: `DELETE /api/v1/tokens/<gateway-token-id>`; calls fail within about 2 s; then mint a new gateway token and update Claude Code.
   - Presets: `POST /api/v1/servers/{alias}/policy:preset {"preset":"read-only"|"ask-before-writes"}`. Default policy: `PATCH /api/v1/settings/policy {"default":"allow|ask|deny"}`.
   - Audit: `GET /api/v1/audit/events?limit=N` returns `{"events":[...]}` newest first (default 50, max 500; above 500 falls back to 50). Fields: time, actor, server, tool, decision, outcome (`ok`, `tool_error`, `error`, `pending`, `denied_by_approver`, `pending_approval`), latency_ms. Arguments only appear if `PATCH /api/v1/settings/audit {"record_arguments":true}` (off by default; stored encrypted, shown decrypted to admins). Admin actions are not in this list.

## Known gaps and decisions (also in `docs/todo.md` where noted)

- Failed logins and bad tokens are neither logged nor audited (not yet in todo.md; add it).
- No cap on the size of upstream responses (F19 item added).
- `privo token create` (CLI) writes no audit row.
- Unreleased: stdio servers on Coolify (F13 runner), claude.ai web/app connectors (F06 OAuth server), Zerodha (F07 upstream OAuth broker), admin sign-in F05 and UI F14, backups F15 (before real customers), client compatibility suite F21 (in progress: validate raw responses against the MCP schema).
- Claude Code has been confirmed for tools only; prompts and resources through a real client are covered by acceptance tests only.
- Suggested roadmap: F05 minimal sign-in, then a thin UI slice, then F15 backups before any real customer.

## Environment and how to work (for the next session)

- **Repo:** GitHub `talk2sohail/privoclub`, branch `main`. On the user's Mac at `~/Downloads/code/privoclub`; from the cloud session it is `$HOME/mnt/privoclub` through `mcp__remote-devices__device_bash` (needs the computer linked and the desktop app open). The shell has no GitHub credentials: the **user** commits and pushes; Claude never does unless asked.
- **Go:** toolchain at `$HOME/sdk/go/bin` (Go 1.27.3, linux/arm64 VM); the VM's HOME path changes per session, so run `go mod download` again (about 20 s). Commands: `go vet ./...`, `go vet -tags acceptance ./...`, `go test -race -count=1 ./...`, `go test -tags acceptance -count=1 -timeout 5m ./test/acceptance/...` (about 20 s).
- **`rm` is blocked** on the connected folder. `gofmt -w` can leave temp files; move them into `_to_delete/` and tell the user to delete it before committing (it is not gitignored). Use `git --no-optional-locks ...` for read-only git; avoid git writes.
- **Coolify:** use the `coolify-deploy` skill; token and URL live in `~/.config/coolify` on the Mac, mounted at `$HOME/mnt/coolify`. Never print the token or env values (key names only). Webhook auto-deploys with watch paths already work.
- **Network limits seen:** the VM's egress proxy returns 403 for app.privo.club and public MCP hosts (diagnose through Coolify's API/logs or the user's own terminal). GitHub API is not available from the cloud shell for this repo.
- **Working agreement (`CLAUDE.md`):** tests first; enterprise-quality bar; secrets never logged or returned; independent review for auth/secrets/tenancy/pipeline changes; update `docs/todo.md`. Verify before stating: several earlier slips were commands given with unverified variable names (`$PRIVO_ADMIN` was never defined in the setup steps, which used `$BOOT` and `$OWNER`).

## First steps in the next session

1. Ask the user to run `GET /api/v1/tokens` (or check `$OWNER`'s expiry) and settle open item 1.
2. Get the CI failure log from the user and fix it (open item 2).
3. Re-run the independent review from the brief and relay the findings (open item 3).
