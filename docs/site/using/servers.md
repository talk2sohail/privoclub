# Adding servers

A server is an MCP server Privo reaches for your team. Each one has an alias:
its tools reach AI clients as `<alias>__<tool>`.

## Three ways a server sees the caller

| The server sees | Add it with | Page |
| --- | --- | --- |
| One shared credential for everyone | `headers` (for example an `Authorization` header) | below |
| Each person's own account (OAuth) | `"oauth": {}` | [Each person's own account](connections.md) |
| Each person's own API key | `"personal_key": {...}` | [Each person's own account](connections.md#each-persons-own-api-key) |

On the admin site, **Servers → Add another server** asks **Whose account does
this server see?**; **Servers → Add from the catalog** fills everything in for
25 hosted servers ([Teams and the catalog](teams.md)).

## A server with a shared credential

```sh
curl -X POST $PRIVO/api/v1/servers -H "Authorization: Bearer $OWNER" -d '{
  "alias": "acme", "transport": "http", "url": "https://mcp.acme.example/mcp",
  "headers": {"Authorization": "Bearer <the key>"}
}'
```

`transport` is `http` (streamable HTTP) or `sse`. Header values are stored
encrypted and never shown again, only which headers are set. `"teams"` names
the teams that reach the server; without it the server is for everyone.

Stdio servers run a command on the host. They are refused unless
`PRIVO_RUNNER` allows them, and the local runner is for development only
([ADR 0007](https://github.com/talk2sohail/privoclub/blob/main/docs/adr/0007-stdio-runner.md)).

!!! note
    Every tool is reviewed before anyone sees it: a server's page lists new
    and changed tools to accept or block.

## Editing a server

0.17.0, [ADR 0029](https://github.com/talk2sohail/privoclub/blob/main/docs/adr/0029-editing-a-server.md).
A server's page changes its address, the headers sent with every request, and
the OAuth client of a server whose client was entered by hand. Values are
never shown again, only which headers are set.

What was given for one address never follows a new one: a new address ends
everyone's connection, drops header values not entered again, and sends every
tool back to review. A server that uses each person's own account keeps its
address (delete it and add it again); a new client id ends everyone's
connection.

API: `PATCH /api/v1/servers/{alias}` with `url`, `headers` (`""` removes one)
and `oauth_client` (`client_id`, `client_secret`).
