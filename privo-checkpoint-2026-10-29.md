# Privo checkpoint: 2026-10-29, 07:45 IST (02:15 UTC)

Read this first in a new session. It is self-contained; details live in the repo (`docs/todo.md`, `docs/reviews/`, `docs/upstream/`, `CLAUDE.md`).

## Where things stand

- **Privo gateway is live** at https://app.privo.club (Coolify app `privo-gateway`, uuid `f46equvu3pb9kmq0shusvojo`), running commit `c6c86d0` (R-01 to R-04 and the SSE fix; checked healthy on 2026-10-29: `/readyz` ok, no panic in the log, unauthenticated API answers "missing bearer token", `deepwiki` up). The landing page (https://privo.club, Coolify app uuid `ekc8ocwowwcoc0c80swo08gk`) is separate and untouched.
- **Not committed yet (working tree of the Mac checkout): R-05 to R-11**, all tests green (see below). Nothing of it is deployed. After the user's push, if a deploy does not start within about a minute, trigger it with the `coolify-deploy` skill (`POST /deploy?uuid=f46equvu3pb9kmq0shusvojo`), then check the deployment status and that the app is healthy.
- **Verified end to end earlier:** Claude Code → Privo → DeepWiki `tools/call` works; the audit log shows it (`GET /api/v1/audit/events`).
- **Gateway env keys in Coolify:** `PRIVO_DATA_DIR`, `PRIVO_LISTEN`, `PRIVO_MASTER_KEY`, `PRIVO_PUBLIC_URL`. The one-time `PRIVO_BOOTSTRAP_TOKEN` is already removed. The new variables below all have defaults; none needs to be set.
- **Admin tokens:** settled. An owner control token listed on 2026-10-29 expires 2027-01-26; new ones can be minted with `POST /api/v1/tokens` (`{"name":..., "role":"owner", "kind":"control"}`) or `privo token create` inside the container.

## Independent review: done

Report: `docs/reviews/2026-10-29-independent-review-f24-cache-hints-result-type.md` (11 findings: 1 High, 3 Medium, 6 Low, 1 Info; verdicts: bootstrap token and resultType wrapper "approve with changes", hostile-upstream hardening "block" until R-01 was fixed). The two earlier High crash bugs were fixed in `a6742fa`.

**Fixed after the review (tests first, all green; R-01 to R-04 deployed in `c6c86d0`, R-05 to R-11 still to be committed):**

