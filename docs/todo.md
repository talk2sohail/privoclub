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
- [x] Versioning follows semver (owner's decision, 2026-10-29): the version is the line in `internal/buildinfo/VERSION` (now `0.1.0`), embedded in the binary, so Coolify builds report it without build arguments; `/version` and `privo version` return `{version, go_version}` and no longer a commit hash or build date; the ldflags and Dockerfile build arguments for version, commit and date are gone. Rules for bumping are in `docs/versioning.md`. Tests: `internal/buildinfo` `TestVersionIsSemver`, `TestSemverGrammar`, `TestGetShape`; `internal/server` `TestEndpoints`; `cmd/privo` `TestVersion`
- [x] The master key moved from the `PRIVO_MASTER_KEY` variable to a file mount (owner's decision, 2026-10-29): `PRIVO_MASTER_KEY_FILE=/run/secrets/privo_master_key` on Coolify, the variable deleted, both deploys healthy, the "master key given as PRIVO_MASTER_KEY" warning gone. Steps and the password-manager backup note are in `docs/deploy-coolify.md`. To be streamlined with the admin UI
- [x] CI lint fixed (2026-10-29): `errorlint` flagged `err != want` in `internal/gateway/resulttype_test.go` (now `errors.Is`); it was the only finding. While checking, `go test -race` on the acceptance suite showed 23 data races in the harness (the gateway's log buffer was read by the test while the process wrote it): `logBuffer` in `test/acceptance/harness_test.go` now has a lock, and the suite passes under `-race`. `/_to_delete/` is in `.gitignore`
- [ ] Make the acceptance job in `.github/workflows/ci.yml` a real gate: it still has `continue-on-error: true` and the comment "allowed to fail until F02", but the suite passes. Decide with the owner, then drop both (the file is protected from remote writes)
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
Status: Not started
ADRs: 0003

- [ ] First-boot setup link (until then, F24's `PRIVO_BOOTSTRAP_TOKEN` gives the first owner token)
- [ ] OIDC (Google, Microsoft); magic-link fallback
- [ ] Orgs, members, roles, groups, invites

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
Status: Not started
ADRs: 0003

- [ ] Per-user connect flow, refresh, rotation
- [ ] Shared credentials chosen by admins
- [ ] "Connect your account" error with link

## F08. Toolsets
Status: Not started

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
- [ ] Web approval page with sessions, CSRF and step-up (needs F05)
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
Status: Not started

- [ ] Servers, people, toolsets, policies, approvals, audit screens

## F15. Coolify deployment
Status: Not started

ADRs: 0010

- [ ] Docker Compose file with all defaults; generated secrets; master key file
- [ ] `privo migrate`, snapshot before upgrade, pinned tags
- [ ] Built-in WAL shipping backup; `privo restore`; restore drill
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
ADRs: 0003, 0010 (no new ADR: the bootstrap token is an interim measure until the F05 first-boot setup link replaces it)

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
