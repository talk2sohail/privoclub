# Independent review: sign-in state cookie (#8), refused-sign-in audit limit (#9), OAuth authorization server (#10)

- Date: 2026-11-15
- Reviewer: independent review agent
- Commits reviewed: `main` e6ff9f49bb284a4af07acb20a3c17232bb86cbd4; `f06-signin-state-cookie` e94745dfff22190eaec9346216ca9fee75d4c5b8 (#8, contains #7); `f05-denied-audit` f1abc64467882be49883ad81b96b5bba71b4705c (#9); `f06-oauth-server` 1ec290a2ce34535df24bd28a24f6b496bce4e152 (#10). Changes under review: `git diff main..f06-oauth-server`.
- Scope: ADR 0016 (with amendment 1), 0022, 0014, 0003, 0008; `docs/todo.md` F05 and F06; the code listed in the brief, plus the library `authelia.com/provider/oauth2` v0.3.3 where Privo depends on its behaviour.
- Method: read the code and the library paths it relies on; ran the unit, race and acceptance suites; wrote throwaway tests in a private copy (unit tests in `internal/oauthserver`, one acceptance test against the real binary); ran 24 single mutations. Nothing was written to the author's clone or to GitHub.

## Verdict per pull request

| PR | Verdict | Why |
|----|---------|-----|
| #7 (docs: library and no-grace decisions) | Approve | Documents only; matches what was built. |
| #8 (`f06-signin-state-cookie`) | Approve | The sealed cookie holds up: bound to state, purpose, org and key version; tampering, swapping, expiry and replay are refused. Only Info notes. |
| #9 (`f05-denied-audit`) | Approve with changes | It bounds the audit as claimed, but the shared budget can hide a disabled member's refusal, and the "left out" count goes only to the log, and only when a later refusal arrives (S-06). |
| #10 (`f06-oauth-server`) | Approve with changes | No Critical or High. The core (redirect rule, PKCE, code and refresh single use, revocation, member binding, admin API scoping) held under attack. Fix S-02 and S-04 before switching the server on; fix S-01 before anyone sets `PRIVO_OAUTH_DYNAMIC_REGISTRATION=on`. |

## Findings

Counts: Critical 0, High 0, Medium 1, Low 5, Info 5.

### S-01 Medium: dynamic registration limits are keyed on `r.RemoteAddr`; behind the documented proxy (or with IPv6) anyone can lock all self-registering clients out

- Where: `internal/oauthserver/server.go:620-627` (address from `r.RemoteAddr`), `:571-590` (`allowRegistration`, map reset when it passes 4096 addresses), `:643-647` and `internal/store/oauth_server.go:400-407` (200 in total).
- What is wrong: Privo has no trusted-proxy handling anywhere (no `X-Forwarded-For` handling in the tree). On the documented deployment (Cloudflare to Traefik on Coolify, `docs/deploy-coolify.md`), every request reaches Privo from Traefik's address, so "ten per address per hour" is ten per hour for the whole internet. Without a proxy, the per-address key is a full IPv6 address, and one /64 has more than enough. A registration nobody allows stays for an hour, so the 200 total is easy to keep full. This is the same class as F05 R-01 (an unauthenticated write that fills a shared cap), which ADR 0022 says the design must not add.
- Scenario: with DCR on, a script posts ten registrations at the top of each hour (behind the proxy), or 200 from rotating IPv6 addresses. Every Cursor, Gemini CLI or Mistral user then gets `429 too_many_requests` for as long as the script runs.
- How established: ran it. Unit test: 10 from one address, then 429; 190 more from rotating `2001:db8::/64` addresses, then a new address gets 429. Real binary (acceptance harness, all requests from 127.0.0.1, as behind a proxy): the 11th registration gets 429.
- Fix: do not ship DCR with a global cap that unauthenticated callers can fill. Options: key the limit on a trusted client address (configured proxy header, and a /64 for IPv6); do not store a registration until a signed-in member allows it, for example by putting the registration in a signed `client_id` (stateless registration, so nothing is written before consent, in line with ADR 0022 point 6); or count only registrations that someone allowed against the 200. Until then, say in the deploy guide that DCR can be blocked by anyone.

### S-02 Low: `grant_type=authorization_code ` (with a space) skips Privo's resource rule and the `oauth.token.exchange` audit row

- Where: `internal/oauthserver/server.go:433-450`.
- What is wrong: Privo compares `r.PostForm.Get("grant_type")` to the exact string `"authorization_code"`. The library splits the same value on spaces and drops empty parts (`access_request_handler.go:77`, `RemoveEmpty(strings.Split(...))`), so `"authorization_code "` or `" authorization_code"` is still a code exchange to it. Both of Privo's own checks keyed on the string are then skipped: "`resource` must be present on the token request" (ADR 0022 point 9, the second check that point 16 says is Privo's own) and the audit row for the exchange.
- Scenario: a client exchanges a code with `grant_type=authorization_code%20` and no `resource`. It gets working tokens, and the audit for that grant shows `oauth.grant.allow` but no exchange. The token is still bound to the right resource (granted at Allow), so this is a rule and audit gap, not access beyond the grant.
- How established: ran it. Unit test: 200 with access and refresh tokens, audit `[oauth.grant.allow]`. Real binary: 200, the access token works on `/mcp` (200), admin audit has `oauth.grant.allow` and `oauth.client.register` only.
- Fix: decide on the library's parse, not the raw string: refuse any `grant_type` that is not exactly `authorization_code` or `refresh_token` before the library sees it, and audit after the response from `ar.GetGrantTypes().ExactOne("authorization_code")`.

