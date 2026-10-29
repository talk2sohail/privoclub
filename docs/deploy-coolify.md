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
- `PRIVO_MASTER_KEY`: 32 random bytes, base64. Generated once with `openssl rand -base64 32` and set straight into Coolify.
- `PRIVO_BOOTSTRAP_TOKEN`: first run only, see below. Delete it after you have your own token.

> **Back up the master key.** Copy it from Coolify → privo-gateway → Environment Variables into your password manager. Without it, every stored credential in `/data` is unreadable. Never rotate it by just replacing the value: set the old one as `PRIVO_MASTER_KEY_PREVIOUS` first.

Later: move the key to a file mount and use `PRIVO_MASTER_KEY_FILE`, since env vars stay readable in `/proc/<pid>/environ`.

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
