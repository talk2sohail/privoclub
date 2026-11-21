# 0029. Editing a server: what an edit keeps and what it ends

- Status: Accepted, 2026-11-21 by the owner (proposed 2026-11-21); built in 0.17.0
- Date: 2026-11-21
- Related: 0008 (egress), 0017 (each person's own grant), 0023 (each person's
  own key, OAuth settings entered by hand), F11 (accepted definitions)

## Context

Until now a server could only be switched on or off, given teams, or deleted
and added again. Deleting loses its rules, toolsets, accepted tools and every
person's connection, so admins re-add servers rarely and live with a wrong
address or an expired header. They asked to edit a server instead.

Three things were given **for one address** and must not silently follow a
new one:

- **Each person's grant or API key** (ADR 0017, 0023) was handed to Privo
  for that server. Sent to another host, it leaks.
- **The org's static headers** (an API key in `Authorization`) were entered
  for that host. A mistyped new address would send them to whoever owns it.
- **Accepted tool definitions** (F11) were accepted from that server. Another
  host may present the same names with a different meaning.

## Decision

`PATCH /api/v1/servers/{alias}` (managers of servers) changes, besides
`enabled` and `teams`:

1. **`url`** for http and sse servers. It is checked by the egress policy
   (ADR 0008) like a new server. When it changes:
   - every person's connection to the server ends (grants and personal keys
     deleted, pooled sessions closed); people connect again;
   - static header values are dropped unless the same request gives them
     again;
   - every accepted definition goes back to review (pending), and the server
     offers nothing until an admin accepts again.
   A server that uses each person's own account (OAuth) cannot change its
   address: its authorization settings were discovered for the old one.
   Remove it and add it again.
2. **`headers`** for servers with static secrets: `{"Name": "value"}` sets or
   replaces a header, `{"Name": ""}` removes it. Values are sealed like at
   creation and never returned. A server that takes each person's own key or
   account takes no static headers, as at creation.
3. **`oauth_client`** for OAuth servers whose client was entered by hand
   (`admin` or `manual` registration): `client_id`, and `client_secret`
   (omitted keeps the stored one, `""` removes it). Refresh tokens belong to
   the client that got them, so a new client id ends every person's
   connection; a new secret alone does not.
4. Everything one request changes is one transaction with its audit events
   (`server.url`, `server.headers:+Name,-Name`, `server.oauth_client`,
   `server.connections_ended:N`, `server.review_again:N`). Header values,
   secrets and keys never appear in an event or a response.
5. An edit is refused (409) when the server changed since it was read, so
   two admins' edits never mix. The upstream connection sends only the
   headers and env the configuration names.

Stdio servers are not edited here: their command and hash are code
execution (ADR 0007) and stay remove-and-add.

## Consequences

- An edit that changes the address costs every person a reconnect and the
  admin a review. That is the price of never sending a credential, or
  trusting a definition, somewhere it was not given.
- Tokens at the provider are not revoked when connections end on an edit;
  they expire there. Revocation at the provider is the same open item as for
  disconnecting (F07).
