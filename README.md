# Privo

The team gateway to AI work and the MCP world: one endpoint per organisation in
front of every MCP server, with policies, human approvals, encrypted
credentials and a tamper-evident audit trail.

Status: early development. Architecture is settled in `docs/adr/`; progress
per feature is in `docs/todo.md`.

## Build

Requires Go 1.26 or later; `go.mod` pins the toolchain (1.27.4 or later: GO-2026-6859).

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
| `PRIVO_TELEMETRY_RETENTION_DAYS` | `30` | How long call statistics and the diagnostic log behind the Activity page are kept, 0 to 400 days; `0` records none (ADR 0024). Nothing leaves the server |
| `PRIVO_METRICS_ADDR` | unset (off) | Serve Prometheus metrics at `/metrics` on this address, such as `127.0.0.1:9464`. No sign-in: keep it private (localhost or an internal network), never the public port |
| `PRIVO_TOKEN_TTL` | `2160h` | Default personal token lifetime |
| `PRIVO_UPSTREAM_RETRY_BASE` / `PRIVO_UPSTREAM_RETRY_MAX` | `2s` / `30s` | Backoff bounds when an upstream server drops and is reconnected |
| `PRIVO_RUNNER` / `PRIVO_INSECURE_LOCAL_RUNNER` | unset | Stdio servers: empty refuses them; `local` runs them unisolated on this host and needs `PRIVO_INSECURE_LOCAL_RUNNER=i-understand` (development only, ADR 0007) |
| `PRIVO_EGRESS_ALLOW_CIDRS` | unset | Internal networks remote servers may use (self-hosted only) |
| `PRIVO_TRUSTED_PROXIES` | unset | Reverse proxies in front of Privo, as CIDRs (for example `10.0.0.0/8,172.16.0.0/12` for the Docker networks of Coolify's Traefik). Per-address limits then count the client named in `X-Forwarded-For` instead of the proxy. Never list an address a client can reach Privo from directly |
| `PRIVO_BOOTSTRAP_TOKEN_FILE` / `PRIVO_BOOTSTRAP_TOKEN` | unset | First boot only: a token from `privo token generate`, installed once as the owner's control token (expires after 24h). Prefer the file form: the plain variable stays readable in `/proc/<pid>/environ` (a warning says so). Remove after first use |
| `PRIVO_GITHUB_CLIENT_ID` | unset | Client ID of the GitHub OAuth App that signs people in to the admin site (ADR 0016). Web sign-in is on only when this, the client secret and `PRIVO_PUBLIC_URL` are all set; otherwise `/admin` and `/auth` answer 503 |
| `PRIVO_GITHUB_CLIENT_SECRET_FILE` / `PRIVO_GITHUB_CLIENT_SECRET` | unset | The OAuth App's client secret. Prefer the file form (a Coolify file mount); the plain variable is never accepted in the config file |
| `PRIVO_GITHUB_BASE_URL` / `PRIVO_GITHUB_API_URL` | `https://github.com` / `https://api.github.com` | Where people sign in and where the profile is read; set both for GitHub Enterprise Server (https unless loopback; a private address needs `PRIVO_EGRESS_ALLOW_CIDRS`) |
| `PRIVO_SESSION_IDLE_TIMEOUT` | `2h` | A browser session ends after this long without a request (1m to 24h) |
| `PRIVO_SESSION_MAX_AGE` | `12h` | A browser session ends this long after sign-in whatever happens (5m to 720h) |
| `PRIVO_OAUTH_SERVER` | `off` | `on` lets AI clients sign in to the gateway with OAuth instead of a pasted token (ADR 0022): Privo becomes the authorization server for `/mcp`. Needs web sign-in. Off, `/oauth` answers 503 and personal tokens are the only way in |
| `PRIVO_OAUTH_CLIENTS` | `known` | Which AI clients may ask a member for access: `known` (Privo's built-in list plus the ones an administrator approves on the Clients page), `approved` (only approved ones) or `any` (every client that publishes a metadata document) |
| `PRIVO_OAUTH_DYNAMIC_REGISTRATION` | `off` | `on` lets a client register itself (RFC 7591), which some clients need because they publish no metadata document. Registrations are limited, labelled "not verified" on the consent page and deleted when unused |

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
   this Privo, and you are in. The link works once. `--person` is the name your
   tokens carry (default: your GitHub login in lowercase); self-approval is
   refused between tokens and sessions of the same person.
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
her account is bound by its numeric GitHub id. She then makes a token for each
of her AI clients on the **Tokens** page.

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

**A server that does not say where its people sign in** (no protected
resource metadata, common before the MCP authorization spec): give its
authorization server, `"oauth": {"issuer": "https://login.example.com"}`. If
that publishes no metadata either, give its endpoints and a client id:
`"oauth": {"authorization_endpoint": "...", "token_endpoint": "...",
"client_id": "...", "client_secret": "..."}` (ADR 0023). `"resource"` is sent
only when the server names one or you give it.

### Each person's own API key (0.9.0)

For a server that takes an API key where each person has their own (ADR
0023):

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
over the tool list. The header cannot be one that names the caller to a server
or a proxy (such as `X-Forwarded-User` or `Remote-User`), or `Cookie`.

For OAuth settings entered by hand without an issuer, an answer that names an
issuer is refused (a mix-up defence); if your authorization server sends `iss`,
enter its issuer. Endpoints by hand are refused for a server that publishes its
own sign-in metadata.

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
- **A server's page → Which tools each team sees** (toolsets, ADR 0026): per
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

## AI clients sign in with OAuth (0.8.0, off unless switched on)

With `PRIVO_OAUTH_SERVER=on` an AI client needs no token at all (ADR 0022).
Give it the gateway's address, `https://<privo>/mcp`; it finds Privo's
authorization server by itself, opens a browser, the person signs in to Privo
(with GitHub, as for the admin site), sees a page that names the client and
where it will send them back, and presses **Allow**. The client then calls as
that member: their teams' servers and their own accounts.

- **Which clients may ask** (`PRIVO_OAUTH_CLIENTS`): `known` (default)
  accepts Privo's built-in list (Claude Code, Claude on the web and desktop,
  VS Code, ChatGPT, Codex, GitHub Copilot CLI, Zed, Antigravity) plus any
  client an administrator approves on **Clients** by the address of its
  metadata document; `approved` accepts only approved ones; `any` accepts
  every client that publishes a metadata document. A member still allows
  each client on a page that names it, every time.
- **Clients that register themselves** (Cursor, Gemini CLI, Mistral) need
  `PRIVO_OAUTH_DYNAMIC_REGISTRATION=on`. They are shown as "registered
  itself, not verified", limited per address and in total, and removed when
  unused.
- **Tokens:** access tokens live an hour and work only on `/mcp`, never on
  the admin API. A refresh token rotates on every use; a refresh token that
  was already used, presented again, ends that client's access at once (no
  grace window). A client that is not used for 30 days, or 90 days after the
  person allowed it, has to ask again.
