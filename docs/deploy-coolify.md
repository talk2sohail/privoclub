# Deploying on Coolify

The repo holds two apps. Both deploy from `main` on talk2sohail/privoclub, using the same deploy key.

| | Landing page | Gateway |
|---|---|---|
| Coolify app | `nutty-narwhal-…` (`ekc8ocwowwcoc0c80swo08gk`) | `privo-gateway` (`f46equvu3pb9kmq0shusvojo`) |
| Domain | privo.club | app.privo.club |
| Base directory | `/site` | `/` |
| Dockerfile | `site/Dockerfile` (Node) | `Dockerfile` (Go, distroless, nonroot) |
| Port | 3000 | 8080 |
| Volume | `…-privo` at `/data` (waitlist DB) | `…-data` at `/data` (SQLite, vault) |
| Health | container | container `HEALTHCHECK` (`privo healthcheck`); public `/livez`, `/readyz`. No curl in image, so Coolify's own HTTP health check stays off |
| Rolling updates | on | **off** (consistent container name enabled): only one gateway may hold the database, so the old container must stop before the new one starts. Expect a few seconds of downtime per deploy. |

## Gateway environment (names only)

- `PRIVO_DATA_DIR=/data`
- `PRIVO_LISTEN=:8080`
- `PRIVO_PUBLIC_URL=https://app.privo.club`
- `PRIVO_MASTER_KEY_FILE=/run/secrets/privo_master_key`: the key is not an environment variable. It is 32 random bytes, base64 (`openssl rand -base64 32`), held in a file mount (Coolify → privo-gateway → Persistent Storage → a file at `/run/secrets/privo_master_key`; Coolify keeps it on the host under the application's directory). The process reads the file at start; an unreadable or invalid file stops the start, so a wrong mount shows up at once and never as silently weaker protection.
- `PRIVO_BOOTSTRAP_TOKEN`: first run only, see below. Delete it after you have your own token.
- `PRIVO_GITHUB_CLIENT_ID` and `PRIVO_GITHUB_CLIENT_SECRET_FILE=/run/secrets/privo_github_client_secret`: sign-in to the admin site, see "Sign in with GitHub" below. Optional; without them `/admin` answers 503.

> **Back up the master key.** Copy the file's content from Coolify → privo-gateway → Persistent Storage into your password manager. Without it, every stored credential in `/data` is unreadable. Never rotate it by just replacing the content: mount the old key as a second file and point `PRIVO_MASTER_KEY_PREVIOUS_FILE` at it first.

Why a file: an environment variable stays readable in `/proc/<pid>/environ` for the life of the process, and the gateway logs a warning when the key arrives that way. The file removes that copy; anyone with access to Coolify or the host can still read it, as before. This is the interim arrangement, to be streamlined with the admin UI (`docs/todo.md`).

Moving an existing install from the variable to the file (done on 2026-10-29): add the file mount with the same content, set `PRIVO_MASTER_KEY_FILE`, deploy and check the app is healthy, delete `PRIVO_MASTER_KEY` (the normal and the preview entry), deploy again and check that the warning is gone from the logs. The file wins when both are set, so the first deploy is safe.

## Settings: environment or a configuration file

Everything above can stay in Coolify's environment, as today. For settings you would rather read in one place, Privo also reads a YAML file (ADR 0015, template: `docs/privo.example.yaml`). Two parts:

1. Coolify → privo-gateway → Persistent Storage → add a **file** mount, for example at `/etc/privo/privo.yaml`, and put the settings in it:
   ```yaml
   listen: ":8080"
   data_dir: /data
   public_url: https://app.privo.club
   master_key_file: /run/secrets/privo_master_key
   ```
2. Set `PRIVO_CONFIG_FILE=/etc/privo/privo.yaml` in the environment (the variable, not `--config`, so the image's healthcheck reads the file too) and redeploy.

The environment overrides the file, so a value left in Coolify's environment still wins; `docker exec <container> /privo config show` lists every setting with its value and where it comes from (default, file, environment). Secrets are refused in the file (`master_key`, `master_key_previous`, `bootstrap_token`): the file names the key file with `master_key_file`, which is how it is already set up here. An unknown or repeated key, a list, or a missing file stops the start with exit 2 and the reason in the log; nothing falls back to defaults. The file is read at start only; changing it needs a redeploy.

Nothing on the live app uses a file yet, and nothing needs to: moving settings across is optional and can be done one deploy at a time.

## First run: your first admin token

The image has no shell, so Coolify's web terminal cannot run `privo token create`.
Instead the first boot installs a token you generated yourself. The secret
never appears in a log or an API response, and only its hash is stored.

1. On your own machine, from the repo: `go run ./cmd/privo token generate`. The token is printed once; keep it in your password manager.
2. In Coolify → privo-gateway → Environment Variables, add `PRIVO_BOOTSTRAP_TOKEN` with that value (mark it secret / shown once if your Coolify version offers it). Deploy. The variable stays readable in `/proc/<pid>/environ` while the process runs, and the log says so; to avoid that, mount the token as a file (Coolify → Persistent Storage → file mount) and set `PRIVO_BOOTSTRAP_TOKEN_FILE` to its path instead. A token made by hand, such as a repeated character, is refused at startup: use the output of `token generate`.
3. The log shows `bootstrap owner token installed ... token_id=... expires=...`. The token is an **owner control token** (admin API only, never `/mcp`) and **expires 24 hours after install**.
4. Use it to mint your own token, then revoke it:

   ```sh
   read -rs BOOT                       # paste the bootstrap token; keeps it out of shell history
   URL=https://app.privo.club
   curl -sS -H "Authorization: Bearer $BOOT" $URL/api/v1/tokens            # shows the "bootstrap" token and its id
   curl -sS -X POST -H "Authorization: Bearer $BOOT" -H 'Content-Type: application/json' \
        -d '{"name":"owner","role":"owner","kind":"control","person":"your name"}' $URL/api/v1/tokens   # copy "token" from the reply
   read -rs OWNER                      # paste your new token
   curl -sS -X DELETE -H "Authorization: Bearer $OWNER" $URL/api/v1/tokens/<bootstrap token id>
   ```

   Revocation takes effect within about two seconds.
5. Delete `PRIVO_BOOTSTRAP_TOKEN` from Coolify and redeploy. If you forget, nothing is exposed beyond the 24 hours, and it never installs again (the gateway records that it was used); each boot logs a warning until the variable is gone.

To see that the install happened without a shell, read the admin events: `curl -sS -H "Authorization: Bearer $OWNER" "$URL/api/v1/audit/events?kind=admin"` lists it as `bootstrap.token.install:<id>` (actor `system`), next to the token you minted and the one you revoked.

Anyone who can read Coolify's environment can read the variable while it is set, as they can the master key, so keep step 2 to step 5 short.

**If the 24 hours pass unused**, the bootstrap cannot run again. Use the host: `docker exec $(docker ps -qf name=f46equvu3pb9kmq0shusvojo) /privo token create --name owner --role owner --kind control` (the binary runs without a shell; this needs SSH to the server).

## Sign in with GitHub (admin site)

People approve calls at `https://app.privo.club/admin/approvals` with their GitHub account (ADR 0016). Nothing here is needed for the gateway itself; until it is set up, `/admin` answers 503 with the names of the missing settings.

1. In GitHub, Settings > Developer settings > OAuth Apps > New OAuth App (an organisation's settings work too). Homepage URL `https://app.privo.club`, authorization callback URL `https://app.privo.club/auth/github/callback`. Leave device flow off. Copy the **Client ID**, then **Generate a new client secret** and copy it at once.
2. In Coolify → privo-gateway → Persistent Storage, add a file mount at `/run/secrets/privo_github_client_secret` holding the client secret (one line). The secret is a file for the same reason as the master key: an environment variable stays readable in `/proc/<pid>/environ`. Store a copy in your password manager.
3. In Environment Variables add `PRIVO_GITHUB_CLIENT_ID=<the Client ID>` and `PRIVO_GITHUB_CLIENT_SECRET_FILE=/run/secrets/privo_github_client_secret`. `PRIVO_PUBLIC_URL` must be `https://app.privo.club` with no path. Deploy and check the log: `admin site is on`.
4. Print the setup link on the host (the binary runs without a shell): `docker exec $(docker ps -qf name=f46equvu3pb9kmq0shusvojo) /privo admin setup-link --person <your name in Privo>`. It prints `https://app.privo.club/admin/setup/privo_setup_...` once and stores only a hash.
5. Open the link within an hour and choose **Continue with GitHub**. The GitHub account you sign in with becomes the owner. The link works once; a new link cancels the old one.

Treat the setup link as a secret until it is used. It sits in the URL path, so a proxy's access log may record it; once claimed it is worthless, and unclaimed it expires after an hour.

After the claim, everything else is in the browser (the admin app, ADR 0019; nothing to install, it is in the image): add servers, rules, invite people, and make your AI client's token on **Tokens** (no curl, no bootstrap token needed). Later sign-ins: `https://app.privo.club/admin/signin`. Sessions end after 2 hours without a request or 12 hours in all (`PRIVO_SESSION_IDLE_TIMEOUT`, `PRIVO_SESSION_MAX_AGE`). With GitHub Enterprise Server, set `PRIVO_GITHUB_BASE_URL` and `PRIVO_GITHUB_API_URL`, and add its network to `PRIVO_EGRESS_ALLOW_CIDRS` if it is private.

Privo cannot require two-factor authentication at GitHub; turn it on for the accounts that approve calls (an organisation can require it).

## Servers where each person uses their own account

Nothing new to configure: once sign-in with GitHub works, add a remote server with `"oauth": {}` (README, "Servers where each person uses their own account"). Two things depend on the deployment:

- Privo's client ID metadata document is served at `https://app.privo.club/oauth/client-metadata.json`; authorization servers that use it must reach that address from the internet.
- For GitHub's MCP server, create a second GitHub OAuth App for it (not the sign-in one) with the callback URL `https://app.privo.club/connect/callback`, and give its client id and secret when adding the server.

Invite people with `privo admin invite --person NAME` in the container's terminal, or with `POST /api/v1/invites`.

## Teams (0.6.0)

0.6.0 adds teams (ADR 0020) and migration `00006_teams.sql`, which runs on the
first start of the new image. Every server that exists keeps being reachable by
everyone, so nothing changes for AI clients until an admin gives a server to a
team. Take a backup before the deploy (below); the migration has a down step,
and restoring the backup is the way back. Members now land on **Start** instead
of **Tokens**.

## Backups

A backup is a consistent copy of the database (`privo.db`) taken while the gateway runs, with no downtime. It holds token hashes, policy, audit and the wrapped keys and credentials, and nothing in the clear. It does **not** hold the master key, and it is useless without it: keep the key in your password manager (see above). Restoring with another key is refused at start with a message that names the master key.

**On demand** (needs SSH to the server; the image has no shell, so Coolify's terminal and Coolify's scheduled tasks, which run a command in a shell inside the container, cannot do this):

```sh
docker exec $(docker ps -qf name=f46equvu3pb9kmq0shusvojo) /privo backup            # into /data/backups/
docker exec $(docker ps -qf name=f46equvu3pb9kmq0shusvojo) /privo backup --keep 7   # and keep only the newest 7
```

**On a schedule**, built in, off by default. In Coolify → privo-gateway → Environment, then redeploy:

| Variable | Default | Meaning |
| --- | --- | --- |
| `PRIVO_BACKUP_INTERVAL` | unset (off) | A snapshot every interval (`24h`, `6h`, at least `1s`). At start the gateway waits out what is left of the interval since the newest snapshot, or takes one at once if there is none |
| `PRIVO_BACKUP_KEEP` | `7` | How many of the newest snapshots to keep (1 to 1000); only files named `privo-<UTC time>.db` are ever deleted |
| `PRIVO_BACKUP_DIR` | `<data dir>/backups` | Where snapshots go |

Each backup is an admin audit event (`backup.create`, actor `system` or `cli`; outcome `error` if it failed). A failed scheduled backup is logged, retried within at most 5 minutes, and never stops the gateway.

**Off-host copy is your job for now.** The snapshots are on the same volume as the database, so they protect against mistakes and corruption, not against losing the server. Copy `/data/backups/` somewhere else on a schedule of your own (Coolify's S3 backups of the volume, `rsync`, `restic`). Built-in upload to S3-compatible storage is on the list (`docs/todo.md`, F15).

**Restore** (stop the gateway first; a restore refuses while one is running):

1. Coolify → privo-gateway → Stop.
2. On the server, with the application's data volume (find it with `docker volume ls | grep f46equvu3pb9kmq0shusvojo`) and the image Coolify built (`docker ps -a` shows its name), run the binary once as the image's user:
   `docker run --rm --user 65532:65532 -v <data volume>:/data -e PRIVO_DATA_DIR=/data <image> restore --from /data/backups/privo-<time>.db --force`
3. The old `privo.db`, `-wal` and `-shm` are kept beside it as `*.replaced-<time>`; nothing is deleted. Delete them yourself when you are sure.
4. Coolify → Start. The gateway must run with the master key the backup was made with. With another key it exits at start and says that the master key cannot open the database.

The restore is tested end to end with the real binary (`test/acceptance`: `TestBackupAndRestoreDrill`, `TestScheduledBackupsKeepTheNewestAndRestore`, `TestRestoreWithTheWrongKeyRefusesToStart`). It has **not** been run on the production server; do one drill into a scratch directory (`--force` is not needed there) before you rely on it.

## DNS and TLS

- privo.club reaches Coolify through the Cloudflare Tunnel (CNAME to `*.cfargotunnel.com`, service `http://localhost:80`), so its Coolify domain is `http://privo.club`.
- app.privo.club is a proxied A record straight to the server. Cloudflare then connects on 443, so the Coolify domain **must be `https://app.privo.club`**: that creates Traefik's HTTPS router with a Let's Encrypt certificate. With `http://` Traefik answers `503 no available server`.

## Deploys

Pushes to `main` deploy through one GitHub webhook (Coolify's "Manual Git webhooks", because the repo uses a deploy key). Each app's **Watch Paths** then decide whether the push concerns it, so a change to the landing page does not restart the gateway and the reverse. Both apps track the same repo and branch, so GitHub sends one delivery and Coolify checks it against each app.

### One-time setup

1. **Both apps**, Advanced → Deployment: Auto deploy = "Deploy on push (webhooks)".
2. **Both apps**, Webhooks → GitHub: set "Webhook secret" to the **same** value in both (16+ characters, use the generator on the first app and paste it into the second), save. Coolify finds the apps by repo and branch and checks each delivery's signature against that app's own secret, so one webhook needs one shared secret. Confirmed on 2026-10-29 with a signed test delivery: both apps accepted it.
3. GitHub → talk2sohail/privoclub → Settings → Webhooks → Add webhook: Payload URL is the GitHub URL Coolify shows under "Manual Git webhooks" (the same for both apps); Content type `application/json`; Secret is the shared value; keep SSL verification on; events "Just the push event".
4. **Watch Paths** (General → Build; one pattern per line, no commas; patterns are checked in order and the last match wins; relative to the repo root, confirmed by the same test delivery):

   Gateway (`privo-gateway`):

   ```
   **
   !site/**
   !docs/**
   !test/**
   !.github/**
   !**/*.md
   !**/*_test.go
   !Makefile
   !.golangci.yml
   !.gitignore
   ```

   Landing page (`nutty-narwhal-…`):

   ```
   site/**
   !site/**/*.md
   ```

   Everything excluded for the gateway is outside the binary (the `.dockerignore` already leaves `docs`, `site` and `*.md` out of the build context).

### Status (2026-10-29)

Done through the Coolify API: watch paths on both apps, auto deploy on, and one shared webhook secret in both. A signed test delivery of a docs-only push (`X-GitHub-Event: push`, HMAC-SHA256) was accepted for both apps and skipped by both watch-path lists; a badly signed one was rejected; nothing deployed. **Still to do by hand: add the webhook on GitHub (step 3 above)**, using the secret from Coolify's Webhooks tab. The positive path (a `site/` or `internal/` push actually deploying the right app) is confirmed by the first real push after that.

### Check it works

- GitHub → the webhook → Recent Deliveries: the ping and each push should answer 200. Redeliver a push to retest without committing.
- Push a docs-only commit: neither app deploys. Push a change under `site/`: only the landing page deploys. Push a change under `internal/`: only the gateway deploys. Watch Paths are the part to test before relying on them; the Coolify docs suggest the same.
- Watch Paths filter webhook pushes only. A deploy started from the dashboard, the API or the coolify-deploy skill always runs.
- Gateway deploys still stop the old container first (see Rolling updates above), so expect a few seconds of downtime each time.
