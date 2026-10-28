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
- [ ] Decide whether `/version` stays unauthenticated
- [x] List results (tools, prompts, resources, templates) carry `cacheScope: "private"` and `ttlMs: 5000`; `resources/read` is private with ttl 0 and overrides an upstream's own hints (ADR 0001). Found with Claude Code on 2026-10-29: it rejected the empty `cacheScope` and showed "tools fetch failed". Test: `TestListAndReadResultsCarryValidCacheHints` (raw JSON, since the Go client does not check the field)

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
- [ ] Cap the size of upstream responses (tool results, resource reads, list pages); today the gateway relays whatever an upstream sends and holds it in memory

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
- [ ] Report to the Go SDK: results returned from receiving middleware miss `resultType` (the field is unexported and only the SDK's own handlers set it). Drop `typedResult` when a release fixes it
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
3. A malformed value stops the process at startup (exit 2) and the message never contains the value.
4. The install is audited (`bootstrap.token.install:<id>`, actor `system`); the log carries the token id only.
5. The variable is removed from the process environment after loading.
6. `privo token generate` prints a fresh well-formed token and touches neither the data directory nor the configuration, so it can run on a laptop.
7. `GET /` returns a static HTML page (200) with security headers; any other unknown path stays 404; `POST /` is 405; the page shows no version, org or configuration.
8. A push to `main` that changes only `site/**` deploys the landing page and not the gateway; a push that changes anything else deploys the gateway and not the landing page. The GitHub webhook delivers (verified signature) to Coolify.

Threat note:
- The bootstrap token sits in the Coolify environment, readable by anyone with Coolify access and via `/proc/<pid>/environ`. Those people already hold the master key. Mitigations: 24-hour expiry from install, install-once marker, variable cleared from the process environment, `is_shown_once` in Coolify, and the guide says to delete the variable after first use.
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
