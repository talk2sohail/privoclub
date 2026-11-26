# 0003. Secrets and authentication

- Status: Accepted (revision 2, after the 2026-10-28 review), 2026-10-28
- Date: 2026-10-28
- Related: 0008 egress, 0009 threat model, 0011 enterprise identity
- Amended by: 0016 (the first sign-in provider is GitHub; Google and Microsoft follow)
- Detailed by: 0017 (section 3, the upstream OAuth broker)

## Context

Privo holds the keys to customers' GitHub, Google, Slack and databases. There
are three sign-in problems: people to the admin site, AI clients to the
gateway, and Privo to upstream servers on a person's behalf. The prototype
stored upstream secrets in plain text and ran scrypt against every token row
on every request; both are replaced.

## Decision

### 1. People to the admin site

- OIDC sign-in with Google and Microsoft; generic OIDC and SAML in 0011.
  Users are keyed on issuer plus stable subject (Microsoft `tid`+`oid`), never
  on the email claim. Google sign-in checks `hd` against the org's verified
  domains. Accounts are never auto-linked by email.
- Magic links (self-hosted fallback): single use, hashed at rest, 15-minute
  lifetime, rate limited, responses that do not reveal whether an email exists.
- First-boot setup link: 1-hour lifetime, claimed atomically; a new one can be
  issued with `privo admin setup-link` inside the container.
- Sessions: server-side, `__Host-` cookies, HTTP-only, SameSite; rotated on
  login; idle and absolute timeouts; invalidated on role change; CSRF tokens on
  every state-changing request.
- Step-up re-authentication to add a stdio server (code execution), change
  policies or audit settings, rotate keys, or approve high-risk calls.

### 2. AI clients to the gateway

- Privo runs an OAuth 2.1 authorization server per org MCP endpoint, built on a
  maintained library (`zitadel/oidc` or `ory/fosite`, chosen by a spike that
  checks RFC 8707 resource indicators); not hand-rolled. The Go MCP SDK provides
  only resource-server helpers.
- Registration: **Client ID Metadata Documents first**, fetched through the
  egress client (0008), with a trust policy (known client domains plus
  admin-approved ones). Dynamic client registration stays behind a
  compatibility flag for clients that still need it; rate limited; unused
  registrations expire.
- PKCE with S256 only. Exact redirect URI match. The consent screen shows the
  redirect host and warns on localhost-only clients.
- `iss` in authorization responses (RFC 9207). The `resource` parameter is
  validated on authorize and token requests; `aud` equals the org's canonical
  MCP URL, so a token for org A is rejected at org B. Privo never passes a
  client's token through to an upstream.
- **Opaque access tokens** (1 hour), stored hashed, cached per replica for at
  most 5 seconds and invalidated by notification. Revocation takes effect
  within 5 seconds.
- Refresh tokens: hashed, bound to the client, rotated on use, with a 30-second
  grace window that returns the same successor (parallel windows and lost
  responses must not log people out); reuse after the window revokes the grant.
  Idle timeout 30 days, absolute 90 days.
- **Personal tokens** for clients without OAuth: `privo_pat_<id>_<secret><crc32>`
  (indexed lookup by id, recognisable by secret scanners), secret hashed with
  SHA-256 (sound for 256-bit random secrets), mandatory expiry (default 90
  days), bound to one org, optionally scoped to a toolset. Not usable by
  ChatGPT or Claude web connectors, which require OAuth.

### 3. Privo to upstream servers

- **Per-user OAuth broker** on `golang.org/x/oauth2` (the SDK's client OAuth
  is still experimental). Calls run as the person, so upstream permissions and
  audit stay honest.
- Connect flow, hardened against account-linking attacks: requires the user's
  own admin-site session; `state` is single use, at most 10 minutes, stored
  server side and bound to that session; Privo's own consent screen names the
  Privo user, the upstream account and the scopes. A "connect your account"
  link always points to Privo, never to the provider, and only completes for
  the same person who made the MCP call.
- The link reaches the client as URL-mode elicitation where supported (2026:
  input-required; 2025-11-25: `URLElicitationRequiredError`), otherwise as an
  error message.
- Refresh of rotating upstream tokens is single-flight across replicas (row
  lock on the credential, re-read after locking).
- **Shared credentials** only by explicit admin choice, and only with a policy
  limiting which tools may use them.
- Privo Cloud's shared OAuth apps (GitHub, Google) are a single blast radius:
  minimal scopes, monitored, and self-hosted installs bring their own apps.

### Encrypting secrets

- Envelope encryption: each org has a data key (DEK); credentials are
  encrypted with AES-256-GCM under a per-credential subkey derived with HKDF
  from the DEK and (credential id, version). Associated data binds org id,
  credential id, owner id, server id, purpose, key version and row version, so
  a ciphertext moved to another row, owner or older version fails to decrypt.
- DEKs are wrapped by the key-encryption key (KEK) with associated data = org
  id, through a `KeyProvider`:
  - Self-hosted: `PRIVO_MASTER_KEY_FILE` (a mounted secret file) preferred;
    `PRIVO_MASTER_KEY` accepted. `PRIVO_MASTER_KEY_PREVIOUS` during rotation.
    The key is removed from the environment of every child process.
  - Privo Cloud: a cloud KMS, with a warm DEK cache and alerts on KMS errors.
- Rotation: KEK rotation re-wraps DEKs; DEK rotation re-encrypts credentials
  in the background. Both are commands with tests.
- Secrets are write-only through the API, never logged, never given to hooks,
  decrypted only in memory at injection.
- Redaction of known secret values and secret-like patterns (gitleaks rules)
  from results and audit is **log hygiene, not a security boundary**: encoded
  or split secrets can slip through, and a stdio server holds its secret anyway.

### Lost master key

Losing the master key makes stored credentials unrecoverable. Install and
admin pages say so plainly and require a key backup step, kept separate from
data backups (see 0010).

## Consequences

- The authorization server is the largest v1 component; using a library and a
  per-client compatibility matrix (Claude Code, Cursor, Codex, VS Code, Amp,
  ChatGPT) keeps it tractable.
- Opaque tokens cost one cached lookup per request in exchange for fast,
  honest revocation.

## As built (0.20.0, 2026-11-26)

For operators: [Secrets and keys](https://docs.privo.club/security/secrets/).

- **Encrypting secrets** is built as decided (`internal/vault`,
  `internal/keyring`), with the master key from a file or the environment,
  removed from the environment once read. Personal tokens are built as
  decided.
- **Rotation is not.** `PRIVO_MASTER_KEY_PREVIOUS` is read, so a data key
  wrapped with the previous key still opens. But nothing re-wraps data keys
  under the new key, and no DEK rotation exists (F04, "KEK rotation
  command"). A change of master key therefore cannot finish, and the old key
  stays needed; the page says so.
- **Section 1:** sign-in is with GitHub (ADR 0016); OpenID Connect is ADR
  0034. Magic links and step-up re-authentication are not built.
- **Section 2:** the OAuth authorization server is built in 0.8.0, as
  changed by ADR 0022. **Section 3:** the per-person broker is built with
  ADRs 0017 and 0023.
- **Redaction** of known secrets from results and logs is not built (F04).
