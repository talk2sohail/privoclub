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
- [ ] R-04 (Low): self-approval compares `person` exactly while the owner's default person is lower-cased, so `Alice` (tokens) and `alice` (session) are two people; normalise person names everywhere and make the index case-insensitive
- [ ] R-05, R-06 (Info): the GitHub token is dropped, not revoked, at GitHub; a second `admin setup-link` makes another owner without saying so; `PRIVO_GITHUB_CLIENT_SECRET` as a plain variable gets no warning; timestamps compare as strings of uneven width (sub-second error at expiry edges)

### Later in F05

- [ ] Orgs and groups (invites came with F07, bound by the numeric GitHub id; the People page with role changes and removal came with F14; teams with F26)
- [ ] Google and Microsoft OIDC; magic-link fallback
- [ ] Restrict sign-in by GitHub organisation membership (`read:org`)
- [ ] Step-up re-authentication for high-risk actions
- [x] Sessions accepted by the admin API for the UI (F14): through `/admin/api/v1`, never on `/api/v1` (ADR 0019)
- [ ] Approvals page: page through more than 50 pending approvals; show decided ones

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
- [ ] R-04 (Low): removing a member, deleting a server and reconnecting delete grants locally without revoking them at the provider; backups keep them
- [ ] R-05 (Low): require `oauth.issuer` when a client secret is supplied, so the secret cannot go to an authorization server the MCP server names
- [ ] R-06 (Low): a failed save after a rotated refresh token loses the grant; retry the save with the token already obtained
- [x] R-07 (Low): the GitHub callback uses up a state of any purpose before checking it; look the state up with its purpose and use one cookie per flow **Fixed in 0.7.1:** the sign-in callback reads no table; sign-in and connect have a cookie each; the connect lookup takes only connect rows. Tests: `internal/web` `TestSignInAndConnectFlowsDoNotEndEachOther`, `internal/store` `TestRowsOfOtherPurposesAreNotConnectStates`
- [ ] R-08 (Low): every upstream HTTP request reads, decrypts and touches the grant on the single writer connection; cache the opened token until its expiry and flush `last_used_at` periodically
- [ ] R-09 (Info, ten notes): among them no backoff when a server rejects every token, discovery of up to a minute inside one admin request, an orphaned dynamic registration when the create fails afterwards

### Later in F07

- [ ] URL-mode elicitation (2025-11-25) and input-required (2026-07-28) for the connect link
- [ ] Shared org-wide credentials, only by explicit admin choice and with a policy limiting the tools
- [ ] Scope step-up on `insufficient_scope`
- [ ] Admin page of everyone's connections (a server's page lists its connected people since F14; a page across servers is still to do)
- [ ] Refresh coordination across replicas beyond compare-and-swap (Postgres)

## F08. Toolsets
Status: Not started (server-level access per team came first, in F26 and ADR 0020)

- [ ] Toolsets per user and group; hide unseen tools from lists

## F09. Policies
Status: In progress (core done, reviewed)
ADRs: 0001, 0011, 0014

- [x] Allow, deny, ask rules by server alias, tool pattern, subject (`*`, `role:`, `token:`); priority, ties deny > ask > allow; expiring rules (`internal/policy`)
- [x] Own glob matcher where `*` covers any characters, including `/`
- [x] Org default action (allow, ask, deny); rules apply to tools, prompts and resources ("ask" fails closed for prompts and resources)
- [x] Presets per server: read-only, ask-before-writes; preset rules tied to the reviewed definition hash
- [x] Annotations tighten defaults only (explicit destructiveHint means ask)
- [x] Admin API: list, create, delete rules, apply presets, set default; every change audited
- [x] Rules removed with their server
- [ ] Export and import as YAML
- [ ] Cross-server flow rule (source and sink tags)
- [ ] Admin overrides of annotations shown to clients
- [ ] Cache rules per org with invalidation (now read per call)
- [ ] Argument conditions in rules

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
- [ ] Scoped temporary grants from an approval, batching, approver dashboard
- [ ] Withdraw endpoint for requesters

## F11. Manifest pinning and scanning
Status: Not started

ADRs: 0001, 0002

- [ ] Per-tool canonical hash pins with scope keys; first pin reviewed
- [ ] New tools hidden until accepted; changed tools blocked; diff view
- [ ] Scan tool descriptions for hidden instructions

## F12. Audit log
Status: In progress
ADRs: 0001, 0002, 0006

- [x] Synchronous intent record; batched completion; bounded queue that refuses rather than drops
- [x] Arguments recording off by default; when on, arguments stored encrypted under the org key
- [ ] Per-org fairness in the queue
- [ ] Sealer, block chain, Ed25519-signed exportable checkpoints
- [ ] Recording off by default; encrypted payloads; GDPR erasure
- [ ] Search, CSV export, retention by partition

## F13. Stdio runner
Status: Not started

ADRs: 0007

- [ ] Versioned runner protocol (spawn, stream, kill, health) over mutual TLS
- [ ] Per-process user, cgroup limits, read-only root, seccomp
- [ ] Pinned packages installed at registration; no `npx -y`
- [ ] Isolated network; egress allow-list proxy

