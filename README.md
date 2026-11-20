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
make acceptance # black-box suite against the binary
make ui-check   # the admin app: types, tests, and web/dist matches the source (Node 22)
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

Endpoints: `/` (home page: **Sign in** to the admin site and the MCP address for AI clients), `/mcp` (MCP gateway), `/api/v1/...`
(admin API), `/admin/...` (the admin site, with `/auth/...`, `/connect/...` and
`/oauth/client-metadata.json`; see below), `/livez`, `/readyz`, `/version` (the
semantic version, see `docs/versioning.md`).

## Admin site: sign in with GitHub

Everything after the first sign-in happens in the browser at
`<PRIVO_PUBLIC_URL>/admin/`: **Start** (each person's servers and client
setup), **Servers** (add, review tools, turn off), **Teams**, **Rules** (allow,
ask, deny), **People** (invite, roles, remove), **Tokens** (each person makes
tokens for their AI clients, with a ready Claude Code command), **Activity**,
**Approvals** and **Connections**. It is a small
Preact app built into the binary (ADR 0019): moving between pages loads no
page, and every action is a call to the admin API below, run in process as the
signed-in person; the API stays for scripts. Changing the app needs Node 22
and `make ui`; building Privo does not.

People approve calls in a browser, with their GitHub account and no token
(ADR 0016). It is off until you set `PRIVO_GITHUB_CLIENT_ID`,
`PRIVO_GITHUB_CLIENT_SECRET` (or `_FILE`) and `PRIVO_PUBLIC_URL`; until then
`/admin`, `/auth` and `/connect` answer 503 and name what is missing.

1. In GitHub, create an OAuth App (Settings > Developer settings). Its
   authorization callback URL is `<PRIVO_PUBLIC_URL>/auth/github/callback`.
   Privo asks for no scope.
2. Give Privo the client ID and the client secret, restart, and print a setup
   link next to the data directory:

   ```sh
   privo admin setup-link [--person NAME]
   ```

   Open the link within an hour, sign in with the GitHub account that should own
   this Privo, and you are in. The link works once. `--person` is your name in
   Privo (default: your GitHub login in lowercase); self-approval is refused
   between your tokens and your sessions.
3. `/admin/approvals` lists the calls waiting for a decision, with their
   arguments. Owners, admins and approvers can approve or deny. Approving can
   also allow the same tool for that person for 15 minutes, 1 hour or 8 hours
   (a rule with that expiry, shown on Rules; it never outranks a deny that
   already won). **History** lists decided requests. A person sees their own
   waiting requests on Start and can withdraw one.

Only people who were added can sign in; any other GitHub account sees a plain
refusal. See `docs/deploy-coolify.md` for the Coolify steps.

### Invite your team

```sh
privo admin invite --person ada [--role admin | --role member,approver,auditor]
```

prints a link that works once, for 72 hours (also `POST /api/v1/invites` with
`{"person": "ada", "roles": ["member"]}`). Ada opens it and signs in with GitHub;
her account is bound by its numeric GitHub id. She makes the token for her AI
client herself, on the Tokens page: only a token a person makes while signed in
uses their own accounts and teams. Nobody else, owners included, can issue a
token that acts as her.

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

## Teams and the server catalog

Each team sees only the servers given to it (ADR 0020). On the admin site:

- **Teams → New team**: start from a profile (Developers, Designers, Product
  managers, Sales and support) or a blank team, then add its people. A team made
  from a profile shows the catalog's servers for it, each one click away.
- **Servers → Add from the catalog**: 25 hosted MCP servers (GitHub, Linear,
  Sentry, Figma, Canva, Notion, Asana, Slack…), checked with Privo's own
  discovery on 2026-11-03. Pick the teams that may use it. Most register Privo
  by themselves; GitHub, Asana, Slack and HubSpot need an OAuth app of yours,
  and the form shows the callback URL to give them. Every tool is still
  reviewed before anyone sees it.
- **A server's page → Who can use it**: the teams that reach it.
  **Everyone** is built in (every member is in it). A server with no team is
  reachable by nobody.
- **A server's page → Which tools each team sees** (toolsets, ADR 0023): per
  team, all of the server's tools or only the ones you tick. A person in
  several teams sees what any of them sees; a new tool stays hidden from a
  team that sees only some until you tick it. Rules, approvals and the tool
  review still apply to every tool a person sees. API:
  `PATCH /api/v1/servers/<alias>/teams/<slug>` with `{"tools": ["a", "b"]}`,
  or `{"tools": null}` for all.
