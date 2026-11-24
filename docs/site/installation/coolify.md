# Deploy on Coolify

Privo runs on [Coolify](https://coolify.io/) as a single distroless, non-root
(`65532:65532`) container. Replace `app.example.com` below with your domain.

## 1. The application

| Setting | Value |
|---|---|
| Build / Image | Repository `Dockerfile` (base directory `/`), or the release image once published |
| Port | `8080` |
| Persistent volume | `/data` (SQLite database, vault, snapshots) |
| Health check | Container `HEALTHCHECK` (`privo healthcheck`); keep Coolify's HTTP check **off** (no `curl` in image) |
| Rolling updates | **Off** (consistent container name **on**): only one replica may hold the SQLite lease; expect a few seconds of downtime per deploy |
| Domain | `https://app.example.com` (must use `https://` so Traefik terminates TLS) |

## 2. Secrets and environment

In **Persistent Storage**, add two **file mounts** (readable by UID `65532`):

- `/run/secrets/privo_master_key`: output of `openssl rand -base64 32`
- `/run/secrets/privo_github_client_secret`: client secret from step 3

In **Environment Variables**, set:

```ini
PRIVO_DATA_DIR=/data
PRIVO_LISTEN=:8080
PRIVO_PUBLIC_URL=https://app.example.com
PRIVO_MASTER_KEY_FILE=/run/secrets/privo_master_key
PRIVO_GITHUB_CLIENT_ID=<your-github-oauth-client-id>
PRIVO_GITHUB_CLIENT_SECRET_FILE=/run/secrets/privo_github_client_secret
PRIVO_TRUSTED_PROXIES=10.0.0.0/8,172.16.0.0/12
PRIVO_BACKUP_INTERVAL=24h
```

!!! warning "Back up the master key"
    Save `/run/secrets/privo_master_key` in your password manager. Without it,
    stored credentials in `/data` and backups cannot be read. To rotate it
    later, mount the retiring key at `PRIVO_MASTER_KEY_PREVIOUS_FILE` first.

See the [configuration reference](configuration.md) for every setting (including
`PRIVO_GITHUB_ORG_IDS` and GitHub Enterprise Server).

## Sign in with GitHub

1. In GitHub (**Settings → Developer settings → OAuth Apps → New OAuth App**),
   set Homepage URL `https://app.example.com` and Callback URL
   `https://app.example.com/auth/github/callback` (device flow **off**).
2. Put the **Client ID** and **Client Secret** in Coolify (step 2 above) and
   **Deploy**. Check logs for `admin site is on`.
3. On the Coolify host, print the one-time owner setup link:

   ```sh
   docker exec <container> /privo admin setup-link
   ```

4. Open the printed URL within 1 hour and choose **Continue with GitHub**.

!!! warning
    Treat the setup link as a secret until claimed (it expires after 1 hour
    and works once). Enable GitHub 2FA for accounts that approve tool calls.

Everything else now happens in the browser at `https://app.example.com/admin`:
add servers, rules, invite teammates, and mint AI client tokens on **Tokens**
(no `curl` or bootstrap token needed). For per-user GitHub MCP connections,
create a *second* OAuth App with callback
`https://app.example.com/connect/callback` ([Each person's own account](../using/connections.md)).

## Optional setup

### Settings: environment or a configuration file

Instead of environment variables, mount [a YAML file](configuration-file.md)
(for example at `/etc/privo/privo.yaml` via **Persistent Storage**) and set
`PRIVO_CONFIG_FILE=/etc/privo/privo.yaml` (not `--config`, so `healthcheck`
reads the same file). Environment variables still override the file; inspect
effective values with `docker exec <container> /privo config show`.

### AI clients that sign in with OAuth

To let AI clients sign in directly without pasted tokens
([AI clients that sign in with OAuth](../using/oauth-clients.md)):

1. Set `PRIVO_OAUTH_SERVER=on` (requires `PRIVO_PUBLIC_URL` with no path and
   working web sign-in) and redeploy.
2. Optional: `PRIVO_OAUTH_CLIENTS=approved|any` (default `known`);
   `PRIVO_OAUTH_DYNAMIC_REGISTRATION=on` for Cursor, Gemini CLI and Mistral.

### Metrics

Set `PRIVO_METRICS_ADDR=0.0.0.0:9464` to let Prometheus scrape `/metrics` on
an internal Docker network. **Never** publish port `9464` or attach a domain
to it — `/metrics` has no authentication.

### First run: your first admin token

*Only needed for headless, API-only installs without browser sign-in or host
SSH.*

1. Generate a one-time owner control token locally:
   `go run ./cmd/privo token generate`
2. Mount it as a file at `PRIVO_BOOTSTRAP_TOKEN_FILE` and deploy (valid for 24
   hours on `/api/v1` only).
3. Mint your permanent token and revoke the bootstrap token:

   ```sh
   read -rs BOOT
   URL=https://app.example.com
   curl -sS -H "Authorization: Bearer $BOOT" $URL/api/v1/tokens
   curl -sS -X POST -H "Authorization: Bearer $BOOT" -H 'Content-Type: application/json' \
     -d '{"name":"owner","role":"owner","kind":"control","person":"your name"}' $URL/api/v1/tokens
   read -rs OWNER
   curl -sS -X DELETE -H "Authorization: Bearer $OWNER" $URL/api/v1/tokens/<bootstrap-id>
   ```

4. Remove `PRIVO_BOOTSTRAP_TOKEN_FILE` from Coolify and redeploy. (If 24 hours
   pass unused, run `docker exec <container> /privo token create --name owner
   --role owner --kind control` over SSH instead.)

Next: [Backup, restore and upgrades](operations.md).
