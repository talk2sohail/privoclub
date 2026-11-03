# Privo feature tracker

One section per feature. Each section keeps its goal, its acceptance criteria
and a to-do list. Tick items as they land; add items as they are discovered.
Status: Not started, In progress, Blocked, Done.

## F00. Architecture review
Status: In progress

- [x] Draft ADR 0001 request pipeline and extension points
- [x] Draft ADR 0002 data model and tenancy
- [x] Draft ADR 0003 secrets and authentication
- [x] Independent reviews: security, scale and reliability, MCP protocol
- [x] Revise ADRs 0001 to 0003 (revision 2, target MCP 2026-07-28)
- [x] Write ADRs 0004 to 0011 for the gaps
- [x] Owner sign-off; ADRs accepted 2026-10-28
- [x] Spike: OAuth server library, desk review; ory/fosite chosen, see ADR 0012 (accept after F06 prototype)

## F01. Project foundation
Status: In progress

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
- [ ] Build the container image in CI (no Docker in the workspace)
- [ ] Initialise git repository for `privo` (it currently sits inside the prototype's repository) and first commit
- [ ] Place `Makefile` and `.github/workflows/ci.yml` (protected from remote writes; sent to owner)

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
- [x] Versioning follows semver (owner's decision, 2026-10-29): the version is the line in `internal/buildinfo/VERSION` (now `0.5.0`), embedded in the binary, so Coolify builds report it without build arguments; `/version` and `privo version` return `{version, go_version}` and no longer a commit hash or build date; the ldflags and Dockerfile build arguments for version, commit and date are gone. Rules for bumping are in `docs/versioning.md`. Tests: `internal/buildinfo` `TestVersionIsSemver`, `TestSemverGrammar`, `TestGetShape`; `internal/server` `TestEndpoints`; `cmd/privo` `TestVersion`
- [x] The master key moved from the `PRIVO_MASTER_KEY` variable to a file mount (owner's decision, 2026-10-29): `PRIVO_MASTER_KEY_FILE=/run/secrets/privo_master_key` on Coolify, the variable deleted, both deploys healthy, the "master key given as PRIVO_MASTER_KEY" warning gone. Steps and the password-manager backup note are in `docs/deploy-coolify.md`. To be streamlined with the admin UI
- [x] CI lint fixed (2026-10-29): `errorlint` flagged `err != want` in `internal/gateway/resulttype_test.go` (now `errors.Is`); it was the only finding. While checking, `go test -race` on the acceptance suite showed 23 data races in the harness (the gateway's log buffer was read by the test while the process wrote it): `logBuffer` in `test/acceptance/harness_test.go` now has a lock, and the suite passes under `-race`. `/_to_delete/` is in `.gitignore`
- [x] The acceptance job in `.github/workflows/ci.yml` is a real gate (owner's go-ahead, 2026-10-29): `continue-on-error` and the stale "allowed to fail until F02" comments (workflow, Makefile) removed; the suite passes, also under `-race`
- [x] List results (tools, prompts, resources, templates) carry `cacheScope: "private"` and `ttlMs: 5000`; `resources/read` is private with ttl 0 and overrides an upstream's own hints (ADR 0001). Found with Claude Code on 2026-10-29: it rejected the empty `cacheScope` and showed "tools fetch failed". Test: `TestListAndReadResultsCarryValidCacheHints` (raw JSON, since the Go client does not check the field)
- [x] Hostile upstream replies cannot crash the gateway (two High findings of the independent review, 2026-10-29). An upstream is untrusted input and one process serves every tenant, so a reply that breaks the protocol may cost only that server. Acceptance: (1) a `null` entry in an upstream's tools, prompts or resources list makes that server `down` with an error that says so, at first connect and on a list-changed refresh, while the gateway keeps serving its other servers; (2) a `resources/read` reply with a `null` entry in `contents`, or with no result, is refused with an error that calls the reply malformed, nothing of it is relayed, and the next request on the same connection works. Tests first: `internal/upstream` `TestLoadRefusesNullListEntries`; `internal/gateway` `TestRewriteRead`; acceptance `TestHostileUpstreamNullListEntry` and `TestHostileUpstreamNullReadContent` (echoserver `-mode hostile -null tools|prompts|resources|read`). Fix: `upstream.load` and `readResource` (via `rewriteRead`) check for nil before use. The Go SDK already drops a null tool entry itself (`filterValidTools`), so a null in tools/list leaves the server up with its real tools; the tools loop is guarded anyway. Not yet redeployed. Still open from the same review: a result size cap (F19), and the audit outcome for calls the wrapper refuses.
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
Status: In progress (first slice built and tested: GitHub sign-in and the web approvals page; independent review and the Coolify run open)
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
- [ ] Independent review of the finished slice (auth, sessions, CSRF: required by CLAUDE.md; run when the owner says go)
- [ ] On Coolify: create the GitHub OAuth App, mount the client secret, set the two variables, deploy, run `privo admin setup-link`, claim it, approve one real call (owner's steps; `docs/deploy-coolify.md`)

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

### Later in F05

- [ ] Orgs, members, roles, groups (invites came with F07: `privo admin invite`, bound by the numeric GitHub id; a members page and role changes in the browser are still to do)
- [ ] Google and Microsoft OIDC; magic-link fallback
- [ ] Restrict sign-in by GitHub organisation membership (`read:org`)
- [ ] Step-up re-authentication for high-risk actions
- [x] Sessions accepted by the admin API for the UI (F14): through `/admin/api/v1`, never on `/api/v1` (ADR 0019)
- [ ] Approvals page: page through more than 50 pending approvals; show decided ones

## F06. Client authentication (OAuth server and personal tokens)
Status: In progress
ADRs: 0003

- [ ] Protected resource and authorization server metadata
- [ ] Client registration, PKCE, audience-bound tokens, refresh rotation
- [x] Gateway and control tokens, each refused on the other surface; tokens belong to a person (ADR 0014)
- [x] Personal token format `privo_pat_<id>_<secret><crc32>`, SHA-256 secret hash, constant-time verify, checksum rejection without lookup (`internal/token`)
- [x] Token storage, roles (owner, admin, auditor, member), mandatory expiry (default 90 days), revocation effective within 2 s (verification cache), owner tokens only issuable by owners
- [ ] Tests with real clients: Claude Code, Cursor, Amp, Codex

## F07. Upstream OAuth broker
Status: In progress (first slice built and tested 2026-11-01: each person connects their own account; independent review open)
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
- [ ] Independent review (auth, secrets, tenancy, the request pipeline: required by CLAUDE.md)

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

### Later in F07

- [ ] URL-mode elicitation (2025-11-25) and input-required (2026-07-28) for the connect link
- [ ] Shared org-wide credentials, only by explicit admin choice and with a policy limiting the tools
- [ ] Scope step-up on `insufficient_scope`
- [ ] Admin page of everyone's connections
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
ADRs: 0001, 0002

ADRs: 0006

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
Status: In progress (second slice built 2026-11-03: the Preact admin app replaces the server-rendered pages; independent review open)
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

### Later in F14

- [ ] Toolsets (F08), context-cost report (F17), policy presets in the page
- [ ] Re-enabling a removed member; editing a server

## F15. Coolify deployment
Status: Not started

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
Status: Not started

ADRs: 0010

- [ ] OpenTelemetry span per stage; metrics without org labels in Cloud
- [ ] `/livez`, `/readyz`; 30-second drain on SIGTERM

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

- [x] Claude Code over a static bearer header (`claude mcp add --transport http`): connects; the empty `cacheScope` bug was found and fixed (see F02). Still to confirm in Claude Code: `tools/call`, prompts and resources
- [x] `tools/call` through DeepWiki failed in Claude Code: results lacked `resultType` (required from 2026-07-28). Fixed in `internal/gateway/resulttype.go`; `TestEveryResultCarriesResultTypeOnNewProtocol` speaks the new protocol as raw JSON against an upstream that does and one that does not. Needs a re-run in Claude Code after deploy
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
Status: In progress (code done, tested; Coolify settings and independent review open)
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
- [ ] Independent security review of the bootstrap change (auth, secrets). A self-review by the implementing session is in `docs/reviews/2026-10-29-f24-bootstrap-self-review.md`; it does not replace this
- [ ] Discovered: tokens created by `privo token create` are not audited (the admin API audits its own); decide whether the CLI writes an audit event
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
Status: In progress (first slice built 2026-11-03; independent review and the owner's browser walkthrough open)
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
- [ ] Independent review (tenancy and the request pipeline: required by CLAUDE.md)
- [ ] Owner's browser walkthrough (`docs/manual-test.md` section 9)
- [ ] Discovered: token names are unique in the whole org, so two people cannot both call their token `claude-code`; make them unique per person (with the person in the name shown to admins)
- [ ] Later: tool-level toolsets inside a server (F08); a team's own rule presets; identity provider groups mapped onto teams (F20)
