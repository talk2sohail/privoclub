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
| Health | container | container `HEALTHCHECK` (`privo healthcheck`); no curl in image, so Coolify's own HTTP health check stays off |

## Gateway environment (names only)

- `PRIVO_DATA_DIR=/data`
- `PRIVO_LISTEN=:8080`
- `PRIVO_PUBLIC_URL=https://app.privo.club`
- `PRIVO_MASTER_KEY`: 32 random bytes, base64. Generated once with `openssl rand -base64 32` and set straight into Coolify.

> **Back up the master key.** Copy it from Coolify → privo-gateway → Environment Variables into your password manager. Without it, every stored credential in `/data` is unreadable. Never rotate it by just replacing the value: set the old one as `PRIVO_MASTER_KEY_PREVIOUS` first.

Later: move the key to a file mount and use `PRIVO_MASTER_KEY_FILE`, since env vars stay readable in `/proc/<pid>/environ`.

## DNS

privo.club is on Cloudflare. app.privo.club needs a record pointing the same way privo.club does (or the same Cloudflare Tunnel public hostname, to Coolify's proxy).

## Deploys

Pushing to `main` does not reliably trigger a build: there's no GitHub webhook. Trigger a deploy with the coolify-deploy skill, or from the Coolify UI, or add the webhook from the app's Webhooks tab.
