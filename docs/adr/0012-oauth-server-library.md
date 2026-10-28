# 0012. OAuth authorization server library

- Status: Proposed (desk spike); to be accepted after the prototype in F06
- Date: 2026-10-28
- Related: 0003

## Context

ADR 0003 requires an OAuth 2.1 authorization server per organisation, built on
a maintained library rather than hand-rolled. The Go MCP SDK provides only
resource-server helpers. Candidates: `ory/fosite` and `zitadel/oidc`.

| Need (ADR 0003) | ory/fosite | zitadel/oidc (`pkg/op`) |
| --- | --- | --- |
| Authorization code with PKCE | Yes | Yes |
| Opaque access and refresh tokens | Yes (HMAC-signed, opaque) | JWT oriented |
| Refresh rotation, reuse detection, revocation | Yes | Revocation not listed |
| Pluggable storage (our Postgres and SQLite stores) | Yes (storage interfaces; memory reference) | Yes |
| RFC 8707 resource indicators | Not documented | Not documented |
| RFC 9207 `iss` in responses | Not documented | Not documented |
| Client ID metadata documents, dynamic registration | Not provided | Not provided |
| Licence | Apache-2.0 | Apache-2.0 |
| Notes | OAuth core, no OpenID baggage | OpenID Provider focus; OP certification not yet achieved; maintainers note limited review capacity |

Sources: [ory/fosite](https://github.com/ory/fosite), [zitadel/oidc](https://github.com/zitadel/oidc).

## Decision

Use **ory/fosite** for the OAuth core. It matches ADR 0003's model directly
(opaque tokens, rotation, revocation, our own storage). Privo builds the parts
neither library offers, each behind its own tests:

1. `resource` parameter (RFC 8707) on authorize and token requests, mapped to
   the token audience and checked against the org's canonical MCP URL.
2. `iss` in authorization responses (RFC 9207).
3. Client resolution for client ID metadata documents, fetched through the
   egress client (0008) with the trust policy from 0003; dynamic registration
   as a compatibility endpoint.
4. A 30-second refresh reuse grace window (fosite revokes on reuse by
   default).
5. S256-only PKCE, exact redirect matching, per-org issuer and keys.

## Validation before acceptance

A prototype in F06 must complete sign-in and token refresh with Claude Code,
Cursor, VS Code and ChatGPT, and pass the token and resource checks above. If
fosite blocks any item, the fallback is a minimal in-house server limited to
authorization code with PKCE and refresh, with an external security review
before release.

## Consequences

- Extensions to fosite are security-critical code we own; they get the same
  review bar as the vault.