### S-03 Low: an already-used code ends the grant for anyone who presents it, without the verifier and after the code has expired, until the next prune

- Where: `internal/oauthserver/storage.go:245-255` (an inactive code is returned as reuse regardless of its `expires_at`); the library's `flow_generic_code_token.go` revokes in `HandleTokenEndpointRequest` before the PKCE handler runs; prune runs every 10 minutes (`cmd/privo/serve.go:195`).
- What is wrong: the reuse path needs only the code and the public `client_id`. It does not need the PKCE verifier or `redirect_uri`, and it does not look at the code's time. The used code row lives until the next `PruneOAuth`, up to about 11 minutes.
- Scenario: a code leaks after use (browser history, a proxy or server log of the client's callback URL, a screen share). Anyone replaying it within those minutes ends the member's new grant, and the client has to sign in again. Denial of service only, no access.
- How established: ran it. Unit test: code exchanged, clock advanced 9 minutes, code posted again with no verifier and no redirect URI: `invalid_grant`, and the first access token stops verifying; audit `oauth.grant.reuse`.
- Fix: treat an inactive code past its `expires_at` (plus a few seconds) as not found in `GetAuthorizeCodeSession`. RFC 6749 4.1.2 asks for revocation on reuse, not for a window of minutes.

### S-04 Low: a self-registered client stays accepted after dynamic registration is switched off, even under `PRIVO_OAUTH_CLIENTS=approved`

- Where: `internal/oauthserver/clients.go:195-201` (`kindOf`: any row is "accepted under every policy"), against `internal/config/web.go` ("OAuthClientsApproved accepts only what an administrator approved") and ADR 0022 points 12 and 13.
- What is wrong: switching DCR off removes `/oauth/register` but not the registrations already made. Each stays usable for an hour if nobody allowed it, or for 90 days after its last use. The strictest policy does not stop them either.
- Scenario: an administrator turns DCR on for Cursor, sees abuse (for example a registration named "Claude Code" with an attacker's redirect address), switches DCR off and sets the policy to `approved`, and believes no unapproved client can now reach the consent page. The registered client still can, and members see "registered itself" only as a small warning.
- How established: ran it (unit test: registered with DCR on, server rebuilt with DCR off and policy `approved`; `ParseAuthorize` accepts the registered client).
- Fix: accept `registered` rows only while `Registration` is on, and never under `approved`. Or make the documented behaviour say so and offer "remove all registered clients" on the Clients page.

### S-05 Low: client metadata documents are fetched with the operator's private-range allow list; under `PRIVO_OAUTH_CLIENTS=any` that is an unauthenticated SSRF probe

- Where: `cmd/privo/serve.go:176` (`NewDocumentClient(policy)`, the same `egress.Policy{Allow: PRIVO_EGRESS_ALLOW_CIDRS}` the upstream connections use); `internal/oauthserver/clients.go:296-343` (distinct reasons: "could not be fetched", "answered %d", "is not JSON", "names another client id"); no rate limit on fetches, and the cache is wiped at 512 entries (`:277-279`).
- What is wrong: ADR 0022 point 11 and the F06 threat note say documents are fetched from public addresses only. An operator who allows a private range for internal MCP servers also allows it for document fetches. Under policy `any`, `GET /oauth/authorize?client_id=https://10.0.0.5:8443/x` needs no session and returns a page whose wording tells whether the host answered, its status code, and whether the body was JSON. Distinct ids also defeat the one-minute failure cache, so Privo can be made to send an unbounded number of outbound GETs.
- How established: read it (the wiring and the egress check). Not run against a private range.
- Fix: build the document client with an empty allow list (`egress.Policy{}`), give the page one reason for every fetch failure, and limit fetches of unknown documents per minute.

### S-06 Low (#9): the refused-sign-in budget can hide a disabled member's refusal, and the "left out" count never reaches the audit

- Where: `internal/web/signinstate.go:186-200` (`denials.record`), `internal/web/signin.go:180-193` (`denied`, used for strangers and disabled members alike).
- What is wrong: (a) The 60-row budget is shared by every refused account, including a disabled or removed member trying to come back, who is the refusal an administrator most wants to see. Sixty throwaway GitHub accounts refused at the start of a window keep every later refusal in that window out of the audit. (b) What was left out is counted, but it is written only to the process log, and only when a refusal arrives after the window has ended. If none arrives, the count is never written. A restart loses it.
- How established: read it; the PR's own test (`TestRefusedSignInsCannotFloodTheAudit`) shows the window behaviour. Mutation M8 (no global cap) is caught by that test.
- Fix: never limit refusals of a known (disabled) member: they are bounded by the number of members. Write the skipped count as one audit row (for example `signin.denied.summary`) when a window closes, from a timer or at the next audit write, not only at the next refusal.

### S-07 Info: the authorization request cookie can be replaced by any site; with hosted multi-tenant clients the consent page cannot show the difference

- Where: `internal/web/oauth.go:106-122` (the `authorize` seal is bound to a random id carried in the same cookie, so to nothing in particular); `internal/oauthserver/clients.go:68-77` (known hosted clients `claude.ai`, `chatgpt.com`).
- What happens: while a person is signing in at GitHub, any page they open can navigate the top window to `/oauth/authorize?...` and replace `__Host-privo_authz`. After sign-in they get a consent page for the replacement. With a known hosted client the attacker starts a real connector flow from their own claude.ai account. The page then reads "Claude (claude.ai)", the same as the person's own flow, and Allow connects the attacker's claude.ai account to the person's Privo. The same consent phishing works without the cookie by sending a signed-in member the link; the cookie only makes it look like the person's own flow. This comes with known multi-tenant CIMD clients and is not specific to this code.
- Suggestion: on the consent page for hosted clients, say "Allow only if you started this just now from your own account at <host>". Consider binding the authz cookie to the sign-in cookie of the same flow.

### S-08 Info: `response_mode=jwt` (and `query.jwt`, `form_post.jwt`) gives a 500 at `/oauth/authorize` without a session; `form_post` errors become an auto-submit page

- Where: `internal/oauthserver/server.go:351-363`. JARM is not configured, so the library's error writer fails. The result is a 500 `server_error` JSON body on a browser endpoint (confirmed on the real binary). For `response_mode=form_post` the library writes an HTML form that posts the error to the client. Privo's CSP blocks it (no script, `form-action 'self'`), so it is harmless.
- Fix: refuse any `response_mode` other than absent or `query` as a page, before the library sees the request.

### S-09 Info: the consent page's CSP is built from the redirect host without checking its characters

- Where: `internal/web/oauth.go:152`, `internal/oauthserver/server.go:293-299`; `regexp.ReplaceAllString` also expands `$` in the replacement.
- What happens: Go's `url.Parse` accepts `;`, `$` and `'` in a host (`https://evil.com;sandbox/cb` parses), and a registered client (DCR on) or a document client (policy `any`) chooses its own redirect addresses. Whitespace is refused, so only directives without a value can be added. I found no way to weaken the page (no `frame-ancestors` value can be injected; `X-Frame-Options: DENY` is also set). No impact found.
- Fix: in `checkRedirects`, accept only hosts that are a valid DNS name or IP literal. Use `ReplaceAllLiteralString`.

### S-10 Info: checks that no test notices when removed (mutation results)

- `VerifyAccessToken`: the session subject equals the grant's member (`server.go:534`, M1), and the granted resource (`server.go:534`, M6). Both are defence in depth (Privo writes both values), but nothing would notice a regression.
- `documentURL` refusing userinfo in a client id (`clients.go:175`, M13).
- The role check on the consent POST (`internal/web/oauth.go:182`, M18). The store's `CreateClientGrant` refuses anyway, so it is defence in depth.
- Suggest one test each.

### S-11 Info: the memory of finished sign-ins can be flushed by an anonymous script

- Where: `internal/web/signinstate.go:139-169`. An entry is added after the cookie opens and before the `error` parameter is checked. A script can start a sign-in and call back with `error=x` 8192 times in ten minutes, which evicts every older entry. A replay still needs the victim's HttpOnly cookie, and GitHub refuses a used code, so this only widens the corner that ADR 0016 amendment 1 point 5 already accepts. Noted so the owner weighs that point with it.

## Checked and found sound

- **Redirect rule** (`MatchRedirect`, `checkRedirects`) and the library behind it. Tried: other loopback host (`127.0.0.1` for a `localhost` client), path case, trailing slash, percent-encoded path, query change, fragment, userinfo, `LOCALHOST`, IPv6 zone (`[::1%25eth0]`), `0x7f000001`, `127.1`, empty port (accepted, same host, harmless), upper-case scheme (accepted, normalised to the same address). Every bypass attempt got a page and no redirect. The library is handed only the one address Privo accepted, and the token endpoint compares `redirect_uri` with the stored authorization form.
- **Parameter pollution:** a parameter given twice is refused before the library. A missing or short `state`, no or `plain` challenge, `response_type=code token`, an unknown scope, `audience`, and a `resource` with a trailing slash are each refused with a redirect to the accepted address only.
- **PKCE:** S256 only (Privo's check plus `EnablePKCEPlainChallengeMethod: false`); a missing PKCE row with `EnforcePKCE` fails closed (library `pkce/handler.go:145-152`).
- **Code single use under concurrency:** 8 simultaneous exchanges of one code, 5 rounds: exactly one 200, seven `invalid_grant`, and the grant is ended (the strict reading of ADR 0022 point 8). No 500s. SQLite's single writer serialises the compare-and-set in `DeactivateOAuthToken`.
- **Refresh rotation under concurrency:** 6 simultaneous refreshes with one token, 5 rounds: one 200, five `invalid_grant`, grant ended. This is the price ADR 0022 records under Consequences ("a client refreshes twice at once"), and it behaves as written.
- **Revocation reaches `/mcp`:** refresh deletes the grant's earlier access tokens; reuse, client revocation, member revocation, admin revocation, client removal, member disable and a role change to a non-gateway role all delete the grant's rows in one transaction. `VerifyAccessToken` reads the member and role on every uncached check, and the cache is 2 s (`internal/auth`). Mutations that remove the role check (M16), the store's ending of grants on a role change or disable (M23, M24), the ending of an approved client's grants on removal (M25), and the expiry check (M15) are each caught by a test. The acceptance test shows a reused refresh token and a member's revocation stopping `/mcp`.
- **Cross-member and cross-surface:** an access token is accepted only by `VerifyGateway`, used only by `MCPVerifier`. `Verify`, which the admin API uses, rejects it at `token.Parse` (checksum) before the cache, so a cached OAuth principal cannot be read back as a personal token. The admin API also refuses non-control kinds. Confirmed on the real binary (`/api/v1/tokens` with an access token gives 401). Token kinds are stored separately, so a refresh token or code with its prefix changed to `privo_at_` is not found.
- **Token storage:** rows hold the HMAC signature only. The stored request JSON of code, access and refresh rows contains no code, verifier, access or refresh token value (checked by searching the rows after a full flow and a refresh). The HMAC secret is 32 random bytes sealed with the org DEK. Token entropy is the library default of 32 bytes. The acceptance test also greps disk and logs.
- **Consent CSRF and clickjacking:** Allow and Deny need the session's CSRF token, the Origin check, and a sealed request bound to the session id; the request is parsed and checked again on POST. The consent response carries `frame-ancestors 'none'` and `X-Frame-Options: DENY` (seen on the real binary). Templates escape client names, and `cleanName` strips markup.
- **Sealed cookies (#8 and the authz/consent seals):** AES-GCM with org, purpose, bind and key version in the derivation and associated data. A sign-in cookie does not open for another state, a changed character, another purpose (`consent` and `authorize` cannot be swapped), or after expiry. Length is bounded before decoding. The callback leaves the cookie alone when it does not open (F05 R-03). Mutations removing expiry (M9, M10), replay memory (M21), clearing (M22) and session binding (M11, both sides) are caught.
- **CIMD document checks:** `client_id` must equal the URL (M5 caught); https only, no userinfo, fragment or dot segments; no redirect followed; 5 s; 64 KiB plus one byte; JSON content type; public clients only; cache from 5 minutes to 24 hours. The egress dialer checks the connected IP in `Control`, so DNS rebinding does not get past it. With the default `known` policy an anonymous request can make Privo fetch only the eight known documents and approved ones.
- **Admin API (`internal/control/oauth_clients.go`):** member routes need `gateway.use` and are filtered by `p.UserID` in SQL (another member's grant is a 404). Administrator routes need `tokens.manage`. Every store call takes `p.OrgID`, and every new table has `org_id` in its primary key.
- **Migration 00008:** runs on an existing database in every store test. It clears `oauth_states` before adding the unique index, so duplicates cannot make it fail. The Down section restores the columns. The deploy guide's warning not to go back to 0.7.0 is correct (0.7.0 writes the dropped columns).
- **Error messages:** token endpoint errors are the library's generic RFC texts (`SendDebugMessagesToClients: false`). Fetch failures do not name addresses (but see S-05 for the oracle).
- **Off switch:** with `PRIVO_OAUTH_SERVER` unset, the metadata names no server, `/oauth/*` answers 503, and a `privo_at_`-shaped bearer gets 401 (acceptance `TestOAuthServerIsOffByDefault`).

## What I ran

- `go test -race -count=1 ./...` on `f06-oauth-server`: all packages ok (EXIT 0).
- `go test -race -count=1 -tags acceptance -timeout 10m ./test/acceptance/`: ok (88 s).
- `web/`: `npx tsc --noEmit` ok; `npx vitest run` 38/38.
- Throwaway unit tests in my copy, `internal/oauthserver/zz_review_test.go`: whitespace grant type (S-02); concurrent code exchange and refresh (5 rounds each); used-code replay after 9 minutes (S-03); stored-request contents; registration lockout (S-01); 20 authorization request variants; response modes (S-08); registered client after DCR off (S-04).
- Throwaway acceptance test against the built binary, `test/acceptance/zz_review_test.go`: consent headers, whitespace grant type on `/mcp`, registration from one address, token-endpoint error bodies, `response_mode=jwt`.
- Mutation run (`$WORK/scripts/mutate.py`, one change at a time, files restored between runs; non-race targeted packages): caught: M2 (duplicate parameters allowed), M3 and M4 (loopback host or query not compared), M5 (document id need not equal its URL), M7 (token-endpoint resource rule removed), M8 (no global denial cap), M9 (sign-in cookie without expiry), M10 (consent without expiry), M11 (consent seal not bound to the session, both seal and open changed), M12 (authorize resource check removed), M15 (OAuth expiry not checked in the authenticator), M16 (role not checked at verification), M17 (role condition removed from `CreateClientGrant`), M19 (http redirects anywhere), M21 (replay memory off), M22 (authz cookie not cleared), M23 and M24 (role change or disable does not end grants), M25 (removing an approved client keeps its grants), M26 (no per-address registration limit). Not caught: M1, M6, M13, M18 (S-10).
- Library source read: `handler/oauth2/flow_refresh.go`, `flow_generic_code_token.go`, `flow_authorize_code_token.go` (`revokeCodeGrant`), `revocation.go`, `strategy_hmac.go`, `handler/pkce/handler.go`, `access_request_handler.go`, `config_default.go`.

## Where the PR descriptions are wrong or incomplete

- #10 "Every allow, deny, code exchange, revocation and reuse is recorded": not for an exchange with a padded `grant_type` (S-02). Refreshes are not recorded at all, which is consistent with the ADR but not with "every".
- #10 "Self-registration ... limited per address and in total": behind the documented proxy the per-address limit is one shared limit, and both limits can be filled by anyone (S-01).
- #10 and the F06 threat note, "Metadata is fetched only from public addresses": not when `PRIVO_EGRESS_ALLOW_CIDRS` is set (S-05).
- #10 and `internal/config/web.go`, "approved accepts only what an administrator approved": registered clients are accepted too (S-04).
- #10 "a code used twice ends it too": true, but leaves out that anyone holding the used code can do it, without the verifier, for up to about 11 minutes (S-03).
- #9 "the count is logged when the next window starts": it is logged when the first refusal after the window arrives, and never if none does; it does not reach the audit (S-06).
- #10 "33 breakages, 31 caught": I found four more checks no test notices (S-10).
- #8: accurate. The "works once" weakening is stated honestly; S-11 adds that an anonymous script can empty the memory.

## Not covered / limits

- No real metadata document client (claude.ai and the others) was exercised end to end; the known-list URLs were not fetched (no network calls to vendors were needed for the findings).
- Not tested behind a real Traefik or Cloudflare proxy. S-01's proxy case is argued from the absence of proxy-header handling and shown with all requests from one address.
- S-05 was not run against a private range.
- Postgres is not built; the transaction and concurrency results hold for SQLite's single writer only and must be rechecked for Postgres (row locks or serializable isolation for `DeactivateOAuthToken` and `RotateRefreshToken`).
- Admin app reviewed for output escaping and API use only, not for UX.
- `go vet` not run (owner's preference, as briefed).
