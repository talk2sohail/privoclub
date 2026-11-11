# 0022. AI clients sign in to the gateway with OAuth; Privo is the authorization server

- Status: Proposed, 2026-11-11 (for the owner's review before any code)
- Date: 2026-11-11
- Related: 0003 section 2 (the accepted decision this builds on and, in four
  marked places, makes precise), 0021 (a caller is a member by user id), 0016
  (how a person signs in), 0008 (egress), 0014 (token kinds), 0004 (stateless
  gateway)

## Context

Today an AI client reaches `/mcp` with a personal token that a person makes on
the Tokens page and pastes into the client's configuration. It works, and it
is the only way. It also means a long-lived secret in a config file per client,
and no way at all into clients that only speak OAuth (claude.ai on the web,
ChatGPT).

The owner's direction (2026-11-11): OAuth to use the gateway; each upstream
server then authenticates the way it presents itself (ADR 0017 and its
follow-ups).

ADR 0003 section 2 already decided the shape in 2026-10: an OAuth 2.1
authorization server per org MCP endpoint, on a maintained library; Client ID
Metadata Documents first, dynamic registration behind a flag; PKCE S256; exact
redirect match; `iss` in responses; the `resource` parameter validated;
opaque access tokens of one hour; rotating refresh tokens with a 30-second
grace, 30 days idle and 90 days absolute. Nothing of it is built: the protected
resource metadata at `/.well-known/oauth-protected-resource/mcp` lists no
authorization server.

Three things are known now that were not then:

1. **A caller is a member by user id** (ADR 0021). An OAuth grant has an
   obvious owner: the member who signed in and consented.
2. **What real clients do**, from their own documentation (checked
   2026-11-11; sources at the end):
   - Claude (claude.ai, Desktop, mobile, Claude Code) uses a Client ID
     Metadata Document only when the server's metadata has both
     `client_id_metadata_document_supported: true` and `none` among
     `token_endpoint_auth_methods_supported`; otherwise it registers
     dynamically. The hosted apps redirect to
     `https://claude.ai/api/mcp/auth_callback`. Claude Code redirects to
     `http://localhost:<any port>/callback` and needs the port ignored for
     `localhost` as well as `127.0.0.1`. Claude reads only the first entry of
     `authorization_servers`, refreshes on a 401 and up to five minutes before
     expiry, needs `invalid_grant` for a dead refresh token, and gives the
     token endpoint 10 seconds.
   - VS Code, ChatGPT, Codex, Copilot CLI, Zed and Antigravity also identify
     themselves with a metadata document. **Cursor**, Gemini CLI and Mistral
     register dynamically only.
3. **Unauthenticated writes are a way to lock people out** (F05 R-01: anyone
   can fill the sign-in state table). Sign-in becomes the front door of every
   AI client with this change, so the new flow must not add such a table.

## Decision

### Who signs in, and what a grant is

1. **The person signs in to Privo exactly as they do for the admin site**
   (GitHub today, ADR 0016; the company's identity provider later). The
   authorization endpoint uses that session; it has no login of its own.
2. **Only a member who may use the gateway can authorize a client** (owner,
   admin, member; ADR 0011). An approver or auditor is told so.
3. **A grant belongs to the member who consented** (`user_id`, ADR 0021), for
   one client and for this gateway. Calls with its access token run as that
   member: their teams, their own upstream accounts. The member's **role is
   read when the token is verified**, not copied into the grant, so a role
   change or a removal takes effect within the verification cache (2 s).
   Removing a member revokes their grants.
4. **One scope, `mcp`**: use the gateway as me. No scope reaches the admin
   API. `offline_access` is accepted and listed because Claude adds it when it
   is listed; it changes nothing (point 9).

### The flow

5. **Endpoints**, all under the public URL, which is the issuer:
   `/.well-known/oauth-authorization-server` (RFC 8414),
   `/oauth/authorize`, `/oauth/token`, `/oauth/revoke` (RFC 7009), and
   `/oauth/register` only when dynamic registration is on. The protected
   resource metadata names the issuer as its only authorization server and
   `mcp` as its scope; a 401 from `/mcp` carries `resource_metadata` and
   `scope="mcp"`.
6. **No table row is written for someone who is not signed in.** The
   authorization request (client, redirect URI, state, code challenge, scope,
   resource) is validated first. If the person has no session, the request is
   sealed into a short-lived cookie (authenticated encryption, 10 minutes,
   `__Host-` prefix, bound to this flow) and they are sent to sign in; after
   sign-in they return to `/oauth/authorize` with the request from the cookie.
   There is still no `next` parameter and no redirect to an address a request
   supplies. The first write is the authorization code, made by a signed-in
   member pressing Allow.
7. **Consent is a page, every time.** It names the client, the host it will
   send the person back to, who they are signed in as, and what is granted
   ("use Privo as you: the servers your teams reach and your connected
   accounts"). A client whose only redirect addresses are loopback gets a
   warning that any program on the computer can claim to be it. Allow and Deny
   are a CSRF-protected POST. Consent is not remembered: with 90-day grants
   that is one click per client per quarter. *(Makes 0003 precise: it said
   only that the screen shows the redirect host and warns on localhost.)*
8. **PKCE with S256 is required** of every client; `plain` and a missing
   challenge are refused. Authorization codes live 60 seconds, work once, and
   are bound to the client, the redirect URI and the challenge. A second use
   revokes the grant the first use created.
9. **Tokens.** Access tokens are opaque, stored hashed, live one hour and are
   bound to the gateway's canonical URL: `resource` must be present on the
   authorization and the token request and equal `<public URL>/mcp`. A refresh
   token is **always** issued with a code exchange (no client has to ask),
   stored hashed, bound to the client, and rotated on every use. Within 30
   seconds of a rotation the old refresh token returns the same successor;
   after that its use revokes the whole grant. Idle 30 days, absolute 90 days
   from consent. A dead refresh token is answered `invalid_grant`. *(0003,
   unchanged, except "always issued".)*
10. **Verification** goes through the one authenticator that verifies personal
    tokens today, by prefix, with the same 2-second cache, so revocation and
    removal are as fast as for personal tokens. *(0003 says 5 seconds; the
    code and every other document have said 2 since F06's first slice. The
    owner amends 0003 or this number.)*

### Which clients are accepted

11. **A client identifies itself with a Client ID Metadata Document** (an
    `https` URL as `client_id`). Privo fetches it through the egress client
    (public addresses only, 5 seconds, 64 KiB), requires `client_id` to equal
    the URL and the redirect URI to be listed, and keeps it for at most 24
    hours. Loopback redirect URIs match with the port ignored, for
    `127.0.0.1`, `[::1]` and `localhost`; every other redirect URI matches
    exactly. Clients are public (`token_endpoint_auth_method: none`); a
    confidential client is not needed by any client above and is left out.
12. **Trust policy: known clients, plus the ones an administrator approves.**
    Privo ships a short list of client documents that are accepted without
    setup (Claude Code, the hosted Claude apps, VS Code, ChatGPT, Codex,
    Copilot CLI, Zed, Antigravity: the exact URLs are checked against each
    vendor's page when the list is written, and the list is one file). Any
    other document is refused with a page that names its host and says an
    administrator can approve it on the Clients page; approving is audited.
    The alternative, accepting any `https` client behind the consent page, is
    how most public servers behave and is one setting away
    (`PRIVO_OAUTH_CLIENTS=known|approved|any`, default `known`); it is not
    the default because Privo guards a company's accounts. *(0003, unchanged.)*
13. **Dynamic registration is off by default** (`PRIVO_OAUTH_DYNAMIC_REGISTRATION`).
    Turned on, `/oauth/register` accepts public clients only, is limited per
    address and in total, shows such a client on the consent page as
    "registered itself, not verified", and deletes a registration that no one
    has authorized within an hour or that has had no live grant for 90 days.
    Cursor, Gemini CLI and Mistral need it; with it off they keep using a
    pasted token. *(0003, with the default and the limits named.)*

### What stays

14. **Personal tokens stay**, for clients without OAuth and for scripts. They
    are the same kind of credential to the rest of Privo: a gateway principal
    with a member (ADR 0021) or without one.
15. **One org per deployment today**, so the issuer is the public URL and the
    resource is `<public URL>/mcp`. With more than one org per deployment the
    issuer gets a path per org (0003: "per org MCP endpoint"); nothing here
    prevents that.

### The library

16. ADR 0003 decided "built on a maintained library, not hand-rolled", chosen
    by a spike. That stands. **The spike is the first task of the build**, and
    it passes a candidate (`ory/fosite`, `zitadel/oidc`) only if all of this
    can be done without forking it: public clients with S256 required; client
    lookup that Privo implements (for points 11 to 13); the redirect matching
    of point 11; opaque tokens that Privo stores hashed in SQLite; rotation
    with the 30-second same-successor grace of point 9; no server-side row
    before consent (point 6); `resource` and `iss` handled (in the library or
    around it). Maintenance is checked too: a release in the last six months,
    and what its dependencies add to the binary. **If neither passes, the work
    stops and the owner decides** between a narrower library and amending
    0003; it is not hand-rolled quietly.

## Consequences

- Claude Code, Claude on the web and desktop, VS Code and ChatGPT connect to
  `https://<privo>/mcp` by signing in; nothing is pasted, nothing long-lived
  sits in a config file, and each client can be revoked by itself on a
  "Connected clients" list.
- For the hosted Claude apps and ChatGPT the gateway must be reachable from
  their servers, and Privo must be allowed to fetch their metadata documents.
  A Privo on a private network keeps personal tokens for those.
- The audit gains who allowed which client, code exchanges, reuse detection
  and revocations. A call names the client and the member (ADR 0021).
- The authorization server is the largest piece of auth code in Privo. It is
  built behind the independent review CLAUDE.md requires, and it is exposed
  to the internet without a session, so its limits and its tests come first.
- The sign-in state cap (F05 R-01) is fixed in the same series, before the
  authorization server is switched on, because every client's first step now
  goes through sign-in.
- Not decided here: enterprise-managed authorization (an identity assertion
  from the company's identity provider instead of the consent page), which
  needs the identity-provider sign-in first; toolset scopes (F08); a
  confidential client for server-side agents.

## Sources (read 2026-11-11)

- MCP authorization specification, 2025-11-25:
  https://modelcontextprotocol.io/specification/2025-11-25/basic/authorization
- Claude, "Authentication for connectors":
  https://claude.com/docs/connectors/building/authentication
- "CIMD adoption three months in" (2026-10-27), per-client table:
  https://gofranz.com/blog/state-of-cimd-adoption/
- Ory, graceful refresh token rotation (the behaviour a library offers):
  https://www.ory.com/docs/hydra/guides/graceful-token-refresh