- **Taking access away:** each person on **Tokens → AI clients that signed
  in**; an administrator for everyone on **Clients**. Removing a member, or
  giving them a role that cannot use the gateway, ends their clients' access.
  Every case counts within 2 seconds.
- The protected resource metadata names the authorization server only while
  it is on. Off, `/oauth/authorize`, `/oauth/token`, `/oauth/revoke` and
  `/oauth/register` answer 503, and personal tokens are the only way in.
- The hosted Claude apps and ChatGPT must reach Privo from their own servers,
  and Privo must be allowed to fetch their metadata documents (public
  addresses only, through the egress policy).

Personal tokens keep working for clients without OAuth and for scripts.

## Activity: how calls went (0.10.0)

**Activity → Overview** shows, for the last hour, day, week or month: how many
calls there were, how many failed, were tool errors or were refused, the
median and p95 time, a chart over the window, and a table by server, tool,
member or client with p50/p95/p99, the time spent getting the server's session
(including a sign-in refresh), the time the server itself took, and the
average size of what it sent back (what the model reads). **Log** lists what
happened to connections, sign-ins and keys, the audit and the process: a
server that failed to connect and came back, a grant the provider refused, a
person's key the server refused; the same event within a minute is one line
with a count. **Tool calls** and **Admin changes** are the audit as before.

These statistics are kept apart from the audit (ADR 0024): one row per call
with its phases and sizes, never its arguments, result or error text. They are
best effort (a full queue drops a row and counts it rather than slow a call;
counts cover every call, and times come from the most recent 100,000 calls
in a window; the time a person takes to approve is kept out of latency),
kept `PRIVO_TELEMETRY_RETENTION_DAYS` days (30 by default; `0` turns them off),
and nothing leaves the server. Owners, admins and auditors see them, like the
audit. The same figures come from the admin API under `/api/v1/telemetry/`
(`summary`, `timeseries`, `calls`, `logs`, `storage`).

For Prometheus and Grafana, set `PRIVO_METRICS_ADDR=127.0.0.1:9464` (or an
address on an internal network): `/metrics` there gives
`privo_mcp_calls_total{server,method,outcome}`,
`privo_mcp_call_duration_seconds` (a histogram by server and method),
`privo_upstream_up{server}`, `privo_audit_events_lost_total`,
`privo_telemetry_dropped_total` and `privo_build_info`. It has no sign-in, so
it never shares the public port.

## Rules on arguments, and rules as a file

A rule can look at what a call does (ADR 0027): **Rules → Add a rule → Only
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

A gateway token **belongs to the member who made it** on the Tokens page,
signed in (ADR 0021). Only such a token uses that member's teams and their own
accounts on OAuth servers, and the audit names the member with every call
(`user`). A token issued any other way (`POST /api/v1/tokens`,
`privo token create`) belongs to nobody, whatever `person` it names: it
reaches the servers everyone can use and never anyone's account. `person` is a
label for approvals and the audit. The lists show which is which (`bound`).

First setup, where you have a shell on the server:

```sh
privo token create --name you --role owner --kind control       # admin API
privo token create --name ci --role member --kind gateway        # a script: servers everyone can use
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
