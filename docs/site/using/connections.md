# Each person's own account

Remote MCP servers such as GitHub, Linear, Notion, Atlassian or Sentry let each
person sign in with their own account (OAuth). Privo keeps one grant per
person and server, encrypted, refreshes it, and runs every call with the
caller's own grant, so the upstream's permissions and audit stay honest
([ADR 0017](https://github.com/talk2sohail/privoclub/blob/main/docs/adr/0017-upstream-oauth-broker.md)).
This needs [sign-in with GitHub](admin-site.md#sign-in-with-github).

## Adding an OAuth server

```sh
curl -X POST $PRIVO/api/v1/servers -H "Authorization: Bearer $OWNER" -d '{
  "alias": "linear", "transport": "http", "url": "https://mcp.linear.app/mcp",
  "oauth": {}
}'
```

Privo finds the server's authorization server and registers itself:

- with the client id you give (`"oauth": {"client_id": "...",
  "client_secret": "..."}`), needed for GitHub, whose OAuth app gets the
  callback URL `<PRIVO_PUBLIC_URL>/connect/callback`;
- with its client ID metadata document
  (`<PRIVO_PUBLIC_URL>/oauth/client-metadata.json`);
- or by dynamic registration.

`"scopes"` and `"issuer"` are optional. A server added with an OAuth client
secret (`oauth.client_secret`) also names its authorization server
(`oauth.issuer`, 0.18.0): the secret goes only there, never to one the MCP
server picks. The catalog fills it in.

Then an admin connects their own account at `/connect/<alias>`; the server's
tool list is read with it. Everyone else connects theirs from **Connections**
on the admin site, or follows the link their AI client shows on the first
call.

### A server that does not say where its people sign in

Some servers publish no protected resource metadata (common before the MCP
authorization spec). Give its authorization server,
`"oauth": {"issuer": "https://login.example.com"}`. If that publishes no
metadata either, give its endpoints and a client id:
`"oauth": {"authorization_endpoint": "...", "token_endpoint": "...",
"client_id": "...", "client_secret": "..."}`
([ADR 0023](https://github.com/talk2sohail/privoclub/blob/main/docs/adr/0023-each-server-as-it-presents-itself.md)).
`"resource"` is sent only when the server names one or you give it.

For OAuth settings entered by hand without an issuer, an answer that names an
issuer is refused (a mix-up defence); if your authorization server sends
`iss`, enter its issuer. Endpoints by hand are refused for a server that
publishes its own sign-in metadata.

## Each person's own API key

0.9.0. For a server that takes an API key where each person has their own
([ADR 0023](https://github.com/talk2sohail/privoclub/blob/main/docs/adr/0023-each-server-as-it-presents-itself.md)):

```sh
curl -X POST $PRIVO/api/v1/servers -H "Authorization: Bearer $OWNER" -d '{
  "alias": "acme", "transport": "http", "url": "https://mcp.acme.example/mcp",
  "personal_key": {"header": "X-Api-Key", "help": "Settings, then API keys.",
                   "help_url": "https://acme.example/settings/api"}
}'
```

`header` defaults to `Authorization`, with `Bearer ` before the key (change it
with `"prefix"`). Each person enters their own key once at `/connect/<alias>`
(Connections, or the link their AI client shows); Privo keeps it encrypted,
never shows it again, and sends it only with that person's calls. The first
owner or admin who enters a key provides the tool list. When the server
refuses a key, the call says so with the link to enter a new one. In the
admin app this is the question **Whose account does this server see?** on
**Servers → Add another server**.

A server that answers 401 or 403 to a key is treated as refusing it. When the
key the tool list is read with is refused, another administrator's key takes
over the tool list. The header cannot be one that names the caller to a
server or a proxy (such as `X-Forwarded-User` or `Remote-User`), or `Cookie`.

## Managing connections

A person disconnects on **Connections**. An admin lists and removes one
server's connections with `GET /api/v1/servers/<alias>/connections` and
`DELETE /api/v1/servers/<alias>/connections/<person>`.

Admins see everyone's connections across servers on **Connections →
Everyone's connections** (`GET /api/v1/connections?person=&server=`, 0.19.0),
with those unused for 90 days marked; **End** revokes at the provider and
deletes.
