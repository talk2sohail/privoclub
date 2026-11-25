# privo.club's own deployment (internal)

Internal record of how privo.club itself runs on Coolify. The general guide
is the documentation site's **Deploy on Coolify**
(`docs/site/installation/coolify.md`) and **Backup, restore and upgrades**
(`docs/site/installation/operations.md`); this file keeps only what is
particular to our servers. It is not published.

The repository holds three apps on Coolify, all deploying from `main` on
talk2sohail/privoclub with the same deploy key: the landing page, the
gateway and the documentation site.

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

On the server, the gateway's container is
`$(docker ps -qf name=f46equvu3pb9kmq0shusvojo)` and its volume is
`docker volume ls | grep f46equvu3pb9kmq0shusvojo`; use them where the guide
says `<container>` and `<data volume>`.

## The documentation site (`privo-docs`)

| | |
|---|---|
| Coolify app | `privo-docs` (`8an2pu8fbxd8n1kmbjf2ufow`), project SN Enterprise, production |
| Domain | `https://docs.privo.club` (proxied A record, as app.privo.club) |
| Base directory | `/` (the build needs `mkdocs.yml` at the root) |
| Dockerfile location | `/docs/Dockerfile` |
| Port | 8080 |
| Volume | none: the site is static and holds nothing secret |
| Health | the container's `HEALTHCHECK` |
| Environment | none |
| Watch Paths | below |

```
docs/site/**
docs/requirements.txt
docs/Dockerfile
docs/Caddyfile
docs/externalize_inline_scripts.py
mkdocs.yml
```

It deploys from `main` through the same webhook and shared secret as the
other two apps (Deploys, below). Rolling updates can stay on: nothing is
shared between two containers.

Created through the Coolify API on 2026-11-24 with the deploy key the other
apps use, the watch paths above and the gateway's webhook secret; first
deploy of `6f10fc7` finished and healthy, and the live site answers with the
security headers of `docs/Caddyfile`. The first push that touches the site
confirms the webhook deploys it.

## State of the live gateway

- The master key is a file mount at `/run/secrets/privo_master_key` (moved
  from the variable on 2026-10-29).
- Nothing on the live app uses a configuration file yet.
- Scheduled backups are off (deferred by the owner on 2026-10-29). The
  restore has **not** been drilled on the production server.

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
   !mkdocs.yml
   !.golangci.yml
   !.gitignore
   ```

   Landing page (`nutty-narwhal-…`):

   ```
   site/**
   !site/**/*.md
   ```

   Everything excluded for the gateway is outside the binary (the `.dockerignore` already leaves `docs`, `site` and `*.md` out of the build context).

   `!mkdocs.yml` was added to this list on 2026-11-22 with the documentation site and set on the live gateway app on 2026-11-24.

### Status (2026-10-29)

Done through the Coolify API: watch paths on both apps, auto deploy on, and one shared webhook secret in both. A signed test delivery of a docs-only push (`X-GitHub-Event: push`, HMAC-SHA256) was accepted for both apps and skipped by both watch-path lists; a badly signed one was rejected; nothing deployed. **Still to do by hand: add the webhook on GitHub (step 3 above)**, using the secret from Coolify's Webhooks tab. The positive path (a `site/` or `internal/` push actually deploying the right app) is confirmed by the first real push after that.

### Check it works

- GitHub → the webhook → Recent Deliveries: the ping and each push should answer 200. Redeliver a push to retest without committing.
- Push a docs-only commit: neither app deploys. Push a change under `site/`: only the landing page deploys. Push a change under `internal/`: only the gateway deploys. Watch Paths are the part to test before relying on them; the Coolify docs suggest the same.
- Watch Paths filter webhook pushes only. A deploy started from the dashboard, the API or the coolify-deploy skill always runs.
- Gateway deploys still stop the old container first (see Rolling updates above), so expect a few seconds of downtime each time.
