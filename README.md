# Privo

The team gateway to AI work and the MCP world: one endpoint per organisation in
front of every MCP server, with policies, human approvals, encrypted
credentials and a tamper-evident audit trail.

Status: early development. Architecture is settled in `docs/adr/`; progress
per feature is in `docs/todo.md`.

## Build

Requires Go 1.26 or later; `go.mod` pins the 1.27.3 toolchain.

```sh
make            # vet, lint, test, build
make build      # bin/privo
make acceptance # black-box suite against the binary (fails until F02 lands)
make image      # container image
```

## Run

```sh
PRIVO_LISTEN=:8080 PRIVO_DATA_DIR=./data bin/privo serve
curl localhost:8080/livez
```

| Variable | Default | Meaning |
| --- | --- | --- |
| `PRIVO_LISTEN` | `:8080` | Listen address |
| `PRIVO_DATA_DIR` | `./data` | Database and local state |
| `PRIVO_PUBLIC_URL` | unset | External base URL; https unless loopback |
| `PRIVO_LOG_LEVEL` | `info` | debug, info, warn, error |
| `PRIVO_LOG_FORMAT` | `json` | json or text |
| `PRIVO_SHUTDOWN_TIMEOUT` | `30s` | Graceful drain on SIGTERM |
| `PRIVO_MASTER_KEY_FILE` | required | File holding the base64 master key (preferred over `PRIVO_MASTER_KEY`) |
| `PRIVO_APPROVAL_HOLD` | `25s` | How long a call waits for an approval decision |
| `PRIVO_UPSTREAM_CALL_TIMEOUT` | `10m` | How long one call to an upstream server may take before the caller is told it did not answer |
| `PRIVO_UPSTREAM_MAX_RESPONSE_BYTES` | `16777216` | Largest reply (a JSON body, or one event of an event stream) accepted from an HTTP or SSE upstream, 1024 to 1 GiB; a larger one fails the call and the connection is re-established. Stdio servers are capped at 16 MiB per message by the MCP SDK |
| `PRIVO_UPSTREAM_REFRESH_GAP` | `5s` | Least time between two refreshes of one server's tool, prompt and resource lists, however often it announces a change |
| `PRIVO_TOKEN_TTL` | `2160h` | Default personal token lifetime |
| `PRIVO_EGRESS_ALLOW_CIDRS` | unset | Internal networks remote servers may use (self-hosted only) |
| `PRIVO_BOOTSTRAP_TOKEN_FILE` / `PRIVO_BOOTSTRAP_TOKEN` | unset | First boot only: a token from `privo token generate`, installed once as the owner's control token (expires after 24h). Prefer the file form: the plain variable stays readable in `/proc/<pid>/environ` (a warning says so). Remove after first use |

Endpoints: `/` (static landing page), `/mcp` (MCP gateway), `/api/v1/...`
(admin API), `/livez`, `/readyz`, `/version` (the semantic version, see `docs/versioning.md`).

## Tokens

Privo has two kinds of personal token (ADR 0014):

- **Gateway tokens** go in AI clients (Claude Code, Cursor, Amp...) and work
  only on `/mcp`.
- **Control tokens** are for people administering Privo and work only on the
  admin API. Approvers only get control tokens.

First setup, where you have a shell on the server:

```sh
privo token create --name you --role owner --kind control       # admin API
privo token create --name you-agent --role owner --kind gateway --person you   # AI client
```

Where you have no shell (the distroless image on Coolify), generate a token on
your own machine and give it to the first boot instead; see
`docs/deploy-coolify.md`:

```sh
go run ./cmd/privo token generate     # prints the token; keep it, it is not shown again
```
