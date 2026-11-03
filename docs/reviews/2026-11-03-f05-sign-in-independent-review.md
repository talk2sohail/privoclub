# Independent review: F05 first slice, admin sign-in with GitHub

- Date: 2026-11-03
- Reviewer: independent review agent
- Reviewed code: commits `509c3ec` ("partial github sigin") and `c7801b0` ("admin ui and signin"), on `main` at the HEAD current at the start (working tree clean). Source, tests and docs were not modified; this file is the only write.
- Scope: ADR 0016, `docs/todo.md` F05 first slice and threat note; `internal/githubauth/`, `internal/web/{web,signin,secrets}.go` and templates, `internal/store/signin.go`, migration `00004_sign_in.sql`, `internal/config/web.go`, `cmd/privo/admin.go`, `cmd/privo/serve.go` wiring, `internal/approval` Decider. The Preact app and `/admin/api` bridge are covered by another review and were touched only where session issuance meets them.
- Method: read every path adversarially against the ADR and the acceptance list, ran `go test -race ./internal/web/... ./internal/githubauth/... ./internal/store/...` (all pass). No finding below needed a probe; each is established by reading and is marked "read" accordingly. No network calls were made.

## Verdict

**Approve with changes.** The core design holds: state and PKCE binding with a cookie nonce, single-use state, hash-only storage, numeric-id identity, atomic setup claim, fail-closed origin check from configuration, live role reads. No Critical or High finding. One Medium (sign-in availability) should be fixed before the slice is called done; the Lows are cheap.

Counts: Critical 0, High 0, Medium 1 (R-01), Low 3 (R-02 to R-04), Info 2 (R-05, R-06).

## Findings

### R-01 Medium: unauthenticated callers can fill the 500-state cap and lock every sign-in, setup and invite for ten minutes

- Where: `internal/web/signin.go` `start` (lines 55-100), `internal/store/signin.go` `CreateOAuthState` (the `n >= limit` check), `internal/web/web.go` `sameOrigin`.
- What is wrong: the cap is the only defence against disk growth, and it is global per org across all purposes. `POST /auth/github/start` needs no session, and its only gate is `Origin == public origin` (or `Sec-Fetch-Site: same-origin`). Both headers are set by the caller outside a browser, so the "origin rule" does not restrict a script at all. Each request inserts one row living 10 minutes.
- Scenario: `for i in $(seq 600); do curl -XPOST -H 'Origin: https://privo.example.com' https://privo.example.com/auth/github/start; done`, repeated every 10 minutes. Every legitimate `start` then gets 503 "Too many sign-ins in progress". New and expired sessions cannot be replaced, the owner cannot claim a setup link, and invitees cannot join. Existing sessions keep working until they expire (12 h max), so approvers are locked out within a day. It also shares the cap with `PurposeConnect`.
- How established: read. The existing test `TestStartCapReturns503` proves the cap works, which is the same property the attack uses.
- Fix: (a) per-source-IP and global rate limit on `/auth/github/start` (the trusted-proxy address handling needs a decision); (b) keep a reserved share of the cap for setup and invite purposes, which require a valid secret and so cannot be attacker-filled (count only `signin` rows against the open cap); (c) shorten the lifetime of an unstarted state, or create the row on a cheaper path. Add a test where 500 anonymous signin starts do not block `PurposeSetup`.

### R-02 Low: any GitHub account can write unlimited `signin.denied` audit rows

- Where: `internal/web/signin.go` `denied` (writes an admin audit event for every non-member), `finishSignIn`.
- What is wrong: a stranger with any GitHub account can loop start, authorize (GitHub re-authorizes silently after the first grant), callback. Each pass appends a hash-chained audit row with actor = their login. There is no rate limit on the callback path and `denied` writes through `Audit.Admin` each time. With R-01's missing limiter, tens of thousands of rows per hour are possible; this grows the audit table (a table that is meant to be kept) and buries real denials.
- How established: read.
- Fix: rate-limit by GitHub id or source IP; or audit a denial for a given `github:<id>` at most once per minute (collapse into a counter on the last row).

### R-03 Low: a cross-site link can cancel a victim's sign-in in progress

- Where: `internal/web/signin.go` `callbackHandler`: `s.clearCookie(w, s.stateCookie)` runs before `UseOAuthState`, on every request whose cookie and `state` have the right shape.
- What is wrong: `SameSite=Lax` cookies are sent on top-level cross-site GET navigations. A page that navigates the victim to `/auth/github/callback?state=<any 43 chars>` while the victim is mid sign-in clears their nonce cookie. The real callback then finds no cookie and fails with "not started in this browser". The real state row is not consumed (good), but the user must start again. Repeatable by the attacker only while they control the victim's navigation, so this is nuisance, not takeover.
- How established: read.
- Fix: clear the cookie only after a successful `UseOAuthState` (or when the lookup proves the state was theirs). A failed lookup should leave the cookie alone.

### R-04 Low: self-approval compares `person` exactly, while the owner's default person is forced to lower case

- Where: `internal/approval/approval.go` `Decide` (`by.Person == a.RequesterPerson`), `internal/web/signin.go` `finishSetup` (`strings.ToLower(p.Login)`), `users_org_person` unique index (case-sensitive).
- What is wrong: the rule "requesters cannot decide their own calls" depends on a human-chosen string matching exactly. An owner who created agent tokens with person `Alice` and then signed in as GitHub `Alice` is stored as person `alice`; the comparison is false and self-approval is allowed even though the org setting forbids it. `Alice` and `alice` can also exist as two users. ADR 0016 lists the person link as a residual risk, but the case and whitespace mismatch is an avoidable hole inside it, and the setup page never tells the owner which person name will be used.
- How established: read (comparison code and claim code); not run.
- Fix: normalise person names at every entry (CLI flags, token creation, claim) and compare with the normalised form, with a case-insensitive unique index; show the chosen person on the setup page.

