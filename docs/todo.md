# Privo feature tracker

One section per feature. Each section keeps its goal, its acceptance criteria
and a to-do list. Tick items as they land; add items as they are discovered.
Status: Not started, In progress, Blocked, Done.

## F00. Architecture review
Status: Done

- [x] Draft ADR 0001 request pipeline and extension points
- [x] Draft ADR 0002 data model and tenancy
- [x] Draft ADR 0003 secrets and authentication
- [x] Independent reviews: security, scale and reliability, MCP protocol
- [x] Revise ADRs 0001 to 0003 (revision 2, target MCP 2026-07-28)
- [x] Write ADRs 0004 to 0011 for the gaps
- [x] Owner sign-off; ADRs accepted 2026-10-28
- [x] Spike: OAuth server library, desk review; ory/fosite chosen, see ADR 0012 (superseded 2026-11-12 by the working test: ADR 0022 point 16)

## F01. Project foundation
Status: Done

Goal: a Go repo that builds one static binary, with CI and release images.

- [x] `go.mod` (module `privo.club/privo`, Go 1.26 minimum, toolchain 1.27.3), layout, Makefile
- [x] `privo serve | version | healthcheck`; config from env with validation
- [x] `/livez`, `/readyz` (checks with timeouts, panic-safe, draining), `/version`
- [x] Graceful shutdown with drain timeout; tests for in-flight completion and forced close
- [x] Lint config (golangci-lint v2), CI workflow (vet, race tests, lint, govulncheck, image with SBOM)
- [x] Distroless non-root container image with built-in health check
- [x] Port the prototype's six tests as the black-box acceptance suite (`test/acceptance`, build tag `acceptance`), updated to the ADRs; plus a fixture self-test
- [x] Verified: unit tests pass 5x with the race detector (Go 1.24 scratch copy, stdlib only); acceptance suite type-checks and fails as expected (gateway not built)
- [x] Go 1.27.3 installed in the build workspace (checksum verified)
- [x] `github.com/modelcontextprotocol/go-sdk` v1.8.0 added; go.sum committed
- [x] Verified on Go 1.27.3: golangci-lint 0 issues; unit tests pass 3x with race detector; fixture test passes; six acceptance tests fail as expected (gateway not built)
- [x] go.sum re-verified against sum.golang.org (identical); `go mod verify` passes
- [x] govulncheck: no vulnerabilities found
- [x] Build the container image in CI: the `image` job (buildx, SBOM, provenance) runs on every push
- [x] Repository: the Go gateway replaced the prototype at the root of `talk2sohail/privoclub` (`bd11fbc`, 2026-10-29); the landing page moved to `site/`
- [x] `Makefile` and `.github/workflows/ci.yml` are in the repository and CI runs on every push

Notes: allowed domains: go.dev, dl.google.com, proxy.golang.org, sum.golang.org,
vuln.go.dev, storage.googleapis.com.

## F02. Gateway core (data plane)
Status: In progress. Milestone 1 reached: all six ported acceptance tests pass (also with a race-detector build)

Goal: aggregate stdio and remote MCP servers behind one endpoint.

ADRs: 0001, 0004

