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
| `PRIVO_CONFIG_FILE` | unset | Path of a configuration file (same as `--config FILE` before the command); see below |
| `PRIVO_LISTEN` | `:8080` | Listen address |
| `PRIVO_DATA_DIR` | `./data` | Database and local state |
| `PRIVO_PUBLIC_URL` | unset | External base URL; https unless loopback |
| `PRIVO_LOG_LEVEL` | `info` | debug, info, warn, error |
| `PRIVO_LOG_FORMAT` | `json` | json or text |
| `PRIVO_SHUTDOWN_TIMEOUT` | `30s` | Graceful drain on SIGTERM |
| `PRIVO_MASTER_KEY_FILE` | required | File holding the base64 master key (preferred over `PRIVO_MASTER_KEY`) |
| `PRIVO_MASTER_KEY` | unset | The master key itself, base64; read by `serve`, removed from the process environment, never accepted in the config file. Prefer the file |
| `PRIVO_MASTER_KEY_PREVIOUS_FILE` / `PRIVO_MASTER_KEY_PREVIOUS` | unset | During key rotation only: the key being retired |
| `PRIVO_APPROVAL_HOLD` | `25s` | How long a call waits for an approval decision |
| `PRIVO_UPSTREAM_CALL_TIMEOUT` | `10m` | How long one call to an upstream server may take before the caller is told it did not answer |
| `PRIVO_UPSTREAM_MAX_RESPONSE_BYTES` | `16777216` | Largest reply (a JSON body, or one event of an event stream) accepted from an HTTP or SSE upstream, 1024 to 1 GiB; a larger one fails the call and the connection is re-established. Stdio servers are capped at 16 MiB per message by the MCP SDK |
| `PRIVO_UPSTREAM_REFRESH_GAP` | `5s` | Least time between two refreshes of one server's tool, prompt and resource lists, however often it announces a change |
| `PRIVO_BACKUP_INTERVAL` | unset (off) | Write a database snapshot this often (`24h`); see `docs/deploy-coolify.md`, Backups |
| `PRIVO_BACKUP_KEEP` | `7` | Snapshots to keep (1 to 1000) |
| `PRIVO_BACKUP_DIR` | `<data dir>/backups` | Where snapshots go |
| `PRIVO_TOKEN_TTL` | `2160h` | Default personal token lifetime |
| `PRIVO_UPSTREAM_RETRY_BASE` / `PRIVO_UPSTREAM_RETRY_MAX` | `2s` / `30s` | Backoff bounds when an upstream server drops and is reconnected |
| `PRIVO_RUNNER` / `PRIVO_INSECURE_LOCAL_RUNNER` | unset | Stdio servers: empty refuses them; `local` runs them unisolated on this host and needs `PRIVO_INSECURE_LOCAL_RUNNER=i-understand` (development only, ADR 0007) |
| `PRIVO_EGRESS_ALLOW_CIDRS` | unset | Internal networks remote servers may use (self-hosted only) |
| `PRIVO_BOOTSTRAP_TOKEN_FILE` / `PRIVO_BOOTSTRAP_TOKEN` | unset | First boot only: a token from `privo token generate`, installed once as the owner's control token (expires after 24h). Prefer the file form: the plain variable stays readable in `/proc/<pid>/environ` (a warning says so). Remove after first use |
| `PRIVO_GITHUB_CLIENT_ID` | unset | Client ID of the GitHub OAuth App that signs people in to the admin site (ADR 0016). Web sign-in is on only when this, the client secret and `PRIVO_PUBLIC_URL` are all set; otherwise `/admin` and `/auth` answer 503 |
| `PRIVO_GITHUB_CLIENT_SECRET_FILE` / `PRIVO_GITHUB_CLIENT_SECRET` | unset | The OAuth App's client secret. Prefer the file form (a Coolify file mount); the plain variable is never accepted in the config file |
| `PRIVO_GITHUB_BASE_URL` / `PRIVO_GITHUB_API_URL` | `https://github.com` / `https://api.github.com` | Where people sign in and where the profile is read; set both for GitHub Enterprise Server (https unless loopback; a private address needs `PRIVO_EGRESS_ALLOW_CIDRS`) |
| `PRIVO_SESSION_IDLE_TIMEOUT` | `2h` | A browser session ends after this long without a request (1m to 24h) |
| `PRIVO_SESSION_MAX_AGE` | `12h` | A browser session ends this long after sign-in whatever happens (5m to 720h) |

### Configuration file

Every setting above can also live in a YAML file (ADR 0015, a commented
template is in `docs/privo.example.yaml`):

```yaml
listen: ":8080"
data_dir: /data
public_url: https://app.example.com
master_key_file: /run/secrets/privo_master_key
backup_interval: 24h
```

