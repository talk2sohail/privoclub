# 0023. Each upstream server is reached the way it presents itself

- Status: Accepted, 2026-11-14 (the owner's direction of 2026-11-11: "the
  per-server auth works as they present themselves, either personal token or
  individual sign-in with the upstream"; built in 0.9.0 under his instruction
  of 2026-11-14 to follow industry practice and ask only what is critical)
- Date: 2026-11-14
- Related: 0017 (each person's own account through OAuth), 0003 (the vault),
  0008 (egress), 0020 (teams), 0021 (a caller is a member by user id)

## Context

Before this, a server added to Privo was reached in one of three ways: with
no credential; with one shared credential in a header that every call used;
or with each person's own account through OAuth (ADR 0017). Two common cases
had no answer:

1. **Servers that take an API key, where each person has their own.** Many
   in-house and smaller servers authenticate with a key in a header and have
   no OAuth. With a shared key every call looks like one person to the
   server, and the person's own permissions there are lost.
2. **OAuth servers that do not say where their people sign in.** ADR 0017
   finds the authorization server through the server's protected resource
   metadata (RFC 9728). Servers built before the MCP authorization spec, and
   many internal ones, publish none, so they could not be added with OAuth.

## Decision

1. **One question when adding a server: whose account does it see?**
   Nobody's (no credential); one shared account (a header every call uses);
   each person's own account by signing in (OAuth, ADR 0017); each person's
   own account by their own API key (this ADR). The admin API's fields stay
   as they were (`headers`, `oauth`) plus `personal_key`; only one of
   `headers`, `oauth`, `personal_key` is accepted.

2. **Each person's own API key is kept like an OAuth grant.** It is a row in
   `upstream_grants` with a credential of purpose `key`, sealed with the
   org's data key and bound to the person and the server, exactly as ADR
   0017's tokens are. No migration is needed. Every call runs with the
   caller's own key and never anyone else's; a person without a key is told
   where to enter one. The first owner or admin who enters a key provides
   the server's tool list, as with OAuth.

3. **Where the key goes is the admin's choice, within limits.** A header
   name (default `Authorization`) and what comes before the key (default
   `Bearer ` for `Authorization`, nothing otherwise). The reserved headers of
   shared credentials are refused, and so are `Cookie`, `Proxy-*`, `Mcp-*`
   and `Sec-*`. A server that takes personal keys takes no shared headers:
   per-person connections carry the person's credential and nothing shared.
   The admin may add a short help text and an https link that tell people
   where to get their key.

4. **The person enters the key on Privo's own page** (`/connect/<server>`,
   signed in, CSRF and origin checked, a password field). Privo checks its
   shape only: printable ASCII without spaces, at most 4096 characters. It
   does not try the key against the server, so a key that does not work
   shows up on the first call. Privo never shows a key again, to the person
   or anyone else; a new key replaces the old one; removing it is one click
   on Connections.

5. **A refused key is kept, and the person is told.** When the server answers
   401 to a person's key, the call fails with "<server> refused your API key.
   Enter a new one: <link>". The key is not deleted: a 401 can be the
   server's mistake, and an OAuth refresh failure is a different thing (ADR
   0017 deletes a grant whose refresh token is refused, because it can never
   work again).

6. **OAuth settings entered by hand, in two steps.** When the server publishes
   no protected resource metadata, Privo says so and the admin names the
   authorization server by its issuer: Privo fetches that server's metadata
   and applies every check of ADR 0017 (issuer match, S256, https
   endpoints). If the authorization server publishes no metadata either, the
   admin gives its authorization and token endpoints and a client id (and
   secret, if it has one); nothing is registered. Endpoints must be https and
   pass the egress policy when they are entered, as every request to them
   will later.

7. **The `resource` parameter is sent only when it is known**: the server
   named it in its metadata, or the admin gave it. An empty `resource=` is
   never sent (before this ADR it could not be empty). The `iss` check of the
   connect callback (RFC 9207) is skipped only when no issuer is known, which
   happens only with endpoints entered by hand; the state still binds the
   answer to the server.

## Consequences

- In-house servers with per-person keys, and older OAuth servers, can be
  added and used the way ADR 0017's servers are: each person's own account,
  per call, with the same pool, the same audit (`upstream.connect`,
  `server.personal_key:<alias>:<header>`) and the same "connect first"
  answer.
- A server's way of signing in is chosen when it is added. Changing it means
  adding the server again; editing it in place is not offered yet.
- Not decided here, and still parked in issue 3: one shared OAuth account for
  a server; Privo vouching for the person with a signed assertion for
  in-house servers; catalog entries that take personal keys.

## Threat notes

- **A key is a bearer secret.** It is stored only sealed, never logged, never
  returned by the API or shown on a page, and sent only to the server's own
  origin (the origin lock of ADR 0017). The acceptance test checks that keys
  are neither on disk in the clear nor in the log.
- **Header injection.** The header name follows the RFC 9110 token grammar;
  the prefix and the key are printable ASCII without line breaks.
- **One person's key for another's call.** The connection pool is keyed by
  server and member; the gateway picks the member by the calling token's user
  id (ADR 0021), never by a name. The acceptance test checks that two people
  each get their own account.
- **Hand-entered endpoints** could point at an internal address. They pass
  the egress policy when entered and on every request.