- [x] Stateless Streamable HTTP endpoint `/mcp` (go-sdk, Stateless=true) behind bearer-token auth; 401 advertises protected resource metadata
- [x] Gateway as receiving middleware: tools, prompts, resources list/call/get/read routed per principal and org (`internal/gateway`)
- [x] Upstream manager: stdio (local runner with digest pin), Streamable HTTP and SSE via the egress client; only advertised capabilities are listed; list-changed refresh; capped exponential reconnect; enable/disable (`internal/upstream`)
- [x] Registration waits for the first connection so the response shows status and items awaiting review
- [x] Only reviewed (accepted) items are listed or callable; changed definitions return to pending
- [x] Audit intent written before every call (call refused if it fails); completion queued and batched (`internal/audit`)
- [x] Admin API v1: servers, pins:accept, tokens, audit events, audit settings; admin actions audited (`internal/control`)
- [x] `privo token create` (works while the server runs)
- [ ] 2025-11-25 client compatibility verified with real clients (F21)
- [ ] Production stdio runner (F13); local runner is development only and now needs PRIVO_INSECURE_LOCAL_RUNNER=i-understand
- [x] Public names `<alias>__<name>` within 46 characters, normalised with hash suffix; aliases `^[a-z][a-z0-9-]{1,15}$`
- [ ] Persist the alias table (names are computed deterministically today)
- [ ] Resource URI rewriting with templates intact; subscriptions fan-out
- [ ] Pipeline as pluggable hooks (stages exist inline today: resolve, pin check, args digest, decide=allow, intent, call, completion); List, Transform, Decision, After hooks and observers
- [ ] Validate arguments against the pinned input schema
- [ ] Server-to-client requests: elicitation forwarding, sampling refused, roots from config
- [ ] Notifications: list changes, cancellation, redacted progress and logs
- [ ] Rate limits, in-flight limits, bulkheads, circuit breakers
- [x] Tests: aggregation, routing, server drop and recovery, enable/disable (acceptance suite); name normalisation and collisions (unit)
- [x] Security review of milestone 1: 4 high, 4 medium, 4 low findings, all fixed (docs/reviews/2026-10-28-milestone-1-security-review.md)
- [ ] Tests for the admin API (token revocation ranks, header validation) and refresh coalescing
- [x] `/version` stays unauthenticated (owner's decision, 2026-10-29). It now shows only the semantic version and the Go version, no commit or build date (see the next item)
- [x] Versioning follows semver (owner's decision, 2026-10-29): the version is the line in `internal/buildinfo/VERSION` (`0.6.0` since F26), embedded in the binary, so Coolify builds report it without build arguments; `/version` and `privo version` return `{version, go_version}` and no longer a commit hash or build date; the ldflags and Dockerfile build arguments for version, commit and date are gone. Rules for bumping are in `docs/versioning.md`. Tests: `internal/buildinfo` `TestVersionIsSemver`, `TestSemverGrammar`, `TestGetShape`; `internal/server` `TestEndpoints`; `cmd/privo` `TestVersion`
- [x] The master key moved from the `PRIVO_MASTER_KEY` variable to a file mount (owner's decision, 2026-10-29): `PRIVO_MASTER_KEY_FILE=/run/secrets/privo_master_key` on Coolify, the variable deleted, both deploys healthy, the "master key given as PRIVO_MASTER_KEY" warning gone. Steps and the password-manager backup note are in `docs/deploy-coolify.md`. To be streamlined with the admin UI
- [x] CI lint fixed (2026-10-29): `errorlint` flagged `err != want` in `internal/gateway/resulttype_test.go` (now `errors.Is`); it was the only finding. While checking, `go test -race` on the acceptance suite showed 23 data races in the harness (the gateway's log buffer was read by the test while the process wrote it): `logBuffer` in `test/acceptance/harness_test.go` now has a lock, and the suite passes under `-race`. `/_to_delete/` is in `.gitignore`
- [x] The acceptance job in `.github/workflows/ci.yml` is a real gate (owner's go-ahead, 2026-10-29): `continue-on-error` and the stale "allowed to fail until F02" comments (workflow, Makefile) removed; the suite passes, also under `-race`
- [x] List results (tools, prompts, resources, templates) carry `cacheScope: "private"` and `ttlMs: 5000`; `resources/read` is private with ttl 0 and overrides an upstream's own hints (ADR 0001). Found with Claude Code on 2026-10-29: it rejected the empty `cacheScope` and showed "tools fetch failed". Test: `TestListAndReadResultsCarryValidCacheHints` (raw JSON, since the Go client does not check the field)
- [x] Hostile upstream replies cannot crash the gateway (two High findings of the independent review, 2026-10-29). An upstream is untrusted input and one process serves every tenant, so a reply that breaks the protocol may cost only that server. Acceptance: (1) a `null` entry in an upstream's tools, prompts or resources list makes that server `down` with an error that says so, at first connect and on a list-changed refresh, while the gateway keeps serving its other servers; (2) a `resources/read` reply with a `null` entry in `contents`, or with no result, is refused with an error that calls the reply malformed, nothing of it is relayed, and the next request on the same connection works. Tests first: `internal/upstream` `TestLoadRefusesNullListEntries`; `internal/gateway` `TestRewriteRead`; acceptance `TestHostileUpstreamNullListEntry` and `TestHostileUpstreamNullReadContent` (echoserver `-mode hostile -null tools|prompts|resources|read`). Fix: `upstream.load` and `readResource` (via `rewriteRead`) check for nil before use. The Go SDK already drops a null tool entry itself (`filterValidTools`), so a null in tools/list leaves the server up with its real tools; the tools loop is guarded anyway. Deployed. The two items that review left open were done later: the result size cap (R-04, in F24 below) and the audit outcome for calls the wrapper refuses (R-07, in F24 below).
- [x] A malformed message from an upstream cannot crash the gateway (R-01, High, independent review 2026-10-29; the first fix covered three null cases, not the class). The go-sdk v1.8.0 client dereferences `params` in `notifications/elicitation/complete` and has no `recover()` anywhere, so one upstream notification without `params` killed the process for every tenant, again after each restart. Acceptance: (1) every incoming MCP message on an upstream connection, and every one on the client-facing server, runs under a recover wrapper that logs the panic (method, panic text, stack, no message content) and turns it into an error; (2) a notification of each method the SDK client handles, with `params` absent or null, leaves the gateway up and its other servers working. Tests first: `internal/safe` `TestRecover*`; acceptance `TestHostileUpstreamNotification` (echoserver `-mode raw-notify -method M -params absent|null`) and `TestMalformedClientNotificationsDoNotCrash`. Done: `internal/safe` `Recover`, installed outermost on the upstream client (`connect`) and on the client-facing server. Follow-ups: report the nil dereference to go-sdk (draft with a runnable reproduction: `docs/upstream/go-sdk-elicitation-complete-nil-params.md`, owner files it); the go-sdk has no `recover()` at all, so any other handler bug of that kind is now an error for one message instead of an outage.
- [x] Transport errors of an upstream never reach the AI client (R-02, Medium, independent review 2026-10-29): the text held the upstream's URL with the API key in its query, and went out as `call failed: ...` (tools/call) and as the error of prompts/get and resources/read. `Gateway.clientFacing` (in `run`) now replaces it with a fixed message naming the server, logs the detail through `upstream.RedactText` (user info, query and fragment removed from every URL in the text), and passes through only the upstream's own JSON-RPC error and the gateway's own. The SDK's local codes (-32001, -32003, -32004, -32005) wrap transport text and count as local. Tests: `TestClientFacing`, `TestRedactText`, acceptance `TestUpstreamTransportErrorsDoNotReachTheClient`.

## F03. Storage and tenancy
Status: In progress
ADRs: 0002, 0013

- [x] SQLite engine (pure Go, `modernc.org/sqlite`): WAL, synchronous=NORMAL, busy_timeout, single writer with BEGIN IMMEDIATE, read pool, database file mode 600 (`internal/store`)
- [x] Writer lease taken before migrations; a second process refuses to start; heartbeat renewal
- [x] goose migrations embedded in the binary; schema 00001 (orgs, org_keys with provider and key_ref per ADR 0013, api_tokens, servers, credentials, pins, audit_events, settings)
- [x] Typed OrgID on every org-scoped method; UUIDv7 ids (`internal/ids`)
- [x] Test: every table carries org_id (documented exceptions: orgs, leases)
- [x] Tests: cross-org isolation for servers, credentials, tokens, pins, audit, settings; uniqueness conflicts mapped to ErrConflict
- [ ] Postgres engine with forced row-level security; shared conformance suite for both engines
- [ ] sqlc-generated queries (hand-written for now; revisit with the Postgres engine)
- [ ] `privo migrate` command; snapshot before migrating; `privo export` and `privo import`
- [x] Driver switched to modernc.org/sqlite (pure Go, widely used); all suites pass

## F04. Secrets vault
Status: In progress
ADRs: 0003

- [x] Envelope encryption: per-org DEK wrapped by KEK (AAD = org id); per-credential HKDF subkey; AES-256-GCM; length-prefixed AAD binding org, credential, owner, server, purpose, key version, row version (`internal/vault`)
- [x] Tests: round trip, nonce uniqueness, every binding change rejected, every single-bit tamper rejected, wrong keys, truncation
- [x] KeyProvider: `PRIVO_MASTER_KEY_FILE` preferred, `PRIVO_MASTER_KEY`, `PRIVO_MASTER_KEY_PREVIOUS`; plaintext variables removed from the environment after load; KMS later
- [x] Per-org key reference `org_keys(org_id, version, provider, key_ref, wrapped_dek)` and provider registry (`internal/keyring`); KEK rotation via previous key
- [x] DEK cache, 5-minute lifetime
- [ ] Clear error surfaced to admins when a provider key is revoked
- [ ] Shamir recovery kit for the self-hosted master key (M-of-N printable shares; vetted library)
- [ ] Cloud KMS provider for Privo Cloud (AWS first)
- [ ] Customer-managed keys (BYOK) for Business and Enterprise: AWS KMS, then GCP, Azure
- [ ] KEK rotation command
- [ ] Redaction of known secrets from results and logs
- [ ] Tests: tamper, swap, rotation, lost-key behaviour

## F05. Admin sign-in and organisations
Status: In progress (first slice live on app.privo.club since 0.3.0, real GitHub sign-in works since 2026-11-03; independent review done 2026-11-03, its findings are open below)
ADRs: 0003, 0011, 0014, 0016

### First slice: goal

A person opens the admin site in a browser, signs in with GitHub, sees the calls
waiting for approval with their full arguments, and approves or denies them,
with no control token. The first owner is created with a one-hour setup link
that `privo admin setup-link` prints. Not in this slice: invites and member
management, Google and Microsoft sign-in, magic links, using a session on
`/api/v1` (the admin API stays token-only), step-up re-authentication, the rest
of the UI (F14).

### First slice: acceptance

1. **Off unless configured.** Web sign-in needs `PRIVO_GITHUB_CLIENT_ID`,
   `PRIVO_GITHUB_CLIENT_SECRET` (or `_FILE`) and `PRIVO_PUBLIC_URL` (without a
   path: the site cannot run under a prefix). Without all of them, every
   `/admin` and `/auth` route answers 503 with a page naming the missing
   settings (never values); nothing else changes. A mistake in the web settings
   stops `serve` (exit 2) but not `healthcheck` or `token create` (R-03).
2. **Setup link.** `privo admin setup-link [--person NAME]` needs
   `PRIVO_PUBLIC_URL`, prints `<public url>/admin/setup/<token>` once, stores
   only the SHA-256 of the token, lives 1 hour, works once, and retires older
   unclaimed links. Audited as `setup_link.create` (actor `cli`). It says what
   is missing when GitHub is not configured yet.
3. **Claim.** The link is claimed only after a successful GitHub login, in one
   transaction: create the owner user, bind the identity (provider `github`,
   numeric GitHub id), consume the link, write `setup.claim`. The owner's person
   name is the one on the link (`--person`), else the lowercased GitHub login.
   If the GitHub id is already a member, or the person name is taken, the claim
   is refused and the link stays usable. Of two concurrent claims exactly one
   wins. Every way a link can be unusable (unknown, malformed, expired, used,
   replaced) shows the same page.
4. **Sign-in.** `state` (256 bits) and PKCE S256 are bound to the browser by a
   short-lived cookie holding a second 256-bit nonce; the stored state is keyed
   to both, so a callback from another browser finds nothing and does not use
   the real one up. The state is single use (deleted as it is read) and lives 10
   minutes. No scope is requested; `allow_signup=false`. Only `id`, `login` and
   `name` are read from `GET /user`; the GitHub token is never stored. An
   unknown or disabled account sees one generic page and `signin.denied` is
   audited. A failed exchange leaves a setup link unconsumed. What GitHub said
   in an error is never repeated.
5. **Sessions.** A 256-bit random secret in a cookie, stored hashed, new at
   every login. `__Host-privo_session` (Secure, Path=/) on https;
   `privo_session` without Secure only for a loopback `PRIVO_PUBLIC_URL`;
   HttpOnly, SameSite=Lax. Idle timeout 2 h and absolute 12 h (settings). The
   live user row is read on every request, so a role change or a disabled user
   takes effect at once. Sign-out deletes the row.
6. **CSRF.** Every POST needs a same-origin `Origin` (or, without `Origin`,
   `Sec-Fetch-Site: same-origin`); with neither it is refused. After sign-in,
   POSTs also need the session's CSRF token in the body (not the URL). Anything
   else is 403 and nothing happens. The redirect URI and the origin come from
   `PRIVO_PUBLIC_URL`, never from the Host header.
7. **Approvals page.** `/admin/approvals` lists pending approvals with their
   arguments for roles that may decide (owner, admin, approver); Approve and
   Deny call `approval.Service.Decide`, so self-approval, single use and "not
   pending" behave exactly as on the API. Other roles get 403. Views and
   decisions are audited with the user as actor, and the approver is recorded
   on the call's audit row. An approver must see what they allow: when the
   arguments cannot be decrypted or are longer than 4 KiB, the page offers Deny
   only.
8. **Pages.** `html/template` escaping, no inline script or style, CSP
   `default-src 'none'; style-src 'self'; form-action 'self' <GitHub base
   origin>; frame-ancestors 'none'; base-uri 'none'` (Chrome applies
   `form-action` to the redirect after a form post, so the sign-in form may go on
   to GitHub), `Cache-Control: no-store`, `Referrer-Policy: same-origin` (not
   `no-referrer`: browsers then post forms with `Origin: null`),
   `X-Content-Type-Options: nosniff`, `X-Frame-Options: DENY`.
9. **Bounded.** At most 500 unexpired sign-in states (503 beyond); expired
   states, setup links and sessions are pruned every 10 minutes; request bodies
   are capped at 8 KiB; at most 50 approvals are shown; GitHub's answers are
   capped at 256 KiB and 15 s.
10. **No secrets in logs or audit.** Codes, states, cookies, CSRF tokens, setup
    tokens and the client secret never appear in a log line, an audit row, an
    error page or an API response; the database holds only hashes of session
    secrets, setup tokens and states.
11. **Settings documented.** Every new setting is in the registry, the README
    table and `docs/privo.example.yaml` (the registry tests enforce this).

### First slice: tasks and the tests that name them

- [x] Spec and ADR 0016 (this section)
- [x] Settings: client id, client secret (+ `_FILE`), GitHub base and API URLs, session idle timeout and max age (`internal/config`, loaded by `LoadWeb`, not by `Load`)
  - tests: `TestWebSettingsDefaults`, `TestWebSettingsValues`, `TestWebSettingsLimits`, `TestWebURLsAllowLoopbackHTTP`, `TestWebSignInNeedsAllThree`, `TestGitHubSecretFromFile`, `TestLoadIgnoresWebSettings`, registry tests
- [x] Store: migration `00004_sign_in.sql` (`users`, `identities`, `sessions`, `setup_links`, `oauth_states`), every table with `org_id`
  - tests: `TestSetupLinkSingleUseAndExpiry`, `TestSetupLinkNewOneRetiresOld`, `TestClaimUsesTheLinksPersonFirst`, `TestClaimRacesHaveOneWinner`, `TestClaimRefusedForExistingIdentityKeepsLink`, `TestIdentityUniquePerProvider`, `TestSessionExpiry`, `TestTouchSessionWritesAtMostOncePerMinute`, `TestSessionReadsLiveUser`, `TestCreateAndDeleteSession`, `TestOAuthStateSingleUse`, `TestOAuthStateCap`, `TestPruneSignIn`, `TestSignInSchemaHasNoSecretColumns`, `TestSignInTablesAreIsolatedPerOrg`
- [x] Approvals: record a web decider (`approval.Decider.UserID`)
  - tests: `TestDecideRecordsUser`, `TestDecideSelfApprovalByPersonForUsers`, `TestEmptyTokenIDIsNotTheRequester`, `TestDecideUnknownApproval`
- [x] GitHub client (`internal/githubauth`) through the egress client: authorization URL, code exchange, `GET /user`
  - tests: `TestChallengeMatchesTheRFCExample`, `TestNewVerifier`, `TestAuthorizeURL`, `TestExchangeSendsSecretVerifierRedirect`, `TestExchangeFailures`, `TestResponsesAreBounded`, `TestRequestsTimeOut`, `TestUser`, `TestUserRefusesWhatIsNotAProfile`, `TestDisplayNameIsCleaned`, `TestRedirectsAreNotFollowed`, `TestTransportErrorsDoNotPrintURLs`
- [x] `internal/web`: sessions, CSRF, sign-in and setup flow, approvals page
  - tests: `TestDisabledWithoutConfiguration`, `TestPublicURLWithAPathDisablesTheSite`, `TestSetupTokenFormat`, `TestSetupFlowCreatesOwner`, `TestSetupLinkPersonOverridesLogin`, `TestSetupFailedExchangeKeepsLink`, `TestSetupRefusedForExistingMemberKeepsLink`, `TestSetupPersonTakenKeepsLink`, `TestStartWithBadSetupToken`, `TestSignInPage`, `TestSignInOfAMember`, `TestStateCookieRequired`, `TestStateReplayRefused`, `TestStateExpired`, `TestCallbackErrorsAndBounds`, `TestUnknownAccountGenericDenial`, `TestExchangeFailureAndGitHubDown`, `TestStartCapReturns503`, `TestCookieFlagsHTTPS`, `TestCookieFlagsLoopback`, `TestSessionRotatedOnLogin`, `TestIdleAndAbsoluteExpiry`, `TestRoleChangeAppliesAtOnce`, `TestSignOutDeletesSession`, `TestCSRFMissingWrongCrossOrigin`, `TestOriginComesFromThePublicURLNotTheHost`, `TestSignInStartNeedsASameOriginPost`, `TestWrongMethodsAreRefused`, `TestMemberCannotSeeOrDecideApprovals`, `TestApproveAndDeny`, `TestSelfApprovalBlocked`, `TestEscapesWhatOthersControl`, `TestLongArgumentsAreShortened`, `TestUnreadableArgumentsCanOnlyBeDenied`, `TestSecurityHeaders`, `TestSessionSecretNotStored`, `TestNothingSecretInLogsOrAudit`, `TestExpiredStatesArePrunedOnStart`, `TestPruneRemovesWhatCannotBeUsed`, `TestRunPrunesNowAndStopsWithItsContext`
- [x] CLI `privo admin setup-link`
  - tests: `TestSetupLinkPrintsOnceStoresHash`, `TestSetupLinkAudited`, `TestSetupLinkPersonFlag`, `TestSetupLinkNeedsPublicURL`, `TestSetupLinkRejectsAPublicURLWithAPath`, `TestSetupLinkWarnsWhenGitHubIsNotConfigured`, `TestSetupLinkRejectsBadWebSettings`, `TestAdminUsage`, `TestAdminReadsTheConfigurationFile`
- [x] Wired into `serve` (settings loaded there only; pruning every 10 minutes); version 0.3.0; README, deploy guide, example file
  - tests: `TestServeRejectsBadWebSettings`, `TestWebSettingsDoNotBreakOtherCommands`
- [x] Acceptance with the real binary and a fake GitHub that checks the client secret, redirect URI and PKCE: `TestWebSetupAndApprovals` (setup link, sign in, an `ask` call from a real MCP client, approve in the browser flow, the call succeeds, the audit names the approver, a stranger is refused, sign-out, no secret in the logs), `TestWebDisabledByDefault`
- [x] Independent review of the finished slice (2026-11-03, `docs/reviews/2026-11-03-f05-sign-in-independent-review.md`): approve with changes; 0 Critical, 0 High, 1 Medium, 3 Low, 2 Info. The fixes were parked by the owner; they are the open items under "Review findings, open" below
- [x] On Coolify: the GitHub OAuth App, the client secret file mount and the two variables are set, and the owner claimed the setup link and signed in with real GitHub on 2026-11-03 (`docs/checkpoint-2026-11-03.md`)
- [ ] Approve one real call in the browser on app.privo.club with a second person's token (`docs/manual-test.md` steps 4 to 6; needs a second GitHub account)

### First slice: how it was checked

Besides the tests above, the security checks were broken on purpose, one at a
time, in a scratch copy (by hand on 2026-10-30; the script is not kept): the CSRF
comparison, the CSRF token read from the URL, the origin check (always true, and
taken from the Host header), the fetch-metadata fallback, the cookie nonce, the
cookie flags and the `__Host-` prefix, both role checks, `html/template`
swapped for `text/template`, self-approval by person, the idle timeout, the
disabled-user check, the generic denial page, the CSP and its `form-action`, the
redirect URI taken from the Host header, Approve offered for shortened or
unreadable arguments, the identity keyed by login, sign-out without a CSRF token,
the state cap. Each made at least one test fail. One mutation survived by
design: dropping the shape check on the `state` parameter changes nothing,
because an ill-formed state finds no row either.

### First slice: threat note

Assets: the ability to approve calls (an approved call runs with the org's
credentials), the session cookie, the setup link, the GitHub client secret.
Attackers: an AI agent or hostile upstream server (cannot reach a browser
session: `/mcp` and `/admin` are separate surfaces, and the agent's token is a
gateway token); a website the approver visits (CSRF, login CSRF, clickjacking);
someone who finds a setup link in a proxy log; someone with a GitHub account
who is not a member; a stolen session cookie; a tool name, argument or GitHub
profile name written to inject markup.
Mitigations: see the acceptance points above and ADR 0016.
Residual risks (recorded in ADR 0016): the setup token travels in a URL path
(single use and the one hour limit it); GitHub accounts may lack MFA and Privo
cannot require it; a GitHub login can change, so identity is the numeric id;
the link between a person's web user and their gateway tokens is the `person`
label an administrator chose (verified linking arrives with members); no
step-up before approving high-risk calls yet; the CSRF token and the PKCE
verifier are stored in the clear for the life of a session and ten minutes.

### Review findings, open (independent review 2026-11-03; fixes parked by the owner)

- [x] R-01 (Medium): anyone can fill the 500-state cap with `POST /auth/github/start` and lock every sign-in, setup link and invite for ten minutes at a time. Rate-limit the start, or count only `signin` states against the open cap so setup and invite states keep a reserved share. Test: 500 anonymous starts do not block a setup claim **Fixed in 0.7.1 at the root (F06 task 2, ADR 0016 amendment 1):** a sign-in in flight is not stored on the server at all, so there is no cap and nothing to fill. Tests: `internal/web` `TestSignInStartWritesNothing`, `TestAnonymousStartsCannotBlockSetupInvitesOrSignIn`
- [x] R-02 (Low): any GitHub account can write unlimited `signin.denied` audit rows. **Fixed in 0.7.2:** one row per account in ten minutes and at most 60 rows in ten minutes over all accounts; what is left out is counted and logged. In memory, per process. Test: `internal/web` `TestRefusedSignInsCannotFloodTheAudit` **Independent review of 2026-11-15, S-06 (Low), fixed:** a disabled member is always recorded (once per account per window), so sixty throwaway accounts cannot hide them; the left-out count is written to the audit as `signin.denied.skipped:<n>` when the window ends, by the next refusal or by the periodic prune, not only to the log. Test: `TestRefusedMembersAreAlwaysAudited`
- [x] R-03 (Low): a cross-site link to the callback clears the state cookie of a sign-in in progress; clear it only after the state was found **Fixed in 0.7.1:** the cookie is cleared only when it opened for the state the callback brought (`TestSignInCookieIsBoundExpiresAndWorksOnce`)
- [x] R-04 (Low): self-approval compares `person` exactly while the owner's default person is lower-cased, so `Alice` (tokens) and `alice` (session) are two people; normalise person names everywhere and make the index case-insensitive **Fixed in 0.18.0:** approvals record the requester's user id (migration `00017`); a person is the requester by token, by user id, or by name in any case (`TestOwnKnowsTheMemberAndIgnoresCase`). Person names stay as they are; the comparison no longer depends on their case. Two members whose names differ only in case cannot approve each other's requests (it only ever refuses more); security review of the batch: `docs/reviews/2026-11-21-control-plane-findings-security-review.md` (one Medium, two Low, one note; fixed: a secret added by editing the client also needs a named issuer, the hidden-call record runs after the answer, a held token is not used once its grant is gone, earlier accepted definitions are listed apart)
- [ ] R-05, R-06 (Info): the GitHub token is dropped, not revoked, at GitHub; a second `admin setup-link` makes another owner without saying so; `PRIVO_GITHUB_CLIENT_SECRET` as a plain variable gets no warning; timestamps compare as strings of uneven width (sub-second error at expiry edges)

### Later in F05

- [ ] Orgs and groups (invites came with F07, bound by the numeric GitHub id; the People page with role changes and removal came with F14; teams with F26)
- [ ] Google and Microsoft OIDC; magic-link fallback
- [x] Restrict sign-in by GitHub organisation membership (`read:org`) (F05 A, 0.19.0)
- [ ] Step-up re-authentication for high-risk actions
- [x] Sessions accepted by the admin API for the UI (F14): through `/admin/api/v1`, never on `/api/v1` (ADR 0019)
- [x] Approvals page: page through more than 50 pending approvals; show decided ones (done with F10's second slice: `ListApprovalsPage`, pending and history with a cursor)

### Next slices (spec 2026-11-21)

**A. Sign-in limited to a GitHub organisation.** Goal: a company lets in
only its own GitHub organisation's people, even with an invite link that
leaked.

Acceptance:
1. `PRIVO_GITHUB_ORG_IDS=12345678,87654321` (unset: anyone with an invite,
   as today). Sign-in asks for `read:org` only when it is set.
2. Signing in (setup, invite, every later sign-in) is refused, audited as
   `signin.refused:not_in_org`, when the person is in none of them. The
   check uses the numeric org ids, which survive renames.
3. A person who leaves the organisation cannot sign in again; a session
   already open lasts until its idle timeout or maximum age (12 hours by
   default). Checking membership during a session is a later step.

Tasks and tests:
- [x] Config `PRIVO_GITHUB_ORG_IDS` (numeric ids; names can be re-registered) in the registry, README and example (`TestGitHubOrgIDs`); githubauth `Orgs`, paged, malformed answers fail (`TestOrgsUsesTheNumericID`, `TestAuthorizeAsksReadOrgOnlyWhenNeeded`)
- [x] web: the check in the sign-in callback for setup, invite and sign-in, audited `signin.refused:not_in_org` under the strangers' bound (`TestSignInRefusedOutsideTheOrgs`, `TestNoReadOrgScopeWhenUnset`)
- [x] Acceptance: `TestSignInLimitedToAGitHubOrg`; README; walkthrough section 24 (0.19.0)
- [x] Security review 2026-11-21 (with F07 A): nothing at Medium or above; no bypass (every sign-in passes the callback check), fails closed
- [x] Review Low: a 401/403 from `/user/orgs` (read:org not granted) shows "could not reach GitHub" and is not audited; treat it as a refusal (`notInOrg`) **Fixed:** `githubauth.ErrOrgsHidden`, refused and audited as outside the organisations (`TestHiddenOrgsAreARefusal`)
- [x] Review Low: membership is checked only at sign-in; sessions, OAuth client grants and tokens of someone who left the organisation last until they expire or are revoked. Document in the README, or recheck on refresh **Documented** in the README setting: remove the person on People, which ends everything at once. Rechecking during a session stays a later step

**B. Step-up re-authentication.** Goal: actions that hand out power ask
for a fresh sign-in, not just a session. Those actions are: an owner or
admin control token, a role change to owner or admin, editing or deleting
a server, and deciding approvals the org marks high-risk (F10 C). ADR
needed first (0030): what counts as fresh (default 10 minutes), how
GitHub is asked to re-prompt (`prompt=login` is not offered, so: a new
OAuth round trip with `max_age`-like checking of `auth_time` we record),
and how the app resumes the action.

Acceptance:
1. The API refuses those actions from a session whose last sign-in is
   older than the window, with 401 `step_up_required`. Tokens are not
   asked (they are made with step-up already).
2. The app sends the person through sign-in and back to the same page, and
   the action is repeated only by the person's own click.
3. Audit records carry `stepped_up_at`.

Tasks and tests:
- [ ] ADR 0030; sessions record `authenticated_at`
- [ ] API: `RequireFresh` around the listed handlers (`TestStepUpRequiredForElevatedTokens`, `TestFreshSessionPasses`)
- [ ] web: `/admin/signin?step_up=1&next=` (`TestStepUpReturnsToThePage`)
- [ ] App: the step-up prompt; Vitest; acceptance `TestStepUpEndToEnd`

**C. Google and Microsoft sign-in** moves to F20 A (generic OIDC), which
covers them.

## F06. Client authentication (OAuth server and personal tokens)
Status: In progress (personal tokens done; OAuth sign-in for AI clients: ADR 0022 accepted 2026-11-12; library decided 2026-11-12: `authelia.com/provider/oauth2` v0.3.3; no refresh grace; task 2 done in 0.7.1: a sign-in in flight lives in a sealed cookie; tasks 3 to 9 built in 0.8.0, off unless `PRIVO_OAUTH_SERVER=on`; next: the independent review, task 10)
ADRs: 0003 (section 2), 0014, 0021, 0022

### Done: personal tokens

- [x] Gateway and control tokens, each refused on the other surface; a `person` label on every token (ADR 0014); a gateway token belongs to a member by user id (ADR 0021, F27)
- [x] Personal token format `privo_pat_<id>_<secret><crc32>`, SHA-256 secret hash, constant-time verify, checksum rejection without lookup (`internal/token`)
- [x] Token storage, roles (owner, admin, auditor, approver, member), mandatory expiry (default 90 days), revocation effective within 2 s (verification cache), owner tokens only issuable by owners

### Next: AI clients sign in with OAuth (ADR 0022)

#### Goal
A person adds `https://<privo>/mcp` to Claude Code, Claude, VS Code or ChatGPT,
signs in to Privo in the browser, presses Allow, and the client works as them:
their teams' servers, their own upstream accounts. Nothing is pasted. Each
client can be revoked by itself. Personal tokens keep working.

#### Acceptance
1. **Discovery.** A request to `/mcp` without a token is answered 401 with
   `resource_metadata` and `scope="mcp"`. The protected resource metadata
   names the public URL as its only authorization server.
   `/.well-known/oauth-authorization-server` serves RFC 8414 metadata with
   `code_challenge_methods_supported: ["S256"]`,
   `client_id_metadata_document_supported: true`, `none` among
   `token_endpoint_auth_methods_supported`, and a `registration_endpoint`
   only when dynamic registration is on.
2. **Authorize.** `/oauth/authorize` refuses, without redirecting, a client
   that is not accepted or a redirect URI that does not match; refuses, by
   redirecting with an error, a missing or non-S256 challenge, an unknown
   scope and a `resource` that is not this gateway. Loopback redirect URIs
   match with any port for `127.0.0.1`, `[::1]` and `localhost`; all others
   match exactly.
3. **Sign-in on the way.** Without a session the person is sent to sign in and
   comes back to the consent page for the same request. No row is written
   before a signed-in member presses Allow. A request cookie that is expired,
   altered or from another browser is refused.
4. **Consent.** The page names the client, the redirect host, the signed-in
   person and what is granted; warns for a loopback-only client and for one
   that registered itself. Allow and Deny need the CSRF token and the Origin
   check. An approver or auditor cannot allow. Deny returns `access_denied`.
5. **Code.** Lives 60 seconds, works once, only with the same client, redirect
   URI, verifier and resource. A second use fails and revokes the grant.
6. **Tokens.** The access token is opaque, lives one hour and works only on
   `/mcp`, never on the admin API. A refresh token is always issued and
   rotates on use. A refresh token that was already rotated is refused at
   once, and that use revokes the grant: the tokens issued after it stop
   working and the person signs in again (no grace window; owner,
   2026-11-12). Idle 30 days, absolute 90 days. Errors are RFC 6749
   codes (`invalid_grant` for a dead refresh token). The token endpoint takes
   form-encoded bodies and answers well inside 10 seconds.
7. **Who the caller is.** A call with an access token runs as the member who
   consented: `gateway.Access` and `grantOwner` get their user id; the role is
   read at verification. The audit names the client and the member.
8. **Ending it.** The person revokes a client on "Connected clients"; an admin
   sees and revokes everyone's; `/oauth/revoke` works for the client itself;
   removing a member or changing them to a role without gateway use revokes
   their grants. Each takes effect on `/mcp` within 2 seconds.
9. **Clients.** The built-in known clients are accepted without setup; another
   metadata document is refused with a page naming its host until an admin
   approves it; with dynamic registration on, a client that registers is
   limited, labelled and expires when unused. Metadata is fetched only from
   public addresses, at most 64 KiB, in at most 5 seconds.
10. **Fail closed.** With no public URL or with sign-in off, the metadata lists
    no authorization server and the OAuth endpoints answer 503 naming what is
    missing, as the admin site does today.

#### Threat note
- **Open to the internet without a session:** metadata, authorize, token,
  register. None writes before consent except register (off by default,
  limited). Every lookup is by a hashed secret or an indexed id.
- **Client impersonation:** a loopback client cannot be told from another
  program on the same computer; the consent page says so and PKCE binds the
  code to whoever started. A hosted client is bound by its exact redirect URI.
- **SSRF through the metadata URL:** the egress client refuses private,
  loopback and link-local addresses, follows no redirect to them, and limits
  size and time.
- **Token theft:** tokens are stored hashed; refresh rotation with reuse
  detection and no grace, so a copied refresh token is dead once the real
  client has refreshed, and using it ends the grant; access tokens are bound to this gateway and never forwarded to an
  upstream (ADR 0003).
- **Confused deputy:** consent is shown every time and names the redirect
  host; `state` is returned untouched; `iss` is in the response (RFC 9207).
- **Locking people out:** the sign-in state cap (F05 R-01) is gone since
  0.7.1 (task 2): a request without a session writes nothing when it starts a
  sign-in. The audit rows a refused GitHub account can cause are bounded
  since 0.7.2 (F05 R-02).
- **An administrator acting as a member:** not possible through this flow; a
  grant is made only by the member's own session (ADR 0021).

#### Tasks, in the order of the pull requests
- [x] 1. Spike (2026-11-12, `docs/reviews/2026-11-12-oauth-library-spike.md`): neither named library passes as named (`ory/fosite`: no release since 2024-12; `zitadel/oidc`: accepts `plain`, stores the request before login, always issues an ID token). The maintained fosite fork `authelia.com/provider/oauth2` v0.3.3 passes all checks in a working test, with the redirect rule and "resource must be present" done by Privo in front
- [x] 1a. Owner's decisions (2026-11-12, recorded in ADR 0022 points 16 and 9): the library is `authelia.com/provider/oauth2`, pinned at v0.3.3; a refresh token that was already rotated is refused at once and its use revokes the grant, with no grace window (this replaces the 30-second grace of ADR 0003). The second decision removes work: the library does this by itself, so nothing is built in front of it for refresh
- [x] 2. F05 R-01, at the root (0.7.1, ADR 0016 amendment 1): the state of a sign-in in flight (purpose, link id, PKCE verifier, return path, expiry) is sealed into the cookie `__Host-privo_signin` with the organisation's data key, bound to the `state` sent to GitHub, so `POST /auth/github/start` writes nothing and there is no cap to fill. `oauth_states` keeps only connect states, which need a session, at most one per member and server (F07 R-03). Also closed by it: F05 R-03 and F07 R-07. Tests: `internal/web` `TestSignInStartWritesNothing` (SQLite `data_version` and every table's row count do not move), `TestAnonymousStartsCannotBlockSetupInvitesOrSignIn`, `TestSignInCookieIsBoundExpiresAndWorksOnce`, `TestSignInCookieCarriesPurposeLinkAndReturn`, `TestSignInCookieSurvivesARestartAndHidesItsContent`, `TestSignInAndConnectFlowsDoNotEndEachOther`, `TestConnectStartReplacesTheMembersOlderState`, `TestFinishedRemembersBoundsAndForgets`, `TestDisabledWithoutTheSealingKey`; `internal/store` `TestConnectStateSingleUse`, `TestConnectStatesAreCappedPerMember`, `TestRowsOfOtherPurposesAreNotConnectStates`. Mutation check: 14 deliberate breakages (no replay memory, cookie not bound to its state, no expiry, a start that writes, no per-member replace, any purpose accepted, cookies swapped, and others), each made a test fail. Removed: `TestStartCapReturns503`, `TestOAuthStateCap`, `TestExpiredStatesArePrunedOnStart` (they tested the cap)
  - [ ] The owner weighs point 5 of the amendment: Privo itself refuses a second use of a callback only within one process (a bounded memory of finished sign-ins); across a restart or on another replica a replay needs the HttpOnly cookie too and is refused by GitHub, which accepts a code once
  - [ ] Independent security review of this change (auth, request pipeline), on the owner's word; it can run together with the review of the OAuth server (task 10)
- [x] 3. Store and migration `00008` (0.8.0): `oauth_keys`, `oauth_clients`, `oauth_grants`, `oauth_tokens`, each with `org_id`; drops the three columns of `oauth_states` that task 2 stopped writing and adds a unique index on member and server; a member removed or given a role that cannot use the gateway loses their grants in the same transaction. Settings `PRIVO_OAUTH_SERVER` (off unless set; not in the ADR, see its "As built"), `PRIVO_OAUTH_CLIENTS`, `PRIVO_OAUTH_DYNAMIC_REGISTRATION` in the registry, README and example file. Tests (`internal/store`): `TestGrantBelongsToAMember`, `TestCodeWorksOnce`, `TestRefreshRotates`, `TestRotatedRefreshTokenIsRefusedAndRevokesTheGrant`, `TestRemovingAMemberRevokesTheirGrants`, `TestListAndRevokeClientGrants`, `TestOAuthTransactionCommitsRollsBackAndLetsGo`, `TestStoredClients`, `TestPruneOAuth`, `TestOAuthSigningKeyIsStoredOnce`; `internal/config` `TestOAuthServerSettings`
- [x] 4. Client acceptance (`internal/oauthserver/clients.go`): metadata documents through the egress client (5 s, 64 KiB, no redirects, cached 5 minutes to 24 hours, a failure cached a minute), the known list, the redirect rule (loopback port ignored). Tests: `TestClientDocumentMustMatchItsURL`, `TestClientDocumentIsCachedAndAFailureIsNotRepeated`, `TestClientDocumentFetchRefusesPrivateAddresses`, `TestLoopbackRedirectIgnoresThePort`, `TestOtherRedirectsMatchExactly`, `TestUnknownClientIsRefusedUntilApproved`
- [x] 5. Metadata, authorize, the sealed request cookie (`__Host-privo_authz`), consent page (`internal/web/oauth.go`). Tests: `internal/oauthserver` `TestAuthorizeWritesNothingBeforeConsent`, `TestAuthorizeErrorsRedirectOnlyToAnAcceptedURI`, `TestMetadata`; `internal/web` `TestAuthorizeRequestSurvivesSignInInASealedCookie` (planned as `TestRequestCookieIsBoundAndExpires`), `TestConsentNamesClientRedirectHostAndPerson`, `TestLoopbackClientGetsAWarning`, `TestConsentNeedsCSRFAndASession` (planned as `…AndOrigin`; it checks the Origin too), `TestApproverCannotAuthorizeAClient`, `TestAuthorizeRefusalsAreAPageOrARedirectToTheClient`, `TestOAuthEndpointsAnswer503WhenOff`
- [x] 6. Token and revoke endpoints; verification by prefix in `internal/auth` (`VerifyGateway`; `Verify`, which the admin API uses, never takes an access token). Tests: `internal/oauthserver` `TestPKCERequired`, `TestTokenRequestNeedsTheSameResource`, `TestGrantBelongsToTheMemberWhoConsented`, `TestCodeWorksOnceAndASecondUseEndsTheGrant`, `TestRefreshRotates`, `TestRotatedRefreshTokenIsRefusedAndRevokesTheGrant`, `TestGrantRunsOutIdleAndAbsolute`, `TestRevocationEndsTheGrant`, `TestVerifyReadsTheMembersRoleLive`, `TestVerifyRefusesEverythingThatIsNotALiveAccessToken`, `TestTokensAreStoredBySignatureOnlyAndSurviveARestart`; `internal/auth` `TestOAuthCallerIsTheMemberWhoAllowedTheClient`, `TestAccessTokenWorksOnlyOnTheGateway`, `TestOAuthVerificationIsCachedForTwoSeconds`, `TestGatewayRoleMatchesThePermissions`. `TestReusedRefreshTokenStopsTheAccessTokenOnTheGateway` became step 5 of the acceptance test (task 9), on the real binary
- [x] 7. Admin app: "AI clients that signed in" on Tokens for each person; a Clients page for administrators (the policy, the accepted clients, approve by document address, remove, everyone's connected clients); Activity shows the member a client acted as. API in `internal/control/oauth_clients.go`. Tests: `internal/control` `TestAMemberSeesAndRevokesOnlyTheirOwnClients`, `TestAnAdministratorSeesAndRevokesEveryonesClients`, `TestApprovingAndRemovingAClient`, `TestClientAPIWhenTheAuthorizationServerIsOff`; `web/src/pages.test.tsx` "AI clients that signed in with OAuth" and "activity"
- [x] 8. Dynamic registration behind the setting (ten per address per hour, 200 in total, unused ones removed). Tests: `TestRegistrationIsOffByDefault`, `TestRegistrationLimitsAndExpiry`
- [x] 9. Acceptance with the real binary: `TestClientSignsInWithOAuthAndCallsAsTheMember` (discovery from the gateway's address alone, registration, sign-in redirect, consent, a call that uses the member's own upstream account, the admin API refusing the access token, refresh, a rotated refresh token reused ending the access on `/mcp` with the audit naming client and member, a second sign-in revoked in the app, nothing on disk or in the log) and `TestOAuthServerIsOffByDefault`. The client registers itself: a metadata document client needs an https address the binary trusts (see ADR 0022 "As built")
- [ ] 10. Independent review (required by CLAUDE.md), then switch on for app.privo.club
- [ ] 11. Real clients, by the owner with Claude: Claude Code, Claude (hosted; the gateway must be reachable from Anthropic), VS Code; Cursor with dynamic registration on
- [x] Docs (0.8.0): README "AI clients sign in with OAuth", `docs/deploy-coolify.md` (upgrade, switching on, going back), `docs/manual-test.md` step 10, ADR 0022 "As built"; version 0.8.0 (MINOR)
- [x] Mutation check (2026-11-14): 33 deliberate breakages of the server, the web endpoint, the store and the authenticator, one at a time; 31 made a test fail. The other 2 are covered by a second check: "verification does not look at the token's time" (the library's own validation refuses it) and "an extra redirect address handed to the library" (Privo's rule runs before the library sees the request)

#### Independent review of 2026-11-15 (`docs/reviews/2026-11-15-f05-f06-signin-oauth-server-independent-review.md`)
Approve with changes; 0 Critical, 0 High, 1 Medium, 5 Low, 5 Info. Fixed on this branch, each with a test that fails without it:
- [x] S-01 (Medium): self-registration limits counted the proxy, not the client. New setting `PRIVO_TRUSTED_PROXIES` (`internal/clientip`: X-Forwarded-For read from the right, a forged left part never used; IPv6 counted per /64), and at the total the oldest registration nobody allowed makes room instead of refusing. Tests: `TestRegistrationLimitsCountTheClientBehindProxies`, `TestRegistrationsNobodyAllowedMakeRoom`, `internal/clientip`, `internal/config` `TestTrustedProxies`, `internal/store` `TestStoredClients`. Left: a churn of 200 registrations from 200 /64s within the minutes between a real registration and its consent can still evict it; stateless registration (a signed client id, nothing written before consent) is the complete answer
- [x] S-02: `grant_type` taken exactly as written (`TestPaddedGrantTypeIsRefused`)
- [x] S-03: a used code past its expiry is unknown and no longer ends the grant (`TestAUsedCodeCannotEndTheGrantAfterItExpired`)
- [x] S-04: a registered client is accepted only while registration is on and never under `approved` (`TestRegisteredClientNeedsRegistrationOn`)
- [x] S-05: client documents are fetched with no private range allowed; every fetch failure reads the same; at most 30 uncached fetches a minute (`TestDocumentFetchFailuresReadTheSameAndAreBounded`)
- [x] S-07: the consent page for a hosted client says to allow only a request one started oneself (`TestConsentNamesClientRedirectHostAndPerson`)
- [x] S-08: `response_mode` is refused as a page (`TestResponseModesAreRefusedAsAPage`)
- [x] S-09: redirect hosts must be DNS names or IP literals; the CSP replacement is literal (`TestRedirectHostsAreNamesOrAddresses`)
- [x] S-10: tests for the four checks no test noticed (`TestVerifyChecksTheStoredSubjectAndResource`, `TestClientIDWithUserInfoIsRefused`, the consent POST role check in `TestApproverCannotAuthorizeAClient`)
- [ ] S-11 (Info): the memory of finished sign-ins can be emptied by an anonymous script; weigh with point 5 of ADR 0016 amendment 1

## F07. Upstream OAuth broker
Status: In progress (first slice deployed, app.privo.club runs 0.6.0; independent review done 2026-11-03 with one High finding, open below; connecting a real provider account on app.privo.club is still to do, see F26)
ADRs: 0003, 0017

### First slice: goal

An admin adds a remote MCP server that uses OAuth (GitHub, Linear, Notion,
Atlassian, Sentry) and connects their own account so Privo can read its tools.
Each teammate, invited with a link, connects their own account once in the
browser. From then on every tool call on that server runs with the caller's own
upstream grant, refreshed by Privo, so upstream permissions and the audit stay
honest. A call by someone who has not connected fails with a link to connect.
Not in this slice: URL-mode elicitation and input-required, shared org-wide
credentials, scope step-up, an admin view of everyone's connections in the
browser.

### First slice: acceptance

1. **Auth mode.** A server is `auth: none` (unchanged behaviour) or
   `auth: oauth` (http and sse only). The API accepts a client id, a write-only
   client secret, scopes and an expected issuer; it never returns the secret.
2. **Discovery** (ADR 0017 point 2) runs when the server is added or changed,
   through the egress client; a server whose resource metadata names another
   resource, whose issuer differs from where it was fetched, or whose AS lacks
   S256 is refused with a message that names the problem.
3. **Registration** in the spec's order: admin credentials, then a client ID
   metadata document served at `/oauth/client-metadata.json`, then dynamic
   registration (secret in the vault); with none, the API says what to supply.
4. **Connect flow.** `/connect/<alias>` needs a session; the page names the
   user, server, AS host and scopes; a CSRF-protected POST starts it; `state`
   is single use, 10 minutes, bound to the session and a cookie nonce; the
   request carries `resource`, scopes and S256; the callback needs the same
   session, checks `iss`, exchanges the code with the verifier and `resource`,
   and stores nothing on any error. The CSP of the connect page allows the AS
   origin in `form-action`.
5. **Grants** are encrypted per org, server and user, bound to that owner and
   server, never returned or logged.
6. **Calls** run with the caller's own grant on a pooled per-person session
   (5 minutes idle, at most 500). The bearer token goes only to the server's
   origin. No grant: a tool error with the connect link. No user for the
   person: a tool error that says to ask for an invite. Never another person's
   grant.
7. **Refresh** before expiry and once after `invalid_token`; single-flight;
   row-version compare-and-swap; `invalid_grant` drops the grant and the next
   call gets the link.
8. **Disconnect** (own, or any through the API by an admin): revocation when
   offered, grant deleted, sessions closed. Disabling a user or deleting a
   server deletes the grants.
9. **Invites:** `privo admin invite` and `POST /api/v1/invites`, single use, 72
   hours, hash stored, role set by the link, account bound by numeric id.
10. **Audit:** `invite.create`, `invite.claim`, `server.oauth_register:<alias>`,
    `upstream.connect:<alias>`, `upstream.disconnect:<alias>`,
    `upstream.grant_lost:<alias>`.

### First slice: tasks and the tests that name them

- [x] Spec and ADR 0017 (this section)
- [x] Store: migration `00005_upstream_oauth.sql` (`invites`, `upstream_grants` with one catalog grant per server, connect and invite fields and `return_to` on `oauth_states`); tokens and client secrets are credentials (`oauth`, `oauth:client`); a server's OAuth settings live in its config (`ServerConfig.Auth`, `ServerOAuth`)
  - tests: `TestInviteSingleUseRoleAndExpiry`, `TestInviteBindsTheAccountNotTheLogin`, `TestGrantOnePerServerAndUser`, `TestCatalogGrantFirstManagerAndPromotion`, `TestGrantCompareAndSwap`, `TestDisableUserDeletesGrants`, `TestDeleteServerDeletesGrants`, `TestServerOAuthConfigAndClientSecret`, `TestConnectStateCarriesUserSessionServerIssuer`, `TestPruneRemovesDeadInvites`, `TestUpstreamOAuthTablesIsolatedPerOrg`, `TestUpstreamOAuthSchemaHasNoSecretColumns`
- [x] Discovery, registration and token requests (`internal/upstreamauth`), every request through the egress client, bounded, no redirects followed
  - tests: `TestResourceMetadataFromWWWAuthenticate`, `TestResourceMetadataWellKnownFallback`, `TestResourceMismatchRefused`, `TestAuthServerMetadataRFC8414ThenOIDC`, `TestIssuerMismatchRefused`, `TestAuthServerWithoutS256Refused`, `TestDiscoveryRefusesPrivateAddressesAndOtherHosts`, `TestDiscoveryAnswersAreBounded`, `TestRegistrationOrder`, `TestNoRegistrationPathNamesWhatToSupply`, `TestClientMetadataDocument`, `TestDynamicRegistration`, `TestAuthorizeURLCarriesResourceScopesAndS256`, `TestExchangeSendsVerifierResourceAndClientAuth`, `TestRefreshSendsResourceAndKeepsOldRefreshToken`, `TestTokenErrorsAreClassifiedAndNotRepeated`, `TestRevoke`, `TestTokenNeverPrints`, `TestParseBearer`
- [x] Grant service (`internal/grants`): seal, hand out, refresh single-flight with compare-and-swap, disconnect, register again
  - tests: `TestNotConnected`, `TestSaveSealsBoundToOwnerAndServer`, `TestRefreshBeforeExpiry`, `TestRefreshOnceAfterInvalidToken`, `TestConcurrentRefreshIsSingleFlight`, `TestRefreshSurvivesCallerCancel`, `TestInvalidGrantDropsTheGrant`, `TestRaceLoserReadsTheWinnersToken`, `TestDisconnectRevokesAndDeletes`, `TestClientSecretAndCIMDEndpoint`, `TestTokensNeverInLogsAuditOrErrors`
- [x] Admin API: `oauth` on new http/sse servers (`auth`, `registration`, `connect_url` in the server view), `POST /api/v1/invites`, `GET /api/v1/servers/{alias}/connections`, `DELETE /api/v1/servers/{alias}/connections/{person}` (covered by the acceptance tests below)
- [x] Web: invite page and claim, connect flow, connections page, sign-in that returns to a connect link, client ID metadata document (`internal/web`)
  - tests: `TestConnectNeedsASession`, `TestSignInReturnsToTheConnectPage`, `TestConnectPageNamesUserServerHostAndScopes`, `TestConnectCSPAllowsOnlyThatAuthServer`, `TestConnectStartNeedsCSRFAndSameOrigin`, `TestConnectSuccessStoresAnEncryptedGrant`, `TestConnectStateSingleUseExpiresAndBoundToBrowser`, `TestConnectCallbackNeedsTheSameSession`, `TestConnectIssMismatchRefused`, `TestConnectErrorStoresNothing`, `TestConnectionsPageListsOwnGrantsOnly`, `TestDisconnectRevokesAndDeletes`, `TestInviteClaimCreatesMemberWithRole`, `TestInviteRefusedForExistingMember`, `TestClientMetadataDocumentIsServed`, `TestMembersLandOnConnectionsApproversOnApprovals`, `TestDynamicRegistrationRetriedWhenRejected`, `TestNavShowsApprovalsOnlyToDeciders`
- [x] Data plane: per-person sessions and token injection (`internal/upstream`, `internal/gateway`)
  - tests: `TestCatalogReadWithTheAdminsGrant`, `TestCatalogWaitsForAnAdminAndKickBringsItUp`, `TestCallUsesTheCallersGrant`, `TestCallNeverUsesAnotherPersonsGrant`, `TestDisconnectStopsAPooledSessionAtOnce`, `TestRefreshOnceAfterInvalidTokenOnTheWire`, `TestBearerOnlySentToTheServerOrigin`, `TestSessionPoolIdleCloseAndCap`, `TestConcurrentFirstCallsShareOneConnection`, `TestNoGrantGivesTheConnectLink`, `TestNoUserGivesTheInviteMessage`
- [x] CLI `privo admin invite`
  - tests: `TestInvitePrintsOnceStoresHash`, `TestInviteAudited`, `TestInviteNeedsPersonAndValidRole`
- [x] Acceptance with the real binary and a fixture that is both an MCP server needing a bearer token (its tool answers with the token's subject) and its authorization server (PKCE, `resource`, redirect URI, rotating refresh tokens): `TestUpstreamOAuthTwoPeople` (admin adds the server and connects, invites a member, the member's first call gets the link, the member connects, each person's call reaches the upstream as themselves, a stranger is told to ask for an invite, refresh after the upstream drops the access tokens, the connections list, disconnect by the person and by an admin, audit, no code or token in the logs), `TestUpstreamOAuthPreRegisteredClient`, `TestUpstreamOAuthDynamicRegistration` (with the refused configurations and roles)
- [x] Real browser run (headless Chromium, 2026-11-01): owner setup, connect (the post-then-redirect to the authorization server passes the CSP), invite claimed on a phone-sized screen, connect, disconnect, a used invite is 404, a member gets 403 on approvals; no CSP or console errors besides that 403. Led to hiding the approvals link from people who cannot decide.
- [x] Docs: README (invites, servers where each person uses their own account), ADR 0017 (registration per server, token requests without `x/oauth2`, what building found), version 0.4.0
- [ ] Scope step-up: `TestInsufficientScopeNamesTheScope` was planned; a 403 `insufficient_scope` ends in the gateway's generic failure message today (later in F07)
- [x] Independent review (2026-11-03, `docs/reviews/2026-11-03-f07-upstream-oauth-independent-review.md`): approve with changes; 0 Critical, 1 High, 2 Medium, 5 Low, 1 Info. The fixes were parked by the owner; they are the open items under "Review findings, open" below

### First slice: threat note

Assets: every person's upstream tokens (they reach the person's GitHub, Notion,
Linear data); the registration secrets; the binding between a Privo person and
an upstream account.
Attackers: a website the person visits (login CSRF into the connect flow, so
the victim's Privo account gets the attacker's upstream account, or the other
way round); a malicious or compromised authorization server (mix-up: sending a
code meant for another AS, SSRF through metadata URLs, huge answers); a hostile
upstream MCP server (trying to make Privo send the bearer elsewhere); another
member of the org (using someone else's grant); an AI agent (tool arguments
that try to reach the connect flow, which needs a browser session).
Mitigations: ADR 0017 points 2 to 8 and the acceptance points above.
Residual risks: the catalog is read with an admin's grant; upstream tokens are
as safe as the master key; Privo cannot always tell which upstream account a
person connected; the connect link travels through AI clients in error text
until URL elicitation lands.

### Review findings, open (independent review 2026-11-03; fixes parked by the owner)

- [x] R-01 (High; the same hole as F14 R-02): an owner or admin can issue a gateway token with any member's `person` (`POST /api/v1/tokens`; an omitted person defaults to the token name) and call upstream servers with that member's grant; the audit names the token, not whose account was used. Bind tokens to a user id (`api_tokens.user_id`, set by `/api/v1/me/tokens`), resolve grants and team membership by user id and never by name, and record the grant's owner in the call's audit event. The acceptance fixture `g.issue("ada")` relies on the hole and has to change with it. F26's team membership follows the same string **Fixed in 0.7.0 (F27, ADR 0021), deployed 2026-11-11.**
- [x] R-02 (Medium): after a re-registration (`Grants.Reregister` from the connect callback) the running upstream manager keeps the old client id until a restart or an admin edit, so grants issued to the new client are refreshed as the old one, answered with `invalid_grant` and deleted. Call `Upstream.Sync` after the registration changes, or read the client from the store at refresh time; store the client id on the grant **Fixed in 0.8.1:** the grants service reads the server as it is stored now before a refresh or a revocation, so a process holding the old copy uses the current client id. Test: `internal/grants` `TestRefreshUsesTheClientStoredNow` (fails without the fix: the old client id was sent and the grant deleted). Grants issued to the old client still depend on the authorization server honouring them, as `Reregister` says. Review of 2026-11-15, U-06: the revocation half was untested; the test now checks the client id the revocation is sent with (and fails without `current()` in `Disconnect`)
- [x] R-03 (Medium): any signed-in member can fill the org-wide state cap through `POST /connect/<alias>/start` and lock out sign-in and connecting for everyone (see F05 R-01); cap connect states per user and replace that user's older one **Fixed in 0.7.1 (F06 task 2):** `oauth_states` holds only connect states, one per member and server; a new start replaces the member's older one; no cap. Tests: `internal/store` `TestConnectStatesAreCappedPerMember`, `internal/web` `TestConnectStartReplacesTheMembersOlderState`
- [x] R-04 (Low): removing a member, deleting a server and reconnecting delete grants locally without revoking them at the provider; backups keep them **Fixed in 0.18.1:** removing a member, deleting a server and an edit that ends connections (ADR 0029) read the grants' tokens first and revoke them at the provider once the deletion is done (best effort, in the background, all at once within 10 seconds, with the client they were issued to); a person's own API key cannot be revoked by Privo and is not sent (`TestRevocationsAreReadBeforeAndSentAfter`, `TestRevocationsSkipKeys`, `TestGrantsDeletedAreRevokedAtTheProvider`). Connecting again does not revoke the replaced grant: same client and person, and providers that revoke the whole authorization (Google) or return the same refresh token would end the new grant (`TestReconnectDoesNotRevoke`; security review of the fix, Medium, 2026-11-21). A refresh between reading and deleting can leave one rotated refresh token valid at the provider until it expires. A backup taken before keeps the sealed tokens, but the provider no longer honours them; a provider without a revocation endpoint is told nothing and its tokens run out on their own
- [x] R-05 (Low): require `oauth.issuer` when a client secret is supplied, so the secret cannot go to an authorization server the MCP server names **Fixed in 0.18.0:** a discovered server with a client secret needs `oauth.issuer` (`TestClientSecretNeedsANamedIssuer`); the catalog's own-app entries name theirs (GitHub, Asana, HubSpot, Slack, read from their protected resource metadata on 2026-11-21; `TestOwnAppEntriesNameTheirIssuer`)
- [x] R-06 (Low): a failed save after a rotated refresh token loses the grant; retry the save with the token already obtained **Fixed in 0.18.0:** the save is retried with the token already obtained; when it keeps failing the new pair is kept in memory, used, and saved on the next refresh, never over a newer connection (`TestRotatedTokenIsNotLostWhenSavingFails`, `TestUnsavedTokenDoesNotOutliveAReconnection`)
- [x] R-07 (Low): the GitHub callback uses up a state of any purpose before checking it; look the state up with its purpose and use one cookie per flow **Fixed in 0.7.1:** the sign-in callback reads no table; sign-in and connect have a cookie each; the connect lookup takes only connect rows. Tests: `internal/web` `TestSignInAndConnectFlowsDoNotEndEachOther`, `internal/store` `TestRowsOfOtherPurposesAreNotConnectStates`
- [ ] R-08 (Low): every upstream HTTP request reads, decrypts and touches the grant on the single writer connection; cache the opened token until its expiry and flush `last_used_at` periodically
- [ ] R-09 (Info, ten notes): among them no backoff when a server rejects every token, discovery of up to a minute inside one admin request, an orphaned dynamic registration when the create fails afterwards

### Later in F07

- [ ] URL-mode elicitation (2025-11-25) and input-required (2026-07-28) for the connect link
- [ ] Shared org-wide credentials, only by explicit admin choice and with a policy limiting the tools
- [ ] Scope step-up on `insufficient_scope`
- [x] Admin page of everyone's connections (F07 A, 0.19.0)
- [ ] Refresh coordination across replicas beyond compare-and-swap (Postgres)

### Next slices (spec 2026-11-21)

**A. Everyone's connections, on one page.** Goal: an admin sees, across
servers, who has connected what, and ends a connection without visiting
each server.

Acceptance:
1. `GET /api/v1/connections` (managers of servers) lists every grant and
   personal key. Each row has the person, server, kind (OAuth or key),
   scopes, connected, last used and last refreshed. It is filterable by
   person and server, paged. It never holds a token.
2. **Connections → Everyone** in the app (admins): the list, and **End**,
   which revokes at the provider (R-04) and deletes, audited
   `upstream.disconnect:<alias>:<person>` with the admin as actor.
3. Unused for 90 days is marked; a person's own Connections page is
   unchanged.

Tasks and tests:
- [x] Store: `ListAllGrants` (paged, org-scoped) (`TestListAllGrantsIsOrgScopedAndPaged`)
- [x] API: `GET /api/v1/connections` (`TestConnectionsAcrossServers`); ending one is the existing `DELETE /api/v1/servers/{alias}/connections/{person}`, which revokes at the provider (`TestDisconnectRevokesAndDeletes`)
- [x] App: **Connections → Everyone's connections** for admins (filters, unused marked, End with a second click); Vitest; walkthrough section 23 (0.19.0)

**B. Scope step-up on `insufficient_scope`.** Goal: a call refused for a
missing scope tells the person what to grant and lets them do it, instead
of a generic failure.

Acceptance:
1. A 403 with `WWW-Authenticate: Bearer error="insufficient_scope",
   scope="…"` gets an answer to the model naming the scope and the connect
   link with `?scope=`.
2. The connect flow asks for the union of the granted and the requested
   scopes, never one the server did not name; the admin's scope list for
   the server still bounds it.
3. Audited as `upstream.scope_needed:<alias>:<scope>`.

Tasks and tests:
- [ ] upstream: parse the challenge (`TestInsufficientScopeNamesTheScope`, already planned)
- [ ] connect page with `scope` (`TestConnectAsksForTheUnionOfScopes`, `TestScopeOutsideTheServersListIsRefused`)
- [ ] Acceptance `TestScopeStepUpEndToEnd`

**C. Shared org-wide credentials** (by explicit admin choice). ADR needed
first (0031). It decides who may create one (owners), which tools it may
serve (a required toolset, ADR 0026), how calls through it are audited
(`account=org`), and why per-person stays the default (ADR 0017).

Tasks and tests: after the ADR.

**D. URL-mode elicitation and input-required** for the connect link
(MCP 2025-11-25 / 2026-07-28). The goal is that a client that supports it
opens the connect page itself. This needs the protocol package
(`internal/protocol`, ADR 0004): spec with F02.

**E. Refresh coordination across replicas** comes with the Postgres store
(F03): `SELECT … FOR UPDATE SKIP LOCKED` on the grant row. Nothing to do
on SQLite (one writer).

## F08. Toolsets
Status: In progress (first slice built and reviewed 2026-11-20, ported onto main as 0.14.0 on 2026-11-21; ADR 0026 accepted 2026-11-21; the owner's walkthrough, `docs/manual-test.md` section 16, open)
ADRs: 0020, 0026

Goal: an admin gives a team some of a server's tools rather than all of
them, so each AI client lists what its person needs (less context, fewer
tools the model may call).

Acceptance:
1. For each team on a server, an admin chooses all tools (the default) or a
   list of tool names. Existing installs see no change.
2. A person sees a tool when any of their teams that reaches the server
   sees it. A team that sees all tools opens all of them.
3. The gateway lists only the tools a person sees. A call on any other tool
   is answered exactly like an unknown name. A change applies to the next
   request.
4. A new tool on a server is hidden from narrowed teams until chosen; a
   chosen tool still needs review (pins).
5. Removing a team from a server, or deleting the team, removes its list.
   Each change is audited. Only those who manage servers change it; members
   cannot read it.
6. The server page shows, per team, "all tools" or the chosen ones, and lets
   an admin choose them from the server's reviewed tools. Start shows each
   person only the tools they see.

Threat note:
- Toolsets narrow visibility only: rules, approvals and pins still decide
  every call on a visible tool, and a toolset never makes an unreviewed
  definition visible.
- Fail closed: a narrowed team sees nothing that is not chosen, new tools
  included. An error reading toolsets fails the request.
- No probing: a hidden tool answers like a name that does not exist.

Tasks and the tests that name them:

- [x] Spec (this section) and ADR 0026
- [x] Store: migration `00013_server_team_tools.sql` (`server_teams.narrowed`, `server_team_tools` deleted with its `server_teams` row); `SetTeamTools` (nil means all), `TeamTools`; `Access` carries per-server tool sets (nil = all); removing a server's team and deleting a team or the server drop the list; saving a server's teams keeps the lists of the teams that stay
  - tests: `TestTeamToolsNarrowAccess` (union across teams, all wins, an empty list hides all but still reaches the server, `everyone` narrowed, back to all, only a team on the server), `TestTeamToolsFollowTeamChanges`, `TestTeamToolsAreIsolatedPerOrg`
- [x] Gateway: lists and resolve filter tools by `Access.SeesTool`; `/me/tools` counts only seen tools
  - tests: in the acceptance test below (the list stage needs a live upstream)
- [x] Admin API: `PATCH /api/v1/servers/{alias}/teams/{slug}` (`{"tools": [...]}` or `null` for all; `servers.manage`; audited); the server list shows `team_tools` to managers; `/me/tools` counts follow
  - tests: `TestTeamToolsAPI` (repeats dropped and sorted, managers see `team_tools` and members do not, validation, a team not on the server and an unknown server are 404, none and back to all, audit); the counts on Start in the acceptance test
- [x] App: on the server page, per team, "All tools" or "Only these" with the reviewed tools to tick
  - tests (Vitest): `pages.test.tsx` toolsets (a saved name the server no longer offers is shown, choosing tools sends the list, back to all sends null); 48/48
- [x] Acceptance: `TestToolsetsLimitWhatATeamSees` (a designer lists the one chosen tool while a developer lists both; a call on the hidden one answers like an unknown name; Start counts what the designer sees; a new choice applies once the client's short-lived list is refreshed; members cannot change it)
- [x] Docs: README, `docs/manual-test.md` section 16, version 0.14.0
- [x] Security review 2026-11-20 (`docs/reviews/2026-11-20-f08-toolsets-security-review.md`): no findings at 0.8 or above. Acted on: the server list counts for members only the tools they see; a grant needs the requester to still see the tool; the server page says prompts and resources are not narrowed
- [ ] Later: prompts and resources (a `kind` column); scope keys on pins (F11)

### Next slice (spec 2026-11-21): prompts and resources

Goal: a toolset can leave out a server's prompts and resources too, not
only its tools.

Acceptance:
1. `server_team_tools` gains `kind` (tool, prompt, resource). A narrowed
   team sees only what is listed, kind by kind. A kind with no entries
   means all of it, as today for tools, so existing lists keep their
   meaning.
2. A hidden prompt or resource is answered like an unknown name and
   audited `denied:not_in_toolset` (F26 R-03).
3. The server page's "Which tools each team sees" becomes "What each team
   sees", grouped by kind.

Tasks and tests:
- [ ] Migration; `SetTeamTools` takes a kind (`TestToolsetsPerKind`, `TestExistingListsKeepMeaningAll`)
- [ ] Gateway lists and reads (`TestHiddenPromptIsUnknown`, `TestHiddenResourceIsUnknown`)
- [ ] App; Vitest; acceptance `TestToolsetsForPromptsAndResources`

## F09. Policies
Status: In progress (core done, reviewed; second slice built 2026-11-20, ported onto main as 0.15.0 on 2026-11-21, ADR 0027 accepted 2026-11-21)
ADRs: 0001, 0011, 0014, 0027

- [x] Allow, deny, ask rules by server alias, tool pattern, subject (`*`, `role:`, `token:`); priority, ties deny > ask > allow; expiring rules (`internal/policy`)
- [x] Own glob matcher where `*` covers any characters, including `/`
- [x] Org default action (allow, ask, deny); rules apply to tools, prompts and resources ("ask" fails closed for prompts and resources)
- [x] Presets per server: read-only, ask-before-writes; preset rules tied to the reviewed definition hash
- [x] Annotations tighten defaults only (explicit destructiveHint means ask)
- [x] Admin API: list, create, delete rules, apply presets, set default; every change audited
- [x] Rules removed with their server
- [ ] Cross-server flow rule (source and sink tags)
- [ ] Admin overrides of annotations shown to clients
- [ ] Cache rules per org with invalidation (now read per call)

### Second slice: conditions on arguments, safer grants, YAML (2026-11-20)

ADR 0027. Goal: rules about what a call does, not only which tool it is;
grants that can never outrank a deny; rules kept in Git.

Acceptance:
1. A rule may carry up to 8 conditions on the call's arguments (`equals`,
   `not_equals`, `in`, `matches` a glob, `exists`, `missing`), all of which
   must hold. "Deny `delete_repo` when `repo` is `prod`" denies that call
   and leaves `sandbox` to the other rules.
2. Conditions read the canonical arguments, the ones sent upstream. A value
   of another type or a missing path never satisfies `equals`, `in` or
   `matches`.
3. A grant (F10) turns an ask into allow and never a deny into allow,
   whatever the deny's rank or conditions.
4. Rules export as YAML and import back (`merge` or `replace`, `dry_run`),
   refused whole when a rule is invalid or names something unknown.
5. The Rules page shows each rule's conditions, lets an admin add them, and
   offers export and import.

Threat note:
- Conditions read exactly what runs upstream. Numbers compare by exact value
  at any length. An argument of another type than the rule's value is
  unsure, since servers often read "1" as 1 or "yes" as true: an unsure
  deny or ask applies, an unsure allow does not. Case-insensitive names need
  the "any case" tests. Allow-lists fail closed by construction (security
  review 2026-11-20).
- No regular expressions: `matches` is the rules' own glob, so a rule
  cannot be made to take unbounded time.
- Import runs in one transaction and only for `policy.manage`. A replace
  never deletes grants; the dry run changes nothing.

Tasks and the tests that name them:

- [x] Spec (this section) and ADR 0027
- [x] Policy: `Condition` (`Valid`, `Holds`), `Request.Args`; grants evaluated after the rest and only over an ask
  - tests: `TestConditions` (each operator, types never mix, numbers by value, a huge exponent compared as written, nested and missing paths), `TestConditionValidation`, `TestGrantOnlyReplacesAsk` (a conditioned deny tied with an ask still wins over a grant at any rank; no ask, nothing to replace; another member's grant does nothing), `TestUnreadableConditionsFailClosed`
- [x] Store: migration `00014_rule_conditions.sql` (`policies.conditions` JSON, `policies.grant_of`); `ImportPolicies` (one transaction, replace keeps grants, default set with it)
  - tests: `TestPolicyConditionsRoundTrip`, `TestImportPoliciesKeepsGrants` (a refused import changes neither rules nor default)
- [x] Gateway: the canonical arguments go to the decision; a grant is checked against the approved call's own arguments
  - tests: in the acceptance test below
- [x] Admin API: `conditions` on create and list; F10 grants carry `grant_of`; `GET /api/v1/policies/export`, `POST /api/v1/policies/import`
  - tests: `TestCreateRuleWithConditions`, `TestPoliciesExportImport` (a member's rule exports as `person:`, grants left out, dry run changes nothing, replace keeps grants, merge adds, unknown team, unknown person, bad condition, unknown field, bad version and bad YAML refuse all in dry run and for real, approvers refused, audited); `TestApproveWithGrant` and `TestGrantRefusedWhenRulesChanged` now check the grant mark instead of a rank
- [x] App: conditions shown and added on Rules (a person's way of typing values: JSON when it is JSON, else text; a comma list for "is one of"); grants labelled; export and import with a check first
  - tests (Vitest): `pages.test.tsx` rules (a condition shown and sent, import checked before applied); 50/50
- [x] Acceptance: `TestRulesOnArguments` (a deny on `text = secret` refuses that call while others ask; a grant lets the others through and not that one; the rules survive a YAML round trip)
- [x] Docs: README, `docs/manual-test.md` section 17, version 0.15.0
- [x] Security review 2026-11-20 (`docs/reviews/2026-11-20-f09-policies-security-review.md`): two Medium findings, both fixed. Long numbers were compared as text (`42.000…0` missed "42"): numbers now compare by exact value at any length and arguments refuse exponents beyond 9 digits (`TestConditions`, `TestCanonicalRefusesHugeExponents`). Servers coerce types and ignore case: an argument of another type is unsure and fails closed, and "any case" tests exist (`TestUnsureFailsClosed`). Also acted on: a grant lifts asks up to the rank it was made over, never a higher one (`TestGrantCoversItsRankOnly`); condition values keep exact numbers from JSON (`TestConditionNumbersStayExact`); grants made before migration 11 are marked (`TestMigrationMarksOldGrants`); the dry run checks members; an export leaves out rules for removed members

### Next slices (spec 2026-11-21)

**A. Rules cached per org.** Goal: a call no longer reads every rule from
the database; a change still applies at once.

Acceptance:
1. Rules are kept per org with a version number. Every policy write bumps
   the version in the same transaction, and the next call sees the new
   rules (no time-based staleness).
2. A failed read never falls back to an empty rule set: the call is
   refused (fails closed).

Tasks and tests:
- [ ] Store: `policy_version` per org (`TestEveryPolicyWriteBumpsTheVersion`)
- [ ] Gateway cache (`TestRuleChangeAppliesToTheNextCall`, `TestCacheReadFailureRefuses`); benchmark before and after

**B. Annotation overrides.** Goal: an admin corrects what a server says
about a tool (`readOnlyHint`, `destructiveHint`, `openWorldHint`) for
clients and for rules.

Acceptance:
1. Per server and tool, an override of each hint. It is kept with the pin
   (it follows the accepted definition), audited, and shown in the review.
2. Clients get the overridden annotations. Rules on annotations
   (presets) read them. The upstream's own values stay visible to admins.

Tasks and tests: `TestOverrideShownToClients`, `TestRulesReadOverrides`,
`TestOverrideFollowsTheAcceptedDefinition`; app; acceptance.

**C. Cross-server flow rules** (source and sink tags), e.g. nothing read
from `crm` may be sent to `web`. ADR needed first (0032). It decides:

- how a session's reads are remembered (per client session, bounded, not
  stored);
- what "sent to" means (a later call on a sink tag in the same session);
- why this is a tripwire that asks or denies, not data-flow proof.

Tasks and tests: after the ADR.

## F10. Approvals
Status: In progress (core done, reviewed)

ADRs: 0005

- [x] Durable, single-use approvals bound to requester, server, tool and canonical arguments digest; atomic consume; one open approval per exact call (`internal/approval`)
- [x] Arguments frozen to a canonical form (sorted keys, duplicates refused) that is hashed, approved and sent upstream; fuzz-tested
- [x] Hold-then-retry: calls wait up to `PRIVO_APPROVAL_HOLD` (25 s); otherwise the model is told to retry the same call once approved
- [x] Denials stand for the identical call until expiry; 10-minute lifetime
- [x] Approver role (control tokens only); self-approval refused per person unless allowed by the org
- [x] Approvers see full arguments while open; arguments purged once used or denied; views audited
- [x] Limits: 20 pending approvals and 5 held calls per requester
- [x] Approval restored if the call cannot start; approver recorded on the call's audit row
- [ ] Input-required (URL elicitation) for 2026-07-28 clients
- [x] Web approval page with sessions and CSRF (F05 first slice, ADR 0016)
- [ ] Step-up re-authentication before high-risk approvals (F05 later)
- [ ] Slack with signing verification and linked identities; email
- [ ] Batching of similar requests, approver dashboard (who approves everything)

### Second slice: approvals people live with (2026-11-20)

Goal (owner's plan, 2026-11-20): an approver is not asked the same thing
all day, a person can take back a request they no longer want, and decided
approvals can be looked back on.

Acceptance:
1. Approving can also allow the same tool on the same server for the
   requester for 15 minutes, 1 hour or 8 hours ("scoped grant", ADR 0005).
   It is an allow rule with that expiry, subject `user:<id>` (the
   requester's member; `token:<id>` for a token of no member), named after
   the approval, shown on Rules and removable there. It ranks one above the
   rule that asked, so a deny that outranked the ask still wins.
2. Only someone who may decide the approval can grant, never for longer than
   8 hours, never for their own request, and only for that exact tool (not
   a pattern). The approval and the grant are each audited.
3. A person sees their own pending requests (Start page,
   `GET /api/v1/me/approvals`) and withdraws one
   (`POST /api/v1/me/approvals/{id}/withdraw`). A held call ends as refused;
   a retry asks again. Nobody withdraws another person's request.
4. Approvers page through waiting requests beyond the first page and see
   the history of decided ones (approved, denied, withdrawn, used,
   expired), newest first, without their arguments.
5. Rules accept `user:<id>` for an existing member of the org only.

Threat note:
- A grant widens access, so it is narrow by construction: one tool by exact
  name, one server, one person, at most 8 hours, from a person who could
  have approved each call anyway. It never outranks a deny that already won.
- Withdrawing is limited to the requester's own member, found by the
  token's bound user (ADR 0022), so a request cannot be cancelled by
  someone else.
- History shows no arguments (they are purged once a call is decided or
  used).
- A grant is checked when it is made, against the rules and the requester
  as they are then: it is made only when the call would still ask, ranks
  above the rule that asks now, and is pinned to the reviewed definition.
  Nobody grants for their own request, even where self-approval is allowed.
  A grant covers all of the person's tokens (it is "for this person").

Tasks and the tests that name them:

- [x] Store: migration `00012_approval_rule.sql` (`approvals.rule_id`: the rule that asked); `ListApprovalsPage` (pending or history, newest first, a cursor); `MyApprovals` (by the member's tokens); `user:<id>` rules need a member of the org
  - tests: `TestApprovalPages` (pages never repeat a row, including two asked in the same instant; a cursor from elsewhere is refused; other orgs' rows never show), `TestMyApprovalsAndWithdraw` (a token that only names the person is not theirs), `TestUserRuleNeedsAMember` (another org's member and a removed one are refused)
- [x] Policy: subject `user:<id>` matched against the caller's member; `ExactTool` turns a name into a pattern that matches only it
  - tests: `TestUserSubject` (another member, no member and another tool are not matched), `TestValidSubjectUser`, `TestValidation`
- [x] Approval service: the rule that asked is kept; `Withdraw` by the requester's member only; a withdrawn request is its own outcome, and the AI client is told to call again rather than that a person denied it
  - tests: `TestWithdrawOwnRequestOnly`, `TestHeldCallEndsWhenWithdrawn`
- [x] Admin API: approve with `{"grant": "15m"|"1h"|"8h"}`; `GET /api/v1/approvals?status=pending|history&before=&limit=` with `next`; `GET /api/v1/me/approvals`; `POST /api/v1/me/approvals/{id}/withdraw`
  - tests: `TestApproveWithGrant` (rule shape, one above the asking rule, expiry, the person named on Rules, audit; a bad grant decides nothing), `TestGrantFromTheDefaultAndRefusals` (rank 0 under the default; a refused self-approval grants nothing; a plain approve grants nothing), `TestApprovalHistoryAPI` (pages, history without arguments, bad cursor, members refused), `TestWithdrawAPI`
- [x] App: Approvals has Waiting and History, with "Load more"; approving offers "also allow for…"; Start lists the person's waiting requests with Withdraw; Rules names a grant's person
  - tests (Vitest): `pages.test.tsx` (approve with a grant, load more and history, withdraw from Start); 46/46
- [x] Acceptance: `TestApprovalGrantAndWithdraw` (a grant lets the next calls through without asking and names the person on Rules; without it the call asks again; a withdrawn request cannot be approved and a retry asks anew; history lists both without arguments. A held call ending on withdrawal is `internal/approval` `TestHeldCallEndsWhenWithdrawn`)
- [x] Docs: README, `docs/manual-test.md` section 18, version 0.16.0
- [x] Security review 2026-11-20 (`docs/reviews/2026-11-20-f10-approvals-security-review.md`): two Medium findings, both fixed. No grant for one's own request (`TestNoGrantForOneself`); a grant is checked against the current rules, requester and accepted definition before anything is decided, and pinned to that definition (`TestGrantRefusedWhenRulesChanged`, `TestGrantNeedsTheAcceptedDefinition`)

### Next slices (spec 2026-11-21)

**A. Slack.** Goal: approvers decide in Slack without opening Privo, and
their decision is theirs.

Acceptance:
1. An org connects a Slack app: `PRIVO_SLACK_SIGNING_SECRET` and a bot
   token in the vault, plus a channel per org.
2. A new approval posts its server, tool, requester, reason and a digest of
   the arguments. The arguments themselves are posted only if the org opts
   in.
3. The post has Approve and Deny buttons. Every request from Slack is
   checked with the signing secret (timestamp within 5 minutes). The
   Slack user must be linked to a Privo member who may decide: linked once
   from Privo's People page by a signed-in person, never by Slack email
   matching. Self-approval rules apply.
4. The post is updated with the outcome, whoever decided and wherever.
   Audited like a decision in the app, with `client=slack`.

Tasks and tests:
- [ ] ADR 0033 (identity linking, what is posted)
- [ ] `internal/notify/slack` (`TestSlackSignatureChecked`, `TestOldTimestampRefused`,
  `TestUnlinkedSlackUserCannotDecide`, `TestSelfApprovalRefusedFromSlack`)
- [ ] Acceptance with a fake Slack API

**B. Email.** Goal: approvers are told by email. Deciding still happens
in Privo: links in mail only open the approval, never decide by a click.

Acceptance: SMTP settings (`PRIVO_SMTP_*`); one mail per new approval to
those who may decide, batched within a minute; no arguments in the mail.
Tests: `TestApprovalMailHasNoArguments`, `TestMailBatchedPerMinute`.

**C. Step-up before high-risk approvals.** A rule can mark its asks
`high_risk`; deciding one needs a fresh sign-in (F05 B).
Tests: `TestHighRiskApprovalNeedsStepUp`.

**D. Approver dashboard and batching.**
- Similar pending requests (same requester, server, tool and arguments
  digest) are shown as one row with a count, decided together.
- The dashboard shows who decides what and how fast, and flags an
  approver who approves everything.

Tests: `TestSimilarRequestsGrouped`, `TestBatchDecisionAuditsEach`.

**E. Input-required (2026-07-28 clients)** comes with the protocol package
(F02, ADR 0004).

## F11. Manifest pinning and scanning
Status: In progress (pins built with F02; the whole definition and its diff with F14 R-01 in 0.8.1; block, scan and opt-in choosing 2026-11-20, ported onto main 2026-11-21)

ADRs: 0001, 0002

Threat note: the scan is a hint for the reviewer, never a gate: it accepts,
blocks and hides nothing by itself, and finding nothing proves nothing. The
gate is still a person accepting an exact hash. Upstream text is shown only as
text, with characters a person cannot see made visible (⟦U+200B⟧).

- [x] Per-tool canonical hash pins; first pin reviewed (F02: `pins`, `RecordSeen`, `AcceptPins`, the gateway serves accepted definitions only)
- [x] New tools hidden until accepted; changed tools hidden until the new definition is accepted (F02)
- [x] Diff view: main's review (F14 R-01, 0.8.1) shows the whole definition with hidden characters made visible and a line diff against the accepted version; the API also returns that version as `previous`
- [x] Block: `POST /api/v1/servers/{alias}/pins:block` (managers of servers; audited `pins.block:N`) blocks exactly the listed pending definitions; they leave the review, stay hidden, are never pruned, and can still be accepted (`AcceptPins` takes pending or blocked)
- [x] Scan for hidden instructions (`internal/scan`): every string of a definition (description, titles, the schema's property names and descriptions, annotations) for characters a person cannot see, tags that address the model, overriding or concealing instructions, "before using this tool you must", secret files and exfiltration (warnings); secret words, long texts and encoded runs (notes). At most 50 findings; `findings` on every pin
  - tests: `internal/scan` `TestScanFindsInstructionsAfterTheFirstLine`, `TestScanFindsHiddenCharacters`, `TestScanWalksSchemaAndAnnotations`, `TestScanNotesLongAndEncodedText`, `TestScanQuietOnOrdinaryTools` (no warning on GitHub, Notion and DeepWiki shapes), `TestScanUnreadableAndBounded`, `TestReveal`; `internal/store` `TestBlockPins`; `internal/control` `TestPinReviewShowsPreviousAndFindings`; acceptance `TestAdminUIFromScratch` (an honest tool has no findings); Vitest `review.test.ts`, `pages.test.tsx` (the whole description as text, nothing chosen by default, the diff and warnings, block)
- [ ] Scope keys on pins (per team or per person), with toolsets (F08)
- [ ] Phrases in other languages; a scan of resource contents and tool results (F02 result checks)
- [x] Security review 2026-11-20 (`docs/reviews/2026-11-20-control-plane-1-security-review.md`, with F05 R-01, F07 R-02, R-03 and F24 R-01, R-02): no findings at 0.8 or above. Acted on: pin decisions are audited with each `kind/name@hash`
- [x] From that review: `previous` and "Available to AI clients" should follow the definition the gateway serves now, not the newest accepted one by time; route token events through `audit.Writer`; refuse refreshing a disabled server's grant **Done in 0.18.0:** pins carry `serving` (accepted and presented by the server now); `previous` is the one served now, else the newest accepted; the app lists only served ones (`TestPinViewsFollowWhatIsServedNow`, Vitest). A disabled server's grants are not refreshed (`ErrServerOff`, `TestNoRefreshForAServerTurnedOff`). Token events stay in the token's own transaction (F24 R-02 needs it); since F12 every audit row is sealed into the chain whatever wrote it, so the chain, not `audit.Writer`, is the complete record a future sink reads

### Next slices (spec 2026-11-21)

**A. Scan of what servers send back.** Goal: hidden instructions in tool
results and resource contents are flagged like those in definitions.

Acceptance:
1. The F11 scanner runs on text in results and contents up to a size cap.
   On a warning, the call goes through but is audited
   `result.flagged:<rule>`, and the org may choose to withhold the result
   (`result_scan=block`, default off).
2. It never logs the text; the record has the rule and position only.

Tests: `TestResultWithHiddenInstructionIsFlagged`,
`TestResultTextNeverLogged`, `TestBlockModeWithholds`.

**B. Phrases in other languages.** The scanner's phrase lists gain German,
French, Spanish, Portuguese, Japanese and Chinese, with tests per
language, plus a corpus of harmless descriptions that must stay
unflagged.

**C. Pins scoped to a team or person.** Comes after F08's prompts and
resources. Spec with ADR 0026's follow-up.

## F12. Audit log
Status: In progress (second slice built 2026-11-21, ported onto main as 0.16.0; ADR 0028 accepted 2026-11-21)
ADRs: 0001, 0002, 0006, 0028

- [x] Synchronous intent record; batched completion; bounded queue that refuses rather than drops
- [x] Arguments recording off by default; when on, arguments stored encrypted under the org key
- [ ] Per-org fairness in the queue (with the Postgres engine)

### Second slice: tamper evidence, search, export, retention (2026-11-20)

ADRs 0006 and 0028. Goal: an owner can show that the trail was not
rewritten; an auditor finds and exports what they need; old events go away
on a schedule, and recorded arguments can be erased, all without breaking
the chain.

Acceptance:
1. A sealer chains new events into signed blocks every few seconds. The
   signing key is derived from the master key and never stored.
2. `privo audit verify` and `GET /api/v1/audit/integrity` report the
   blocks and events checked and the first problem found. A changed,
   deleted or reordered row, or a forged block, is found.
3. `GET /api/v1/audit/checkpoints` and `GET /api/v1/audit/keys` give a
   customer what to keep outside Privo.
4. Calls and admin events can be filtered (person, server, action, decision,
   time range), paged, and exported as CSV.
5. A retention of N days deletes sealed events older than that; blocks stay
   and verification still passes. Erasing a person's recorded arguments
   keeps the chain valid.
6. The Activity page offers the filters, Load more, Export CSV, the
   integrity check, and, for those who manage the audit, the retention and
   erasure settings.

Threat note:
- The signing key never touches the database. A database administrator who
  edits rows breaks verification.
- Retention never deletes unsealed events, and erasure clears payloads
  only, never the fields the chain covers.
- Search and export need `audit.read`; retention and erasure need
  `audit.manage`. Each is audited.

Tasks and the tests that name them:

- [x] Spec (this section) and ADR 0028
- [x] Store: migration `00015_audit_chain.sql` (`audit_events.chain_seq`, `payload_sha256`; `audit_blocks`); `SealAudit`, `AuditBlocks`, `AuditEventsInRange`, `SearchAudit`, `PruneAudit`, `EraseAuditPayloads`
  - tests: `TestSearchAudit` (pages, filters by actor, server, decision and time, admin kind, bad cursor, other orgs); sealing, retention (whole blocks, never unsealed) and erasure (digests kept) in the audit tests below
- [x] Audit: signing key derivation (HKDF from the master key, per org), event and block hashes, `Sealer` (with hourly retention), `Verify`
  - tests: `TestSigningKeyIsDerived`, `TestSealAndVerify`, `TestVerifyFindsTampering` (a changed field or actor, a deleted or reordered event, changed arguments or digest, a deleted block, a forged signature, an untrusted key, a broken link, an edited block hash), `TestVerifyRefusesAnotherKey` (a forger re-signing with their own key), `TestVerifyAfterRetentionAndErasure`
- [x] Admin API and CLI: filters and cursor on `/api/v1/audit/events`; `/audit/export`, `/audit/integrity`, `/audit/checkpoints`, `/audit/keys`, `/audit/erase`; `retention_days` in audit settings; `privo audit verify`
  - tests: `TestAuditSearchAPI` (pages, bad filters, CSV without arguments and with formulas neutralised, members refused), `TestAuditIntegrityAPI` (off without keys, report, keys and checkpoints agree, retention bounds and permission), `TestAuditEraseAPI` (only that person's calls, permission), `cmd/privo` `TestAuditVerifyCommand` (intact, a changed row, another master key)
- [x] App: Activity filters, Load more, Export CSV, integrity, retention and erasure
  - tests (Vitest): `pages.test.tsx` audit (filters, load more, an integrity problem shown, retention and erasure sent; hidden from those who only read); 52/52
- [x] Acceptance: `TestAuditChainEndToEnd` (calls sealed, verify passes, a row changed in the database is found)
- [x] Docs: README, `docs/manual-test.md` section 18, version 0.16.0
- [x] Security review 2026-11-21 (`docs/reviews/2026-11-21-f12-audit-security-review.md`): three Medium findings, all fixed. Each let someone with database write access but no master key change history while verification said intact.
  - Unsigned deletion passed as retention: deletions are now signed prune records (`audit_prunes`), and events may be missing only up to the latest one (`TestDeletingOldEventsWithoutASignedPruneIsCaught`, `TestSignedPruneVerifies`, `TestForgedPruneRecordIsCaught`)
  - Events outside every block were never checked (`TestEventOutsideTheBlocksIsCaught`)
  - Backdated events were sealed as if old: a block's seal time is signed, and an event dated more than 5 minutes before the previous seal fails (`TestBackdatedEventIsCaught`, `TestSealTimeIsSigned`)
  - Notes fixed: an erased unsealed payload keeps its digest (`TestErasedUnsealedPayloadKeepsItsDigest`); the new key endorses the old one during a rotation (`TestOldBlocksVerifyAfterKeyRotation`); retention keeps at least 30 days (`TestRetentionKeepsAtLeastThirtyDays`, API refuses 1 to 29); ADR 0028 says what only outside checkpoints show
- [ ] Owner's walkthrough: `docs/manual-test.md` section 18

### Next slice (spec 2026-11-21)

**Fairness in the audit queue.** One busy org must not delay another's
completions. Comes with the Postgres store (F03): a queue per org with
round-robin flushing. Test: `TestBusyOrgDoesNotStarveAnother`. On SQLite
(one org per install) nothing to do.

## F13. Stdio runner
Status: Not started

ADRs: 0007

- [ ] Versioned runner protocol (spawn, stream, kill, health) over mutual TLS
- [ ] Per-process user, cgroup limits, read-only root, seccomp
- [ ] Pinned packages installed at registration; no `npx -y`
- [ ] Isolated network; egress allow-list proxy

## F14. Admin UI
Status: In progress (the Preact admin app is deployed; third slice, the admin gaps, built and reviewed 2026-11-21 as 0.17.0; every finding of the independent review of 2026-11-03 fixed by 0.17.1)
ADRs: 0016, 0017, 0018 (superseded), 0019

### First slice: goal

After claiming the setup link, the owner does everything in the browser: adds
a server, reviews its tools, sets rules, invites people, gives each AI client
a token, and reads what happened. As simple as possible: plain pages, one
form per task, no script.

### First slice: acceptance

1. Pages call the admin API in process as the signed-in person (ADR 0018); a
   page can do exactly what the person's role can do through the API, and every
   action is audited with the person as actor (`user:<id>`).
2. Navigation shows only the pages the role can use.
3. Every form posts with the session's CSRF token and passes the origin check;
   errors from the API are shown on the page in plain words.
4. Everything an upstream or a person controls (tool names and descriptions,
   server errors, names) is escaped; no inline script or style anywhere.
5. Tokens are shown once, on the page that created them, with the MCP address
   and a ready-to-paste command for Claude Code.
6. Members: owners alone grant or change owner and admin; nobody changes their
   own role or removes themselves; the last owner cannot be demoted. Removing a
   member ends their sessions, deletes their upstream grants and revokes every
   active token that carries their person.

### First slice: tasks and the tests that name them

- [x] Spec and ADR 0018 (this section); CLAUDE.md stack line
- [x] `control.API.Serve` for session principals; `auth.Principal.UserID` and `ActorID()` (audit actor `user:<id>`); `auth.PermManageMembers` (owners, admins)
  - tests: `TestServeRunsHandlersAsTheSessionUser`, `TestServeChecksPermissions`, `TestServeAuditsTheUser`, `TestServeIsNotReachableOverHTTP`
- [x] Members and own-token endpoints (`/api/v1/members`, `/api/v1/me/tokens`); `store.ListUsers`; removing a member also closes their pooled upstream sessions
  - tests: `TestMembersRoleRules`, `TestLastOwnerCannotBeRemovedOrDemoted`, `TestRemoveMemberRevokesTokensSessionsAndGrants`, `TestMyTokensAreOnlyMine`
- [x] Pages: servers, server, rules, people, tokens, activity; owners and admins land on Servers, members on Tokens, auditors on Activity (replaced by the app in the second slice; the page tests went with them)
  - tests: `TestNavByRole`, `TestPagesNeedSignInAndRole`, `TestServersPageListsAndAdds` (API errors shown, secrets never echoed), `TestServerPageReviewsTools`, `TestUpstreamTextIsEscaped`, `TestRulesPage`, `TestPeoplePage`, `TestTokensPageShowsTheTokenOnce`, `TestActivityPage`, `TestEveryFormNeedsCSRF`, `TestManagersLandOnServers`, `TestMembersLandOnTokens`
- [x] Acceptance through the real binary with a browser-like client: `TestAdminUIFromScratch` (setup link, add a server, accept its tool, add an ask rule, invite a member, the member makes their token, the call waits, approve, activity, remove the member and their token stops working)
- [x] Real browser walkthrough from an empty install (Chromium, 2026-11-03): no console or CSP errors; it led to an escaped `pattern` attribute (Chrome compiles it with the `v` flag), shorter decision and approver text in Activity, and members landing on Tokens
- [x] Docs: README, deploy guide, `docs/manual-test.md` (the test plan, browser only); version 0.5.0

### Second slice: the admin app (ADR 0019)

Goal: the same tasks, but fast. The owner found the server-rendered pages slow
(every click a page load, every button a post, a redirect and a reload). The
admin site becomes a Preact single-page app like VictoriaLogs' UI, embedded
in the binary; sign-in and OAuth pages stay server-rendered.

Acceptance:

1. Moving between pages loads no page; a page revisited shows its last data
   at once and refreshes it. Assets are content-hashed, cached for a year and
   served gzipped; the shell is never cached.
2. The app reaches the admin API only through `/admin/api/v1/...`, run in
   process as the signed-in person: the API's checks, rules and audit apply
   unchanged. `/api/v1` never accepts the session cookie.
3. Every bridge request needs the session and the session's CSRF token in
   `X-Privo-CSRF` (reads too); changes also need a same-origin `Origin` (or
   `Sec-Fetch-Site: same-origin`) and a JSON body. Bodies are capped at
   64 KiB. A refused request never reaches the API.
4. CSP for the app: own script only, no inline script or style, no eval, no
   other origin. Data is rendered only as text; the source never sets HTML.
5. The build is committed (`web/dist`) and embedded; Go builds and the image
   need no Node; CI fails if the committed build differs from the source.
6. A refused self-approval is audited by the API (it was by the old page).

Tasks and the tests that name them:

- [x] ADR 0019; ADR 0018 superseded (its in-process API, member endpoints and setup link stand); CLAUDE.md stack line
- [x] `web/`: Preact 10, Vite 8, TypeScript 6, no router or UI library; `make ui`, `make ui-test`, `make ui-check`; CI job `ui`
  - tests (Vitest): `api.test.ts` (CSRF header on every call, JSON only with a body, the server's message, 401 goes to sign-in, unreachable server), `router.test.ts`, `source.test.ts` (no HTML from data), `pages.test.tsx` (nav by role, refused pages ask nothing, sign-out form, add a server, upstream text stays text, accept the ticked tools, delete needs the name, a token shown once with its command, approve and deny with the API's refusal shown, invite link)
- [x] Website: app pages, hashed assets, `/admin/api/session`, the bridge, `/admin/api/me/connections`; the server-rendered admin, approvals and connections pages removed
  - tests: `TestAppPagesServeTheShellToSignedIn`, `TestAppPagesRedirectWithoutSession`, `TestAppAssetsAreCachedForever`, `TestAppNeedsItsBuild`, `TestSessionEndpointSaysWhoAndWhatTheyMayDo`, `TestBridgeRunsAsThePerson`, `TestBridgeNeedsSessionHeaderTokenOriginAndJSON`, `TestSignedOutSessionIsRefusedByTheBridge`, `TestRoleChangeAppliesAtOnce`, `TestConnectionsPageListsOwnGrantsOnly`, `TestDisconnectRevokesAndDeletes`, `TestWhereEachRoleLands`
- [x] API: a refused self-approval is audited (`TestSelfApprovalRefusalIsAudited`)
- [x] Acceptance through the real binary, as the app calls it: `TestAdminUIFromScratch` (also: the shell's script is served and cached for good), `TestWebSetupAndApprovals` (wrong header token and other origin refused; the token API ignores the session cookie), `TestUpstreamOAuthTwoPeople` (disconnect through the app)
- [x] Real browser walkthrough from an empty install (Chromium, 2026-11-03): the whole first-day flow with no console or CSP errors; in-app navigation 40–70 ms locally with no page load
- [x] Docs: README, deploy guide, `docs/manual-test.md`

### Review findings, open (independent review 2026-11-03, `docs/reviews/2026-11-03-f14-admin-app-independent-review.md`: approve with changes; 0 Critical, 0 High, 2 Medium, 4 Low, 2 Info; fixes parked by the owner)

- [x] R-01 (Medium): the tool review shows only the first line (at most 300 characters) of a description, while the hash that is accepted covers all of it, the input schema and the annotations. Show the whole description as text, the schema, and a diff for a changed definition **Fixed in 0.8.1:** the review shows the whole description as text, the full definition (schema and annotations) behind "Full definition (what is accepted)", and for a changed tool a line diff against the accepted version (`web/src/review.tsx`). Tests: `web/src/review.test.ts`, "the tool review (F14 R-01)" in `web/src/pages.test.tsx`. **Independent review of 2026-11-15, U-02 (Medium), fixed:** the browser drew invisible characters (Unicode tag characters, zero-width, bidi overrides, private use, unassigned, separators, variation selectors) as nothing, though the hash covers them, so a hidden instruction could pass review. Every one is now shown as `⟦U+XXXX⟧` in the description, the diff and the full definition, with a warning that names how many there are. Tests: `review.test.ts` "reveal", "shows hidden characters and warns about them" in `pages.test.tsx`
- [x] R-02 (Medium): the same hole as F07 R-01, one click away in the app: the Tokens page offers a "person" field to admins. Fix with F07 R-01 **Fixed in 0.7.0 (F27).** The field stays as a label and says so.
- [x] R-03 (Low): a refused or cross-site request still slides the session's idle timeout; touch the session only after every check passed **Fixed in 0.17.1:** sessions are looked up without a touch; the bridge and forms touch only after every check passed, pages only when opened from this site or directly (Fetch Metadata, or the Referer without it) (`TestOnlyAcceptedRequestsKeepTheSessionAlive`). Security review of the fixes: `docs/reviews/2026-11-21-f14-review-fixes-security-review.md`, one Low, fixed
- [x] R-04 (Low): the review ticks everything by default, also items that arrive while the page polls. Fixed 2026-11-20: nothing is chosen until the reviewer chooses it ("Choose all N shown" chooses only what is on the page)
- [x] R-05 (Low): the UI build has no audit step (`web/.npmrc` has `audit=false` and `legacy-peer-deps=true`; `npm ci` runs lifecycle scripts; actions are pinned by tag): `npm ci --ignore-scripts`, `npm audit --omit=dev`, `npm audit signatures` in the `ui` job, actions by SHA **Fixed in 0.17.1:** `web/.npmrc` runs no install scripts and no longer turns audit off (legacy peer handling stays, documented: npm 10 crashes on vitest's optional peers); `make ui-audit` (`npm audit --omit=dev`, `npm audit signatures`) runs in the `ui` job; vitest 4.1.12 (GHSA-qxg6-89mg-rqv8); every action in CI pinned by commit SHA
- [x] R-06 (Low): the bridge passes a body on GET and DELETE and accepts any content type with an empty body **Fixed in 0.17.1:** a GET or DELETE with a body is 400, any content type but JSON is 415 with or without a body, both before the session is touched (`TestBridgeNeedsSessionHeaderTokenOriginAndJSON`)
- [x] R-07, R-08 (Info): a session can mint long-lived owner control tokens (consider a confirmation and an audit tag); a token shown once stays in the page's state until the person leaves **Fixed in 0.17.1:** an owner or admin control token made in the browser needs `confirm: true` (the app asks with a checkbox) and its audit event carries client `web` (`TestElevatedTokenFromASessionNeedsConfirmation`); a token shown once goes with "I copied it, hide it" or by itself after 10 minutes (Vitest)

### Third slice: the admin gaps (2026-11-21)

Goal: an admin fixes a server or a removal without starting over, and a
failure says what failed, where. ADR 0029 (editing a server).

Acceptance:

1. A server's page edits its address, its static headers and, for an OAuth
   server whose client was entered by hand, its client id and secret. Header
   values and secrets are never shown or returned; the form says which
   headers are set.
2. A new address ends every person's connection, drops static header values
   not given again in the same edit, and puts every accepted tool back into
   review. An OAuth server cannot change its address. A new client id ends
   every person's connection. One transaction, audited without values.
3. A removed member can be restored by whoever could change their roles;
   they sign in again with the same account and roles, with no tokens,
   sessions or connections from before.
4. Two people can each have a token named `claude-code`; names stay unique
   per person, and service tokens stay unique among service tokens.
5. A failure adding a server shows next to the form that failed, not only at
   the top of the page.
6. A provider that does not answer in time is named, with the step and the
   seconds waited, never "context deadline exceeded".

Tasks and the tests that name them:

- [x] Spec (this section) and ADR 0029
- [x] Store: `EditServer` (config, credentials set and removed, connections ended, review again, audit; one transaction); `RestoreUser`; migration `00016_token_names_per_person.sql`
  - tests: `TestEditServerIsOneTransaction`, `TestEditServerEndsConnectionsAndCredentials`, `TestEditServerSendsToolsBackToReview`, `TestRestoreUser`, `TestTokenNamesAreUniquePerPerson`
- [x] API: `PATCH /api/v1/servers/{alias}` takes `url`, `headers`, `oauth_client` (ADR 0029); `POST /api/v1/members/{person}/restore`; timeouts named in plain words
  - tests: `TestEditServerAddress`, `TestEditServerAddressNeedsHeadersAgain`, `TestEditServerHeaders`, `TestEditServerRefusesOAuthAddress`, `TestEditServerOAuthClient`, `TestEditServerNeverReturnsSecrets`, `TestEditServerAddressEndsPeoplesKeys`, `TestRestoreMemberRules`, `TestTimeoutNamesTheHostAndStep`; upstreamauth `TestTimeoutNamesTheHostOnly`
- [x] App: "Edit" on a server's page; "Restore" on People for removed members; add-server errors next to the form
  - tests (Vitest): edit sends only what changed and warns before an address change; restore; the error shows beside the catalog form
- [x] Acceptance: `TestEditServerEndToEnd` (a new address is reviewed again before any tool is served)
- [x] Security review 2026-11-21 (`docs/reviews/2026-11-21-f14-admin-gaps-security-review.md`): one Medium, three Low, all fixed
  - M1: a concurrent edit could leave a header credential the configuration no longer names, sent to a new address: stale edits refused (409), a new address clears every static header, only named secrets are sent (`TestEditServerRefusesAStaleEdit`, `TestNewAddressDropsEveryStaticHeader`, `TestSecretsSendOnlyWhatTheConfigurationNames`)
  - L1: a pooled per-person session of an older version is never reused (`TestPooledSessionOfAnOlderVersionIsNotReused`); L2: teams checked before anything is written (`TestRefusedEditChangesNothing`); L3: restoring revokes tokens removal did not get to (`TestRestoreRevokesLeftoverTokens`)
- [x] Docs: README, `docs/manual-test.md` section 19, version 0.17.0

### Later in F14

- [ ] Toolsets (F08), context-cost report (F17), policy presets in the page
- [x] Re-enabling a removed member; editing a server (third slice above, 0.17.0)
- [x] Home page (owner's request, 2026-11-20): `/` offered nothing but a link to privo.club, so people typed `/admin` by hand. It now has **Sign in** (to `/admin/`) when the admin site is set up, says so when it is not, and shows the MCP address from `PRIVO_PUBLIC_URL` for AI clients. Still static for a configuration: no script, nothing read from the request, the configured address escaped. Tests: `internal/server` `TestRootPage` (unchanged hardening checks), `TestHomeOffersSignInAndTheMCPAddress`, `TestHomeWithoutTheAdminSite`, `TestHomeEscapesItsConfiguration`

## F15. Coolify deployment
Status: In progress (deployed on Coolify since 2026-10-29, guide in `docs/site/installation/coolify.md`; snapshot backup, restore and the built-in schedule done; scheduled backups are off in production by the owner's choice)

ADRs: 0010

- [ ] Docker Compose file with all defaults; generated secrets; master key file
- [ ] `privo migrate`, snapshot before upgrade, pinned tags
- [x] Snapshot backup and restore, the first slice of the ADR 0010 backup plan (owner's go-ahead, 2026-10-29): `privo backup [--to PATH] [--keep N]` and `privo restore --from FILE [--force]`. Acceptance:
  1. `backup` takes a consistent copy of a live database (gateway running, writes in flight, no downtime) with `VACUUM INTO` on a reader connection. It writes `<name>.partial`, removes the writer-lease row from the copy, checks it (`PRAGMA integrity_check`, schema version known to this binary), and only then links it to its final name. Mode 0600 in a 0700 directory, one file (no `-wal`/`-shm`), and it never overwrites: an existing target is an error and stays intact.
  2. `--to` is a file or a directory (default `<data dir>/backups`). A directory gets `privo-<UTC yyyymmddThhmmssZ>.db`. `--keep N` (N >= 1, directories only) keeps the newest N files of that name pattern and deletes older ones of that pattern only.
  3. The backup holds no plaintext secret and not the master key: credentials are in it only as the vault wrote them (envelope encryption, ADR 0013); acceptance proves a registered credential does not appear in the file.
  4. `restore` checks the file the same way and refuses one from a newer schema. It refuses while a gateway holds the writer lease, refuses to replace an existing database unless `--force`, and with `--force` moves the old `privo.db`, `-wal` and `-shm` aside as `*.replaced-<UTC ts>` (never deleted). A failed restore leaves the current database untouched. The restored file is put in place by rename, mode 0600.
  5. Both write an admin audit event (`backup.create` into the live database after the copy; `restore.apply` into the restored one); a failure to write it is a warning, not a failure.
  6. Restore drill (acceptance, real binary): a gateway with a registered server and credential is backed up while running; a new data directory is restored from the file; a gateway on it with the same master key lists the server and a `tools/call` works; with a different key the gateway refuses to start and says that the master key cannot open the database (a startup check, `runGateway`: the org's current key must unwrap). Tests, written first: `internal/store` `TestBackup*`, `TestRestore*`, `TestBackupNameAndPrune`; `cmd/privo` `TestBackup*`, `TestRestore*`; acceptance `TestBackupAndRestoreDrill`, `TestRestoreWithTheWrongKeyRefusesToStart`.
  Threat note: a backup is as sensitive as the database (token hashes, audit, policy, wrapped keys) but useless without the master key, which is kept apart (ADR 0013). A backup on the same volume protects against mistakes and corruption, not host loss: the off-host copy is the operator's job for now (docs/deploy-coolify.md); built-in S3 upload is left open. Retention deletes only files named like our own snapshots. `restore` is destructive by nature, so it needs `--force`, keeps the old files and refuses a live gateway.
- [x] Scheduled backups, built in (owner's go-ahead, 2026-10-29): the image has no shell, so Coolify's scheduled tasks cannot call `privo backup`. `PRIVO_BACKUP_INTERVAL` (unset = off, at least `1s`), `PRIVO_BACKUP_KEEP` (default 7, 1 to 1000), `PRIVO_BACKUP_DIR` (default `<data dir>/backups`). Acceptance: (1) with an interval set the gateway takes a snapshot at start if there is none or the newest is older than the interval, otherwise waits out the rest of the interval (so restarts neither skip nor double one), then one per interval, and prunes to the newest N; (2) each backup writes an admin audit event `backup.create` with actor `system` and outcome `ok` or `error`; (3) a failed backup is logged, audited and retried within `min(interval, 5m)`, and never stops the gateway or the job; (4) shutdown waits for the job before the store closes; (5) off by default: no files, no events. Tests, written first: `internal/config` `TestBackupConfig`; `cmd/privo` `TestBackupJobRunsAtStartWhenNoneExistsKeepsTheNewestAndAudits`, `TestBackupJobWaitsWhenARecentBackupExists`, `TestBackupJobFailureIsAuditedAndDoesNotStopTheJob`; acceptance `TestScheduledBackupsKeepTheNewestAndRestore`, `TestNoScheduledBackupsByDefault`. Threat note: snapshots sit on the data volume, so they do not cover host loss (off-host copy stays the operator's job); the schedule is off in production until the owner turns it on.
- [x] The master key must open the database at start: after `EnsureOrgKey`, `runGateway` unwraps the org's current key and refuses to start, naming the master key and `PRIVO_MASTER_KEY_FILE`, if it cannot. Found while writing the restore drill: a gateway on a restored database with another key used to start and fail on every credential later. Test: acceptance `TestRestoreWithTheWrongKeyRefusesToStart` (also checks that the key is not in the output)
- [x] The owner's manual test plan is automated (owner's request, 2026-10-29), through the real binary, the admin API and a real MCP client (the Go SDK's) against the echo server: `test/acceptance/testplan_test.go` `TestPlanDenyRuleCanBeListedAndRemoved`, `TestPlanAskApprovalAndAllowSelfToggle`, `TestPlanRevocationOnALiveSession`, `TestPlanPresetsAndDefaultPolicy`, `TestPlanAuditFieldsAndOutcomes`, `TestPlanPromptsAndResourcesThroughTheClient`; flows `policy_test.go` already covers are not repeated (the header of the file lists them). The echoserver has an `outcomes` mode (tools `fine`, `fail`, `boom`) for the audit outcomes
- [ ] Built-in WAL shipping backup (RPO 1 minute) to S3-compatible storage; off-host upload of snapshots
- [ ] Install, upgrade, backup and key-backup guide

## F16. Observability
Status: In progress (health endpoints and drain only)

ADRs: 0010

- [x] Prometheus metrics on their own listener (`PRIVO_METRICS_ADDR`, F28, ADR 0024)
- [ ] OpenTelemetry span per stage (the statistic names of ADR 0024 map onto the MCP conventions); metrics without org labels in Cloud
- [x] `/livez`, `/readyz`; 30-second drain on SIGTERM (`PRIVO_SHUTDOWN_TIMEOUT`; built in F01)

## F17. Context-cost report
Status: Not started

- [ ] Tokens added per server's tool list; usage per tool

## F18. Billing (Privo Cloud)
Status: Not started

- [ ] Plans, seats, card checkout in USD and INR

## F19. Egress and SSRF protection
Status: In progress
ADRs: 0008

- [x] Egress client: resolved-IP check at dial time (defeats DNS rebinding), non-public ranges refused incl. IPv4-mapped IPv6 and cloud metadata, no environment proxy, redirect checks, same-host option (`internal/egress`)
- [x] `CheckURL` for validating registered URLs; `PRIVO_EGRESS_ALLOW_CIDRS` parser
- [x] Wired into server registration (422 with reason) and all upstream HTTP clients
- [ ] Metadata fetches (F06, F07); audit blocked attempts
- [x] Cap the size of upstream responses (tool results, resource reads, list pages): `PRIVO_UPSTREAM_MAX_RESPONSE_BYTES` and the list limits, see R-04 in F24

## F20. Enterprise identity and roles
Status: In progress (first slice built and reviewed 2026-11-18, ported onto main as 0.11.0 on 2026-11-21, ADR 0025 accepted; the owner's walkthrough, `docs/manual-test.md` section 13, open)
ADRs: 0011, 0021, 0022

### First slice: goal

Owner's request (2026-11-17): a person can do more than one kind of thing. A
member who uses the gateway from their AI client can also approve other
people's requests, or read the audit log, without being made an admin. A
person's AI client still can never approve or administer anything.

### First slice: acceptance

1. A person holds a set of roles: `{owner}`, `{admin}`, or any non-empty
   subset of `{member, approver, auditor}`. Any other set is refused with a
   message that says which sets are allowed.
2. Permissions are the union of the person's roles. A person with no role
   can do nothing.
3. A person with member and approver uses the gateway from their AI client.
   They also see and decide other people's approvals in the browser.
4. That person's gateway token gets 401 on the admin API, and it cannot
   decide approvals. Every new gateway token is stored as `member`, whatever
   roles its person holds.
5. A person who is only an approver or only an auditor still gets no gateway
   token.
6. A rule with subject `role:<role>` matches a person who holds that role
   among others. A change to someone's roles applies to the gateway's next
   request, and to their session's next request.
7. Owners change any set. Admins change sets within `{member, approver,
   auditor}`, and only for people who are not owners or admins. Nobody
   changes their own roles. The last active owner cannot lose owner. Each
   change is one audit event with the set before and after.
8. Invites carry a role set and follow the same rules.
9. A member who is also an approver cannot decide their own requests unless
   the org allows self-approval. The approvals page marks their own requests
   and says why the buttons are off.
10. Existing installs keep working after the migration: each person has the
    role they had, and existing gateway tokens of signed-in people work
    unchanged.

### First slice: threat note

- **The AI client never approves.** Roles live on the person. A gateway token
  only uses the gateway, and the token kinds still keep it off the admin API
  (ADR 0014). Deciding needs a session with CSRF (ADR 0016) or a control
  token. A prompt-injected agent with a member-and-approver person's gateway
  token can call tools within policy. It cannot approve its own held call,
  turn on self-approval or change rules.
- **Escalation.** Admins cannot grant or remove owner or admin, and nobody
  edits their own roles. Valid sets are fixed, so "admin plus something"
  never appears to confuse a reviewer. Every change is audited with the
  actor.
- **Self-approval.** Combining member and approver makes self-approval
  attempts more likely. The per-person check (ADR 0005) is unchanged, stays
  off by default, and is tested with a person who holds both roles.
- **Person binding.** `role:` rules now follow the token's member. Since
  the F07 R-01 fix (ADR 0021) that is the user a token is bound to by id,
  never a person name, so no token can borrow someone else's roles.
- **Fail closed.** If reading roles fails, the request fails. No roles means
  no permission. An unknown role in the database is ignored, never treated
  as owner.
- **Revocation.** Roles are read on every request (tokens are trusted for at
  most 5 seconds, ADR 0003). Losing owner or admin drops catalog grants in
  the same transaction.

### First slice: tasks and the tests that name them

- [x] Spec (this section) and ADR 0025 (numbered 0021 on its branch); ADRs 0011 and 0014 marked as amended
- [x] Prerequisite: F07 R-01, tokens bound to a user id (ADR 0021, merged 2026-11-11)
- [x] Store: migration `00011_user_roles.sql` (`user_roles` with `source`, `granted_by`, `granted_at`; `invite_roles`, deleted with its invite; both with `org_id`; copies `users.role` and `invites.role`, then drops both; gateway tokens bound to a member become `member`; down keeps each person's strongest role); `NormalizeRoles`, `SetUserRoles` (kept roles keep their grant), `User.Roles` and `Has`, `Access.Roles` read with access; catalog grants and `promoteCatalog` read `user_roles`
  - tests: `TestUserRolesMigration` (each person and invite keeps its role; own gateway tokens become `member`; automation and control tokens unchanged; an invite with roles can still be pruned), `TestRoleSetRules`, `TestSetUserRoles` (sorted set, who granted a new role, kept roles untouched, audit event, an invalid set changes nothing), `TestUserRolesAreIsolatedPerOrg`, `TestAccessCarriesRoles`, `TestInviteCarriesRoleSet`; losing owner and admin drops the catalog grant and promotion reads `user_roles`: `TestCatalogGrantFirstManagerAndPromotion`
- [x] Auth: `Principal.Roles`, `Has`, `Can` as the union (`RolesCan`); no roles, or only unknown ones, grant nothing
  - tests: `TestCanIsUnionOfRoles` (member and approver: the gateway and deciding, nothing else), `TestNoRolesNoPermissions` (unknown roles included), `TestApproverAloneCannotUseGateway`
- [x] Code check: `Principal` and `store.User` have no single `Role` field any more, so the compiler refuses a role comparison on a person; a test keeps the field from coming back (the source scan planned here was replaced by this, which cannot be fooled by formatting). Token roles (`APIToken.Role`) stay single
  - tests: `TestNoSingleRoleField`
- [x] Policy and gateway: `role:` subjects match any of the caller's roles; for a member's own token those are the member's current roles, read with their access on each request; an automation token matches by its own role. Beyond the ADR text, in its spirit: a member's own token reaches nothing while none of the member's current roles uses the gateway (an approver-only person)
  - tests: `internal/policy` `TestEvaluate` (a role among several matches, no roles match no role rule), `internal/gateway` `TestRoleRulesSeeTheMembersCurrentRoles`, `TestDemotedMembersTokenReachesNothing`, `TestAccessFailsClosed`; the change on the next request is in the acceptance test below
- [x] Control API: `PATCH /api/v1/members/{person}` takes `roles` and still reads `role`; members list `roles` and the strongest as `role` for one release; invites take `roles` (also `privo admin invite --role member,approver`); a person's own gateway token is issued as `member`; control tokens keep one role; the audit action is `member.roles:<person>:<before>-><after>`. The last-owner check stays, though only another owner can reach it
  - tests: `TestPatchMemberRolesRules` (an admin cannot grant admin or change owners and admins, nobody edits their own roles, invalid sets are 422, one audit event per change with both sets), `TestPatchMemberRoleStillWorks`, `TestPersonalGatewayTokenIsMemberOnly`, `TestInviteTakesRoles`; the existing `TestMyTokensAreOnlyMine` (an approver alone gets no gateway token)
- [x] Approvals: `approval.Own` is the one rule for "own request", used by `Decide` and by the list, which marks `own` and gives `decide_blocked` unless the org allows self-approval
  - tests: `TestMemberApproverAndOwnRequests` (own marked and refused, another's approved, then allowed once the org allows self-approval)
- [x] App: People shows each person's roles with a **change** picker (checkboxes; owner and admin clear the others; Save disabled with none); admins see no change for owners and admins; the invite form takes a set; Approvals shows why one's own request cannot be decided, without buttons; the bar shows the roles; rules offer approvers and auditors; a member who also approves lands on Start
  - tests (Vitest): `roles.test.ts` (`pickRole`, `roleText`), `pages.test.tsx` (the invite sends a set, a member's roles change as a set, admins get no change for owners and admins, own approvals show the reason and no buttons); 38/38
- [x] Acceptance through the real binary: `TestMemberWhoApproves` (a person with member and approver calls a tool that needs approval; their gateway token gets 401 on the admin API; their own request is marked and refused; they approve another member's call, which then runs; without approver they cannot list approvals; with auditor a `role:auditor` deny applies to their next call; as approver only their token lists nothing and calls are "not available", and they get no new token; every change is audited with the owner as actor). Mutation-checked: reading the token's role instead of the person's fails it
- [x] Docs: README (Roles), `docs/manual-test.md` section 13, version 0.11.0
- [x] Security review 2026-11-18 (`docs/reviews/2026-11-18-f20-roles-security-review.md`): no findings at 0.8 or above. Acted on: an admin could revoke an owner's or admin's own gateway token once those carry `member`; revoking is now protected by the person's roles (`TestAdminCannotRevokeAnOwnersOwnToken`). For the release: a `role:member` rule no longer matches an owner's or admin's own AI client token
- [ ] Owner's walkthrough on app.privo.club: `docs/manual-test.md` section 13

### Later in F20

- [ ] The remaining roles of ADR 0011 (server admin, policy admin) as members of the combinable group
- [ ] Approvers scoped to a team or a server (needs its own ADR; ADR 0025 leaves room)
- [ ] Generic OIDC and SAML SSO; identity provider groups mapped onto teams and roles (`user_roles.source = idp:<group>`)
- [ ] SCIM 2.0 with immediate revocation on deprovisioning
- [ ] Passkeys or TOTP for local accounts

### Next slices (spec 2026-11-21)

**A. Generic OIDC sign-in (also Google and Microsoft).** Goal: a company
signs in with its own identity provider, and GitHub becomes one provider
among several.

Decide first: ADR 0034. It settles how identities are keyed (issuer and
subject, as ADR 0011 planned) and how an existing GitHub member links a
second provider: only while signed in, never by matching email. It also
fixes discovery with an issuer check, PKCE, nonce and `at_hash`, and
whether an org may turn GitHub off.

Acceptance:
1. `PRIVO_OIDC_ISSUER`, `_CLIENT_ID`, `_CLIENT_SECRET(_FILE)`, plus an
   optional allowed-domain list. The sign-in page offers each configured
   provider.
2. Invites and setup links work with any provider. A person signs in with
   the identity the invite was claimed with, or a linked one.
3. Tokens are checked: signature by the issuer's keys, `iss`, `aud`, `exp`,
   `nonce`. `email_verified` is required when domains are restricted.

Tasks and tests:
- [ ] ADR 0034; `internal/oidcauth` (`TestIDTokenChecks`, `TestNonceBound`, `TestWrongAudienceRefused`)
- [ ] web: provider choice, linking (`TestLinkOnlyWhileSignedIn`, `TestNoEmailMatching`)
- [ ] Acceptance with a fake OIDC provider

**B. SAML 2.0 and group mapping.**
- SAML sign-in, with signed assertions required, the audience checked,
  and replay refused.
- Identity provider groups map onto teams and onto member, approver or
  auditor (`user_roles.source = idp:<group>`). Owner and admin are never
  mapped.
- Mapped roles and teams follow each sign-in; hand-given ones stay.

Tests: `TestUnsignedAssertionRefused`, `TestReplayRefused`,
`TestGroupsMapToTeamsAndRoles`, `TestOwnerNeverMapped`.

**C. SCIM 2.0.**
- `/scim/v2/Users` and `/Groups` with a SCIM bearer token per org
  (hashed, like API tokens).
- Deprovisioning removes the member at once (F14's removal, with R-04's
  provider revocation).
- Groups map as in B.

Tests: `TestSCIMDeprovisionEndsEverything`, `TestSCIMTokenScopedToOrg`.

**D. Approvers scoped to a team or a server.** Decide first: ADR 0035.
ADR 0025 leaves room for it. The ADR decides whether a scope is attached
to the role grant or to a separate table, and what an unscoped approver
sees.

Acceptance:
1. An approver's grant can name teams or servers. They see and decide only
   requests from those teams or on those servers.
2. Owners and admins stay unscoped.

Tests: `TestScopedApproverSeesOnlyTheirs`,
`TestScopedApproverCannotDecideOthers`.

**E. The remaining roles of ADR 0011 (server admin, policy admin)** become
members of the combinable set, after D. They come from the same table.

**F. Passkeys or TOTP for local accounts**, for self-hosted installs without
an identity provider. Spec once a local-account sign-in exists; today
every sign-in goes through a provider.

## F21. Client compatibility suite
Status: In progress (first real client tried 2026-10-29)
ADRs: 0004, 0010

- [x] Claude Code over a static bearer header (`claude mcp add --transport http`): connects; the empty `cacheScope` bug was found and fixed (see F02). `tools/call` through DeepWiki was confirmed in Claude Code after the `resultType` fix (next item; checkpoint of 2026-10-29). Still to confirm in Claude Code: prompts and resources (covered so far by the Go SDK client in `TestPlanPromptsAndResourcesThroughTheClient`)
- [x] `tools/call` through DeepWiki failed in Claude Code: results lacked `resultType` (required from 2026-07-28). Fixed in `internal/gateway/resulttype.go`; `TestEveryResultCarriesResultTypeOnNewProtocol` speaks the new protocol as raw JSON against an upstream that does and one that does not. Re-run in Claude Code after the deploy: the call works and the audit log shows it
- [ ] Report to the Go SDK (two drafts in `docs/upstream/`, owner files them): (1) a `notifications/elicitation/complete` without `params` crashes the client and the SDK has no `recover()`: `go-sdk-elicitation-complete-nil-params.md`; (2) results returned from receiving middleware miss `resultType` (the field is unexported and only the SDK's own handlers set it). Issue text with a reproduction is drafted in `docs/upstream/go-sdk-resulttype-from-middleware.md`; search the tracker for duplicates, then file. Drop `typedResult` when a release fixes it
- [ ] Validate the gateway's raw responses against the MCP schema in the acceptance suite, so a strict client cannot surprise us again (the Go SDK client accepts what Claude Code rejects)

- [ ] CI tests against reference clients and upstreams, both protocol versions
- [ ] Manual matrix: Claude Code, Cursor, Codex, VS Code, Amp, ChatGPT

## F22. Security operations
Status: Not started
ADRs: 0009

- [ ] Disclosure policy, dependency and image scanning, signed releases, SBOM
- [ ] SOC 2 control mapping

## F23. Token-bound user keys
Status: Not started (after v1)
ADRs: 0013

Goal: a stolen database plus every Privo key cannot decrypt a person's upstream credentials.

- [ ] Prototype: per-user key wrapped by keys derived from each active token secret (key slots)
- [ ] Re-wrap on token rotation; reconnect flow when all tokens are revoked
- [ ] Security review before release

## F24. First-deploy readiness
Status: In progress (code done, tested and deployed; reviewed twice, the second review's findings are open below; the webhook check is open)
ADRs: 0003, 0010 (no new ADR: the bootstrap token is an interim measure; the F05 setup link, `privo admin setup-link`, is the first-boot path for a person, ADR 0016)

Goal: a fresh Coolify deploy (distroless image, no shell, so no web terminal and
no `privo token create` from the Coolify UI) goes from "running" to "the owner
holds an admin token" without any secret being logged or returned by the API; it
serves a sensible page at `/`; and a push to `main` rebuilds only the app whose
files changed.

Acceptance criteria:
1. With `PRIVO_BOOTSTRAP_TOKEN` (or `PRIVO_BOOTSTRAP_TOKEN_FILE`) set to a well-formed token and no control token ever issued for the org, the first boot installs it as an owner control token named `bootstrap`, expiring 24 hours after install. Only the SHA-256 of its secret is stored.
2. It installs at most once per org. A restart with the variable still set does nothing, even when the token was revoked or has expired. If any control token already exists, nothing is installed and a warning says so.
3. A malformed value, or a well-formed one whose secret is too regular to be random (a repeated character, a short pattern, a walk along the alphabet; R-10), stops the process at startup (exit 2) and the message never contains the value. The installer refuses such a token itself as well. A token file that is not a regular file (a FIFO would block startup for good) is refused (R-11).
4. The install is audited (`bootstrap.token.install:<id>`, actor `system`), the row can be read with `GET /api/v1/audit/events?kind=admin` (R-09), and the log carries the token id only. A live token that is already called `bootstrap` does not stop the install: the bootstrap token is then named `bootstrap-<first 8 of its id>` (R-11).
5. The variable is removed from the process environment after loading, so child processes (stdio servers) do not inherit it. That does not remove it from `/proc/<pid>/environ`, which keeps the block the process started with (R-08): the file form (`PRIVO_BOOTSTRAP_TOKEN_FILE`) is what keeps the token out of there, and the process logs a warning when the plain variable is used.
6. `privo token generate` prints a fresh well-formed token and touches neither the data directory nor the configuration, so it can run on a laptop.
7. `GET /` returns a static HTML page (200) with security headers; any other unknown path stays 404; `POST /` is 405; the page shows no version, org or configuration.
8. A push to `main` that changes only `site/**` deploys the landing page and not the gateway; a push that changes anything else deploys the gateway and not the landing page. The GitHub webhook delivers (verified signature) to Coolify.

Threat note:
- The bootstrap token sits in the Coolify environment, readable by anyone with Coolify access and via `/proc/<pid>/environ`. Those people already hold the master key. Mitigations: 24-hour expiry from install, install-once marker, `is_shown_once` in Coolify, the guide says to delete the variable after first use, and the file form is recommended. Removing the variable from the process environment keeps it from child processes only; it is not a mitigation for `/proc/<pid>/environ` (R-08), and the process warns when the plain variable is used. A token with no entropy is refused (R-10), since it would be an owner credential.
- Resurrection: a revoked or expired bootstrap token must not come back at the next restart. The `bootstrap.consumed` setting is written in the same transaction as the token row.
- Two processes racing at first boot: the writer lease allows one; the install check and insert share one transaction.
- Guessing: 256-bit secret, constant-time hash comparison on the existing verify path.
- Root page: fully static, no reflected input, no version or tenant data; `default-src 'none'` CSP, `nosniff`, no framing.
- Webhook: Coolify verifies the GitHub HMAC secret; the secret never enters the repo.

Tasks (tests named first, per the workflow):
- [x] Tests, `internal/store`: `TestInstallBootstrapToken` (installs once, refuses when a control token exists, refuses after consumed even when the token is revoked, writes marker and row together, cross-org isolation)
- [x] Tests, `internal/control`: `TestBootstrapToken` (malformed rejected without echoing the value, installed token authenticates as owner control, cannot call `/mcp`, expires after 24h, only hash stored)
- [x] Tests, `internal/config`: `TestBootstrapTokenConfig` (env and file forms, file wins, malformed rejected, value not in error text)
- [x] Tests: `internal/server` `TestRootPage` (200 and headers, CSP hash matches the inline style, 404 for other paths, 405 for writes, nothing reflected); `cmd/privo` `TestTokenGenerate`, `TestServeRejectsMalformedBootstrapToken`, `TestInstallBootstrapAtStartup`; acceptance `TestBootstrapTokenFirstRun`, `TestBootstrapTokenIgnoredWhenAlreadyAdministered`, `TestRootPage`
- [x] Build: store method (token, consumed marker and audit in one transaction), control installer, config with a redacting `Secret` type, startup wiring, `privo token generate`, root page
- [ ] Coolify: watch paths on both apps, auto deploy on and a shared webhook secret are set (API, 2026-10-29) and verified with a signed test delivery (docs-only push skipped by both apps, bad signature rejected). Open: add the GitHub webhook (needs a GitHub sign-in), then confirm a `site/` push deploys only the landing page and an `internal/` push only the gateway
- [x] Docs: `docs/deploy-coolify.md` first-run section and deploy setup; README variable table and token section
- [x] Independent security review of the bootstrap change (auth, secrets): first pass 2026-10-29 (`docs/reviews/2026-10-29-independent-review-f24-cache-hints-result-type.md`, R-01 to R-11, all fixed, see the items below); second pass 2026-11-03 (`docs/reviews/2026-11-03-f24-bootstrap-independent-review.md`): approve with changes; 0 Critical, 0 High, 2 Medium, 4 Low, 1 Info, fixes parked by the owner and listed next. The self-review of 2026-10-29 is superseded by both
- [x] Second review R-01 (Medium): a bootstrap token still installs on an organisation that was set up with the setup link, because "needs a bootstrap" looks only at control tokens and a browser-only org has none. Count `users` in the same transaction in `InstallBootstrapToken` and `BootstrapNeeded`. (app.privo.club has had control tokens since 2026-10-29, so it is not affected; an install set up only in the browser is). Fixed 2026-11-20: an org counts as administered when it has ever had a control token or a member (removed ones too), in `InstallBootstrapToken` and `BootstrapNeeded`. Test: `internal/store` `TestBootstrapRefusedOnceASetupLinkWasClaimed` (failed before the fix)
- [x] Second review R-02 (Medium): `privo token create` writes no audit row (the item "Discovered" below); write the token row and a `token.create` event with actor `cli` in one transaction, as `admin setup-link` and `admin invite` do. Fixed 2026-11-20: `IssueToken` writes the token and its `token.create:<kind>:<role>:<name>` event in one transaction (`CreateTokenAudited`), for the CLI (actor, actor id and client `cli`) and for the API alike, so no token exists without its trace. Test: `cmd/privo` `TestTokenCreateIsAudited` (failed before the fix)
- [ ] Second review R-03 to R-06 (Low): a live bootstrap token is not retired when a person claims the setup link; `token.Weak` passes patterns with 16 or more different symbols; a malformed leftover value stops `serve` on an org that no longer needs a token; a reused token id fails the first boot with an SQL conflict that does not name the variable
- [ ] Second review R-07 (Info): unset the plain variable also when the file form was used; one line in the backup docs that restoring a backup older than the install lets the token install again
- [x] Discovered: tokens created by `privo token create` are not audited (the admin API audits its own); the second review rates it Medium (R-02 above) and gives the fix (closed with R-02)
- [x] The bootstrap variables are read only by `serve` (R-03, Medium, independent review 2026-10-29): `config.Load` no longer reads them, so the healthcheck (run every 15 s by the image) and the break-glass `privo token create` cannot fail on a leftover variable, and a bootstrap file that was removed after use is a warning on an organisation that is set up already. A first boot that cannot read the file still refuses to start (exit 1, names the variable), and a value that is not a token still stops `serve` with exit 2. Tests: `TestLoadIgnoresBootstrapVariables`, `TestBootstrapTokenConfig`, `TestBootstrapNeeded`, `TestBootstrapAtStartup`, `TestHealthcheckAndTokenCreateIgnoreBootstrapVariables`, acceptance `TestBootstrapFileRemovedAfterUse` and `TestBootstrapFileMissingOnFirstBootRefusesToStart`.
- [x] An upstream cannot make the gateway hold or do unbounded work (R-04, Medium, independent review 2026-10-29; the reviewer argued it from reading, the tests below measure it with small configured limits). Acceptance: (1) a reply of an HTTP or SSE upstream may not exceed `PRIVO_UPSTREAM_MAX_RESPONSE_BYTES` (default 16 MiB, 1024 to 1 GiB): a JSON body is counted after decompression and one that announces more is refused before it is read, an event stream is counted per event. A reply over the limit fails the call with the same fixed message as any upstream failure (R-02), and the connection is re-established, since the SDK fails a connection whose reply it cannot read. Stdio messages are capped by the SDK at 16 MiB each, which this setting does not change; (2) an upstream call has a deadline, `PRIVO_UPSTREAM_CALL_TIMEOUT` (default 10 min), and the caller is told that the server did not answer within it; (3) a list is walked for at most 100 pages, a repeated cursor ends it, and the definitions of one load may not exceed 8 MiB together: each failure puts that server `down` with the reason; (4) a refresh has the connect timeout as its deadline (a refresh that hung used to keep the refresh slot for good, and no later change of the server's lists was read), and one server gets at most one refresh per `PRIVO_UPSTREAM_REFRESH_GAP` (default 5 s, measured from the end of the last load) however often it announces changes; (5) after each load that listed everything, the pending pins the server no longer offers are deleted, so definitions that change on every refresh cannot fill the disk. Accepted and blocked pins are decisions and are never deleted. Threat: a registered upstream is untrusted input, and one process holds the replies of every tenant. Tests, written first: `internal/upstream` `TestEndlessPaginationBringsServerDown`, `TestPaginationWithinLimitsStaysUp`, `TestLoadStopsAtDefinitionBudget`, `TestRefreshHasADeadline`, `TestRefreshGap`, `TestLoadPrunesPendingPins`, `TestLimitBodies`, `TestEventCapAcrossReads`; `internal/store` `TestPrunePending`; `internal/config` `TestUpstreamLimitsConfig`; `internal/gateway` `TestCallUpstreamTimeout`; acceptance `TestOversizedUpstreamReplyIsRefused` (JSON and event-stream replies, each with a control run without the limit), `TestUpstreamCallTimeout`, `TestListChangedFloodIsRateLimited` (55 refreshes without the gap, 3 to 9 with it). Not covered here: in-flight limits and bulkheads per server (F02, still open), and the stdio message cap, which belongs to the SDK.
- [x] SSE upstreams stay connected (found while testing R-04). The legacy SSE transport ties its event stream to the context given to `Connect`, and `connect` gave it the context of the handshake, which ended when `connect` returned: an `sse` server went up, was dropped at once ("connection closed") and was reconnected over and over, since the SSE transport was added. `connect` now gives it a context that lives as long as the connection; a timer applies `ConnectTimeout` to the handshake and the first load and is stopped once the connection is up. Tests: `internal/upstream` `TestSSEUpstreamStaysConnected`, acceptance `TestListChangedFloodIsRateLimited` (an SSE server is the only kind that delivers list-changed notifications over HTTP).
- [x] An upstream's reply is checked inside the call, and what the gateway relays is the gateway's to say (R-05, R-06, R-07, Low, independent review 2026-10-29). Acceptance: (1) a `tools/call` reply with no result or a null content block, a `prompts/get` reply with `messages` absent or null (or a null message, or a message without content), and a `resources/read` reply with `contents` absent or null are refused with an error that calls the reply malformed and names the field and position, never anything the upstream sent; the audit log records the call as `error`, not `ok`; a result that asks for input is refused the same way. (An empty `content` or `messages` list is valid and relayed; the SDK client reads a null, empty object or missing `content` of a tool reply as an empty result, so those cannot be told apart.) (2) `requestState` and every reserved `_meta` key (a prefix with the label `modelcontextprotocol` or `mcp`) are removed from an upstream's result before it is relayed, so a state handle the gateway never issued does not arrive on a result labelled complete and an upstream's `serverInfo` does not stand in for the gateway's; the upstream's own other `_meta` keys pass. (3) an error that leaves the gateway as the answer to `tools/call`, `prompts/get` or `resources/read` carries a JSON-RPC code: -32602 for an unknown prompt, a URI that is not Privo's and a resource that is not available (the SDK's `CodeResourceNotFound`, SEP-2164), -32600 for a refusal, -32603 for the rest, the upstream's own code when it answered. Code 0, what the SDK sends for a plain error, is not valid. Tests, written first: `internal/gateway` `TestCheckPrompt`, `TestCheckToolResult`, `TestRewriteReadRequiresContents`, `TestReservedMetaKey`, `TestCleanForRelay`, `TestRPCErrorCodes`, `TestClientFacing` (malformed reply passes through); acceptance `TestMalformedPromptAndReadRepliesAreRefusedAndAudited`, `TestUpstreamRequestStateAndReservedMetaAreNotRelayed`, `TestGatewayErrorsCarryJSONRPCCodes` (echoserver `-mode hostile -null messages|contents|poison`). Checked with a mutated copy that the acceptance tests fail without the fix. Done: `internal/gateway/validate.go`, called from the `do` callbacks of `callTool`, `getPrompt` and `readResource`, so the check runs before the audit completion. Not changed: a tool reply with `isError` is still audited `tool_error`.
- [x] Bootstrap token follow-ups from the independent review (2026-10-29, R-08 to R-11, Low or Info). R-08: the F24 text claimed that clearing the variable protects `/proc/<pid>/environ`; it does not (the reviewer measured it). Criterion 5 and the threat note are corrected, `serve.go` says what the unset does, and the start logs a warning when the token came from `PRIVO_BOOTSTRAP_TOKEN` and not from the file. R-09: `GET /api/v1/audit/events?kind=admin` returns the admin-phase events (the control API's actions and the bootstrap install), newest first, with the same permission as the call list (owner, admin, auditor) and the same `limit`; `kind=calls` or none is the list of calls as before; any other kind is 400. R-10: `token.Weak` refuses a secret with fewer than 16 different symbols, a run of 8 of one symbol, or 16 steps along the alphabet in order; `LoadBootstrapToken` (exit 2) and `control.InstallBootstrapToken` (`token.ErrWeak`) both apply it. It is a guard against the careless case and cannot prove randomness; 2000 generated secrets pass it in the test. R-11: a token file that is not a regular file is refused before it is opened (a FIFO without a writer blocked startup; a symlink to a regular file still works), and a live token already named `bootstrap` no longer makes the install fail on the unique name. Left as they are, for the owner to decide: `GET /version` stays unauthenticated (it predates F24 and the root page itself shows nothing); the null-tools guard in `upstream.load` is dead code because the SDK drops null tools, and stays as a guard. Tests, written first: `internal/token` `TestWeak`; `internal/config` `TestBootstrapTokenWeakIsRefused`, `TestBootstrapTokenSource`, `TestBootstrapTokenFileMustBeRegular`; `internal/control` `TestInstallBootstrapTokenRefusesWeakToken`; `internal/store` `TestInstallBootstrapTokenNameTaken`, `TestRecentAdminEvents`; `cmd/privo` `TestBootstrapTokenFromEnvironmentWarns`; acceptance `TestAdminAuditEventsAreReadable`.

## F25. Configuration file
Status: Done (first slice)
ADRs: 0015

Goal (owner's request, 2026-10-29): the server's configuration lives outside the code, in one place, so later settings are added without touching three packages. Acceptance:
1. Sources and precedence: environment, then the file, then the default; an empty or blank variable counts as unset.
2. The file is YAML, a flat mapping whose keys are the variable names in lower case without `PRIVO_`. It is read only when named (`--config FILE` before the command, or `PRIVO_CONFIG_FILE`; the flag wins), by `serve`, `healthcheck`, `token create`, `backup`, `restore` and `config show`. `version`, `help` and `token generate` never read it.
3. A named file that is missing, not a regular file, over 64 KiB, not YAML, empty of a mapping or holding more than one document is exit 2 with the file name. Unknown keys, duplicate keys, aliases and list or map values are errors, all reported together, naming the key and never a value.
4. Secrets never sit in the file: `master_key`, `master_key_previous`, `bootstrap_token` are refused with the `_file` setting to use; `config_file` cannot be set in the file.
5. `vault` (master key) and `cmd/privo` (bootstrap token, key warning) read through the layered lookup, so a file can say `master_key_file`.
6. `config.Settings` is the registry. Tests: every `PRIVO_` name read by non-test Go code is registered; every registered setting is in the README table and in `docs/privo.example.yaml`; the example parses.
7. `privo config show` prints each setting with its effective value and source (default, file, environment), secrets only as set or unset, and the problems found (exit 2).
8. Acceptance (real binary): a gateway started with only a file serves on the file's `listen`; an environment variable beats the same key in the file; a file with a typo or a secret does not start and says why.
Threat note: the file can change behaviour, so it is as trusted as the environment (writable only by the operator); it holds no secret, which the loader enforces, so a leaked or committed file exposes settings and paths, not keys. Strict parsing is the defence against a silent fall back to defaults after a typo. YAML aliases are refused so a small file cannot expand into a large one.
Tests, written first: `internal/config` `TestReadFile*`, `TestLayer*`, `TestLoadReadsValuesFromTheFile`, `TestDescribeSources`, `TestEveryVariableInTheCodeIsRegistered`, `TestEveryRegisteredSettingIsUsedByTheCode`, `TestRegistryIsInTheREADME`, `TestExampleFile*`; `internal/vault` `TestLoadKeyProviderUsesTheGivenLookup`; `cmd/privo` `TestConfigFlag*`, `TestConfigFileFromTheVariableAndFlagWins`, `TestBadConfigFileStopsTheCommandsThatReadIt`, `TestCommandsThatNeedNoConfigIgnoreTheFile`, `TestConfigShow*`, `TestHealthcheckReadsTheListenAddressFromTheFile`; acceptance `TestConfigFileAloneConfiguresTheGateway`, `TestConfigEnvironmentBeatsTheFile`, `TestConfigFileMistakesDoNotStart`. Mutation-checked: six deliberate breakages (a setting dropped from the registry, an unused one added, file over environment, secrets accepted, the file ignored, a key removed from the example) each fail a test. One new dependency: `go.yaml.in/yaml/v3` v3.0.5, already in `go.sum`.

- [x] Registry, file reader and layered lookup (`internal/config`: `settings.go`, `file.go`)
- [x] `--config`, `PRIVO_CONFIG_FILE` and `privo config show` (`cmd/privo/configcmd.go`); key provider (`vault.LoadKeyProvider`) and the bootstrap token read through the lookup
- [x] `docs/privo.example.yaml`, README (settings table completed: it was missing the retry bounds, runner, master key variables), deploy guide (config file on Coolify)
- [ ] Later: structured sections (sign-in providers in F05), reload on SIGHUP only if a setting needs it

## F26. Teams, the server catalog and the start page
Status: In progress (live since 0.6.0, 2026-11-04; independent review done and fixed except R-03; of the owner's walkthrough on app.privo.club, steps 9.2, 9.5 and 9.6 and a real OAuth server are open)
ADRs: 0020

Goal (owner's request, 2026-11-03): adding servers for developers, designers
and product managers is quick, each team sees only its own servers, and every
person knows how to use Privo from their AI client.

Acceptance:
1. Owners and admins create teams (from a profile or blank), rename them,
   add and remove members and delete them; each change is audited with the
   person as actor. `everyone` is built in and cannot be changed.
2. A server is reachable by the teams it is given. New servers get
   `everyone` unless teams are named; existing servers keep working. A
   server with no team is reachable by nobody, and the app says so.
3. The gateway lists only items of reachable servers; a call, prompt or read
   on an unreachable server is answered exactly like an unknown name. A
   change of teams or membership applies to the gateway's next request; a
   client's cached list follows within its 5-second lifetime (ADR 0003).
4. Rules can use `team:<slug>`; deleting a team deletes its rules.
5. The catalog lists hosted servers by profile with their sign-in kind; an
   admin adds one in one step (name prefilled, teams chosen), with an OAuth
   app's client id only where the provider needs it. The entry still goes
   through every check of `POST /api/v1/servers`.
6. Every person who uses the gateway lands on a start page: their teams,
   their servers (connect links where their own account is needed), and
   the setup of Claude Code, Cursor, VS Code, Codex, Windsurf or another
   client with a token made there.
7. Members cannot list teams or the catalog, or change any of it.

Threat note:
- Team access is a filter in front of policy, not a replacement: rules and
  approvals still apply to everything a person reaches.
- Fail closed: an error reading access fails the request; a server without
  teams is reachable by nobody; a removed member is in no team, not even
  `everyone`.
- No probing: an unreachable server's tool is "not available", the same
  answer as a name that does not exist.
- The catalog is embedded data, not fetched at runtime, so it cannot be
  changed from outside; an entry only fills a form, and the egress check,
  discovery and tool review still run.
- Membership follows the token's person (F07 R-01, parked): the fix that
  binds tokens to a user id covers teams too.

Tasks and the tests that name them:

- [x] Spec (this section) and ADR 0020
- [x] Store: migration `00006_teams.sql` (`teams`, `team_members`, `server_teams`, every table with `org_id`; existing servers get `everyone`); teams, members, server teams, `Access`
  - tests: `TestTeamsCreateListRenameDelete`, `TestTeamSlugRules`, `TestEveryoneIsReserved`, `TestTeamMembers`, `TestServerTeamsDefaultToEveryone`, `TestSetServerTeamsNeedsKnownTeams`, `TestAccessFollowsTeams`, `TestAccessOfRemovedMemberIsEmpty`, `TestDeleteTeamDropsItsRulesAndAccess`, `TestDeleteServerDropsItsTeams`, `TestTeamsAreIsolatedPerOrg`, `TestMigrationGivesExistingServersEveryone`
- [x] Policy: subject `team:<slug>`
  - tests: `TestTeamSubject`, `TestValidSubjectTeam`
- [x] Gateway: lists and resolve filtered by access (`gateway.Access`, shared with the admin API); rules get the caller's teams
  - tests: `TestAccessResolvesTheCallersMember` (member token, signed-in user, token without a member), `TestAccessFailsClosed`; lists, calls, the team rule and changes applying at once are tested end to end in the acceptance test below, since they need a live upstream
- [x] Catalog (`internal/catalog`): 25 entries in five profiles; each URL checked on 2026-11-03 with `upstreamauth.Discover` and `ChooseRegistration` (Box left out: its issuer does not match where its metadata is served; Atlassian: no protected resource metadata). GitHub, Asana, Slack and HubSpot need the admin's own OAuth app: embedded entries and profiles
  - tests: `TestCatalogEntriesAreValid` (aliases, https URLs, known profiles, unique ids, a check date), `TestProfilesHaveEntries`, `TestEntriesIsACopy`
- [x] Admin API: `/api/v1/teams` (list, create, patch, delete, members, servers), `teams` on create and patch server, `GET /api/v1/catalog`, `GET /api/v1/me/tools`
  - tests: `TestTeamsAPIRoleRules`, `TestTeamsAPIAudited`, `TestCreateServerWithTeams`, `TestPatchServerTeams` (members list only the servers they reach, without team names), `TestCatalogAPI`, `TestMyTools`
- [x] App: Start page (landing for members), Teams page, catalog on Servers, "Who can use it" on a server; client setup snippets shared with Tokens
  - tests (Vitest): `clients.test.ts` (each client's snippet; Codex keeps the token out of its config file), `pages.test.tsx` (start page lists teams, servers and the connect link, upstream text stays text, a new token fills every tab; teams page creates from a profile and adds a member; the catalog adds a server for the chosen teams with its OAuth app, and warns when nobody could use it; access saved on the server page); 28/28
- [x] Acceptance through the real binary: `TestTeamsLimitWhatEachPersonSees` (two members, a designers server and an everyone server; each lists only theirs; a call on the other's is answered like an unknown name; joining the team opens it on the next call; a `team:` rule applies to members only; a server without teams answers nobody; deleting the team deletes its rule; members cannot see teams or the catalog; the audit names the owner). `TestAdminUIFromScratch` and `TestUpstreamOAuthTwoPeople` updated: members land on Start
- [x] Docs: README, `docs/manual-test.md` (section 9), deploy guide note; version 0.6.0
- [x] Independent review (2026-11-03, `docs/reviews/2026-11-03-f26-teams-independent-review.md`): approve with changes; 0 Critical, 0 High, 1 Medium, 6 Low, 5 Info
- [x] R-01 (Medium): the Connections list, the connect page and the connect callback follow teams. A person sees and connects only servers their teams reach; another answers like an unknown server, and a connection started before the server left their teams stores no grant. Owners and admins connect any server (the first one provides its tool list). Tests: `internal/web` `TestConnectFollowsTeams` (mutation-checked: fails with the check removed)
- [x] R-02: a rule for `team:<slug>` needs the team to exist, checked in the rule's transaction, so a team created later inherits nothing. Tests: `internal/store` `TestRuleNeedsAnExistingTeam`, `internal/control` `TestTeamsReviewFixes`
- [x] R-03: calls on servers outside the caller's teams leave no audit record (same as unknown names today); write a rate-limited `denied:not_in_team` event without changing the answer **Fixed in 0.18.0:** the answer is unchanged; a call on an accepted tool, prompt or resource outside the caller's teams, or hidden by their toolset, is audited as `denied:not_in_team` or `denied:not_in_toolset`, at most once a minute per caller, server and reason; unknown names are not recorded (`TestCallsOutsideTheTeamsAreAudited`)
- [x] R-04: admins get `teams: []` for a server nobody reaches, and the Servers list warns ("nobody")
- [x] R-05: the add-server and catalog forms wait for the team list and stay disabled when it cannot be read, so a server is never opened to everyone by a failed fetch (Vitest)
- [x] R-06: unknown teams and servers in the team endpoints are 404, not 500; `POST /api/v1/servers` checks its teams before any discovery or registration at the provider
- [x] R-07: team names and descriptions refuse line and paragraph separators, private-use and unassigned characters, more than two combining marks in a row and names with no visible letter or digit (Hangul fillers); limits count characters; names are unique ignoring case and "Everyone" is reserved
- [ ] R-09 (Info): `INSERT OR IGNORE` and `server_teams.team` without a foreign key need care when the Postgres engine lands (F03)
- [ ] R-10 (Info): approvers and auditors now list only the servers their teams reach; `/me/tools` reads pins once per server; batch later if it shows
- [x] Local browser walkthrough (Chrome, 2026-11-04, 0.6.0 against a fake GitHub and the real DeepWiki): owner sign-in; Teams (create from a profile, suggested servers); catalog add of DeepWiki for Designers only (status up, 3 tools reviewed and accepted); "Who can use it" on the server page; an invited member lands on Start with no servers, sees DeepWiki once added to Designers, makes a token and every client tab fills it in; no console errors. Found and fixed: a duplicated "Who can use it" heading
- [ ] Owner's walkthrough on Coolify with real GitHub sign-in and an OAuth catalog server (`docs/manual-test.md` section 9). Done in Chrome on app.privo.club, 2026-11-04, after the 0.6.0 deploy (the migration gave deepwiki to Everyone): 9.1, 9.3 (with Miro: dynamic registration ok, "needs an administrator's connected account" as expected), 9.4, 9.7 and 9.8 pass; Miro removed afterwards. Open: 9.2, 9.5 and 9.6 need a second GitHub account; connecting an OAuth server and accepting its tools needs an account at a provider the owner uses (Linear, Notion…)
- [x] Adding a server: a failure is shown in the flash at the top of the page, out of view of a catalog form lower down, so "Add" looks like it did nothing (seen with Figma on app.privo.club, 2026-11-04). Show the error next to the form that failed **Fixed in 0.17.0 (F14 third slice).**
- [x] Adding a server whose provider is slow waits about 10 s on "Adding…" and ends in "context deadline exceeded"; say which step timed out at which host in plain words (Figma's authorization server metadata answered 503 or hung most of the time on 2026-11-04 while its status page was green) **Fixed in 0.17.0 (F14 third slice).**
- [x] Discovered: token names are unique in the whole org, so two people cannot both call their token `claude-code`; make them unique per person (with the person in the name shown to admins) **Fixed in 0.17.0 (F14 third slice).**
- [ ] Later: tool-level toolsets inside a server (F08); a team's own rule presets; identity provider groups mapped onto teams (F20)

### Next slice (spec 2026-11-21)

**A team's own rule presets.** Goal: an admin applies a preset (read-only,
ask before writes, …) to one team instead of the whole org.

Acceptance:
1. `POST /api/v1/servers/{alias}/policy:preset` takes `team`. The rules it
   writes carry the team as subject.
2. Rules for a team are evaluated only for its members. They rank as team
   rules do today.

Tests: `TestPresetForOneTeam`, `TestTeamPresetDoesNotTouchOthers`.

## F27. A token belongs to a member by user id
Status: Done, deployed as 0.7.0 on 2026-11-11 (independent review and the owner's new token are open below)
ADRs: 0021 (amends 0014 point 3, 0017 and 0020)

Closes the High finding of the independent reviews of 2026-11-03 (F07 R-01,
which is F14 R-02 as seen from the app): an owner or admin could issue a
gateway token that named a member and call upstream servers with that member's
own account, and see the servers of their teams.

### Goal
Whose account and whose teams a call uses is decided by the member a token
belongs to, set once when a signed-in member makes the token for themselves.
The person a token names is a label and is never looked up.

### Acceptance
1. A token made on the Tokens page (`POST /api/v1/me/tokens`, signed in)
   belongs to that member: `bound: true`, the member's teams, the member's own
   accounts on OAuth servers.
2. A token issued with `POST /api/v1/tokens` or `privo token create` belongs to
   nobody, whatever `person` or name it carries: `bound: false`, team
   "everyone" only, and on an own-account server the answer "this token does
   not belong to a member" with the address of the Tokens page. No call with it
   reaches the upstream as anyone.
3. An API token that names a member cannot make "its own" gateway token
   (`POST /api/v1/me/tokens` answers 403).
4. Nothing in a request sets the owner; an unknown field is refused.
5. The call audit names the member a call ran as (`user`, `user_id`) and names
   nobody for a token of nobody.
6. Tokens made before this version get no owner from the migration. Each
   person sees them on the Tokens page as "not tied to your account" and can
   revoke them.
7. Removing a member revokes the tokens that belong to them and the tokens of
   nobody that name them; a token that belongs to someone else stays.

### Threat note
- The attacker is an owner or admin (or anyone holding their control token)
  who wants to act as another member at that member's provider. After this
  change they cannot do it with a token. They can still remove the member,
  disconnect an account or change teams, and the audit shows each.
- Binding is by user id only. A user id of another org, of a removed member or
  one that does not exist is refused in the same statement that inserts.
- A token keeps the role its member had when it was made (not new; noted).
- The self-approval check still compares person labels (ADR 0014; F05 R-04 is
  the open item). An administrator can still issue a token of nobody with a
  label of their choice; it gets nobody's accounts.

### Tasks
- [x] ADR 0021
- [x] Migration `00007_token_owner.sql`: `api_tokens.user_id`, `audit_events.user_id`; no backfill, on purpose
- [x] Store: `CreateToken` binds only to an active member of the org; `UserByID`. Tests: `TestTokenBelongsToAMember`, `TestTokenCannotBeBoundToSomeoneWhoIsNotAnActiveMember`, `TestMigrationLeavesExistingTokensWithoutAnOwner`, `TestUserByID`, `TestAuditRecordsWhoseAccountACallRanAs`
- [x] Auth: the principal carries the token's member. Test: `TestVerifyCarriesTheTokensMember`
- [x] Gateway: team access and the per-person session read the user id, never the person. Tests: `TestAccessUsesTheUserIDNeverThePersonName`, `TestAccessFailsClosed`, `TestGrantOwnerIsTheTokensMember`, `TestTokenOfNobodyIsToldToMakeItsOwn`
- [x] Admin API: `/me/tokens` binds, lists and revokes by member; `/tokens` never binds; `bound` in the token views; `user` in the call audit; member removal. Tests: `TestMyTokenBelongsToTheSignedInMember`, `TestAnAdministratorCannotIssueATokenThatIsAMember`, `TestAnAPITokenCannotMakeAMembersToken`, `TestMyTokensAreFoundByMemberNotByName`, `TestRemovingAMemberRevokesTheirTokensAndTheOnesThatNameThem`, `TestAuditListNamesTheMemberACallRanAs`
- [x] Acceptance, with the real binary: `TestUpstreamOAuthTwoPeople` (three tokens that name Ada and a control token that names her are refused, nothing reaches the upstream, the audit names Ada only for her own calls), `TestTeamsLimitWhatEachPersonSees` (a token that names Ada sees and calls only what everyone reaches). The fixtures that had relied on the hole (`g.issue("ada")`, the owner's `boss-agent`) now make their tokens signed in
- [x] The new tests fail with the hole put back (name looked up at issue time): checked 2026-11-11, the upstream answered "you are ada@linear"
- [x] Admin app: the Tokens page marks a token that is not tied to the account, and says that the person of an API token is a label. Test: "says which of my tokens is not tied to my account"
- [x] Docs: README (Tokens, invites), `docs/deploy-coolify.md` ("Tokens belong to members (0.7.0)", breaking), version 0.7.0
- [ ] Owner on app.privo.club after the deploy: make a new token on the Tokens page, put it in Claude Code, revoke the old one
- [ ] Independent review of this change (auth, tenancy, the request pipeline: required by CLAUDE.md; run when the owner says go)
- [x] F07 R-01 and F14 R-02 ticked in their sections
- [ ] Follow-up: record the member (user id) on approvals so the self-approval check compares members, not labels (with F05 R-04)

## F29. Each server as it presents itself (ADR 0023)

Status: Built in 0.9.0 (2026-11-14). Issue 3, section 2.

#### Goal
An admin adding a server answers one question, whose account it sees, and
Privo reaches it the way it presents itself: nobody's, one shared key, each
person's own account by signing in (ADR 0017), or each person's own API key.
OAuth servers that do not say where people sign in can be added too.

#### Acceptance
1. `personal_key` on `POST /api/v1/servers`: header (default Authorization,
   "Bearer " before the key), optional prefix, help text and https link.
   Refused: reserved headers and Cookie, a prefix with line breaks, shared
   headers or oauth alongside, stdio, and no admin site.
2. Each person enters their own key on `/connect/<alias>` (signed in, CSRF,
   origin, password field, shape check only). It is stored like a grant,
   sealed to the person and the server, never shown again; a new one replaces
   it; Connections removes it.
3. Every call uses the caller's own key in the named header and nothing else;
   a person without a key gets the link; a key the server refuses (401, at
   the handshake or later) gives "<server> refused your API key" with the link
   and is kept.
4. A server without protected resource metadata is reported as such; with
   `oauth.issuer` the authorization server's metadata is used with every
   check of ADR 0017; with `authorization_endpoint`, `token_endpoint` and
   `client_id` nothing is discovered or registered. Endpoints are https and
   pass the egress policy. No empty `resource` parameter is sent.
5. The admin app asks "Whose account does this server see?"; the server,
   Start and Connections pages say "enter your key" for such a server.

#### Threat note
In ADR 0023: keys only sealed, never logged or shown, sent only to the
server's origin; header grammar and no line breaks; the member is chosen by
the token's user id; hand-entered endpoints pass the egress policy.

#### Tasks
- [x] `store.ServerConfig.Key`, `AuthKey`, `PurposeKeyGrant`, `PerPerson()`; a grant's credential is an OAuth grant or a key, read with its stored purpose. Test: `internal/store` `TestGrantCredentialMustBeAGrant`
- [x] `grants.SaveKey`, `ValidKey`, `ErrKeyRefused` (a key is never refreshed or deleted on a 401). Test: `internal/grants` `TestPersonalKey`
- [x] Upstream: the per-person header and prefix; a refused key is an error of its own. The gateway answers with the link. Covered by the acceptance test
- [x] Admin API: `personal_key`, the server views (`auth: "key"`, `key_header` for admins), audit `server.personal_key:<alias>:<header>`. Test: `internal/control` `TestAddingAServerThatTakesEachPersonsKey`
- [x] OAuth by hand: `upstreamauth.ErrNoResourceMetadata`, `ServerMetadataFor`, `CheckEndpoint`; registration `manual`; `resource` only when known; `iss` check only with a known issuer. Tests: `internal/upstreamauth` `TestNoResourceMetadataIsNamed`, `TestEmptyResourceIsNotSent`; `internal/control` `TestOAuthSettingsEnteredByHand`
- [x] Website: the key page and `POST /connect/{alias}/key`; Connections lists key servers. Tests: `internal/web` `TestKeyPageSavesThePersonsOwnKey`, `TestKeyAndSignInPathsDoNotCross`
- [x] Admin app: the question, the key and hand-entered OAuth fields, server, Start and Connections pages. Tests in `web/src/pages.test.tsx` "servers where each person brings their own key"
- [x] Acceptance on the real binary: `TestPersonalKeysEachPersonUsesTheirOwn` (two people, their own key each in `X-Api-Key`, no Authorization header; no key, a wrong key, a key revoked upstream while connected; removal; keys not on disk or in the log)
- [x] Docs: README, deploy guide, manual test step 11, ADR 0023; version 0.9.0
- [x] Mutation check (2026-11-14): 14 deliberate breakages of the new code, one at a time; all 14 made a test fail (the first run found a test that passed for the wrong reason, a DNS failure, and a check without a test; both fixed)
- [ ] An acceptance test for OAuth settings by hand against a fake authorization server without metadata (covered at unit level today)
- [ ] Editing an existing server's way of signing in (today: add it again)
- [ ] Independent review (secrets and the request pipeline), on the owner's word

#### Independent review of 2026-11-15 (`docs/reviews/2026-11-15-f07-f29-upstream-credentials-independent-review.md`)
Approve with changes; 0 Critical, 0 High, 2 Medium, 2 Low, 4 Info. Fixed, each with a test that fails without it:
- [x] U-01 (Medium): mix-up through settings entered by hand without an issuer. A callback that names any issuer is refused for a server with no issuer; an administrator whose authorization server sends `iss` enters its issuer. Tests: `internal/web` `TestHandEnteredSettingsRefuseAnAnswerNamingAnIssuer`, `TestConnectWithSettingsEnteredByHand` (the connect flow with hand-entered settings had no test). Left: per-server redirect addresses would close mix-up for authorization servers that send no `iss` at all (GitHub's), at the price of every pre-registered client's callback changing
- [x] U-02 (Medium, #11): see F14 R-01
- [x] U-03: when the upstream refuses the key the tool list is read with, the tool list moves to another administrator's key (`store.PassCatalog`); the refused key stays its owner's. Test: acceptance `TestRefusedCatalogKeyPassesToAnotherAdmin`
- [x] U-04: a key server's 403 is a refused key too (API gateways answer 403 to an unknown key). Test: acceptance `TestForbiddenKeyIsARefusedKey`
- [x] U-05: a person's key cannot go in a header that names the caller or the encoding (`Forwarded`, `X-Forwarded-*`, `X-Real-Ip`, `Remote-User`, `X-Auth-Request-*`, `Cf-*`, `Accept-Encoding`, `Content-Encoding` and others). Test: `TestKeyHeadersThatNameTheCallerAreRefused`
- [x] U-06: tests for the issuer-only path and the R-02 revocation half (acceptance `TestOAuthSettingsByIssuerAndNotOverMetadata`, `internal/grants` `TestRefreshUsesTheClientStoredNow`). The PR text said member removal was tested end to end; it is covered by the store's grant deletion tests, not by the acceptance test
- [x] U-07: a stored credential is used only for a server of its kind; a mismatch reads as not connected. Test: `TestCredentialKindMustMatchTheServer`
- [x] U-08: endpoints by hand are refused for a server that publishes its resource metadata. Test: acceptance `TestOAuthSettingsByIssuerAndNotOverMetadata`

## F28. Activity and telemetry (the audit, reworked)
Status: Built in 0.10.0 (2026-11-14). Issue 3, section 3.
ADRs: 0024 (call statistics, diagnostic log, metrics), 0006 (audit)

Reference: Meridian (https://github.com/rynfar/meridian, `MONITORING.md`):
one row per request with phase timings, a summary over a window, a time
series, a diagnostic log with categories, bounded retention, a dashboard,
Prometheus `/metrics`. Model tokens and cost have no counterpart in Privo
(it does not see model tokens); the nearest is the size of arguments and
results (F17).

#### Goal
An admin sees how calls went, not only who made them: which servers and tools
are slow or failing, where the time goes, and what happened to connections and
grants, for a bounded time and without anything leaving the server. The audit
keeps its guarantees.

#### Acceptance
1. Every call that reaches the decision stage writes one statistic row with
   member, token, client, server, method, item, outcome, error class, the time
   of each phase (decide, audit, connect, upstream, total) and the sizes of
   arguments and result; never arguments, results, credentials or error texts.
   Refused calls are rows with outcome `refused` and their class.
2. A full statistics queue drops and counts; a call never waits for or fails
   because of a statistic. The audit is unchanged.
3. `GET /api/v1/telemetry/summary?window=&by=` (server, tool, member, client,
   method): counts, error rate, p50/p95/p99/min/max/average of the total over
   calls that ran, p50/p95 of each phase, sizes; counts cover every call, times
   come from the most recent 100,000 calls that ran (`sampled`).
4. `GET /api/v1/telemetry/timeseries?window=&bucket=` with optional server and
   tool: per bucket calls, errors, tool errors, refusals, p50 and p95; bucket
   at least 1 minute, at most 1,000 buckets, window at most 31 days, else 400.
5. `GET /api/v1/telemetry/calls` (filters, newest first, at most 500) and
   `GET /api/v1/telemetry/logs` (category, level, newest first, at most 500).
6. `GET /api/v1/telemetry/storage`: retention, rows and the oldest of each
   table, rows dropped since start. All five need `audit.read`; another org's
   rows are never read.
7. Diagnostic log: upstream connect failure, disconnection and recovery; a
   grant refused by the authorization server and failed refreshes; a person's
   key refused; audit events lost; process start. Repeats within a minute
   collapse into one row with a count. Messages are fixed texts, or a fixed
   text and the upstream's answer with every URL reduced to scheme, host and
   path, cut at 512 bytes; what an upstream echoes of its own is its text.
8. `PRIVO_TELEMETRY_RETENTION_DAYS` (default 30, 1 to 400, 0 is off); hourly
   clean-up in batches.
9. `PRIVO_METRICS_ADDR` (off by default) serves Prometheus `/metrics` on its
   own listener: calls, a duration histogram, upstream state, audit lost,
   statistics dropped, build info; labels server, method, outcome only.
10. Activity page: Overview (window, totals, chart, breakdown), Tool calls,
    Admin changes, Log.

#### Threat note
Statistics show who called what and when, like the audit: same permission
(`audit.read`), same org scoping. They hold no arguments or results. The
diagnostic messages are fixed texts or redacted upstream errors (the
redaction used for the server status). `/metrics` has no authentication and
therefore listens on its own address, never the public one; its labels are
server aliases, methods and outcomes, no members or tools. A flood of calls
cannot grow memory: the queue and the deduplication map are bounded.

#### Tasks
- [x] Spec, ADR 0024 (this section)
- [x] Store: migration 00009, `CallStat`, `Diagnostic`, insert, query, storage, prune. Tests: `TestCallStatsRoundTripAndScope`, `TestPruneTelemetry`, `TestDiagnosticsQuery`
- [x] `internal/telemetry`: recorder (bounded queue, drop and count, diagnostics dedupe), summaries and series, Prometheus text. Tests: `TestRecorderDropsWhenFull`, `TestRecorderCountsRowsItCannotWrite`, `TestDiagnosticsRepeatCollapse`, `TestDiagnosticsHeldRepeatsAreWrittenOnClose`, `TestSummarize`, `TestSeries`, `TestMetricsText`
- [x] Gateway: phase timings, sizes, outcome and error class on every path. Tests: `TestCallStatisticsRecordPhases`, `TestRefusedCallsAreCounted`, `TestAuditFailureIsCountedAndLogged`, `TestErrorClasses`, `TestEncodedSizeIsTheRelayedJSON`
- [x] Diagnostics from upstream, grants, gateway, audit, lifecycle. Tests: `TestUpstreamDiagnostics`, `TestGrantLostIsLogged`, `internal/audit` `TestLostEventsAreReported`
- [x] Admin API: the five endpoints. Tests: `TestTelemetryAPI`, `TestTelemetryAPINeedsAuditRead`, `TestTelemetryWindowLimits`
- [x] Settings `PRIVO_TELEMETRY_RETENTION_DAYS`, `PRIVO_METRICS_ADDR`; clean-up job; metrics listener. Test: `TestTelemetrySettings`
- [x] Activity page: Overview (default tab), Tool calls, Admin changes, Log; labelled cells on phones. App tests: "activity overview and log", `overview.test.ts`; looked at in light and dark, desktop and phone
- [x] Acceptance on the real binary: `TestActivityTelemetry` (calls show up with phases and sizes, share the audit's call id; /metrics answers on its own address only), `TestTelemetryOff`
- [x] Docs: README "Activity: how calls went", deploy guide 0.10.0, manual test step 12, ADR index; version 0.10.0
- [x] Mutation check (2026-11-14): 23 deliberate breakages of the new code (org scoping, pruning, a blocking queue, repeat collapse, refusals timed, label escaping, error classes, permissions, window limits, diagnostics, the metrics port), one at a time; all 23 made a test fail
- [ ] Independent security review (on the owner's word)

#### Independent review of 2026-11-15 (`docs/reviews/2026-11-15-f28-telemetry-independent-review.md`)
Approve with changes; 0 Critical, 1 High, 1 Medium, 5 Low, 3 Info. Fixed, each with a test that fails without it:
- [x] T-01 (High): every diagnostic message is cut at 512 bytes on a rune boundary in `Recorder.Diagnose`, whatever its source; server and member at 64. Test: `TestDiagnosticsAreClipped`
- [x] T-02 (Medium): counts and chart buckets come from `GROUP BY` in SQL (`store.CountCallStats`) and cover every call; times from the newest 100,000 calls that ran (`store.SampleCallStats`), flagged `sampled` with `sample_from`, and the Overview says so. Tests: `internal/store` `TestCountAndSampleCallStats`, `internal/telemetry` `TestSummarize`, `TestSeries`
- [x] T-03: compact sample rows; at most two aggregations at once; an answer reused for five seconds. Test: the cache in `TestTelemetryAPI`
- [x] T-04: retention 0 deletes what an earlier run kept. Test: acceptance `TestSwitchingStatisticsOffDeletesThem`
- [x] T-05: every URL in upstream text loses its query and user info (`RedactText`); the threat note no longer promises that upstream text carries nothing. Test: `TestUpstreamDiagnosticsRedactTheURL` (a closed port, so the error names the URL)
- [x] T-06: the result size is a sum of content lengths (text; binary as base64), no second encoding. Test: `TestResultSizeFromContentLengths`
- [x] T-07: the redaction test can fail now; the key-refused diagnostic is checked in acceptance `TestForbiddenKeyIsARefusedKey`; pruning leaves another org's diagnostics (`TestPruneTelemetry`)
- [x] T-08: the deploy guide says what `0.0.0.0:9464` exposes to the Docker network. Open: an optional bearer token for `/metrics`
- [x] T-09: exactly 1,000 buckets allowed; the chart counts the same window as the totals; `limit` over the bound reads the bound; a huge day count is refused before multiplying
- [x] T-10: the time held for a person's approval is its own phase (`wait_us`) and is left out of latency and the duration histogram (`TestMetricsText`, `TestCountAndSampleCallStats`)

## F30. Documentation site (docs.privo.club)
Status: In progress (skeleton and the moved content, 2026-11-22; guideline in `docs/documentation-site.md`)

Goal: the user documentation is a website built with MkDocs from
`docs/site/`, next to the code and changed in the same pull request, the way
osquery builds osquery.readthedocs.io. Internal records (`docs/todo.md`,
`docs/reviews/`, checkpoints, manual tests, `docs/upstream/`) stay out of it.

Acceptance:
1. `mkdocs build --strict` passes in CI (job `docs`, `make docs`); a broken
   link or anchor, a page missing from `nav`, or `nav` naming a missing page
   fails it.
2. MkDocs and every dependency are pinned with hashes
   (`docs/requirements.txt`, generated from `docs/requirements.in`) and
   installed with `--require-hashes --only-binary=:all:`, so no package runs
   code at install time.
3. Every README section has a page; the README keeps the introduction, a
   quick start that works on a fresh data directory, and links.
4. The configuration reference (`docs/site/installation/configuration.md`)
   is the one list of settings: a test fails when a registered setting is
   missing from it, or when it names a `PRIVO_` setting the registry does not
   know.
5. Every moved file leaves a pointer at its old path.

Threat note: the site is static and holds nothing secret; examples use
`example.com`, `privo_pat_...` and documentation addresses. The build runs
third-party Python, so it is pinned and hashed, wheels only, and runs in CI
with read-only repository permissions. The live deployment's specifics (app
ids, DNS, webhook state) stay internal in `docs/privo-club-deployment.md`,
not on the site. Hosting adds headers (CSP, `nosniff`, referrer policy) in
task 4.

### Tasks (the order of `docs/documentation-site.md`, section 3)
- [x] Skeleton: `mkdocs.yml` (readthedocs theme, explicit `nav`, strict
  validation of links, anchors and nav), `docs/requirements.in` and hashed
  `docs/requirements.txt` (MkDocs 1.6.1), `make docs`, `make docs-serve`,
  `make docs-requirements`, `/build/` in `.gitignore`, CI job `docs`.
  Checked: the strict build fails on a broken link, a missing anchor and a
  page left out of `nav`.
- [x] Move the content: the README sections into `docs/site/installation/`
  and `docs/site/using/`; `docs/deploy-coolify.md` into Deploy on Coolify
  (made general, `app.example.com`) and Backup, restore and upgrades (with
  the upgrade notes per version); `docs/versioning.md` and the ADR index into
  `docs/site/development/`. Pointers at `docs/deploy-coolify.md` and
  `docs/versioning.md`; privo.club's own deployment kept internal in
  `docs/privo-club-deployment.md`.
- [x] Settings test on the page. Tests: `internal/config`
  `TestRegistryIsInTheConfigurationPage` (replaces
  `TestRegistryIsInTheREADME`), `TestConfigurationPageNamesOnlyRegisteredSettings`;
  `TestExampleFileListsEverySetting` unchanged
- [x] Pages written from existing sources: Tests, How a feature is made
  (`CLAUDE.md`), Building Privo
- [x] Getting Started section (#60, phase 1 of the docs roadmap #59): How
  Privo works, Key concepts (the "Words Privo uses" page), Deploy Privo (a
  hub for Coolify now, with room for the deployment model of ADR 0030),
  First setup and sign-in, Your first server and AI client; the welcome page
  leads through it, then to the basic and advanced pages
- [ ] Write what is missing: Arguments checked against the schema (#35), and
  the Security section's threat model, secrets and keys, and egress (#63;
  Reporting a vulnerability shipped with F31)
- [x] Hosting on Coolify (guideline 2.6 B, chosen 2026-11-24): `docs/Dockerfile`
  builds the site and serves it with Caddy (pinned by digest, user `nobody`,
  port 8080) with CSP `script-src 'self'` and no `unsafe-inline`: the theme's
  inline scripts move into content-named files
  (`docs/externalize_inline_scripts.py`, which fails the build if one is
  left); highlight.js from a CDN is off, so the site makes no third-party
  request. Also `nosniff`, `Referrer-Policy: same-origin`, HSTS, COOP, a
  year's cache only for the content-named files. CI builds the image.
  Checked in a browser: no CSP violation, search and anchors work
- [ ] The Coolify app `privo-docs` and `docs.privo.club` (owner, 2026-11-24;
  settings in `docs/privo-club-deployment.md`)
- [x] Link the docs from the landing page (its refresh, 2026-11-25: nav, footer,
  quick start, security and FAQ link to docs.privo.club)
- [ ] Link the docs from the admin app's header
- [ ] Coolify: add `!mkdocs.yml` to the gateway's Watch Paths
  (`docs/privo-club-deployment.md`)
- [x] Rules: `CLAUDE.md` names `docs/site/` in the workflow, the quality bar
  and the layout
- [ ] Later: a weekly external link check (for example `lychee` on
  `build/site`)

## F31. Open source (MIT)
Status: In progress (owner's decision, 2026-11-24)

Goal: the repository is public under the MIT License, with a private way to
report vulnerabilities, so any team can read, run and change Privo.

Acceptance:
1. `LICENSE` (MIT), `SECURITY.md`, `CONTRIBUTING.md` at the root; the README,
   the documentation site and both `package.json` files say MIT.
2. Nothing secret in the history before it goes public: checked 2026-11-24
   over all 148 commits (token, key and private-key patterns, base64 keys,
   public IP addresses, committed `.env` or key files). Only test values
   were found (`privo_pat_NOT-A-REAL-SECRET-marker-7731`, a base64 of
   `secret-secret-secret-secret-1234`).
3. Reporting a vulnerability works before the first outside reader arrives
   (GitHub private vulnerability reporting on).

Threat note: going public publishes the independent reviews and their open
findings (all Low or Info on 2026-11-24, listed in this file), the commit
author's e-mail address, the privo.club deployment record
(`docs/privo-club-deployment.md`: Coolify app ids, no secrets) and the
waitlist site's mail host. None of these is a secret; an attacker learns the
same from the binary and DNS. The decision to publish the reviews is
deliberate: they show how Privo is checked.

### Tasks
- [x] `LICENSE`, `SECURITY.md`, `CONTRIBUTING.md`; license in the README, on
  the site (Welcome, Security → Reporting a vulnerability) and in
  `web/package.json`, `site/package.json` and their lock files
- [x] History checked for secrets (acceptance 2)
- [x] The old root checkpoint moved to `docs/checkpoint-2026-10-29.md`
- [ ] Owner, on GitHub: make the repository public; then Settings → Code
  security: private vulnerability reporting, secret scanning with push
  protection, Dependabot alerts; a ruleset on `main` (pull request and the
  CI checks required, no force push)
- [ ] Distribution and deployment for any team: ADR 0030