```sh
privo --config /etc/privo/privo.yaml serve     # or PRIVO_CONFIG_FILE=/etc/privo/privo.yaml
privo --config /etc/privo/privo.yaml config show   # every setting, its value and its source
```

A key is the variable name in lower case without `PRIVO_`. The environment
beats the file, which beats the default. Nothing is read unless the file is
named; a named file that is missing, malformed, has an unknown or repeated key,
or holds a secret (`master_key`, `master_key_previous`, `bootstrap_token`:
name a file with the `_file` form instead) stops the command with exit 2. The
file is read once at start; restart to change a setting. In a container use
`PRIVO_CONFIG_FILE`, so the image's healthcheck reads the same file.

Endpoints: `/` (static landing page), `/mcp` (MCP gateway), `/api/v1/...`
(admin API), `/livez`, `/readyz`, `/version` (the semantic version, see `docs/versioning.md`).

## Admin site: sign in with GitHub

Everything after the first sign-in happens in the browser at
`<PRIVO_PUBLIC_URL>/admin/`: **Servers** (add, review tools, turn off),
**Rules** (allow, ask, deny), **People** (invite, roles, remove), **Tokens**
(each person makes tokens for their AI clients, with a ready Claude Code
command), **Activity**, **Approvals** and **Connections**. The pages call the
admin API below in process, as the signed-in person (ADR 0018); the API stays
for scripts.


People approve calls in a browser, with their GitHub account and no token
(ADR 0016). It is off until you set `PRIVO_GITHUB_CLIENT_ID`,
`PRIVO_GITHUB_CLIENT_SECRET` (or `_FILE`) and `PRIVO_PUBLIC_URL`; until then
`/admin` answers 503 and names what is missing.

1. In GitHub, create an OAuth App (Settings > Developer settings). Its
   authorization callback URL is `<PRIVO_PUBLIC_URL>/auth/github/callback`.
   Privo asks for no scope.
2. Give Privo the client ID and the client secret, restart, and print a setup
   link next to the data directory:

   ```sh
   privo admin setup-link [--person NAME]
   ```

   Open the link within an hour, sign in with the GitHub account that should own
   this Privo, and you are in. The link works once. `--person` is the name your
   tokens carry (default: your GitHub login in lowercase); self-approval is
   refused between tokens and sessions of the same person.
3. `/admin/approvals` lists the calls waiting for a decision, with their
   arguments. Owners, admins and approvers can approve or deny.

Only people who were added can sign in; any other GitHub account sees a plain
refusal. See `docs/deploy-coolify.md` for the Coolify steps.

### Invite your team

```sh
privo admin invite --person ada [--role member|approver|auditor|admin]
```

prints a link that works once, for 72 hours (also `POST /api/v1/invites` with
`{"person": "ada", "role": "member"}`). Ada opens it and signs in with GitHub;
her account is bound by its numeric GitHub id. Give her AI client a gateway
token whose person is `ada`.

## Servers where each person uses their own account

Remote MCP servers such as GitHub, Linear, Notion, Atlassian or Sentry let each
person sign in with their own account (OAuth). Privo keeps one grant per person
and server, encrypted, refreshes it, and runs every call with the caller's own
grant, so the upstream's permissions and audit stay honest (ADR 0017). This
needs the admin site above.

```sh
curl -X POST $PRIVO/api/v1/servers -H "Authorization: Bearer $OWNER" -d '{
  "alias": "linear", "transport": "http", "url": "https://mcp.linear.app/mcp",
  "oauth": {}
}'
```

Privo finds the server's authorization server and registers itself: with the
client id you give (`"oauth": {"client_id": "...", "client_secret": "..."}`,
needed for GitHub, whose OAuth app gets the callback URL
`<PRIVO_PUBLIC_URL>/connect/callback`), with its client ID metadata document
(`<PRIVO_PUBLIC_URL>/oauth/client-metadata.json`), or by dynamic registration.
`"scopes"` and `"issuer"` are optional.

Then an admin connects their own account at `/connect/<alias>`; the server's
tool list is read with it. Everyone else connects theirs from **Connections**
on the admin site, or follows the link their AI client shows on the first call.
A person disconnects there; an admin lists and removes connections with
`GET /api/v1/servers/<alias>/connections` and
`DELETE /api/v1/servers/<alias>/connections/<person>`.

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

Backups and restore (ADR 0010, `docs/deploy-coolify.md`):

```sh
privo backup [--to FILE|DIR] [--keep N]    # consistent copy of the live database; no downtime
privo restore --from FILE [--force]        # into an empty data directory; --force keeps the old files aside
```

Where you have no shell (the distroless image on Coolify), generate a token on
your own machine and give it to the first boot instead; see
`docs/deploy-coolify.md`:

```sh
go run ./cmd/privo token generate     # prints the token; keep it, it is not shown again
```