## F14. Admin UI
Status: In progress (the Preact admin app is deployed, app.privo.club runs 0.6.0; independent review done 2026-11-03, its findings are open below)
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
- [ ] R-03 (Low): a refused or cross-site request still slides the session's idle timeout; touch the session only after every check passed
- [ ] R-04 (Low): the review ticks everything by default, also items that arrive while the page polls; make ticking opt-in or mark new rows
- [ ] R-05 (Low): the UI build has no audit step (`web/.npmrc` has `audit=false` and `legacy-peer-deps=true`; `npm ci` runs lifecycle scripts; actions are pinned by tag): `npm ci --ignore-scripts`, `npm audit --omit=dev`, `npm audit signatures` in the `ui` job, actions by SHA
- [ ] R-06 (Low): the bridge passes a body on GET and DELETE and accepts any content type with an empty body
- [ ] R-07, R-08 (Info): a session can mint long-lived owner control tokens (consider a confirmation and an audit tag); a token shown once stays in the page's state until the person leaves

### Later in F14

- [ ] Toolsets (F08), context-cost report (F17), policy presets in the page
- [ ] Re-enabling a removed member; editing a server

## F15. Coolify deployment
Status: In progress (deployed on Coolify since 2026-10-29, guide in `docs/deploy-coolify.md`; snapshot backup, restore and the built-in schedule done; scheduled backups are off in production by the owner's choice)

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
Status: Not started
ADRs: 0011

- [ ] Seven roles; role changes audited
- [ ] Generic OIDC and SAML SSO; group mapping
- [ ] SCIM 2.0 with immediate revocation on deprovisioning
- [ ] Passkeys or TOTP for local accounts

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
- [ ] Second review R-01 (Medium): a bootstrap token still installs on an organisation that was set up with the setup link, because "needs a bootstrap" looks only at control tokens and a browser-only org has none. Count `users` in the same transaction in `InstallBootstrapToken` and `BootstrapNeeded`. (app.privo.club has had control tokens since 2026-10-29, so it is not affected; an install set up only in the browser is)
- [ ] Second review R-02 (Medium): `privo token create` writes no audit row (the item "Discovered" below); write the token row and a `token.create` event with actor `cli` in one transaction, as `admin setup-link` and `admin invite` do
- [ ] Second review R-03 to R-06 (Low): a live bootstrap token is not retired when a person claims the setup link; `token.Weak` passes patterns with 16 or more different symbols; a malformed leftover value stops `serve` on an org that no longer needs a token; a reused token id fails the first boot with an SQL conflict that does not name the variable
- [ ] Second review R-07 (Info): unset the plain variable also when the file form was used; one line in the backup docs that restoring a backup older than the install lets the token install again
- [ ] Discovered: tokens created by `privo token create` are not audited (the admin API audits its own); the second review rates it Medium (R-02 above) and gives the fix
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
- [ ] R-03: calls on servers outside the caller's teams leave no audit record (same as unknown names today); write a rate-limited `denied:not_in_team` event without changing the answer
- [x] R-04: admins get `teams: []` for a server nobody reaches, and the Servers list warns ("nobody")
- [x] R-05: the add-server and catalog forms wait for the team list and stay disabled when it cannot be read, so a server is never opened to everyone by a failed fetch (Vitest)
- [x] R-06: unknown teams and servers in the team endpoints are 404, not 500; `POST /api/v1/servers` checks its teams before any discovery or registration at the provider
- [x] R-07: team names and descriptions refuse line and paragraph separators, private-use and unassigned characters, more than two combining marks in a row and names with no visible letter or digit (Hangul fillers); limits count characters; names are unique ignoring case and "Everyone" is reserved
- [ ] R-09 (Info): `INSERT OR IGNORE` and `server_teams.team` without a foreign key need care when the Postgres engine lands (F03)
- [ ] R-10 (Info): approvers and auditors now list only the servers their teams reach; `/me/tools` reads pins once per server; batch later if it shows
- [x] Local browser walkthrough (Chrome, 2026-11-04, 0.6.0 against a fake GitHub and the real DeepWiki): owner sign-in; Teams (create from a profile, suggested servers); catalog add of DeepWiki for Designers only (status up, 3 tools reviewed and accepted); "Who can use it" on the server page; an invited member lands on Start with no servers, sees DeepWiki once added to Designers, makes a token and every client tab fills it in; no console errors. Found and fixed: a duplicated "Who can use it" heading
- [ ] Owner's walkthrough on Coolify with real GitHub sign-in and an OAuth catalog server (`docs/manual-test.md` section 9). Done in Chrome on app.privo.club, 2026-11-04, after the 0.6.0 deploy (the migration gave deepwiki to Everyone): 9.1, 9.3 (with Miro: dynamic registration ok, "needs an administrator's connected account" as expected), 9.4, 9.7 and 9.8 pass; Miro removed afterwards. Open: 9.2, 9.5 and 9.6 need a second GitHub account; connecting an OAuth server and accepting its tools needs an account at a provider the owner uses (Linear, Notion…)
- [ ] Adding a server: a failure is shown in the flash at the top of the page, out of view of a catalog form lower down, so "Add" looks like it did nothing (seen with Figma on app.privo.club, 2026-11-04). Show the error next to the form that failed
- [ ] Adding a server whose provider is slow waits about 10 s on "Adding…" and ends in "context deadline exceeded"; say which step timed out at which host in plain words (Figma's authorization server metadata answered 503 or hung most of the time on 2026-11-04 while its status page was green)
- [ ] Discovered: token names are unique in the whole org, so two people cannot both call their token `claude-code`; make them unique per person (with the person in the name shown to admins)
- [ ] Later: tool-level toolsets inside a server (F08); a team's own rule presets; identity provider groups mapped onto teams (F20)

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
   calls that ran, p50/p95 of each phase, sizes; `truncated` past 100,000 rows.
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
   collapse into one row with a count. Messages carry no credentials.
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
