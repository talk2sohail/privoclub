# Independent review: F28 activity and telemetry (PR #13, 0.10.0)

- Date: 2026-11-15
- Reviewer: independent review agent
- Commits: `f07-personal-keys` = `b650528e2b9933478dafe72b803e543f73ebdbbb`, `f28-telemetry` = `75fc2c492a7ccc3489f2668224dfd3851e96a6c8`
- Scope: `git diff f07-personal-keys..f28-telemetry` (one commit, 42 files), ADR 0024 (and 0006, 0001, 0010 as context), `docs/todo.md` F28 acceptance criteria and threat note.
- Work copy: `/tmp/claude-0/-home-claude/381a408b-5da9-543e-8116-8894420450be/scratchpad/reviews/c/` (`repo` = clean checkout used for test runs, `exp` = experiments and mutations, `old` = `f07-personal-keys` for the downgrade check).

## Verdict

| Pull request | Verdict |
|---|---|
| #13 F28 telemetry (0.10.0) | **Approve with changes**: fix T-01 (unbounded, upstream-controlled diagnostic messages) and T-02 (oldest-first truncation makes the 7d/30d views wrong at modest volume) before merge; the rest can follow. |

The audit's guarantees are unchanged: I found no path where a call runs without its intent record, the completion is still queued before anything telemetry does, and the statistic is recorded only after it. Org scoping, permissions, SQL construction, the recorder's queue and shutdown order, metric label cardinality and the UI's text rendering held up. The problems are in what the diagnostic log will store (size and content), in what the aggregate views read when a window has more than 100,000 rows, and in tests that do not prove the redaction claim.

Counts: Critical 0, High 1, Medium 1, Low 5, Info 3.

## Findings

### T-01 High (confirmed by running): diagnostic messages store upstream-controlled text with no length limit; one hostile upstream can fill the disk and make the Log page exhaust memory