| Finding | What changed |
|---|---|
| R-01 High | One notification without `params` from any upstream killed the process (go-sdk v1.8.0 dereferences `params` in `callElicitationCompleteHandler`, and the SDK has no `recover()` anywhere). New `internal/safe.Recover` middleware, outermost on the upstream client and on the client-facing server: a panic becomes an error for one message. Tests: `internal/safe`, acceptance `TestHostileUpstreamNotification`, `TestMalformedClientNotificationsDoNotCrash` (stdio upstream; the HTTP/SSE form is not run) |
| R-02 Medium | An upstream's URL (with its API key in the query) reached the AI client in `call failed: ...`. `Gateway.clientFacing` now returns a fixed message naming the server; the log gets the text through `upstream.RedactText`. SDK local JSON-RPC codes (-32001, -32003, -32004, -32005) count as local. |
| R-03 Medium | Bootstrap variables were checked in the shared `config.Load`, so a leftover variable broke the image's healthcheck and break-glass `privo token create`. Now only `serve` reads them (`config.LoadBootstrapToken`, `store.BootstrapNeeded`). |
| R-04 Medium | Bounds on what an upstream can make the gateway hold or do: `PRIVO_UPSTREAM_MAX_RESPONSE_BYTES` (16 MiB; JSON bodies after decompression, event streams per event), `PRIVO_UPSTREAM_CALL_TIMEOUT` (10 min), at most 100 list pages with repeated-cursor detection, 8 MiB of definitions per load, a refresh deadline (a hung refresh used to block all later refreshes), `PRIVO_UPSTREAM_REFRESH_GAP` (5 s between refreshes of one server), pending pins the server no longer offers are pruned (`store.PrunePending`). An oversized reply fails the call with the fixed message and the connection is re-established. Not covered: per-server in-flight limits and bulkheads (F02, open); the stdio message cap is the SDK's 16 MiB. Measured with small configured limits, as the reviewer could not run these. |
| Found while testing R-04 | SSE upstreams were dropped and reconnected in a loop since the `sse` transport was added: the SDK ties the stream to the context given to `Connect`, which `connect` cancelled on return. Fixed, test `TestSSEUpstreamStaysConnected`. |
| R-05 Low | A malformed reply was relayed and audited `ok`. `internal/gateway/validate.go` checks every reply inside the call (`checkTool`, `checkPrompt`, `rewriteRead` via `checkRead`): no result, a null content block or message, `messages` or `contents` absent or null, or a result that asks for input is refused as malformed, audited `error`, with a message that names the field and never repeats upstream text. An empty list is valid. The SDK client reads a null, `{}` or missing `content` of a tools/call reply as an empty result, so those cannot be told apart. |
| R-06 Low | `requestState` and reserved `_meta` keys (a prefix with the label `modelcontextprotocol` or `mcp`) of an upstream's result are removed (`cleanForRelay`), so a state handle the gateway never issued does not arrive on a result labelled complete and an upstream's `serverInfo` no longer replaces the gateway's. Other `_meta` keys pass. |
| R-07 Low | The audit outcome is decided before the wrapper refuses (see R-05). Errors that leave the gateway for tools/call, prompts/get and resources/read carry a JSON-RPC code (`Gateway.rpcError`): -32602 for an unknown prompt, a URI that is not Privo's and a resource not available (the SDK's `CodeResourceNotFound` is -32602 since SEP-2164, not -32002), -32600 for a refusal, -32603 for the rest, the upstream's own code when it answered. |
| R-08 Low | Docs corrected: clearing the variable only keeps it from child processes, not from `/proc/<pid>/environ`. `serve.go` comment fixed; the start warns when the token came from `PRIVO_BOOTSTRAP_TOKEN`; `deploy-coolify.md` recommends the file form. |
| R-09 Low | `GET /api/v1/audit/events?kind=admin` returns the admin-phase events (control API actions and the bootstrap install), newest first, same permission and `limit` as the call list; `kind=calls` or none is unchanged; other values are 400. (`store.RecentAdminEvents`.) |
| R-10 Low | `token.Weak` (fewer than 16 different symbols, a run of 8, or 16 steps along the alphabet) is applied by `config.LoadBootstrapToken` (exit 2) and `control.InstallBootstrapToken` (`token.ErrWeak`). A guard against the careless case, not a proof of randomness. |
| R-11 Info | A token file that is not a regular file (FIFO, directory) is refused before opening; a live token already named `bootstrap` no longer fails the install (the bootstrap token becomes `bootstrap-<8 chars of its id>`). Not changed, on purpose: `GET /version` stays unauthenticated (user's call), the dead null-tools guard stays as a guard. |

Verification of R-05 to R-11 (2026-10-29 07:40 IST): `go vet ./...` and `go vet -tags acceptance ./...` clean, `go test -race -count=1 ./...` all ok, full acceptance suite ok (43 s). The new acceptance tests were also run against a mutated copy without the fix and fail there. Not reproduced: `golangci-lint` and the docker image build (see open item 3).

## Open items, in priority order

1. **Commit, push, confirm the deploy of R-05 to R-11** (the user commits). First delete `_to_delete/` (4 stray `gofmt` temp files; untracked, and Coolify's watch paths do not exclude it). After the deploy: healthcheck green, `GET /version`, one Claude Code tool call, audit row present, and `GET /api/v1/audit/events?kind=admin` lists the admin actions. Note the 5 s refresh gap: a change of an upstream's tool list can take up to 5 s to show.
2. **Independent review of R-05 to R-07** if wanted: they change the request pipeline (CLAUDE.md asks for an independent review of such changes). Launch a reviewer only when the user asks. The other review findings are all addressed.
3. **GitHub CI run failed; cause unknown.** `.github/workflows/ci.yml` has `test` (vet, race tests, build), `lint` (golangci-lint), `vuln` (govulncheck), `acceptance` (allowed to fail), `image` (docker build). Reproduced locally and passing: vet, race tests, build, govulncheck (earlier), the acceptance suite. **Not reproduced:** `lint` and `image`. GitHub is not linked to the session and the built-in browser is not signed in. Next: the user pastes the failing job's last ~30 log lines, or signs in to GitHub in the built-in pane (never enter credentials for them), or allows installing golangci-lint in the VM (he declined once; ask).
4. **Upstream SDK reports, not filed:** (a) `resultType` is not set for results made from receiving middleware: draft `docs/upstream/go-sdk-resulttype-from-middleware.md`; search the tracker for "resultType" first. (b) The nil dereference in `callElicitationCompleteHandler` (`mcp/client.go` near line 1526) and the missing `recover()`: no draft yet; write one under `docs/upstream/`.
5. **Optional:** run the R-01 notification test over the HTTP and SSE transports (only stdio is covered).
6. **Rest of the user's test plan** (run with a working control token in `$PRIVO_ADMIN`, gateway token in `$PRIVO_TOKEN`, Claude Code server name `privo`, DeepWiki alias `deepwiki` with tools `read_wiki_structure`, `read_wiki_contents`, `ask_question`):
   - Deny: `POST /api/v1/policies {"server":"deepwiki","tool":"ask_question","action":"deny"}`; ask Claude again; expect a "Denied" refusal; remove with `DELETE /api/v1/policies/<id>`.
   - Ask/approval: same with `"action":"ask"`; Claude is told "Approval required: request <id> ..." and to retry the same call after approval; `GET /api/v1/approvals?status=pending`, then `POST /api/v1/approvals/<id>/approve` (or `/deny`). Solo testing hits 403 "requesters cannot decide their own calls": `PATCH /api/v1/settings/approvals {"allow_self":true}` for the test only, then back to `false`.
   - Revocation: `DELETE /api/v1/tokens/<gateway-token-id>`; calls fail within about 2 s; mint a new gateway token and update Claude Code.
   - Presets: `POST /api/v1/servers/{alias}/policy:preset {"preset":"read-only"|"ask-before-writes"}`. Default policy: `PATCH /api/v1/settings/policy {"default":"allow|ask|deny"}`.
   - Audit: `GET /api/v1/audit/events?limit=N` returns `{"events":[...]}` newest first (default 50, max 500). Fields: time, actor, server, tool, decision, outcome (`ok`, `tool_error`, `error`, `pending`, `denied_by_approver`, `pending_approval`), latency_ms. Arguments appear only after `PATCH /api/v1/settings/audit {"record_arguments":true}` (off by default; stored encrypted). Admin actions are in `GET /api/v1/audit/events?kind=admin` (fields: time, actor, actor_id, action, decision, outcome, server) once R-09 is deployed.

## Decisions left to the user

- Whether `GET /version` stays unauthenticated (R-11; it shows version, commit, build date and Go version, and `dev`/`none` on Coolify today because the Dockerfile `COMMIT` build arg is not wired).
- Moving `PRIVO_MASTER_KEY` to a file mount and `PRIVO_MASTER_KEY_FILE` (the log warns about the env var on every start).
- Wiring the commit into the Coolify build so `/version` is useful.

## Known gaps and decisions (also in `docs/todo.md` where noted)

- Failed logins and bad tokens are neither logged nor audited (not yet in todo.md; add it).
- No per-server in-flight limits, bulkheads or circuit breakers (F02).
- `privo token create` (CLI) writes no audit row.
- The null-tools guard in `upstream.load` is dead code (the SDK drops null tools) and stays as a guard.
- Unreleased: stdio servers on Coolify (F13 runner), claude.ai web/app connectors (F06 OAuth server), Zerodha (F07 upstream OAuth broker), admin sign-in F05 and UI F14, backups F15 (before real customers), client compatibility suite F21 (in progress).
- Claude Code has been confirmed for tools only; prompts and resources through a real client are covered by acceptance tests only.
- Suggested roadmap: deploy R-05 to R-11, F05 minimal sign-in, a thin UI slice, then F15 backups before any real customer.

## Environment and how to work (for the next session)

- **Repo:** GitHub `talk2sohail/privoclub`, branch `main`. On the user's Mac at `~/Downloads/code/privoclub`; from the cloud session it is `$HOME/mnt/privoclub` through `mcp__remote-devices__device_bash` (needs the computer linked and the desktop app open). The shell has no GitHub credentials: the **user** commits and pushes; Claude never does unless asked.
- **Go:** toolchain at `$HOME/sdk/go/bin` (Go 1.27.3, linux/arm64 VM): `export PATH="$HOME/sdk/go/bin:$PATH"`. The VM's HOME path changes per session, so run plain `go mod download` again (about 20 s; no `GOFLAGS=-mod=mod`, the user rejected it because it can change go.mod). Commands: `go vet ./...`, `go vet -tags acceptance ./...`, `go test -race -count=1 ./...`, `go test -tags acceptance -count=1 -timeout 175s ./test/acceptance/...` (about 30 s). `device_bash` allows at most 180000 ms per call.
- **`rm` is blocked** on the connected folder. `gofmt -w` can leave temp files (`*.go.<digits>`); move them into `_to_delete/` and tell the user to delete the folder before committing. Use `git --no-optional-locks ...` for read-only git; avoid git writes.
- **Coolify:** use the `coolify-deploy` skill; token and URL live in `~/.config/coolify` on the Mac, mounted at `$HOME/mnt/coolify` after `device_request_folder_access` (ask once per session). Never print the token or env values (key names only). Watch paths work: a docs-only push does not deploy.
- **Network limits seen:** the VM's egress proxy returns 403 for app.privo.club and public MCP hosts (diagnose through Coolify's API/logs or the user's own terminal). GitHub is not reachable from the cloud shell for this repo.
- **Working agreement (`CLAUDE.md`):** tests first; enterprise-quality bar; secrets never logged or returned; fail-closed defaults; independent review for auth/secrets/tenancy/pipeline changes (launch an agent only when the user asks); update `docs/todo.md`. Verify before stating: several earlier slips were commands with unverified variable names.
- **Reviewer experience:** a safety classifier stopped one hostile-payload experiment in the reviewer's session; it stopped there and reported it. Do not work around such a block; tell the user. Keep hostile-upstream tests small by using low configured limits.

## First steps in the next session

1. Ask whether the last commit was pushed and deployed; if not, do open item 1.
2. Check the deployed commit through Coolify (`GET /deployments/applications/<uuid>?take=1`) and the health of the app.
3. Get the CI log and fix it (open item 3); then the suggested roadmap under "Known gaps".
