# Independent review: F24 bootstrap token, cache hints, resultType wrapper, hostile-upstream hardening

- Date: 2026-10-29
- Reviewer: independent review agent
- Reviewed commit: `a6742fa` ("fix crash bugs"), on top of `10a0362` (`git log --oneline -8` confirmed; the four changes are `778c56b`, `7f84b65`, `582fa0c`/`10a0362`, `a6742fa`)
- `git status` at start: clean (`nothing to commit, working tree clean`, up to date with origin/main). At the end the only difference is this untracked report file; no tracked file changed.
- Method: read the code and tests, ran vet / race tests / acceptance, then tried to break each change in a scratch copy under `$HOME/review-scratch/` (copy of the tree without `.git`, plus my own test files). No network call to any Privo deployment. Secret-like values were never printed.

## Verdicts

| Change | Verdict | Why |
|---|---|---|
| 1. F24 bootstrap owner token + root page | **Approve with changes** | Install-once, hash-only storage, atomic marker, TTL cap, kind/role checks, fail-closed parsing and the root page all held up under test. Fix R-03 (config coupling breaks healthcheck and break-glass), correct R-08 (the "cleared from environment" mitigation is not one), consider R-09 and R-10. |
| 2. Cache hints | **Approve** | Every list and read path emits `private` with the stated TTLs at both protocol versions; an upstream's `public` hint is overridden; nothing found that weakens revocation. |
| 3. resultType wrapper | **Approve with changes** | Correct for all three methods and both client generations, fails closed on `input_required` and nil, policy/approval/audit still run before a result exists, tests are strong (mutation-checked). R-05, R-06, R-07 are Low; R-04 (no size cap, extra copies) is Medium and predates it. |
| 4. Hostile-upstream hardening | **Block** | The stated intent ("a reply that breaks the protocol must cost only that server, never crash the gateway") is not met: R-01 shows a single malformed notification from any upstream still ends the whole process. The three null-entry cases the commit fixes are fixed and well tested. |

## Findings (by severity)

Counts: Critical 0, High 1 (R-01), Medium 3 (R-02 to R-04), Low 6 (R-05 to R-10), Info 1 (R-11, five small observations).

### R-01 High: a notification with no `params` from any upstream kills the whole gateway process

- Where: go-sdk v1.8.0 `mcp/client.go:1526`, `(*Client).callElicitationCompleteHandler` reads `req.Params.ElicitationID` with no nil check. Reached from `internal/upstream/upstream.go` `connect` (the `mcp.NewClient(...)` call): no receiving middleware, no recover. The SDK has no `recover()` anywhere in `mcp/` or `internal/jsonrpc2/` (grep), and runs each incoming message in a goroutine it starts itself, so neither `net/http` nor Privo catches the panic.
- What is wrong: commit `a6742fa` fixes three specific null-entry cases but not the class "upstream-controlled data dereferenced in an SDK goroutine". A `notifications/elicitation/complete` notification whose `params` is absent or JSON null panics the process (observed: exit status 2, SIGSEGV trace in the handler above).
- Scenario: one process serves every tenant. Any server an org registers, or any third-party server that turns hostile or is compromised, takes the gateway down for all tenants, and again after every restart once the supervisor reconnects (the retry loop reconnects within seconds).
- How established: RAN. Scratch raw stdio upstream (`$HOME/review-scratch/repo/test/acceptance/testdata/rawhostile/main.go`, cases `n-elicit-absent` and `n-elicit-null`) driven by `review_test.go` in the same directory: process died. Controls in the same run (empty or valid params; absent params on progress, message, cancelled, resources/updated, roots/list_changed; seven variants of server-to-client sampling, roots and elicitation requests; broken JSON, batches, stray responses) did not crash it. Server-side notification handlers (downstream clients) read as nil-safe (`cancel`, `initialized`, roots, progress in `mcp/server.go`). The `http` and `sse` transports feed the same client handler and a notification can precede a response on a POST response stream, so I expect the same result there; that is argued from reading and was NOT run (see Limits).
- Fix (verified in the scratch copy): add `client.AddReceivingMiddleware` in `connect` that wraps `next` in a `defer recover()` and returns an error; middleware runs on the SDK handler's goroutine, so the recover works. With it, both cases pass and the gateway stays up (RAN). Do the same on the downstream `mcp.Server`. Report to go-sdk. Add an acceptance test per notification method with `params` absent and null, so the class is covered and not one instance.