- Where: `internal/upstream/upstream.go:343-346` (connect failure), `:492-494` (list refresh failure), `:366` `diagnose`, `:989` `redactError`; `internal/telemetry/recorder.go:100-108, 151-238` (queue and repeat map bounded by count, not bytes); `internal/store/telemetry.go:94-112` (no cap on `message`); `internal/control/telemetry.go:225-268` and `web/src/pages/overview.tsx:203` (Log tab reads `limit=200` every 15 s).
- What is wrong: the message is `"connection failed: " + redactError(err.Error(), URL)`. With SDK v1.8.0, a non-2xx reply that carries a JSON-RPC error has that error's `message` put into the Go error (`go-sdk/mcp/streamable.go:2590-2595`, `checkResponse`), and list refreshes surface JSON-RPC error messages the same way. So the upstream writes free text of its choosing into the diagnostic log, bounded only by `PRIVO_MAX_RESPONSE_BYTES` (16 MiB by default). Nothing truncates it before the queue, the repeat map, the database row or the API response.
- Scenario: a hostile or compromised upstream answers `initialize` with HTTP 401 and a 16 MiB JSON-RPC error message. The manager retries every 30 s (`RetryMax`). Each repeat window writes the event once and then a "repeats" row with a copy of the same message, so about 2 rows a minute: about 46 GiB a day in the SQLite file, and scheduled backups copy it (`PRIVO_BACKUP_KEEP` 7). An upstream that sends a different message each time (for example with a counter in it) is never collapsed; the refresh path can run every `RefreshGap` (5 s) on `list_changed`. When the disk fills, audit intent writes fail and every call in the org is refused (fail closed), so one server takes the whole gateway down. Before that point, an admin opening Activity → Log asks for 200 rows; at 16 MiB each, the gateway builds a response of about 3 GiB in memory, again every 15 s.
- How established: ran a package test, `TestReviewUpstreamErrorTextInDiagnostics`, in `exp/internal/upstream` (removed afterwards so that the copy's own tests stay clean; "What I ran" below describes it). An httptest upstream answers 401 with a JSON-RPC error of about 1 MiB. Result: a diagnostic row of 1,048,758 bytes, stored as is. The 46 GiB a day and the Log response size are worked out from the code (retry and repeat timing, 16 MiB cap), not measured end to end.
- Fix: cap the message (for example 300 to 500 bytes, cut on a rune boundary, with "…") inside `Recorder.Diagnose` so that every source is covered, and cap `Server` the same way. Put the upstream-supplied part behind a fixed prefix and run it through `upstream.RedactText` as well as `redactError` (see T-05). Consider storing the error class rather than the text for refresh failures. Add a test with a long body and a 401 JSON-RPC error.

### T-02 Medium (confirmed by running): over 100,000 rows, summary and chart keep the oldest rows and drop the newest, so the 7d and 30d views show a false drop to zero

- Where: `internal/store/telemetry.go:149` (`order := "ts_ms ASC, id ASC"` for aggregation, `LIMIT MaxStatRows+1`); used by `internal/control/telemetry.go:114` (summary) and `:162` (timeseries); UI note at `web/src/pages/overview.tsx:137`; the timeseries `truncated` flag is not shown at all.
- What is wrong: when a window holds more than 100,000 rows, the read keeps the first 100,000 in time order. The summary leaves out the most recent calls, and the chart's latest buckets come back empty. 100,000 calls in 30 days is 2.3 calls a minute; in 7 days, 10 a minute. A small team meets that, so the 7d and 30d views will usually be wrong in exactly the part an admin looks at.
- Scenario (measured): 150,000 calls spread evenly over 7 days, window 7d, 6 h buckets. Result: `truncated=true`, newest row read 2026-11-13T09:59Z (now was 2026-11-15T13:20Z), and the last 10 buckets (2.5 days) show 0 calls. An admin reads that as an outage or as traffic stopping. The UI line says "the figures cover the first 100,000", which does not tell the reader that the most recent ones are missing.
- How established: ran `exp/internal/telemetry/review_trunc_test.go` against a real store.
- Fix: aggregate in SQL (counts and buckets with `GROUP BY`; percentiles from a sample or by reading the sorted totals per group), or at least read newest first for aggregation, say "the most recent 100,000", and show `truncated` on the chart too. Add a test that puts more rows than the limit into a window and checks the newest bucket.

### T-03 Low (confirmed by running): each summary or series request reads 100,000 full rows into memory; a read-only auditor can exhaust memory with concurrent requests

- Where: `internal/store/telemetry.go:128-182`, `internal/telemetry/summary.go:53-116, 181-211`; the Overview polls summary and timeseries every 30 s (`overview.tsx:116-117`).
- Measured on 100,000 rows (the read, `Summarize` by tool and `Series`): 0.9 to 1.9 s, 318 MB allocated in total, peak heap about 108 MB, about 44 MB still live while the rows are held. Each open Overview tab costs two of these every 30 s. `audit.read` is a read-only role (auditor), and there is no limit on concurrent telemetry requests: about 20 parallel requests come to roughly 2 GB.
- Fix: aggregate in SQL (the same fix as T-02), or select only the needed columns into compact structs, keep a semaphore of one or two telemetry aggregations at a time, and cache an answer for a few seconds per (org, window, by).

### T-04 Low (read): setting retention to 0 keeps the existing statistics forever, and the API keeps serving them

- Where: `cmd/privo/serve.go:127-145`. The prune goroutine starts only when `TelemetryRetentionDays > 0`, and the endpoints still read the tables (`internal/control/telemetry.go`).
- What is wrong: an operator who turns statistics off (for example for privacy: they record who called which tool when) expects them to stop and to age out. With `0`, the rows already recorded are never deleted, and `/api/v1/telemetry/calls` still lists them, with `recording: false`. ADR 0024 says "`0` records nothing", which is true, but the threat note frames retention as the bound on how long this data lives.
- Fix: with 0, prune everything once at start (or keep pruning with a cutoff of now). Alternatively, document that the rows stay and how to delete them.

### T-05 Low (confirmed by running): the redaction in diagnostics is narrower than the threat note claims; an upstream's echoed key is stored and shown to auditors

- Where: `internal/upstream/upstream.go:989-994` `redactError` replaces only the exact configured URL string. `upstream.RedactText` (`:864`), which removes query, user info and fragment from every URL in the text, exists and is used for the gateway's own logs (`internal/gateway/gateway.go:661`) but not here.
- Scenario (ran): an upstream configured with `…/mcp?api_key=<secret>` answers 401 with `{"error":{"message":"invalid api key <secret> …"}}`. The diagnostic message, and the server status, contain the secret verbatim. URLs other than the configured one (an SSE endpoint URL with a session id, a redirect target, a URL normalised differently by `net/url`) are not redacted either. The server status already showed this text to the same roles (auditors have `servers.read`). What is new: it is now persisted for up to 400 days, copied into backups, and listed in a log.
- Fix: use `RedactText` (as well as the exact-URL replacement), cap the length (T-01), and correct the threat note ("messages carry no credentials" cannot be promised for upstream-supplied text). Optionally, scrub the server's sealed header and query values from the text before storing it.

### T-06 Low (confirmed by running): `encodedSize` re-encodes every result on the request path, roughly doubling the cost of large results

- Where: `internal/gateway/gateway.go:456, 511-517`.
- Measured: a 16 MiB text result adds 90 ms and 67 MB of peak heap. If the text is full of `<`, `>`, `&` or `"` (HTML-escaped, so 55 MB of JSON), it adds 0.97 s of CPU and 264 MB of peak heap per call, the same as the SDK's own `json.Marshal` of the relay. So a hostile upstream gets twice the R-04-class amplification it had. The time is not in `total_us` (measured before), so the statistics hide it. ADR 0024 says "a call never waits for … its statistic", but the call waits for this encoding.
- Fix: measure the size from the bytes the SDK actually writes (a counting `ResponseWriter` or transport hook), or estimate from the content lengths without encoding. Failing that, compute the size after the response is sent, or skip it above a threshold.

### T-07 Low (confirmed by mutation): tests do not prove three of the claims made for them

- `TestUpstreamDiagnostics` (`internal/upstream/diagnostics_test.go:40, 50, 79`) asserts that the key in the URL is not in the message, but the fake upstream answers 502. That is a transient status, so the SDK error never quotes the URL and the assertion cannot fail. Mutation: replacing `msg` with the raw `err.Error()` at `upstream.go:346` → the test still passes. The PR's threat note cites this test ("The test puts a key in the server's URL and checks that it does not appear").
- Key-refused diagnostic (`gateway.go`, `class == "key_refused"`): no test at any level. Mutation that disables it → gateway, grants and control unit tests and the acceptance suite stay green (acceptance has no `CategoryGrant` check).
- Cross-org pruning of `diagnostics`: `TestPruneTelemetry` has no diagnostics row in org b. Mutation that drops `org_id = ?` from the diagnostics delete → the store tests stay green. The PR says "tests cover reading and pruning across orgs".
- (For contrast: setting `ResultBytes` to 0 survives the unit tests but is caught by the acceptance test, which is fine.)
- Fix: make the upstream answer a status that puts the URL into the error (for example a 401 with a JSON-RPC body that echoes the request URL, or a connection to a closed port), add a gateway test for the key-refused diagnostic, and add an org-b diagnostic to `TestPruneTelemetry`.

### T-08 Info (read and ran a bind check): the metrics address check compares strings only; the Coolify guide recommends `0.0.0.0:9464`

- `internal/config/config.go:226` refuses only the literal `PRIVO_LISTEN` string. I checked on this Linux host (IPv4): `:P`, `0.0.0.0:P`, `[::]:P` and `127.0.0.1:P` against a public listener on `:P` or `127.0.0.1:P` all fail with EADDRINUSE. The public listener is opened first (`serve.go:185-196`), so the same port cannot be shared. IPv6 was not available here, so this is not shown for IPv6. The remaining exposure is by design: `docs/deploy-coolify.md:109` says to bind `0.0.0.0:9464`, which lets any container on that Docker network (and a host port mapping, if someone adds one) read server aliases, call volumes by outcome, upstream up or down, and the exact build version, without sign-in. Say this in the guide, and consider an optional bearer token or an allow-list of source addresses for the listener.

### T-09 Info (read): small inconsistencies in windows and buckets

- `internal/telemetry/summary.go:172`: `>= MaxBuckets` allows at most 999 buckets; the ADR and the todo say "at most 1,000".
- The summary covers `[now - window, now]` (`control/telemetry.go:113`), while the series covers `[to - window, to)` with `to` rounded up to the next bucket (`:152-153`). For 30d with 24 h buckets, the chart starts up to a day later than the totals cards, so the two can disagree.
- `telemetryLogs`: `limit` over 500 falls back to 100, not 500.
- `parseWindow` with a huge day count (`n*24h`) overflows silently. The result is still range-checked, so it is harmless.

### T-10 Info (read): approval waiting time counts as call latency

`decide_us` and `total_us` include up to `PRIVO_APPROVAL_HOLD` (25 s by default) of a person deciding. Approved calls count as "ran", so they inflate a server's p95/p99 on the Overview and its `privo_mcp_call_duration_seconds` histogram. "Which servers are slow" then partly measures approvers. Consider excluding the hold from `total` for the per-server latency, or recording it as its own phase.

## Checked and found sound

- **Audit guarantees (ADR 0006).** In `run`, the order is still decide → intent (call refused if the intent write fails) → session → upstream → completion queued. The statistic is recorded after the completion is queued and cannot fail or block the call (`Recorder.send` is non-blocking under an RLock; `Close` takes the write lock before closing the channel, so there is no send on a closed channel). `OnLost` is called from the audit writer goroutine and only counts or queues. The `flushBackoff` const to var change is test-only. One behaviour change not mentioned in the PR: with `CallTimeout <= 0`, `ErrKeyRefused` is now also turned into the key-refused account error (before, only with a timeout). It is an improvement, and it does not affect the audit.
- **No arguments, results or error texts in statistics.** `CallStat` carries lengths only. `errorClass` returns fixed strings (`TestErrorClasses` covers an unknown error mapping to `transport`). Item, client and server are the same values the audit already stores.
- **Org scoping.** Every store method takes `OrgID`, and every query has `org_id = ?` (CallStats, Diagnostics, TelemetryStorage, PruneTelemetry for both tables). The handlers use `p.OrgID`. `memberNames` uses `ListUsers(org)`.
- **SQL via `Sprintf`.** Only fixed column names, a fixed ORDER BY and one of two constant table names are interpolated; every value is bound. Not injectable.
- **Permissions.** All five routes are wrapped with `auth.PermReadAudit` (`control/telemetry.go:21-25`). Owner, admin and auditor have it; member and approver do not (`TestTelemetryAPINeedsAuditRead` covers all five paths).
- **Parsing.** Negative or zero windows and buckets are rejected. A bucket under 1 minute is rejected. A huge bucket gives `to - from > 31d` and is rejected. `before` and `since` need RFC 3339. `limit` is clamped. Outcome, category, level and `by` are allow-listed.
- **Nearest-rank percentile.** `ceil(p*n/100)` with integer arithmetic is correct for n = 1, 2, 100 and 101. Bucket indexing cannot go out of range because `from` and `to` are both aligned to the bucket.
- **Metric labels.** server (admin-defined aliases), method (3 fixed values) and outcome (4 fixed values). Clients and upstreams cannot add series; there are no tool or member labels. Label escaping covers `\`, `"` and line feed, as the text format requires. A histogram value equal to a bound falls into that `le` bucket, which is correct.
- **Recorder memory by count.** The queue holds 10,000 items and the repeat map 10,000 keys; entries leave the map one window after they are written. (By bytes, see T-01.)
- **Shutdown order** (deferred, so run last to first): site, metrics listener, audit writer (its `OnLost` still finds the recorder open), backup job, upstream manager, prune goroutine (stopped and waited for), recorder (flush), store. A handler still running after the HTTP shutdown times out only drops a statistic (`closed` flag).
- **Migration 00009 and downgrade.** Both tables have `org_id` and a primary key `(org_id, id)`, so a retried flush does not count twice. The Down migration drops both tables. Ran: a database migrated by the f28 store opens cleanly with the f07 (0.9.x) store, so the downgrade note in the deploy guide is true.
- **XSS in the new UI.** Server, tool, member, client and message values are rendered as JSX text (escaped), and SVG `<title>` is JSX text too. No `dangerouslySetInnerHTML`; the existing source test forbids such sinks. Links use `URLSearchParams`.
- **`web/dist` matches the source.** A rebuild with vite gives the same hashes (`index-DGSqH-S9.js`, `index-BaujMBVY.css`).
- **Races.** The whole suite under `-race` is green.

## What I ran

- `go test -race -count=1 ./...` on `repo` (f28): all packages ok, exit 0 (store 253 s, web 219 s, control 90 s).
- `go test -race -count=1 -tags acceptance -timeout 10m ./test/acceptance/`: ok in 136 s.
- `web/`: `npx vitest run`: 7 files, 50 tests passed. `npx tsc --noEmit`: clean. `npx vite build` into a scratch directory: the same asset hashes as the committed `web/dist`.
- Experiments in `exp/`:
  - `internal/telemetry/review_trunc_test.go` (T-02, T-03): 150,000 rows over 7 days. The read returns the oldest 100,000, and the last 10 six-hour buckets are 0. The read plus aggregation takes 0.9 to 1.9 s, 318 MB allocated, peak heap about 108 MB.
  - `internal/upstream` review test (T-01, T-05): a 401 JSON-RPC error with the key and 1 MiB of text gives a 1,048,758-byte diagnostic that contains the key. The server status contains it too.
  - `internal/gateway/review_size_test.go` (T-06): `encodedSize` on a 16 MiB result. Plain text: 90 ms, 67 MB peak. HTML-escaped text: 0.97 s, 264 MB peak (55 MB of JSON).
  - `bindtest/` (T-08): port-sharing bind attempts on IPv4, all refused with EADDRINUSE.
  - `cmd/reviewopen` in `exp` and `old`: a database created by f28 opens with the f07 store.
- Mutations, one at a time, each reverted afterwards: redaction removed from the connect diagnostic (survived); key-refused diagnostic disabled (survived the unit tests; the acceptance suite has no check for it); `ResultBytes = 0` (survived the unit tests, caught by acceptance); diagnostics prune without org scope (survived).
- PR #13 description read with `gh api` (read-only), after the findings were drafted.

## Where the PR description is wrong or incomplete

- "Memory is bounded: the queue (10,000 rows), the repeat map (10,000 keys)": bounded by count only. Each item and each key can carry a message of up to 16 MiB of upstream text (T-01).
- "Diagnostic messages are fixed texts or upstream errors with credentials removed. The test puts a key in the server's URL and checks that it does not appear": the redaction covers only the exact configured URL, upstream-supplied text passes through (T-05), and the cited test cannot fail (T-07).
- "tests cover reading and pruning across orgs": pruning of `diagnostics` across orgs is not covered (T-07).
- "`truncated` when more than 100,000 rows fall in the window": true, but it leaves out that the newest rows are the ones dropped, and the chart never shows the flag (T-02).
- "A call never waits for its statistic": it waits for `encodedSize` (T-06).
- "Recording off (`0`) … nothing is stored": true for new rows, but rows from before are kept forever and still served (T-04).
- "Startup refuses the address `PRIVO_LISTEN` uses; another spelling of the same port fails to bind": confirmed for IPv4. The check itself is only a string comparison, and the Coolify advice binds to all interfaces (T-08).
- Not mentioned: the `ErrKeyRefused` mapping now also applies without a call timeout (a client-visible behaviour change, beneficial).

## Not covered / limits

- I did not build a hostile upstream that floods `list_changed` and fails `tools/list` with distinct large messages. That refresh-path rate in T-01 is argued from the code (`RefreshGap` 5 s), not measured. The 46 GiB a day figure is worked out, not measured. I did not fill a disk or crash a process.
- IPv6 binding behaviour (T-08) was not tested: there is no IPv6 in this environment.
- I did not start the real binary by hand. The acceptance suite covered the end-to-end flow, and my experiments ran at package level against the real SQLite store and real SDK transports.
- I did not repeat the 23 mutations the author lists. I ran four of my own, aimed at claims that looked weak.
- Postgres (planned) is not in scope. `PruneTelemetry` uses SQLite's `rowid` and will need rewriting for it.
