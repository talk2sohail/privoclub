# Independent review: F24 bootstrap token (second pass)

- Date: 2026-11-03
- Reviewer: independent review agent (did not write the code)
- Scope: the bootstrap token (F24 criteria 1-8 and threat note), the fixes for R-03 and R-08 to R-11 from `2026-10-29-independent-review-f24-cache-hints-result-type.md`, the self-review `2026-10-29-f24-bootstrap-self-review.md`, and the open sub-item "`privo token create` is not audited".
- Method: read the code (`internal/control/bootstrap.go`, `internal/store/bootstrap.go`, `internal/config/config.go`, `internal/token/token.go`, `internal/auth/auth.go`, `cmd/privo/{serve,bootstrap}.go`, `internal/server/root.go`, `Dockerfile`, `docs/deploy-coolify.md`); ran `go test -race` on control, config, token, store, cmd/privo, server and auth (all pass); built the binary and ran a real first boot in a scratch directory; wrote throwaway probe tests in a copy of the tree under the session scratchpad (nothing in the repo changed; the only new file is this report).
- Not run: Postgres (not present in the tree, so the concurrent-boot analysis for it is by reading), a real container (`/proc/<pid>/environ` was already measured by the previous review and is not re-measured; macOS has no `/proc`).

## Verdict

**Approve with changes.** The install-once, hash-only, atomic-marker, scope and logging properties hold. The previous review's R-03 and R-08 to R-11 are fixed and the fixes work. Two Medium findings remain: the bootstrap check does not know about the person-based setup of F05 (R-01), and the CLI mints owner tokens with no audit row (R-02).

Counts: Critical 0, High 0, Medium 2 (R-01, R-02), Low 4 (R-03 to R-06), Info 1 (R-07, several small notes).

## Confirmed findings

### R-01 Medium: a bootstrap token installs on an organisation that was set up through F05, because "needs a bootstrap" only looks at control tokens

