# 0017. Upstream OAuth broker: each person connects their own account

- Status: Proposed, 2026-11-01. Becomes Accepted after the F07 independent review.
- Date: 2026-11-01
- Details: 0003 section 3 (the direction: per-person broker, connect flow behind
  the admin session, single-flight refresh)
- Related: 0001 (pins), 0004 (protocol versions), 0008 (egress), 0014 (persons),
  0016 (sessions and the GitHub sign-in used as a stopgap)

## Context

Most remote MCP servers that teams want (GitHub, Linear, Notion, Atlassian,
Sentry, Slack, Google) authorize with OAuth: each person signs in to their own
account. Privo connects to an upstream once per server with static headers from
the vault, so these servers cannot be used, or only with one shared token that
makes every call run as one person and hides who did what upstream.

What upstream authorization servers support (checked 2026-11-01): GitHub's
remote MCP server does not support dynamic client registration and needs an
OAuth app registered by the operator; Linear, Notion, Atlassian, Sentry and
many others register clients dynamically; MCP 2026-07-28 prefers client ID
metadata documents (CIMD) and deprecates dynamic registration. The order a
client should try is: pre-registered credentials, CIMD, dynamic registration.

## Decision

### 1. Auth mode per server

A server has `auth: none` (today's behaviour: no auth, or static secret headers
and environment from the vault) or `auth: oauth` (each person's own grant).
`oauth` is allowed for `http` and `sse` servers only. An admin may supply a
client id, a client secret (write-only, kept in the vault), scopes, and the
issuer to expect.

### 2. Discovery

Privo acts as an MCP client under the MCP authorization spec:

- Protected resource metadata (RFC 9728) from the `resource_metadata` URL in the
  `WWW-Authenticate` header of a 401, else from the well-known URIs. Its
  `resource` must equal the server URL (canonical form).
- Authorization server metadata by RFC 8414, then OpenID discovery. The
  `issuer` must equal the URL it was fetched for. An AS that does not list
  `S256` in `code_challenge_methods_supported` is refused.
- Every fetch goes through the egress client (0008): https only (loopback http
  only when an operator allows it, as tests do), private addresses refused,
  256 KiB and 10 s per answer, no redirect to another host.
- Discovery runs when the admin adds or changes the server, so mistakes show up
  there and not on someone's first call. Results are kept for an hour and
  refreshed on failure.

### 3. Client registration, in the spec's order

1. Credentials the admin entered (the GitHub case).
2. A client ID metadata document, when the AS says
   `client_id_metadata_document_supported`. Privo serves
   `GET <public URL>/oauth/client-metadata.json` (the client id is that URL;
   one redirect URI, `<public URL>/connect/callback`; no client secret;
   `application_type: web`). The AS must be able to reach the public URL.
3. Dynamic client registration (RFC 7591) when the AS has a
   `registration_endpoint`: one registration per server (two servers behind
   one AS register twice, and deleting a server removes its registration with
   it), any secret kept in the vault. When the token endpoint answers
   `invalid_client` during a connection, Privo registers again and asks the
   person to start over (the code was issued to the old client).
4. Otherwise the server cannot use `oauth`, and the API says what to supply: a
   client id from the provider, registered with Privo's callback URL.

### 4. The connect flow

- The link is always `<public URL>/connect/<alias>`, never the provider's. It
  needs the person's admin-site session (0016). The page names the Privo user,
  the server, the authorization server's host and the scopes.
- A same-origin POST with the CSRF token starts the flow. The `state` (256 bits)
  is stored hashed for 10 minutes, deleted on use, and keyed with a cookie nonce
  as for sign-in (0016), so another browser can neither finish nor burn it. The
  row records the user, the session, the server, the expected issuer and the
  PKCE verifier.
- The authorization request carries `resource` (RFC 8707) = the server URL,
  the scopes and an S256 challenge.
- `GET /connect/callback` needs the same session that started the flow. It
  checks `iss` (RFC 9207) when the AS advertises
  `authorization_response_iss_parameter_supported`, and refuses an `iss` that
  differs from the expected issuer even when it is not advertised (mix-up
  defence). It exchanges the code with the verifier and `resource`. An error
  from the AS shows a generic page and stores nothing. Success goes to a fixed
  page (the person's connections), never to a URL from the request.
- The connect page's CSP adds the authorization server's origin to
  `form-action`, because Chrome applies `form-action` to the redirect after a
  form post (found in F05). Every other page keeps `form-action 'self'` plus
  GitHub.

### 5. Grants

One grant per org, server and user. Tokens (access, refresh, expiry, scope,
issuer) are encrypted in `credentials` with `owner_id` = the user's id and
purpose `oauth`. The vault binding includes owner and server, so a grant cannot
be moved to another person or server. A table without secrets
(`upstream_grants`) holds what the pages list: scopes, connected, last used,
last refreshed. Tokens are never returned by the API and never logged.

### 6. Calls

- **Catalog.** The tools, prompts and resources of an `oauth` server are read
  with the grant of the admin who added it (they connect right after adding).
  Pins (0001) apply as for every server, so a person whose account would show
  other tools gains nothing from it.
- **Each call runs as the caller.** The gateway maps the token's person to the
  user with that person (0014) and uses that user's grant on a session kept per
  server and user: closed after 5 minutes idle, at most 500 per process (the
  least recently used is closed first). The bearer token is added by the
  origin-locked transport, so it is only ever sent to the server's origin.
- **No grant:** the call fails with a tool error that gives the connect link.
  **No Privo user for the person:** the error says to ask an admin for an
  invite. A call never falls back to another person's grant or the catalog
  grant.

### 7. Refresh

- Refresh happens shortly before expiry (30 s), or once after a 401 with
  `invalid_token`.
- Refresh is single-flight per grant within a process. The new token pair is
  written with a compare-and-swap on the row version, so of two replicas only
  one wins, and the other re-reads.
- `invalid_grant` deletes the grant, is audited and gives the person the
  connect link.
- A 403 with `insufficient_scope` returns an error that names the scope (step-up
  comes later).

### 8. Disconnect

A person disconnects from their connections page, and an admin disconnects
anyone through the API. Privo revokes the token (RFC 7009) when the AS offers
revocation (best effort), deletes the grant and closes the pooled sessions. A
disabled user's grants are deleted at once. Deleting a server deletes all its
grants.

### 9. Invites (so more than the owner can connect)

`privo admin invite --person P --role R` and `POST /api/v1/invites` create a
single-use link (72 hours, only a hash stored), claimed by signing in. The claim
binds the account's numeric id, never a login. Same mechanism as the setup
link, plus a role.

### 10. Audit

New audit actions:

- `invite.create`
- `invite.claim`
- `server.oauth_register:<alias>`
- `upstream.connect:<alias>`
- `upstream.disconnect:<alias>`
- `upstream.grant_lost:<alias>`

Calls already carry the person.

## Not in this slice

- URL-mode elicitation (2025-11-25) and input-required (2026-07-28); the link
  is in the error text for now.
- Shared org-wide credentials by admin choice.
- Step-up for missing scopes.
- An admin view of everyone's connections in the browser (API only).
- `private_key_jwt` or mTLS client authentication.

## Alternatives considered

- **One shared token per server:** simple, but every call runs as one person,
  so upstream permissions and audit stop being honest. It stays as a later,
  explicit admin choice (0003).
- **Passing the client's own upstream token through:** forbidden by the MCP
  spec (token passthrough), and the client would hold the upstream token.
- **The Go SDK's client OAuth:** still experimental.
- **`golang.org/x/oauth2` for the exchange and refresh (0003's plan):** it
  cannot send the `resource` parameter on a refresh, which the MCP spec
  requires on every token request, and it hides the answers' bounds. Privo
  makes the three token requests itself (`internal/upstreamauth`): form posts
  through the egress client, bounded, never repeating what the server sent.

## Found while building

- **The client's protocol version leaked upstream.** The Go SDK reads the MCP
  protocol version from the request context, so a per-person connection opened
  inside a 2026-07-28 client's call offered that version to the upstream, and a
  stateful 2025-11-25 server refused it. Per-person connections are opened with
  a fresh context. Found by the acceptance test.
- **Browser behaviour checked in Chromium:** the connect page's `form-action`
  must name the authorization server (the post is followed by a redirect
  there), and the callback arrives as a cross-site top-level GET, which a
  `SameSite=Lax` session cookie survives.

## Consequences

- The data plane changes. The upstream manager keeps its one session per
  server for the catalog and gains per-person sessions for calls, with memory
  bounded by the pool cap.
- New public endpoints: `/oauth/client-metadata.json` (static, no
  authentication) and `/connect/*` (session).
- Privo becomes an OAuth client of many authorization servers, which opens
  mix-up and SSRF risks: issuers are bound per state, `iss` is checked and
  fetches go through the egress client.
- Residual risks:
  - The catalog is read with an admin's grant (lists only, never calls).
  - Stored upstream tokens are as safe as the master key.
  - A person can connect any upstream account they control. Privo cannot always
    tell which upstream account it is.
