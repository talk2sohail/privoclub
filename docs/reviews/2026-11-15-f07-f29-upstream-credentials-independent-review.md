# Independent review: F07 fixes (0.8.1, PR #11) and each person's own key / OAuth by hand (0.9.0, PR #12)

- Date: 2026-11-15
- Reviewer: independent review agent
- Commits reviewed: `f06-oauth-server` 1ec290a2ce34535df24bd28a24f6b496bce4e152 (base), `f07-upstream-fixes` 0d99ad1b0437f444743dd5abfd31b827e20154b6 (PR #11), `f07-personal-keys` b650528e2b9933478dafe72b803e543f73ebdbbb (PR #12, contains #11)
- Scope: `git diff f06-oauth-server..f07-personal-keys`; ADRs 0023, 0017, 0003, 0008, 0014; `docs/todo.md` F07, F14, F29. Surrounding code read where the flows go.
- Work copies: `$WORK/repo` (branch `f07-personal-keys`, plus my test `internal/web/zz_review_mixup_test.go`) and `$WORK/mut` (mutations, plus `test/acceptance/zz_review_catalogkey_test.go`), `$WORK` = `/tmp/claude-0/-home-claude/381a408b-5da9-543e-8116-8894420450be/scratchpad/reviews/b`. The author's clone was only read.

## Verdict

| Pull request | Verdict | Why |
|---|---|---|
| #11 `f07-upstream-fixes` (0.8.1) | **Approve with changes** | The R-02 fix is correct. The R-01 fix shows the whole definition but cannot show invisible characters, which are the usual way to hide a poisoned instruction (U-02). The revocation half of R-02 is untested (U-06). |
| #12 `f07-personal-keys` (0.9.0) | **Approve with changes** | The per-person key path held up under everything I tried: keys never reached another person's call, the log or the disk. The `iss` skip for hand-entered OAuth settings reopens the mix-up attack against authorization servers that send `iss`; I confirmed it with a test (U-01). The fix is small. |

Counts: 0 Critical, 0 High, 2 Medium, 2 Low, 4 Info.

## Findings

### U-01 Medium: the mix-up defence is switched off for a server whose OAuth settings were entered by hand without an issuer, even when the answer carries another authorization server's `iss`

- Where: `internal/web/connect.go:372` (`if st.Issuer != "" && (...)`), together with `internal/control/upstream_oauth.go:180` `manualOAuth` (issuer optional).
- What is wrong: all servers share one redirect URI (`/connect/callback`), and the state picks the server. For a hand-entered server, `st.Issuer` is empty, so the whole RFC 9207 check is skipped. A callback that carries `iss=<some other authorization server>` is accepted, and its code is exchanged at the hand-entered token endpoint along with the PKCE verifier. Before 0.9.0, every server had an issuer, so an `iss` that did not match was always refused.
- Scenario: an admin adds an in-house server with hand-entered endpoints whose authorization server is malicious or compromised. The admin could also be acting in bad faith: a Privo admin should not be able to get members' tokens for other services. Ada starts "connect" for that server. The evil `/authorize` sends her browser on to an honest authorization server that sends `iss` (for example the one of `linear`), using Privo's client id there (public: CIMD URL or visible in any authorize URL), the same `state` and Privo's own `code_challenge`. Ada approves Privo at the honest server, and it redirects to Privo's callback with `code`, `state` and `iss=<honest>`. Privo resolves the state to the hand-entered server, skips the `iss` check, and POSTs the honest code and `code_verifier` to the evil token endpoint. The attacker redeems them at the honest server as Privo's public client and gets Ada's tokens.
- How established: **ran it.** `$WORK/repo/internal/web/zz_review_mixup_test.go` `TestReviewMixUpWithHandEnteredEndpoints` reproduces the steps above. The callback answered 303 "connected", the evil token endpoint received the code and the verifier, and redeeming them at the fake honest AS returned `AT-ada-…`. With the check changed to `if (hasIss && …) || (…)` (that is, an `iss` is refused when none is expected), the same test is stopped at the callback (400).
- Fix: when no issuer is known, refuse any callback that carries `iss` (one line, confirmed above). Better, also require `oauth.issuer` for hand-entered settings, or give each server its own redirect URI (`/connect/callback/<server id>`). The redirect URI is the RFC 9700 alternative defence, and it also closes the older gap for authorization servers that never send `iss` (see out of scope). Add a test of the connect flow with hand-entered settings: none exists today (U-06).

### U-02 Medium: the tool review (F14 R-01 fix) still hides part of what is accepted: invisible and bidirectional characters

- Where: `web/src/review.tsx:55-61` (description as text), `:80` (full definition as `JSON.stringify` output).
- What is wrong: the accepted hash covers every code point. The review renders the description and the pretty JSON as plain text. `JSON.stringify` does not escape Unicode tag characters (U+E0000 to U+E007F), zero-width characters (U+200B to U+200D, U+2060) or bidi overrides (U+202A to U+202E, U+2066 to U+2069). The browser draws the first two as nothing, and the bidi characters reorder what the admin sees. A tool can carry a complete hidden instruction ("ASCII smuggling" with tag characters) that a model reads and the admin cannot see. For a changed tool, the diff marks a line as changed with no visible difference. For a new tool, nothing shows at all. The server applies no such filter either (grep: only `githubauth`, `teams` and the key help text strip `Cf`).
- How established: **ran** `node -e` on `JSON.stringify({description:"Reads a file.​\u{E0049}…"})`: the output keeps the characters unescaped. **Read** the rendering path; `web/src/source.test.ts` bans `innerHTML`, so this is not XSS, only hiding.
- Fix: in `Definition`, render every code point in Cc, Cf, Co, Zl and Zp, and the tag block, as a visible escape (for example `⟦U+E0049⟧`), and put a warning badge on any definition that contains one. Consider refusing or flagging such definitions server-side when they are recorded (`upstream.go:606`). Add a Vitest case with a tag-character payload.

### U-03 Low: a refused catalog key takes a key server down for every member, and nobody takes over

- Where: `internal/grants/grants.go:328-331` (a refused key is kept, `ErrKeyRefused`), `internal/upstream/upstream.go:369` (`catalogTransport`), `internal/store/upstream_oauth.go:398` `promoteCatalog` (runs only when a grant is deleted), `internal/upstream/upstream.go:260` `Items` (tools only from servers that are up).
- What is wrong: by design, the first owner's or admin's key lists the tools. When the upstream refuses that key (rotated, revoked, the admin left the upstream org), it is kept (ADR 0023 point 5). So it stays the catalog grant, and another admin's key is never promoted. With OAuth, a refused refresh deletes the grant and promotes the next admin. At the next reconnect the server goes down and its tools vanish for everyone, although each member's own key works.
- How established: **ran it** on the real binary: `$WORK/mut/test/acceptance/zz_review_catalogkey_test.go` `TestReviewRefusedCatalogKeyTakesTheServerDownForEveryone`. Boss and Ada both have working keys. The boss's key is revoked upstream and the server is turned off and on. Result: `status="down"`, and Ada no longer sees `acme__whoami`.
- Fix: when the catalog grant's key is refused, move the catalog flag to another eligible admin's key (do not delete the refused key), or try the next eligible key in `CatalogAccessToken`. Say which admin's key was refused in the server status.

### U-04 Low: a key the upstream refuses with 403 gets a generic failure, not "refused your API key" with the link; the PR says 401/403

- Where: `internal/upstream/perperson.go:341` (only `401` goes to refresh and `ErrKeyRefused`).
- What is wrong: many API-key gateways answer 403 to a missing or invalid key (AWS API Gateway's `x-api-key` is the common in-house case). The person then gets "call failed: the upstream server "acme" did not answer or failed; an administrator can see details…" and no way to fix it themselves. The ADR says 401; the PR description says "(401/403)".
- How established: **ran it** on the real binary (`TestReviewForbiddenKeyIsNotMapped` in the same file): exactly that message. The upstream's body was not echoed, which is good.
- Fix: for `AuthKey` servers, treat 403 like 401 (a key cannot be refreshed, so map it to `ErrKeyRefused` without a retry). Or make the PR and docs match the code.

### U-05 Info: the admin can put a member-controlled value into identity, forwarding or content headers

- Where: `internal/control/upstream_oauth.go:55` `check`, `internal/control/control.go:871` `reservedHeaders`.
- What is wrong: header injection is not possible: names follow the token grammar, and prefix and key are printable ASCII with no CR/LF (also checked: `Host`, `Proxy-*`, `Mcp-*`, `Sec-*`, `Cookie`, hop-by-hop all refused; no proxy is configured on the egress client). But each member types the value. If the admin picks `X-Forwarded-User`, `Remote-User`, `X-Auth-Request-Email`, `Forwarded` or `X-Forwarded-For` for an in-house server behind an auth proxy, the "key" becomes an identity claim the member chooses. `Accept-Encoding` and `Content-Encoding` break the transport.
- How established: read it.
- Fix: also refuse `Forwarded`, `X-Forwarded-*`, `X-Real-Ip`, `Remote-User`, `Accept-Encoding` and `Content-Encoding`, or warn in the form that the value is whatever the person types.

### U-06 Info: test gaps behind claims in the PRs and `docs/todo.md`

- `Disconnect` re-reading the server (PR #11: "before every refresh **and revocation**"): removing `if cur, err := s.current(...)` from `grants.go:390-392` leaves `TestRefreshUsesTheClientStoredNow` and every grants test green. **Ran it** in `$WORK/mut`. The test calls `Disconnect` but never checks which client id revoked.
- The connect flow with hand-entered settings (callback, `iss` skip, exchange without `resource`) has no test at any level; `TestOAuthSettingsEnteredByHand` only covers creation. This is where U-01 is.
- The `oauth.issuer` path for a server without resource metadata (`upstream_oauth.go:126`) has no control-level test; only `upstreamauth` covers `ServerMetadataFor`.
- "Hand-entered endpoints pass the egress policy": the only negative case is a private *authorization* endpoint. No case covers the token or revocation endpoint (they are still protected at dial time by the egress client, `cmd/privo/serve.go:133`).
- The PR says acceptance covers "member removal" for keys. `TestPersonalKeysEachPersonUsesTheirOwn` covers the person removing their own key. Member removal deletes keys through the generic `deleteUserGrants`, which I read and which is sound.

### U-07 Info: nothing checks a grant's purpose against the server's way of signing in when the grant is used

- Where: `internal/grants/grants.go:242` `usable`, `:385` `Disconnect`.
- What is wrong: today only the entry points (`SaveKey` checks `AuthKey`, the callback checks `AuthOAuth`) keep a key off an OAuth server. That holds because a server's auth cannot be edited (`patchServer` takes only `enabled` and `teams`). F29 lists "editing an existing server's way of signing in" as next. Once it exists, a leftover key grant on a server switched to OAuth would be sent as `Authorization: Bearer <key>`, and `Disconnect` would POST the key to the revocation endpoint (`t = tok.AccessToken`).
- Fix: in `usable` and `Disconnect`, refuse a grant whose `Credential.Purpose` does not match `srv.Config.Auth`. Delete all grants when the auth mode changes.

### U-08 Info: hand-entered and issuer-named OAuth relax ADR 0017's binding between server and authorization server

- `configureOAuth` takes the manual path whenever either endpoint is given (`upstream_oauth.go:~119`), even for a server that publishes protected resource metadata. That skips `resourceCovers` and the check that the server names that authorization server. The ADR says this path is for servers whose authorization server "publishes no metadata either".
- In the issuer path and the manual path, tokens carry no `resource` unless the admin sets one, so they are not audience-bound to this server. This is the ADR's documented choice. I list it because a token issued that way may also be accepted by other resource servers of the same authorization server.
- Fix (optional): take the manual path only after discovery returned `ErrNoResourceMetadata` and the issuer (if given) publishes no metadata.

## Out of scope (pre-existing, outside the diff)

- **Mix-up against authorization servers that never send `iss`** (GitHub's OAuth among them) was already possible through any server whose (malicious) metadata does not promise `iss`. The cause is the single shared redirect URI. Per-server redirect URIs (U-01 fix) close it.
- Old accepted versions of a tool stay `accepted` (`store/pins.go:48`). A server can switch back to any earlier accepted definition without review, and `previousOf` diffs only against the most recently accepted one.
- Calls on per-person servers use the definitions reviewed on the catalog session (the first admin's account). The person's own session is never checked against them (ADR 0017 design).

## Checked and found sound

- **One person's key on another's call:** the pool is keyed `{serverID, userID}`, and the in-flight `connecting` map uses the same key. `connectPerson` captures `userID`. The key is read fresh per request (`bearer.token`), so a replaced key takes effect at once. `PersonSession` checks the grant on every call. The gateway picks the member by the token's user id (`grantOwner`). The catalog session (admin's key) is used only for listing: `Upstream.Session` is called in one place, behind `PerPerson`. Refresh flights are keyed org+server+user. The acceptance test proves two people get their own account, and my two extra acceptance runs never saw a cross-over.
- **Key versus OAuth grant:** the purpose is now read from the row and validated in `scanGrant`. It is part of the vault AAD (`grants.binding`), so a swapped purpose fails to open. `SaveGrant` takes only `oauth`/`key`. `UpdateGrantToken` takes only `oauth`, so a key is never "refreshed". `connectKey` refuses OAuth servers, and `connectStart` and the callback refuse key servers (`TestKeyAndSignInPathsDoNotCross`).
- **Header injection:** see U-05. `ValidKey` (0x21 to 0x7E, at most 4096) after `TrimSpace`. The prefix is printable and at most 32. A mutation removing the `Cookie` refusal is caught by `TestAddingAServerThatTakesEachPersonsKey` (ran it).
- **The key page:** `sameOrigin`, then `MaxBytesReader` (8 KiB), then session, then constant-time CSRF from the body only. The key is read from `PostForm` only, so a key in the URL is ignored. The team check is `mayConnect` (members only for servers their teams reach). Approvers are refused. `no-store`, `X-Frame-Options: DENY`. The key is never in a template, error page, audit row (`upstream.connect:<alias>`) or log line (`web: save key` logs only the error, which holds no key). Help text is html/template-escaped and the help URL is https only.
- **Removal:** self-disconnect, admin `DELETE …/connections/{person}`, member removal (`DisableUser`, then `deleteUserGrants`) and server removal (`DeleteServer` deletes by `server_id`) all delete the credential row, and pooled sessions are closed. A key server's `Disconnect` does not try to revoke (`Endpoint` refuses non-OAuth).
- **Egress for hand-entered endpoints:** checked with `CheckURL` at creation. At request time the OAuth client is the egress client: dial-time IP check (rebinding-safe), `Proxy: nil`, same-host redirects only, at most 1 (`serve.go:133`). Per-person upstream requests are origin-locked (`originRT`) before the key is added.
- **R-02 (`current()`):** refresh and revoke read the server from the store, scoped to the org. A deleted server gives `ErrNotConnected`. No other path uses a stale client id. The residual (grants issued to the old client fail and are deleted after re-registration) is documented.
- **R-01 rendering:** text nodes only, no HTML sinks. `web/dist` is rebuilt from source: a fresh `vite build` gives identical hashed names (`index-C50-Luk1.js`, `index-C9j_4J7V.css`).
- **Empty `resource`:** not sent in authorize, exchange or refresh (`TestEmptyResourceIsNotSent`).
- **Admin API:** `personal_key` only through `POST /api/v1/servers` (`PermManageServers`). `key_header` is shown to admins only, and a member's view lacks it (test).
- **Upgrade claim:** an older binary forces purpose `oauth` when opening the row, so the AAD fails and a key cannot be opened. Its `secrets()` skips purpose `key`. So no key goes anywhere.

## What I ran

| Command (in `$WORK/repo` unless noted) | Result |
|---|---|
| `go test -race -count=1 ./...` | all packages ok (store 218 s, web 202 s) |
| `go test -race -count=1 -tags acceptance -timeout 10m ./test/acceptance/` | ok (108 s) |
| `web/`: `npx vitest run`; `npx tsc --noEmit`; `npx vite build --outDir $WORK/distcheck` | 45/45; clean; identical to committed `web/dist` |
| `go test -run TestReviewMixUpWithHandEnteredEndpoints -v ./internal/web/` | attack succeeds (U-01); with the one-line fix it is refused (400) |
| `$WORK/mut`: `-tags acceptance -run TestReviewRefusedCatalogKey…` | server down, tool gone for Ada (U-03) |
| `$WORK/mut`: `-tags acceptance -run TestReviewForbiddenKeyIsNotMapped` | generic error, no link (U-04) |
| Mutation: drop `current()` in `Disconnect` | grants tests still pass (U-06) |
| Mutation: drop the `Cookie` refusal | `internal/control` test fails (good) |
| Mutation: refuse `iss` when no issuer is known | web, control and upstreamauth tests all pass, so the hand-entered callback is untested either way |
| `node -e` JSON.stringify on tag and zero-width characters | kept unescaped (U-02) |

## Where the PR descriptions are wrong or incomplete

- #12: "When the upstream refuses the key **(401/403)**". Only 401 is handled (U-04). The quoted message ("your key for <alias> was refused, enter it again at …") is not the text the code sends ("<alias> refused your API key. Enter a new one: open … ").
- #12: "The RFC 9207 `iss` check is skipped only when no issuer is known at all" is presented as safe. It reopens mix-up for authorization servers that do send `iss` (U-01). The ADR's "the state still binds the answer to this server" is the property mix-up exploits.
- #12: "Acceptance … member removal". The test covers a person removing their own key, not member removal (U-06).
- #12: "never passed to hooks". No hook mechanism exists yet (`internal/pipeline/` is planned), so this says nothing.
- #12: "a key can never be read as an OAuth grant or the other way round". True at the entry points and through the AAD, but the use sites do not check (U-07).
- #11: "reads the server's current settings before every refresh **and revocation**". Code yes, test no (U-06). "Shows everything you accept" misses invisible characters (U-02).
- Both omit: the catalog behaviour of a refused key (U-03).

## Not covered / limits

- Postgres store (not built). SSE-transport key servers were read, not run.
- I did not test the issuer-named path (`ServerMetadataFor`) against a live fake beyond reading it and the `upstreamauth` unit test.
- I did not run `go vet` (owner's preference) or golangci-lint.
- U-02 is established by the JSON behaviour and by reading the component. I did not render the page in a browser.
- My tests live only in the work copies listed at the top. Nothing was written to the author's clone or to GitHub.