### R-05 Info: the GitHub access token is dropped from memory but not revoked at GitHub

- Where: `internal/web/signin.go` `callbackHandler`; ADR 0016 point 1 ("dropped from memory").
- Each sign-in leaves a live, non-expiring OAuth App token (public-profile scope only) at GitHub for that user and app. Impact is low, but "dropped" is not "revoked". Optionally call `DELETE /applications/{client_id}/token` with the client credentials after `User`, and say in the ADR that the token lives on at GitHub otherwise.

### R-06 Info: small observations

1. A second `privo admin setup-link` creates another full owner with no check that an owner already exists and no warning. The CLI is the trust anchor so this is by design, but the stderr text ("makes the GitHub account you sign in with the owner") could say "an additional owner" when `users` already holds one.
2. The plain `PRIVO_GITHUB_CLIENT_SECRET` variable is accepted without the warning that the master key path gives (see R-08 of the F24 review). `_FILE` is documented as preferred; add the warning.
3. Timestamps are compared as strings and `time.RFC3339Nano` trims trailing zeros, so within the same second `...:00Z` sorts after `...:00.5Z`. The error is under one second and affects only expiry edges (`internal/store/store.go:219`); a fixed-width format would remove it.
4. `/admin/` and `/auth/` 503 pages list the names of missing settings to anyone. It is the stated design and shows no values.
5. Signing in does not end a browser's earlier session and there is no "sign out everywhere" (documented residual risk).

## Checked and found sound

- **OAuth state and PKCE.** State (256 bits) and a second 256-bit nonce are hashed together (`stateHash`, domain-separated); the row is read and deleted in one `DELETE ... RETURNING` on the single writer, so replay and parallel use find nothing. A callback without the cookie, or with the wrong one, hashes to a different key and does not consume the real row. Expiry is enforced in the same statement. Verifier is 32 random bytes, S256 only. The `error`/`code` check happens after consumption, so a failed GitHub step cannot be retried with the same state.
- **Redirect URI and open redirects.** `callback` is built once from the configured public URL (`web.go` New); the Host header is never read. After sign-in the only variable target is `ReturnTo`, which must match `^/connect/[a-z][a-z0-9-]{1,15}$` both at store time and at use. There is no `next` pass-through.
- **Session issuance.** New 256-bit secret per login, only its SHA-256 stored (`sessions.id`); `HttpOnly`, `SameSite=Lax`, `Secure` and `__Host-` prefix on https, no prefix and no `Secure` only for loopback http (public URL rule rejects other plain http). Absolute expiry is server-side (`expires_at`), idle via `last_seen_at`; `SessionByHash` joins the live user and drops disabled users, so role changes and disable apply on the next request; `DisableUser` also deletes sessions and grants. Sign-out deletes the row and clears the cookie.
- **CSRF and origin.** Every POST route passes through `form` (origin from configuration, 8 KiB body cap); session routes go through `guarded` (CSRF from the body only, constant-time compare). `Origin: null` and other origins are refused. The GET routes (`/admin/*`, callback, setup page) write nothing except the state-protected callback and the once-a-minute `last_seen_at` touch.
- **Setup link.** Token shape is checked before any lookup; only the SHA-256 is stored; creating a link retires older ones in the same transaction as the audit row; claim is a single transaction (`UPDATE ... WHERE claimed_at IS NULL AND retired_at IS NULL AND expires_at > ? RETURNING`), so two claims have one winner, and a refused claim (existing identity, taken person) rolls the consumption back. Unknown, expired, used and replaced links give one page. Identity is the numeric id from a regex-checked `id`; login is display only, never a lookup key.
- **Tenancy.** Every query in `signin.go` takes `OrgID` and filters `org_id`; all five tables have `org_id` in the primary key, with foreign keys on `(org_id, user_id)`.
- **User enumeration.** Stranger and disabled member get the same 403 page; the "already a member" and "name taken" pages are reachable only with a valid setup or invite secret.
- **GitHub calls.** Via the egress client (blocking is at the dialer `Control`, which `githubauth` keeps because it copies the client and replaces only `CheckRedirect`), no redirects followed so the secret cannot go to another host, 15 s timeout, 256 KiB cap, error text rebuilt without URLs, GitHub error bodies never shown, only a regex-restricted error code is carried. Secret travels in the POST body. Profile fields are validated (id regex, login regex, display name stripped of control and format characters, 100 runes).
- **Secrets in logs, audit, errors.** `Secret` redacts for `%v`, `%#v` and slog; no code, state, cookie or token reaches a log line or page; audit actors are regex-validated logins and numeric ids.
- **Templates and headers.** `html/template` throughout, no inline script or style; every wrapped response carries CSP (`default-src 'none'`, `form-action` limited to self plus the GitHub origin), `no-store`, `nosniff`, `X-Frame-Options: DENY`, `Referrer-Policy: same-origin`. The app-specific CSP is the other review's.
- **Config.** `LoadWeb` is separate from `Load`, so a bad web setting cannot break `healthcheck` or `token create` (the F24 R-03 lesson was applied); client id and secret shapes are validated, secret file is a bounded regular file, session durations are bounded with idle <= max.
- **Pruning.** Sessions, expired states, unclaimed links and invites are pruned every 10 minutes; correctness does not depend on it (all reads check expiry).
