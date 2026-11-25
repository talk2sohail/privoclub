# Local stdio servers (development only)

Many MCP servers run as a local command that talks over standard input and
output (stdio). In production, Privo refuses them: they need an isolated
runner, which is designed but not built yet
([ADR 0007](https://github.com/talk2sohail/privoclub/blob/main/docs/adr/0007-stdio-runner.md)).
For development and tests, Privo can run them itself, on its own host.

!!! warning "Development only"
    The local runner runs each server unisolated, as Privo's own user. A
    server's tools can then read Privo's data and keys. Never switch it on
    where Privo holds real credentials.

## Switch it on

Set both settings and restart:

- `PRIVO_RUNNER=local`
- `PRIVO_INSECURE_LOCAL_RUNNER=i-understand`

With `PRIVO_RUNNER` empty, the default, stdio servers are refused. With
`local` but without the second setting, Privo does not start, and says why.
Both are in the [configuration reference](../installation/configuration.md).

## Add a stdio server

The admin site adds HTTP and SSE servers only; add a stdio server with the
admin API:

```sh
curl -sS -X POST -H "Authorization: Bearer $OWNER" -H 'Content-Type: application/json' -d '{
  "alias": "files", "transport": "stdio",
  "command": {
    "path": "/usr/local/bin/mcp-files",
    "sha256": "<hex SHA-256 of that file>",
    "args": ["--root", "/tmp/demo"]
  },
  "env": {"FILES_LOG": "info"}
}' https://privo.example.com/api/v1/servers
```

- `command.path` must be absolute.
- `command.sha256` is the executable's SHA-256, in hex (`shasum -a 256
  /usr/local/bin/mcp-files`). Privo checks it before every start and refuses
  a file that changed.
- `env` holds the server's own variables. The process gets only those and a
  minimal environment: it never inherits Privo's own variables, and names
  Privo reserves are refused.

The process runs in a temporary directory. Its tools then go through review
like any other server's.
