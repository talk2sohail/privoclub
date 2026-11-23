# Deploy on Coolify

Privo runs on [Coolify](https://coolify.io/) from the repository's
`Dockerfile`: a distroless, non-root image with the binary and nothing else.
This page uses `app.example.com` for Privo's public address; put yours in its
place.

## The application

| | |
|---|---|
| Base directory | `/` |
| Dockerfile | `Dockerfile` (Go, distroless, nonroot) |
| Port | 8080 |
| Volume | one volume at `/data` (SQLite, vault) |
| Health | the container's `HEALTHCHECK` (`privo healthcheck`); public `/livez`, `/readyz`. There is no curl in the image, so leave Coolify's own HTTP health check off |
| Rolling updates | **off** (enable a consistent container name): only one gateway may hold the database, so the old container must stop before the new one starts. Expect a few seconds of downtime per deploy |
| Domain | `https://app.example.com`. With `https://`, Coolify's Traefik gets a Let's Encrypt certificate; behind a proxy that connects on 443, `http://` answers `503 no available server` |

## Environment

- `PRIVO_DATA_DIR=/data`
- `PRIVO_LISTEN=:8080`
- `PRIVO_PUBLIC_URL=https://app.example.com`
- `PRIVO_MASTER_KEY_FILE=/run/secrets/privo_master_key`: the key is not an
  environment variable. It is 32 random bytes, base64 (`openssl rand -base64
  32`), held in a file mount (Coolify → your application → Persistent Storage
  → a file at `/run/secrets/privo_master_key`; Coolify keeps it on the host
  under the application's directory). The process reads the file at start; an
  unreadable or invalid file stops the start, so a wrong mount shows up at
  once and never as silently weaker protection.
- `PRIVO_BOOTSTRAP_TOKEN_FILE` or `PRIVO_BOOTSTRAP_TOKEN`: first run only, see
  below. Delete it after you have your own token.
- `PRIVO_GITHUB_CLIENT_ID` and
  `PRIVO_GITHUB_CLIENT_SECRET_FILE=/run/secrets/privo_github_client_secret`:
  sign-in to the admin site, see below. Without them `/admin` answers 503.
- `PRIVO_TRUSTED_PROXIES`: the Docker networks of Coolify's Traefik (for
  example `10.0.0.0/8,172.16.0.0/12`), so per-address limits count the client
  and not the proxy.

!!! warning "Back up the master key"
    Copy the file's content from Coolify → Persistent Storage into your
    password manager. Without it, every stored credential in `/data` is
    unreadable. Never rotate it by just replacing the content: mount the old
    key as a second file and point `PRIVO_MASTER_KEY_PREVIOUS_FILE` at it
    first.

Why a file: an environment variable stays readable in `/proc/<pid>/environ`
for the life of the process, and the gateway logs a warning when the key
arrives that way. The file removes that copy; anyone with access to Coolify or
the host can still read it.

Moving an existing install from the variable to the file: add the file mount
with the same content, set `PRIVO_MASTER_KEY_FILE`, deploy and check the app
is healthy, delete `PRIVO_MASTER_KEY` (the normal and the preview entry),
deploy again and check that the warning is gone from the logs. The file wins
when both are set, so the first deploy is safe.

## Settings: environment or a configuration file

Everything above can stay in Coolify's environment. For settings you would
rather read in one place, Privo also reads [a YAML file](configuration-file.md).
Two parts:

1. Coolify → Persistent Storage → add a **file** mount, for example at
   `/etc/privo/privo.yaml`, and put the settings in it:
   ```yaml
   listen: ":8080"
   data_dir: /data
   public_url: https://app.example.com
   master_key_file: /run/secrets/privo_master_key
   ```
2. Set `PRIVO_CONFIG_FILE=/etc/privo/privo.yaml` in the environment (the
   variable, not `--config`, so the image's health check reads the file too)
   and redeploy.

The environment overrides the file, so a value left in Coolify's environment
still wins; `docker exec <container> /privo config show` lists every setting
with its value and where it comes from (default, file, environment). An
unknown or repeated key, a list, or a missing file stops the start with exit
2 and the reason in the log; nothing falls back to defaults. The file is read
at start only; changing it needs a redeploy. Moving settings across is
optional and can be done one deploy at a time.

## First run: your first admin token

The image has no shell, so Coolify's web terminal cannot run `privo token
create`. Instead the first boot installs a token you generated yourself. The
secret never appears in a log or an API response, and only its hash is stored.

1. On your own machine, from the repository: `go run ./cmd/privo token
   generate`. The token is printed once; keep it in your password manager.
2. Mount the token as a file (Coolify → Persistent Storage → file mount) and
   set `PRIVO_BOOTSTRAP_TOKEN_FILE` to its path. Deploy. You can set
   `PRIVO_BOOTSTRAP_TOKEN` to the value instead, but the variable stays
   readable in `/proc/<pid>/environ` while the process runs, and the log says
   so. A token made by hand, such as a repeated character, is refused at
   startup: use the output of `token generate`.
3. The log shows `bootstrap owner token installed ... token_id=...
   expires=...`. The token is an **owner control token** (admin API only,
   never `/mcp`) and **expires 24 hours after install**.
4. Use it to mint your own token, then revoke it:

   ```sh
   read -rs BOOT                       # paste the bootstrap token; keeps it out of shell history
   URL=https://app.example.com
   curl -sS -H "Authorization: Bearer $BOOT" $URL/api/v1/tokens            # shows the "bootstrap" token and its id
   curl -sS -X POST -H "Authorization: Bearer $BOOT" -H 'Content-Type: application/json' \
        -d '{"name":"owner","role":"owner","kind":"control","person":"your name"}' $URL/api/v1/tokens   # copy "token" from the reply
   read -rs OWNER                      # paste your new token
   curl -sS -X DELETE -H "Authorization: Bearer $OWNER" $URL/api/v1/tokens/<bootstrap token id>
   ```

   Revocation takes effect within about two seconds.
5. Delete the bootstrap setting from Coolify and redeploy. If you forget,
   nothing is exposed beyond the 24 hours, and it never installs again (the
   gateway records that it was used); each boot logs a warning until the
   setting is gone.

To see that the install happened without a shell, read the admin events:
`curl -sS -H "Authorization: Bearer $OWNER" "$URL/api/v1/audit/events?kind=admin"`
lists it as `bootstrap.token.install:<id>` (actor `system`), next to the token
you minted and the one you revoked.

Anyone who can read Coolify's environment can read the variable while it is
set, as they can the master key, so keep step 2 to step 5 short.

**If the 24 hours pass unused**, the bootstrap cannot run again. Use the host:
`docker exec <container> /privo token create --name owner --role owner --kind
control` (the binary runs without a shell; this needs SSH to the server).
`docker ps` shows the container's name.

## Sign in with GitHub

People use the admin site with their GitHub account
([The admin site](../using/admin-site.md)). Nothing here is needed for the
gateway itself; until it is set up, `/admin` answers 503 with the names of the
missing settings.

1. In GitHub, Settings > Developer settings > OAuth Apps > New OAuth App (an
   organisation's settings work too). Homepage URL `https://app.example.com`,
   authorization callback URL `https://app.example.com/auth/github/callback`.
   Leave device flow off. Copy the **Client ID**, then **Generate a new client
   secret** and copy it at once.
2. In Coolify → Persistent Storage, add a file mount at
   `/run/secrets/privo_github_client_secret` holding the client secret (one
   line). The secret is a file for the same reason as the master key. Store a
   copy in your password manager.
3. In Environment Variables add `PRIVO_GITHUB_CLIENT_ID=<the Client ID>` and
   `PRIVO_GITHUB_CLIENT_SECRET_FILE=/run/secrets/privo_github_client_secret`.
   `PRIVO_PUBLIC_URL` must be `https://app.example.com` with no path. Deploy
   and check the log: `admin site is on`.
4. Print the setup link on the host (the binary runs without a shell):
   `docker exec <container> /privo admin setup-link --person <the name your
   tokens carry>`. It prints `https://app.example.com/admin/setup/privo_setup_...`
   once and stores only a hash.
5. Open the link within an hour and choose **Continue with GitHub**. The
   GitHub account you sign in with becomes the owner. The link works once; a
   new link cancels the old one.

!!! warning
    Treat the setup link as a secret until it is used. It sits in the URL
    path, so a proxy's access log may record it; once claimed it is
    worthless, and unclaimed it expires after an hour.

After the claim, everything else is in the browser: add servers, rules, invite
people, and make your AI client's token on **Tokens** (no curl, no bootstrap
token needed). Later sign-ins: `https://app.example.com/admin/signin`.
Sessions end after 2 hours without a request or 12 hours in all
(`PRIVO_SESSION_IDLE_TIMEOUT`, `PRIVO_SESSION_MAX_AGE`). With GitHub
Enterprise Server, set `PRIVO_GITHUB_BASE_URL` and `PRIVO_GITHUB_API_URL`, and
add its network to `PRIVO_EGRESS_ALLOW_CIDRS` if it is private.

Privo cannot require two-factor authentication at GitHub; turn it on for the
accounts that approve calls (an organisation can require it).

## Servers where each person uses their own account

Nothing new to configure: once sign-in with GitHub works, add a remote server
with `"oauth": {}` ([Each person's own account](../using/connections.md)). Two
things depend on the deployment:

- Privo's client ID metadata document is served at
  `https://app.example.com/oauth/client-metadata.json`; authorization servers
  that use it must reach that address from the internet.
- For GitHub's MCP server, create a second GitHub OAuth App for it (not the
  sign-in one) with the callback URL `https://app.example.com/connect/callback`,
  and give its client id and secret when adding the server.

## AI clients that sign in with OAuth

Off until you switch it on ([AI clients that sign in with OAuth](../using/oauth-clients.md)):

1. GitHub sign-in must work (above) and `PRIVO_PUBLIC_URL` must be the public
   address with no path.
2. Add `PRIVO_OAUTH_SERVER=on` in Environment Variables and deploy. The log
   says `authorization server on` with the issuer and the client policy.
3. Check: `https://app.example.com/.well-known/oauth-authorization-server`
   answers JSON, and
   `https://app.example.com/.well-known/oauth-protected-resource/mcp` lists
   `https://app.example.com` under `authorization_servers`.
4. Optional: `PRIVO_OAUTH_CLIENTS=approved|any` (default `known`);
   `PRIVO_OAUTH_DYNAMIC_REGISTRATION=on` for Cursor, Gemini CLI and Mistral.

## Metrics

Prometheus metrics are off. To scrape them from another container on the same
Docker network, set `PRIVO_METRICS_ADDR=0.0.0.0:9464` and **do not** add a
domain or port mapping for 9464 in Coolify (the metrics have no sign-in).
Every container on that Docker network can then read them: the server aliases,
call counts by outcome, whether each upstream is up, and the exact version.
Put Prometheus and Privo on a network of their own if other services share it.

## Deploys from GitHub

Pushes to `main` can deploy through one GitHub webhook (Coolify's "Manual Git
webhooks", for a repository reached with a deploy key):

1. Advanced → Deployment: Auto deploy = "Deploy on push (webhooks)".
2. Webhooks → GitHub: set a "Webhook secret" (16 characters or more). When
   several Coolify apps track the same repository and branch, give them all
   the **same** secret: Coolify checks each delivery against each app's own
   secret, so one webhook needs one shared secret.
3. GitHub → the repository → Settings → Webhooks → Add webhook: Payload URL is
   the URL Coolify shows under "Manual Git webhooks"; content type
   `application/json`; the shared secret; SSL verification on; "Just the push
   event".
4. **Watch Paths** (General → Build; one pattern per line, checked in order,
   the last match wins, relative to the repository root), so a change to the
   documentation does not restart the gateway:

   ```
   **
   !site/**
   !docs/**
   !test/**
   !.github/**
   !**/*.md
   !**/*_test.go
   !Makefile
   !mkdocs.yml
   !.golangci.yml
   !.gitignore
   ```

   Everything excluded is outside the binary (the `.dockerignore` already
   leaves `docs`, `site` and `*.md` out of the build context).

Check it: GitHub → the webhook → Recent Deliveries should answer 200 for each
push. A docs-only push deploys nothing; a change under `internal/` deploys the
gateway. Watch Paths filter webhook pushes only: a deploy started from the
dashboard or the API always runs.

Next: [Backup, restore and upgrades](operations.md).
