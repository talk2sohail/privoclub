# Custom upstream sign-in and connections

How Privo signs in to servers where each person uses their own account, when
the defaults are not enough: OAuth set up by hand, each person's own API key,
and reviewing everyone's connections as an admin. The everyday side, a member
connecting their account, is in
[Connecting your accounts](../basics/connections.md).

Each person's own account arrived with the admin site; their own API key and
OAuth settings by hand in 0.9.0
([ADR 0023](https://github.com/talk2sohail/privoclub/blob/main/docs/adr/0023-each-server-as-it-presents-itself.md)),
the issuer for an OAuth client secret in 0.18.0, revocation at the provider
in 0.18.1, and everyone's connections in 0.19.0. The design is
[ADR 0017](https://github.com/talk2sohail/privoclub/blob/main/docs/adr/0017-upstream-oauth-broker.md).

## How Privo signs in to an OAuth server

Privo keeps one grant per person and server, encrypted, refreshes it, and
runs every call with the caller's own grant, so the server's permissions and
its own audit stay honest. It needs GitHub sign-in and `PRIVO_PUBLIC_URL`.

When you add such a server, Privo finds its authorization server and
registers itself in one of three ways:

- with the client ID you give, and its secret. GitHub needs this: its OAuth
  app gets the callback URL `https://privo.example.com/connect/callback`;
- with Privo's client ID metadata document,
  `https://privo.example.com/oauth/client-metadata.json`, which the provider
  must be able to reach from the internet;
- by dynamic registration.

Scopes and the issuer are optional. A server added with a client secret also
names its authorization server's issuer (0.18.0): the secret goes only
there, never to an authorization server the MCP server picks. The catalog
fills this in.

The first admin who connects their own account at `/connect/<alias>` provides
the server's tool list; the tool list is read with their account. Everyone
else connects from **Start**, **Connections**, or the link their AI client
shows. Changing the server's OAuth client ID ends everyone's connection: they
connect again.

## A server that does not say where its people sign in

Some servers publish no protected resource metadata, which was common before
the MCP authorization spec. Under **Add another server**, open **The server
does not say where people sign in**:

- give its authorization server's **Issuer**, such as
  `https://login.example.com`;
- if that publishes no metadata either, give its **Authorization endpoint**
  and **Token endpoint**, with a client ID and secret under **OAuth app**.

**Scopes**, separated by spaces, are optional.

A `resource` is sent only when the server names one, or when you give it.

Two defences apply. For OAuth settings entered by hand without an issuer, an
answer that names an issuer is refused (a mix-up defence): if your
authorization server sends `iss`, enter its issuer. And endpoints entered by
hand are refused for a server that publishes its own sign-in metadata.

## Each person's own API key

For a server where each person has their own API key, choose **Each person's
own account, by their own API key** under **Add another server**:

| Field | What it does |
| --- | --- |
| **Header the key goes in** | The header that carries the key; `Authorization` by default |
| **Before the key** | Text before the key; `Bearer ` for `Authorization` unless you write something else |
| **Where people get their key** | A hint shown on the page where people enter their key |
| **Link there** | A link to the page where they make one |

Each person enters their key once at `/connect/<alias>`. Privo keeps it
encrypted, never shows it again, and sends it only with that person's calls.
The first owner or admin who enters a key provides the tool list.

- A server that answers 401 or 403 to a key is treated as refusing it: the
  call says so, with the link to enter a new key.
- When the key the tool list is read with is refused, another administrator's
  key takes over the tool list.
- The header cannot be one that names the caller to a server or a proxy, such
  as `X-Forwarded-User` or `Remote-User`, or `Cookie`.

## Everyone's connections

On **Connections**, admins also see **Everyone's connections** across
servers: each person's account or API key, when it was connected and last
used, with those unused for 90 days marked. Filter by person or server, and
**Load more**.

To end one, choose **End…** and then **End** with the person and server.
Privo revokes the grant at the provider where the provider allows it, and
removes it here.

## Revocation at the provider

When Privo deletes a person's grant (they disconnect, a member is removed, a
server is deleted, or an edit ends everyone's connections), it also revokes
the grant at the provider: in the background, all at once within about 10
seconds, as best effort. A person's own API key cannot be revoked at the
provider, and is never sent there; Privo deletes it. Connecting again does
not revoke the grant it replaces.

## With the admin API

- Add an OAuth server: `POST /api/v1/servers` with `"oauth": {}`, or
  `"oauth": {"client_id": "...", "client_secret": "...", "issuer": "..."}`;
  `"scopes"` is optional. Settings by hand:
  `"oauth": {"issuer": "https://login.example.com"}`, or
  `"oauth": {"authorization_endpoint": "...", "token_endpoint": "...",
  "client_id": "...", "client_secret": "..."}`.
- Add a personal-key server: `"personal_key": {"header": "X-Api-Key",
  "prefix": "", "help": "Settings, then API keys.",
  "help_url": "https://example.com/settings/api"}`.
- Connections: `GET /api/v1/connections?person=&server=`,
  `GET /api/v1/servers/<alias>/connections` and
  `DELETE /api/v1/servers/<alias>/connections/<person>`.

More in [Admin API and script tokens](api-and-automation.md).