- **Start** (where members land): their teams, their servers with a Connect
  link where their own account is needed, and the setup of Claude Code,
  Cursor, VS Code, Codex or Windsurf with a token made on the spot.

The gateway lists only the tools of servers the caller's teams reach, and of
those only the ones their teams see, and answers a call on any other tool as
if the name did not exist. Rules can
name a team: subject `team:<slug>`. Existing servers, and servers added through
the API without `"teams"`, are for everyone. API: `GET/POST /api/v1/teams`,
`PATCH/DELETE /api/v1/teams/<slug>`, `POST /api/v1/teams/<slug>/members`
(`{"person": "ada"}`), `DELETE /api/v1/teams/<slug>/members/<person>`,
`POST /api/v1/teams/<slug>/servers` (`{"alias": "figma"}`),
`DELETE /api/v1/teams/<slug>/servers/<alias>`, `"teams": [...]` on
`POST` and `PATCH /api/v1/servers`, `GET /api/v1/catalog`, and
`GET /api/v1/me/tools` for one's own servers.

## Rules on arguments, and rules as a file

A rule can look at what a call does (ADR 0024): **Rules → Add a rule → Only
when the call's arguments…**, for example deny `delete_repo` when `repo` is
`prod`, or ask before `create_payment` when `amount` is one of a list. Tests
are `equals`, `not_equals`, `in`, `matches` (a pattern like `release/*`),
`exists` and `missing`, on a dotted path (`options.force`); all must hold.
They read the arguments exactly as sent to the server, and `"1"` is not `1`.
A grant from an approval only ever turns an "ask" into "allow", never a deny.

**Rules as a file** exports every rule and the default as YAML, and imports a
file after a check that says what would change (`merge` adds, `replace`
replaces everything but grants; one bad rule refuses the whole file). API:
`GET /api/v1/policies/export`, `POST /api/v1/policies/import` with
`{"yaml": "...", "mode": "merge", "dry_run": true}`.

## The audit trail

Every call and every admin change is recorded before it runs (ADR 0006).
Every few seconds Privo seals new records into blocks, each linked to the
one before and signed with a key derived from the master key, which never
touches the database (ADR 0025). A changed, deleted or reordered row, or a
forged block, is found by:

- **Activity → Check the record** (or `GET /api/v1/audit/integrity`);
- `privo audit verify` on the host (exit 1 on a problem).

Keep the signed checkpoints outside Privo (`GET /api/v1/audit/checkpoints`,
public keys at `GET /api/v1/audit/keys`) to prove later that nothing was
rewritten. **Activity** filters by person, server, action, decision and
date, loads more, and exports CSV (`GET /api/v1/audit/export`, at most
10,000 rows, without recorded arguments). Those who manage the audit set a
retention in days (`PATCH /api/v1/settings/audit` with `retention_days`;
whole sealed blocks go, the rest still checks) and erase a person's
recorded arguments (`POST /api/v1/audit/erase`), which keeps the record
checkable.

## Roles

A person holds a set of roles (ADR 0021): **owner** or **admin** alone, or
any of **member** (uses tools through an AI client), **approver** (decides
other people's approvals) and **auditor** (reads the activity). A member who
also approves keeps using their AI client and decides everyone's calls but
their own, in the browser. Change them on **People**, or with
`PATCH /api/v1/members/<person>` and `{"roles": ["member", "approver"]}`.
Owners change any set; admins change sets of member, approver and auditor for
people who are not owners or admins; nobody changes their own. Changes apply to
the person's next request, and rules for `role:<role>` follow them. A person's
own AI client token carries `member` and only uses the gateway, whatever roles
they hold; while none of their roles uses the gateway it reaches nothing. The
single `"role"` of the member API is still read and returned for one release.

## Tokens

Privo has two kinds of personal token (ADR 0014):

- **Gateway tokens** go in AI clients (Claude Code, Cursor, Amp...) and work
  only on `/mcp`.
- **Control tokens** are for people administering Privo and work only on the
  admin API. Approvers only get control tokens.

First setup, where you have a shell on the server:

```sh
privo token create --name you --role owner --kind control       # admin API
privo token create --name ci-agent --role member --kind gateway --person ci   # a service's AI client
```

`--person` (and `person` in `POST /api/v1/tokens`) names the service or script
a token is for; it defaults to the token's name and may not be a member's name.
Such a token reaches the `everyone` team and no one's own accounts. People who
sign in make their own AI client tokens on the Tokens page; those are bound to
them.

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