### R-02 Medium: an upstream URL's query string (API keys) reaches the AI client in `call failed: ...`

- Where: `internal/gateway/gateway.go` `callTool` (`text := "call failed: " + err.Error()`), and the raw `err` returned by `getPrompt` and `readResource` (including the new "malformed resources/read reply" path). `redactError` is applied only in `internal/upstream` for logs and server state.
- What is wrong: when the HTTP transport fails (outage, refused, TLS), the Go error text contains the full request URL. The code base itself says query strings often carry API keys (`RedactURL`). The text goes back to the caller as the tool result, so any holder of a gateway token, including the model, sees the key.
- Scenario: an org registers `https://host/mcp?key=...`; the upstream restarts; the next tool call returns `call failed: ... Post "https://host/mcp?key=...": dial tcp ...: connection refused` to the AI client.
- How established: RAN (`TestReviewOutageErrorText` in `review_http_test.go`): result text contained the key, gateway logs did not, admin server list showed the redacted target.
- Predates the four changes, but it is the path the new errors travel. Fix: return a fixed, redacted message for transport errors to callers (log the detail), or pass `err.Error()` through `redactError` with the server's URL.

### R-03 Medium: bootstrap variables are validated in the shared `config.Load`, which breaks `healthcheck` and break-glass `token create`

- Where: `internal/config/config.go` `loadBootstrapToken` called from `Load`; `cmd/privo/main.go` `healthcheck` and `serve.go` `tokenCreate` both call `config.Load`; `Dockerfile` has `HEALTHCHECK ... /privo healthcheck` every 15 s.
- What is wrong: if `PRIVO_BOOTSTRAP_TOKEN_FILE` points to a file that is later removed, emptied, or changed to an invalid value (the natural clean-up after the bootstrap is used), then (a) the healthcheck of the still-running container exits 2, (b) the next `serve` refuses to start (exit 2), and (c) `privo token create`, the documented break-glass path, exits 2 too. A malformed `PRIVO_BOOTSTRAP_TOKEN` has the same effect. Fail-closed is right for `serve` on a bad value, but not for an unused leftover and not for the other subcommands.
- Scenario: operator mounts the token as a file (the form the config comment and the self-review prefer), uses it, deletes the file but not the variable, and Coolify marks the container unhealthy; a restart then crash-loops, and the shell-less recovery command fails the same way.
- How established: RAN (`boot2.sh`, case G/H): healthcheck exit 2 with server running; token create exit 2; serve exit 2. The documented procedure uses the plain variable, so the default path is not affected.
- Fix: parse the bootstrap token only in `serve`, and only act on it when the org still needs one; make `healthcheck` and `token create` ignore it. Treat a missing or unreadable `_FILE` after consumption as a warning.

### R-04 Medium (argued from reading, NOT run): no resource bounds on what an upstream can make the gateway hold or do

- Where and why (all read, not exercised, see Limits):
  1. `mcp/streamable.go:2490` `handleJSON` does `io.ReadAll(resp.Body)` for `application/json` replies; the egress client has no total timeout (`egress.go` `Timeout 0 means none (streams)`, only a 60 s response-header timeout) and `gateway.run` sets no per-call deadline. Stdio and SSE frames are capped at 16 MiB by the SDK; JSON bodies are not.
  2. `upstream.load` iterates SDK pagination; `MaxItems` counts items added, so pages of zero items with a next cursor never trip it. On connect `cctx` bounds this to 20 s; `scheduleLoad` passes the supervisor context (no timeout), so one such server pins a goroutine and leaves `e.loading` set, which silently ends all later refreshes for that server.
  3. `MaxItems` 2000 x `MaxDefinitionBytes` 64 KiB allows about 128 MiB per refresh in memory and in `pins`; every distinct definition is kept forever (`RecordSeen`, primary key includes the hash), and refreshes run on the single writer connection that audit intent writes also use, so a server that keeps changing and re-announcing its list slows other tenants' calls and fills the disk.
  4. `typedResult.MarshalJSON` adds two more copies of every relayed result (marshal, unmarshal to a map, marshal) on top of the SDK's own.
