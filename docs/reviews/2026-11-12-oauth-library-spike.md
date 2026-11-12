# Which library for the gateway's OAuth server (F06 task 1)

Date: 2026-11-12. Asked by ADR 0022 point 16 and ADR 0003 section 2 ("built on
a maintained library, chosen by a spike, not hand-rolled").

## Result

**Neither of the two libraries the decision records name passes as named.**
`ory/fosite` is no longer released; `zitadel/oidc` is an OpenID Connect
provider and brings things Privo would have to work against.

**A maintained hard fork of fosite passes every check:**
`authelia.com/provider/oauth2` v0.3.3 (2026-09-19, Apache-2.0, the library
behind Authelia). Nine checks ran green against it in a working program, with
two rules of ADR 0022 implemented by Privo in front of the library, as listed
below.

ADR 0022 says that if neither named candidate passes, the work stops and the
owner decides. **The recommendation is to adopt the fork, pinned**, and the
decision is the owner's; the options are at the end.

## What was done

- Versions and release dates from the Go module proxy; sources read in the
  module cache.
- For the fork: a test program that runs the real flow over HTTP (authorize,
  consent, code exchange, refresh, reuse) with Privo's storage standing in.
  It is `2026-11-12-oauth-library-spike.go.txt` next to this file: put it in an
  empty module as `spike_test.go` with
  `require authelia.com/provider/oauth2 v0.3.3` and run `go test -v`.
- `zitadel/oidc` and `ory/fosite` were judged **by reading their source, not
  by running them**; the line references are below so each claim can be
  checked.

## The checks (ADR 0022 point 16)

| Check | `authelia.com/provider/oauth2` v0.3.3 | `zitadel/oidc` v3.52.0 | `ory/fosite` v0.49.0 |
|---|---|---|---|
| Released in the last six months | Yes (2026-09-19) | Yes (2026-11-10) | **No** (2024-12-12, the last tag) |
| Public clients, S256 required | Yes, in the library (`EnforcePKCE`, plain off): no challenge, `plain` and a missing method are each refused | **No**: `plain` and an empty method verify (`oidc/code_challenge.go:47`, `op/auth_request.go:478`); Privo would have to refuse them in front | Yes (same options) |
| Client lookup is Privo's | Yes (`GetClient`; a URL as client id works) | Yes (`GetClientByClientID`) | Yes |
| Redirect rule of point 11 | Exact match and any port for `127.0.0.1`/`[::1]` in the library. **`localhost` with any port is not matched**, and the per-client comparison hook cannot be implemented from outside (its argument type is unexported). Done by Privo's own rule in front; the library then compares against the one address that rule accepted | Yes in the library, `localhost` included (`op/auth_request.go:381`, `:441`) | Same gap as the fork |
| Opaque tokens, stored hashed | Yes: the store is keyed by the token's signature part (43 characters), never the token (87) | Different: the token is an encrypted token id (`op/token.go:137`); a database leak plus the key gives tokens | Yes |
| Rotation, 30-second same successor | Rotation and reuse detection are in the library and go through a storage hook Privo implements (`RotateRefreshToken`). Reuse of the old token is refused and revokes the successor too. **The same-successor grace is not in the library** and was not built in the spike; see "Left to Privo" | Storage is Privo's, so it can be built | The original has the hook too |
| No row before consent | Yes: 0 rows after the request is validated, 2 after Allow (code and PKCE session) | **No by design**: `Authorize` stores the request before the login redirect (`op/auth_request.go:157`); workable only by making the request id itself a sealed blob | Yes |
| `resource` (RFC 8707) | Yes in the library: another resource is `invalid_target` at authorize; the token request must carry a granted one. **A request with no `resource` at all is accepted**; Privo refuses it in front | Not at the authorize endpoint | Not found in this version |
| `iss` in the response (RFC 9207) | Yes, a handler that is switched on in the configuration | Not found | Not found |
| A second use of a code revokes the first use's tokens (point 8) | Yes, in the library | not checked | Yes |
| Makes no request of its own for a request | Yes: `request_uri`, `sector_identifier_uri`, `jwks_uri` and an unsigned request object caused no outbound call; `request_uri` and the request object are refused | not checked | not checked |
| Beyond what Privo needs | PAR, DPoP, device flow, token exchange, OpenID: present, not composed in | Always issues an ID token (`op/token.go:43`), so signing keys and a JWKS endpoint | OpenID parts, optional |
| What it adds to Privo | 10 modules not in the binary today (`golang.org/x/crypto`, `x/net`, `x/text`, `dgraph-io/ristretto`, `cespare/xxhash`, `hashicorp/go-cleanhttp`, `go-retryablehttp`, `pkg/errors`, `stretchr/testify`, itself); about 1.1 MB (a minimal server grows from 5.9 to 7.1 MB) | not measured | 25 direct requirements, among them `ory/x` |

## Left to Privo with the fork (none needs a fork)

1. **The redirect rule** runs in Privo before the library is called, and a
   request whose address it does not accept is answered without a redirect.
   The security of point 11 therefore rests on Privo's own function and its
   tests; the library is the second check.
2. **`resource` must be present**: one line in front of the library.
3. **The 30-second same-successor grace** (point 9). The library refuses an old
   refresh token at once. To return the same successor, Privo keeps the
   successor response for 30 seconds, sealed with a key derived from the old
   refresh token, so the database holds nothing usable, and answers from it
   before the library sees the request. Designed, not built or tested here.
   The simpler alternative is what Ory documents: the old token stays valid for
   the window and each use gets new tokens; that would change point 9.
4. **Client ID Metadata Documents and the known list**: Privo's code behind
   `GetClient`. Dynamic registration: the library has a handler (not tried).
5. **Access token verification** on `/mcp`: validate the token's signature
   with the library's strategy and look the signature up, inside the existing
   authenticator and its 2-second cache.

## Concerns with the fork, stated plainly

- It is version 0.3.x: its API can change between releases. Pin the version
  and read the diff on every bump.
- It imports `testify` from a non-test file, so a test library is linked into
  the binary. Harmless, untidy.
- It is maintained for Authelia's needs. If that stops, Privo owns a fosite
  fork or moves; the storage interface is the same shape as fosite's, so the
  way back to another fosite descendant is short.
- It pulls in an HTTP client with retries. Nothing in the composed handlers
  used it in the test; the build keeps a test that proves no outbound request
  is made for an authorization request (`TestAuthorizeFetchesNothing`).

## For the owner to decide

1. **Adopt `authelia.com/provider/oauth2`, pinned at v0.3.3** (recommended).
   It is fosite, maintained, and the only candidate that passes without
   working against the library. ADR 0003's "fosite or zitadel" becomes "the
   maintained fosite fork".
2. `zitadel/oidc`, with S256, `resource`, `iss` and the stored request handled
   in front, and signing keys Privo does not otherwise need.
3. Amend ADR 0003 and write the narrow server by hand. Not recommended: the
   library's code-reuse revocation, PKCE and refresh rotation are exactly the
   parts that go wrong quietly.

And one smaller one, from item 3 above: keep "the same successor for 30
seconds" (more code, in Privo), or take the library-shaped variant (old token
valid for 30 seconds, new tokens each time).
