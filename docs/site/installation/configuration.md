# Configuration reference

Every setting Privo reads, with its default. Set each one as an environment
variable, or in [a configuration file](configuration-file.md) under its key:
the name in lower case without `PRIVO_` (`PRIVO_BACKUP_INTERVAL` is
`backup_interval`). The environment beats the file, which beats the default.
`privo config show` prints every setting, its value and where it comes from.

This table is the one list of settings. A test fails when the code reads a
setting that is not here, or when this page names one the code does not know.

!!! note
    A secret (`PRIVO_MASTER_KEY`, `PRIVO_MASTER_KEY_PREVIOUS`,
    `PRIVO_BOOTSTRAP_TOKEN`, `PRIVO_GITHUB_CLIENT_SECRET`) has a `_FILE` form
    that names a file holding it. Prefer the file: an environment variable
    stays readable in `/proc/<pid>/environ` for the life of the process. The
    configuration file never accepts the secret itself.

| Variable | Default | Meaning |
| --- | --- | --- |
| `PRIVO_CONFIG_FILE` | unset | Path of a configuration file (same as `--config FILE` before the command); see [The configuration file](configuration-file.md) |
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
| `PRIVO_BACKUP_INTERVAL` | unset (off) | Write a database snapshot this often (`24h`); see [Backups](operations.md#backups) |
| `PRIVO_BACKUP_KEEP` | `7` | Snapshots to keep (1 to 1000) |
| `PRIVO_BACKUP_DIR` | `<data dir>/backups` | Where snapshots go |
| `PRIVO_TELEMETRY_RETENTION_DAYS` | `30` | How long call statistics and the diagnostic log behind the Activity page are kept, 0 to 400 days; `0` records none ([ADR 0024](https://github.com/talk2sohail/privoclub/blob/main/docs/adr/0024-call-statistics-and-diagnostic-log.md)). Nothing leaves the server |
| `PRIVO_METRICS_ADDR` | unset (off) | Serve Prometheus metrics at `/metrics` on this address, such as `127.0.0.1:9464`. No sign-in: keep it private (localhost or an internal network), never the public port |
| `PRIVO_TOKEN_TTL` | `2160h` | Default personal token lifetime |
| `PRIVO_UPSTREAM_RETRY_BASE` / `PRIVO_UPSTREAM_RETRY_MAX` | `2s` / `30s` | Backoff bounds when an upstream server drops and is reconnected |
| `PRIVO_RATE_LIMIT_TOKEN_RPM` | `120` | Requests per minute allowed per bearer token on `/mcp`; `0` disables |
| `PRIVO_RATE_LIMIT_USER_RPM` | `300` | Requests per minute allowed per member on `/mcp` (only applies to tokens bound to a member); `0` disables |
| `PRIVO_RATE_LIMIT_ORG_RPM` | `1200` | Requests per minute allowed per organisation on `/mcp`; `0` disables |
| `PRIVO_MAX_INFLIGHT` | `256` | Maximum concurrent `/mcp` requests across the whole gateway before shedding with `503`; `0` disables |
| `PRIVO_ORG_MAX_INFLIGHT` | `128` | Maximum concurrent `/mcp` requests per organisation before shedding with `503`; `0` disables |
| `PRIVO_UPSTREAM_MAX_INFLIGHT` | `32` | Maximum concurrent upstream calls per organisation and upstream server (bulkhead); `0` disables |
| `PRIVO_CIRCUIT_BREAKER_THRESHOLD` | `5` | Consecutive upstream transport, timeout, 5xx or unavailable failures before opening a server's circuit breaker; `0` disables |
| `PRIVO_CIRCUIT_BREAKER_COOLDOWN` | `30s` | How long an open circuit breaker stays open before allowing one half-open probe (at least `1s` when `PRIVO_CIRCUIT_BREAKER_THRESHOLD` > 0) |
| `PRIVO_RUNNER` / `PRIVO_INSECURE_LOCAL_RUNNER` | unset | Stdio servers: empty refuses them; `local` runs them unisolated on this host and needs `PRIVO_INSECURE_LOCAL_RUNNER=i-understand` (development only, ADR 0007) |
| `PRIVO_EGRESS_ALLOW_CIDRS` | unset | Internal networks remote servers may use (self-hosted only) |
| `PRIVO_TRUSTED_PROXIES` | unset | Reverse proxies in front of Privo, as CIDRs (for example `10.0.0.0/8,172.16.0.0/12` for the Docker networks of Coolify's Traefik). Per-address limits then count the client named in `X-Forwarded-For` instead of the proxy. Never list an address a client can reach Privo from directly |
| `PRIVO_BOOTSTRAP_TOKEN_FILE` / `PRIVO_BOOTSTRAP_TOKEN` | unset | First boot only: a token from `privo token generate`, installed once as the owner's control token (expires after 24h). Prefer the file form: the plain variable stays readable in `/proc/<pid>/environ` (a warning says so). Remove after first use |
| `PRIVO_GITHUB_CLIENT_ID` | unset | Client ID of the GitHub OAuth App that signs people in to the admin site ([ADR 0016](https://github.com/talk2sohail/privoclub/blob/main/docs/adr/0016-admin-sign-in-with-github.md)). Web sign-in is on only when this, the client secret and `PRIVO_PUBLIC_URL` are all set; otherwise `/admin` and `/auth` answer 503 |
| `PRIVO_GITHUB_CLIENT_SECRET_FILE` / `PRIVO_GITHUB_CLIENT_SECRET` | unset | The OAuth App's client secret. Prefer the file form (a Coolify file mount); the plain variable is never accepted in the config file |
| `PRIVO_GITHUB_BASE_URL` / `PRIVO_GITHUB_API_URL` | `https://github.com` / `https://api.github.com` | Where people sign in and where the profile is read; set both for GitHub Enterprise Server (https unless loopback; a private address needs `PRIVO_EGRESS_ALLOW_CIDRS`) |
| `PRIVO_GITHUB_ORG_IDS` | unset | Numeric ids of the GitHub organisations whose members may sign in, comma-separated (0.19.0); `https://api.github.com/orgs/<name>` shows an organisation's `id`. Set, sign-in asks GitHub for `read:org` and refuses anyone in none of them, invite or not. An organisation that restricts OAuth App access must allow Privo's app, or its members are refused. Membership is checked at each sign-in only: when someone leaves the organisation, remove them on **People**, which ends their sessions, tokens and connections at once |
| `PRIVO_SESSION_IDLE_TIMEOUT` | `2h` | A browser session ends after this long without a request (1m to 24h) |
| `PRIVO_SESSION_MAX_AGE` | `12h` | A browser session ends this long after sign-in whatever happens (5m to 720h) |
| `PRIVO_OAUTH_SERVER` | `off` | `on` lets AI clients sign in to the gateway with OAuth instead of a pasted token ([ADR 0022](https://github.com/talk2sohail/privoclub/blob/main/docs/adr/0022-gateway-sign-in-with-oauth.md)): Privo becomes the authorization server for `/mcp`. Needs web sign-in. Off, `/oauth` answers 503 and personal tokens are the only way in |
| `PRIVO_OAUTH_CLIENTS` | `known` | Which AI clients may ask a member for access: `known` (Privo's built-in list plus the ones an administrator approves on the Clients page), `approved` (only approved ones) or `any` (every client that publishes a metadata document) |
| `PRIVO_OAUTH_DYNAMIC_REGISTRATION` | `off` | `on` lets a client register itself (RFC 7591), which some clients need because they publish no metadata document. Registrations are limited, labelled "not verified" on the consent page and deleted when unused |