- Fix: cap bytes read per upstream reply (wrap the egress transport's response body in a limit reader), a deadline per upstream call and per list refresh, a page-count limit in `load`, a cap on pending/total pins per server, and a size cap on relayed results.

### R-05 Low: "refused as an upstream error" is not true for every malformed reply

- Where: `internal/gateway/gateway.go` `callTool`, `getPrompt`, `readResource`/`rewriteRead`.
- What is wrong: only a null entry among `resources/read` contents (and no result there) is refused. A `tools/call` reply of `result: null` is relayed as an empty successful result and audited `ok`; a `prompts/get` reply with `messages: [null]` or `messages: null` is relayed as is; a `resources/read` reply with `contents` absent or null is relayed as `"contents":null` with `resultType: complete`. None crashes the gateway; they hand protocol-breaking results to clients and record `ok`.
- How established: RAN (`call-null-result`, `prompt-msg-null`, `prompt-null-result`, `read-contents-missing`, `read-contents-nullfield`).
- Fix: one validation step before `withResultType` (non-nil, no null elements, required arrays present) with audit outcome `error`.

### R-06 Low: upstream `requestState` and `_meta` pass through into results labelled complete

- Where: `internal/gateway/resulttype.go` (the comment says the gateway does not relay an upstream's questions); `withResultType` tests only `NeedsInput()`.
- What is wrong: `requestState` from the upstream reaches the client intact on a `complete` result (RAN, `call-reqstate-only`), and upstream `_meta` is relayed, including the reserved `io.modelcontextprotocol/serverInfo` key, which the upstream can overwrite (RAN, `call-meta-poison`). `inputRequests` are NOT relayed: the SDK client in the gateway fails any call whose reply has non-empty `inputRequests` ("client does not support elicitation"), independent of `resultType` (RAN, `call-inputreq-*`, `prompt-inputreq-nortype`, `read-inputreq-nortype`); that refusal depends on the SDK, not on the wrapper.
- Impact: low; a client echoes `requestState` only for `input_required`. The comment and the "complete" label are inexact. Fix: clear `RequestState` and reserved `_meta` keys on relayed results.

### R-07 Low: audit says `ok` when the wrapper then refuses the result; error code 0

- Where: `gateway.go` `run` records the completion before `middleware` calls `withResultType`; gateway-built errors reach the client as JSON-RPC code 0 (`prompts/get` not available, bad resource URI, `input_required` refusal), which is not a valid code.
- Established: RAN (wire output), ordering by reading. Fix: run the result checks inside the `do` callback; return `-32602`/`-32603` style errors.

### R-08 Low: `os.Unsetenv` does not remove the token from `/proc/<pid>/environ`; docs list it as a mitigation

- Where: `cmd/privo/serve.go` (comment "gone from the environment"); `docs/todo.md` F24 criterion 5 and threat note (line ~310) list "variable cleared from the process environment" as a mitigation of the `/proc/<pid>/environ` exposure.
- What is wrong: RAN (`boot1.sh`): after startup, `/proc/<pid>/environ` still contained `PRIVO_BOOTSTRAP_TOKEN=`. The unset only helps child processes, and the local runner already uses a minimal environment, so it adds nothing against the threat it is listed under. The master key code logs a warning about this; the bootstrap path does not. The self-review states this limit correctly; the F24 text does not.
- Fix: correct the threat note and criterion 5; warn when the plain variable is used (as for `PRIVO_MASTER_KEY`); recommend `_FILE`.

### R-09 Low: the bootstrap install's audit row cannot be read through the API

- Where: `internal/store/audit.go` `RecentCalls` (`WHERE i.phase = 'intent'`) behind `GET /api/v1/audit/events`.
- What is wrong: the row is written correctly and atomically (RAN: `admin | system | bootstrap.token.install:<id> | allow | ok` in the database, marker `bootstrap.consumed = <id>`), but no endpoint returns `admin`-phase rows (this is true of all admin events, not only this one). On the shell-less deploy F24 targets, the owner has no way to see it. Fix: expose admin events, or say so in the F24 acceptance criteria.

### R-10 Low: any well-formed token is accepted as the bootstrap token, however weak

- Where: `token.Parse` checks shape and a public CRC only; `config.loadBootstrapToken` accepts the result.
- What is wrong: RAN (`boot3.sh`, case J): a token with id `aaaaaaaaaaaaaaaa` and secret `aaaa...` (52 times) plus a computed CRC installed and authenticated against the admin API as owner. An operator who builds a token by hand, or a script that does, gets an owner credential with no entropy for 24 hours. Fix: reject secrets that are not the output of `token generate` is impossible to prove, but a cheap repeated-character/low-distinct-symbol check, and a docs line, would catch the careless case.

### R-11 Info: smaller observations

- A gateway token already named `bootstrap` with no control token in the org makes the install fail on the unique-name index and the process exit 1 (RAN, case K); fail-closed, message free of secrets. Rare.
- `GET /version` is unauthenticated and returns version, commit, build date and Go version (RAN). Predates the work and the root page itself shows none of it; mentioned because F24 criterion 7 says the root shows no version.
- `PRIVO_BOOTSTRAP_TOKEN_FILE` permissions are not checked; a FIFO without a writer would block startup (operator-controlled). The 1 KiB read cap, directory, oversize and two-line cases all fail closed with a message that does not repeat the value (RAN).
- Hostile-input tests: the `tools` null guard in `upstream.load` is dead code today because the SDK drops null tools itself (mutation survived, as the test comment predicts).
- Gateway error text now includes fragments an upstream controls (the SDK's JSON errors, the multi-round-trip input key). It reaches the model only, which already reads the upstream's results.

## Checked and found sound

- Bootstrap: only the SHA-256 of the secret is stored; plaintext found nowhere in the data directory, logs or API (RAN, `boot1.sh`; acceptance test also scans). Log carries the id only. Malformed values stop `serve` with exit 2 and an error that does not repeat the value (RAN with a recognisable string). `Secret` prints `[redacted]` under the formatting paths; mutation of `String` and `LogValue` was caught.
- One-time semantics (RAN): restart with the variable still set warns and installs nothing; revoke then restart: still 401, not reinstalled; an org with an existing control token (made by `token create`) ignores the variable and the token gives 401; a second process on the same data directory is refused by the writer lease ("database is in use"). Check, insert, marker and audit row share one `BEGIN IMMEDIATE` transaction; revoked control tokens count; gateway-kind tokens do not. The installed token is owner/control only: usable on the admin API, 401 on `/mcp`. TTL is 24 h by the installer and capped at 7 days by the store.
- `token generate`: reads no configuration, opens no database; token on stdout, id and hints on stderr (RAN).
- Root page (RAN): the CSP style hash equals the SHA-256 of the served `<style>` bytes (recomputed from the response); no inline style attributes or scripts; `HEAD /` 200; `POST`/`PUT`/`OPTIONS /` 405; `/index.html`, `/favicon.ico`, `/api/v1/` 404; `GET /?x` 200 with nothing reflected; `GET //` redirects; `/mcp` still 401 and `/livez` 200, so `GET /{$}` shadows nothing. Headers: CSP, nosniff, X-Frame-Options DENY, no-referrer, noindex, `Cache-Control: no-cache`. No HSTS (belongs to the proxy).
- Cache hints (RAN, raw wire at 2026-07-28 and 2025-11-25): `tools/list`, `prompts/list`, `resources/list`, `resources/templates/list` all carry `ttlMs: 5000` and `cacheScope: "private"`, including empty lists; `resources/read` carries `ttlMs: 0` / `private`; an upstream's `cacheScope: public` and `ttlMs: 99999999` were overridden. The 5 s list TTL does not weaken revocation: every call re-verifies the token (2 s cache, `auth.CacheTTL`), re-checks pins and policy; a stale list only shows names. (Lists are per organisation, not per person, so "private" is conservative; the comment in `gateway.go` overstates that.)
- Wrapper: all three call sites and every return path produce `resultType: complete` for 2026-07-28 clients (including not-found and policy refusals) and none for older clients; `_meta` server info still added by the SDK; nil and `input_required` results fail closed; duplicate-key, deeply nested, bad content type and bad base64 upstream replies end as tool or protocol errors; policy, approval, argument canonicalisation and the audit intent all run before the upstream call and so before any result exists. Upstream resource URIs in a read reply cannot name another server: they are re-prefixed `privo://<alias>/` (RAN). A client choosing an older protocol version only loses the field. No data race in acceptance with the gateway built with `-race`.
- Name handling: duplicate public names are hidden (both), `__` and empty names do not collide across servers because aliases cannot contain `__`.

## What I ran

| Command | Result |
|---|---|
| `git --no-optional-locks log --oneline -8` / `status` | `a6742fa` on `10a0362`; clean |
| `go vet ./...` and `go vet -tags acceptance ./...` | clean |
| `go test -race -count=1 ./...` | all packages ok |
| `go test -tags acceptance -count=1 -timeout 150s ./test/acceptance/...` | ok (17.8 s) |
| same, `PRIVO_ACCEPTANCE_RACE=1` (gateway binary built with `-race`), scratch copy | ok (95 s), no race report |
| Binary experiments in `$HOME/review-scratch` (`boot1.sh`, `boot2.sh`, `boot3.sh`, `root1.sh`, `wire1.sh`) | see findings above |
| Scratch hostile-upstream suite (`rawhostile`, `review_test.go`, `review_http_test.go`) | R-01, R-02, R-05, R-06, R-07 |
| `mutate.py`: 36 mutations of guards in a scratch copy (wrapper, cache hints, rewriteRead, upstream null guards, bootstrap store/control/config, root page) | 35 caught by some test; 1 survives (the `tools` null guard, dead code today). B14 (bootstrap secret written to the first-boot log) survived the unit test and was caught only by the acceptance test; G15's first form did not compile and was redone |

## Where this differs from the author's self-review

- Agreed and verified: hash-only storage, install-once, atomic marker/audit, two-process race (the self-review said "code reading"; I ran it), owner/control scope, no secret in logs or errors, `/proc/<pid>/environ` limit (the self-review states it correctly), 2 s revocation lag, SDK refusal of upstream input requests.
- Missed by the self-review: R-01 (the hardening class is still open), R-02, R-03, R-04 (it noted the missing result-size cap but not the uncapped JSON body, the unbounded pagination or the pin growth), R-05, R-06, R-07, R-09, R-10.
- Overstated: "variable cleared from the process environment" is listed as a mitigation in `docs/todo.md` F24 although the self-review itself shows it is not one for `/proc`; "wrapper only rewrites one top-level key" (it also leaves `requestState` and upstream `_meta` untouched, and re-sorts keys); the claim that the 5 s list TTL "equals the revocation bound": the effective bound on authority is the 2 s auth cache, and list TTL affects display only.
- Not verified by the self-review and now verified: lease refusal of a second process, CSP hash against served bytes, cache headers at both protocol versions on the wire.

## Not covered and limits

- The `http` and `sse` upstream transports were not run against R-01. One step of the hostile-upstream work was stopped by a safety classifier in my session, and I deliberately stopped building further hostile-upstream payloads after it. So these cases exist in the scratch server but were NOT executed: pagination loops and oversized pages, list-changed floods, very large results, slow upstreams. R-04 is therefore argued from reading (SDK source and Privo code), not measured.
- No real Claude Code or Cursor client was used; client behaviour is inferred from the SDK client and the wire output.
- SQLite only (no Postgres engine exists yet); one platform (Linux arm64, Go 1.27.3).
- Docs and Coolify settings, the GitHub webhook and watch paths were not reviewed. `privo token create` being unaudited is already tracked in F24 and not re-reviewed. The admin API has no rate limit (self-review item 4), not tested.