- Where: `internal/store/bootstrap.go:69-75` (install) and `:107-116` (`BootstrapNeeded`) test only `api_tokens WHERE kind = 'control'`. The F05 path (`privo admin setup-link`, `ClaimSetup` in `internal/store/signin.go:145`) creates a `users` row with role owner, an identity and a session, and never an `api_tokens` row.
- What is wrong: criterion 2 says "an org that has been administered does not need a bootstrap". An org administered only through the browser (the path the Coolify guide now prefers, `docs/deploy-coolify.md` "Sign in with GitHub") has zero control tokens, so it still counts as virgin.
- Scenario: the owner deploys without the variable, claims the org with the setup link, and uses it for months. Later `PRIVO_BOOTSTRAP_TOKEN` appears again (a stale Coolify template, a restored environment, a "just in case" recovery that was copied from the old guide, or anyone who can edit Coolify's environment). On the next restart it installs a fresh owner control token on a live, populated org, valid 24 hours. The admin-events row exists, but nothing in the UI warns, and the owner token can create more owner tokens, change user roles and disable users (`PermManageMembers`, `PermManageTokens`).
- Established: RAN. Probe in the scratch copy: after `CreateSetupLink` + `ClaimSetup` succeeded, `BootstrapNeeded` returned `true` and `InstallBootstrapToken` returned `installed`.
- Fix: count users (and live sessions or identities) in the same transaction as the control-token count in both `InstallBootstrapToken` and `BootstrapNeeded`; return `BootstrapNotNeeded` if any `users` row exists. Add a test next to `TestInstallBootstrapToken`: "refused once a setup link was claimed".

### R-02 Medium: `privo token create` mints owner credentials with no audit row; the other CLI writers do audit

- Where: `cmd/privo/serve.go:255` calls `control.IssueToken` and prints; `IssueToken` (`internal/control/control.go:513-551`) only calls `CreateToken` and has no audit parameter. The admin API audits after the fact in `createToken` (`:569`, `adminEvent`). In contrast `cmd/privo/admin.go:93` (invite) and `:173` (setup-link) write an admin event with actor `cli` in the same transaction as the row.
- Risk assessment: the command needs host access (`docker exec` or the data directory plus the master key). That is a trusted position, so this is not an outside attack path. The gap is accountability: the only command that can mint an owner control token with a long TTL (`cfg.TokenTTL`, not the 24 hours of the bootstrap) leaves no trace, while a setup link, which is weaker (one hour, one use, needs a GitHub account), does. Anyone with host access can silently create a durable owner credential, and `?kind=admin` will show nothing. It is also the "break-glass" path the guide sends people to when the 24 hours pass, which makes the missing trace more, not less, important. The scenario that matters: an insider or a compromised CI step with `docker exec` rights mints an owner token, uses it through the API (those uses are audited as the token's name, but the token's creation is not), and the token looks like it was always there.
- Recommendation: add an `Audit` hook to `IssueToken`/`CreateToken` so the token row and an admin event (`token.create:<kind>:<role>:<name>`, actor `cli`, actor_id `cli`, client `cli`) commit in one transaction, as `CreateSetupLink` does; keep the API's current best-effort event for API callers or move it into the same transaction as well. Record the token id (not the secret). Refuse the audit-less path in the CLI: fail closed if the event cannot be written. Add the named test from the todo item. Tick "Discovered: tokens created by `privo token create`" only once the row is readable through `?kind=admin`.

### R-03 Low: a leftover bootstrap token outlives the person-based setup and is not retired by it

- Where: `internal/store/signin.go` `claim` / `ClaimSetup` does not look at `bootstrap.consumed`; the doc asks the operator to revoke by hand (`docs/deploy-coolify.md` step 4).
- What is wrong: on a deploy where both are used (bootstrap first, setup link later, which the guide permits), the 24-hour owner token stays valid after a person has become owner through GitHub. It has no second factor, no GitHub account behind it, and the person `bootstrap`. It is exactly the credential the F05 model is meant to replace.
- Fix: when `ClaimSetup` succeeds, revoke the token named in the `bootstrap.consumed` setting if it is still live, and write a `bootstrap.token.retire` admin event. Optionally shorten `BootstrapTokenTTL` to a few hours (its job is to mint one token); 24 hours of an owner secret sitting in an environment variable is long.

### R-04 Low: `token.Weak` is bypassed by any 16-symbol pattern or by words

- Where: `internal/token/token.go:105-137`.
- Established: RAN. A secret made of `bdfhjlnprtvxz246` repeated (16 distinct symbols, no run, steps of 2) is accepted by `Parse` and not rejected by `Weak`. Repeating a pronounceable word (`correcthorsebattery...`) was caught only because of the 16-distinct rule; a pattern with 16 or more distinct symbols passes. Entropy of such a secret is a few bits, and it is an owner credential for 24 hours, reachable from the internet at `/api/v1/*`.
- Impact: the guard is documented as "the careless case" and the sentence is honest. The residual risk is a person following the guide's step 1 loosely, for example with a script that derives the token from a name. Low, since `token generate` is the documented path and an attacker needs the operator to hand-make the token.
- Fix: the only robust check is to not accept an operator-chosen secret. Option: make the bootstrap value a seed the server hashes to a 256-bit secret only if the seed is itself a `token generate` output carrying a keyed check (for example an HMAC tag under a key derived from the master key is not possible before first boot, so use a version byte plus an entropy test such as per-symbol chi-square over 52 symbols and a bigram check). Short of that, add a bigram-repeat test (any 4-gram occurring more than twice) and say in the guide that only `token generate` output is supported.

### R-05 Low: the bootstrap value is validated before anyone knows whether it is needed, so a malformed leftover stops an administered org

- Where: `cmd/privo/serve.go:50-55` exits 2 on any `LoadBootstrapToken` error other than `ErrBootstrapUnreadable`; the R-03 fix of the previous review only softened the unreadable-file case.
- Scenario: the operator replaces the variable with a placeholder (`changeme`, empty-looking text with quotes) while cleaning up, or Coolify's "shown once" masking writes a masked value back. The org is long since set up, but `serve` refuses to start (exit 2) and the container crash-loops; the healthcheck and `token create` still work, so recovery is possible, but the failure is an outage caused by a variable that cannot matter.
- Fix: treat a malformed or weak value like an unreadable file: remember the error, ask `BootstrapNeeded`, refuse only when the org still needs a token, otherwise warn (and still never print the value).

### R-06 Low: a stale value can wedge the first boot by colliding with an existing token id

- Where: `internal/store/bootstrap.go:85-91` inserts with the id taken from the operator's token. The unique index `api_tokens_id` is global (`00001_init.sql:35`).
- Scenario: an org with a gateway token (no control token yet, for example one made by `token create --kind gateway`) whose id equals the id of the generated bootstrap token cannot happen by chance (80 bits), but the id is operator-supplied: a token reused across two installs of the same data volume, or a hand-made id, makes the install return a conflict, `serve` exits 1, and the process crash-loops with a message that names the SQL conflict, not the variable. Rare and fail-closed; listed because the R-11 fix covered the name collision and not the id collision.
- Fix: map `ErrConflict` from the insert to a message that names `PRIVO_BOOTSTRAP_TOKEN` and says to generate a new token.

## Info (several small notes)

### R-07 Info

- The unset in `serve.go:65-71` runs only when a token was loaded. If the file form is unreadable and the plain variable is also set, the plain value stays in the environment of stdio children (the local runner uses a minimal environment, so this is theoretical). Unset the variable unconditionally.
- Token file mode is not checked (a world-readable file on a shared host is the operator's choice). A warning on mode `& 0o077 != 0` would match the master-key file handling if that has one.
- The plain variable is also visible in `docker inspect` to any user in the docker group, besides `/proc/<pid>/environ`; the guide mentions only the latter.
- Restoring a database backup older than the install, with the variable still set, reinstalls the bootstrap token (the marker is in the same database). The new owner token lasts 24 hours. Same trust level as the backup itself; worth one line in the backup docs.
- `adminEvent` (`control.go:174`) writes the audit row after the action and only logs on failure, so a bootstrap-token action can succeed without a row if the write fails. This is general to the admin API, not new in F24.
- The guide's `curl -H "Authorization: Bearer $BOOT"` puts the token in `curl`'s argument list, visible in `ps` on the operator's machine for the call's duration; `curl -H @-` or a netrc-style config avoids it. Operator laptop only.
- The root page has no HSTS header; TLS terminates at the proxy, which can add it. Not an issue for the page itself.

## Checked and found sound

- **Install-once under restart, revoke, expiry.** `bootstrap.consumed` is inserted in the same `BEGIN IMMEDIATE` transaction as the token and the audit row (`store/bootstrap.go:59-99`); the marker is read first and wins over the control-token count; nothing in the tree deletes it (`grep` for `DELETE FROM settings` / `DELETE FROM api_tokens`: none; no pruning of `api_tokens`). Real boot, restart with the variable set, revoke then restart, and expiry all end in `Consumed` or `NotNeeded` (tests pass; I also ran the first boot by hand).
- **Concurrent first boot.** The writer lease stops a second `serve`; the install is one `BEGIN IMMEDIATE` transaction with a primary-key marker insert, so even `token create` on a shared handle (`OpenShared`) cannot interleave. A loser fails, never double-installs. (Postgres not exercised; the primary key on `settings` would make the loser fail, by reading.)
- **Scope.** The stored token is `owner` + `control` (the store rejects anything else, and a TTL over 7 days). `auth.MCPVerifier` refuses control tokens (real run: `POST /mcp` with the bootstrap token gives 401), and the admin API refuses gateway tokens. Expiry is enforced both on the store lookup and on the cache hit (`tokenExp`, `auth.go:98-101`); an unparsable stored timestamp reads as the zero time, i.e. expired. Revocation is seen within `CacheTTL` (2 s).
- **Secret hygiene.** Only `SHA-256(secret)` is stored. The install event carries the id only. A real run with the plain variable: the token string appears zero times in the log; `config show` prints `set` and the source, never the value; the `Secret` type redacts in `String`, `GoString`, `LogValue` and `MarshalText`; every error path in `LoadBootstrapToken`, `InstallBootstrapToken` and `installBootstrap` names the variable, not the value. `settings.go` marks the plain variable `Secret`, so it is refused in the config file. `Parse` uses a constant-time CRC compare and `Verify` a constant-time hash compare.
- **File handling.** `os.Stat` (follows symlinks) must be a regular file before `Open`, so a FIFO cannot block startup; read capped at 1 KiB; `TrimSpace` handles trailing newline, CRLF and surrounding blanks; interior whitespace or NUL fails `Parse`. The stat/open gap is operator-controlled only. File wins over the variable, and `BootstrapTokenSource` agrees with that choice for the warning.
- **Tenancy.** Every query in the bootstrap code takes the typed org id; marker, count, name check and insert are all `org_id`-scoped; `TokenByID` is global by design and the id index is globally unique, so one org's token cannot answer for another's. `revokeToken` checks `target.OrgID`.
- **Fixes from the previous review.** R-03 (config coupling): `config.Load` no longer reads the variables, and the healthcheck and `token create` ignore them (tests pass). R-08: docs corrected, warning present (seen in the real run). R-09: `?kind=admin` returned the install row (real run). R-10: `Weak` is wired into both the loader and the installer (but see R-04). R-11: FIFO refused; a live token named `bootstrap` renames the new one rather than failing.
- **Root page.** `GET /{$}` only; static; CSP `default-src 'none'` with a hash for the one inline style, `frame-ancestors 'none'`, `nosniff`, `X-Frame-Options: DENY`, `no-referrer`, `noindex`; no request data is read. Headers verified from a real response.
- **Dockerfile and healthcheck.** No `ARG`/`ENV` carries a secret; the healthcheck subcommand does not load the bootstrap variables; the image runs as nonroot with no shell.
- **`privo token generate`.** Reads no configuration and opens no database; the secret goes to stdout and the hint to stderr.

## Suggested order

R-01 and R-02 before the next release (both small, both testable). R-03 with R-01 since they share the sign-in/bootstrap seam. R-04 to R-06 and R-07 at the owner's discretion.
