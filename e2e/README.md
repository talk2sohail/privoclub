# Privo AI Client Verification Harness (`privo-probe`)

`privo-probe` is Privo's release-grade external verification system. It wraps real, unmodified AI clients (**Claude Code**, **Antigravity CLI (`agy`)**, **Codex CLI**, **Gemini CLI**, the official **MCP TypeScript SDK** for Cursor/VS Code parity, and **Playwright** for Admin UI & OAuth consent) and runs them against a deployed Privo gateway (`--target staging|prod`) backed by **real upstream MCP servers**.

Every client connects through a local **Wire-Inspector Proxy** that validates raw MCP JSON-RPC responses against both `2025-11-25` and `2026-07-28` rules while cross-checking Privo's Admin API, Approvals, Audit Trail integrity, and Telemetry.

## 1. Coolify Staging Setup (One-Time)

In your Coolify **`staging`** project, create `privo-gateway-staging` mirroring `privo-gateway` (`docs/privo-club-deployment.md`):

- **Domain:** `https://staging.privo.club` (Cloudflare proxied A record → Traefik HTTPS)
- **Dockerfile:** `/Dockerfile`, Port `8080`, persistent volume `/data`, Rolling updates **Off**, container `HEALTHCHECK`
- **GitHub OAuth App (staging):** Callback `https://staging.privo.club/auth/github/callback`
- **Secrets (file mounts):** `/run/secrets/privo_master_key`, `/run/secrets/privo_github_client_secret`
- **Environment variables:**
  ```ini
  PRIVO_DATA_DIR=/data
  PRIVO_LISTEN=:8080
  PRIVO_PUBLIC_URL=https://staging.privo.club
  PRIVO_MASTER_KEY_FILE=/run/secrets/privo_master_key
  PRIVO_GITHUB_CLIENT_ID=<staging-github-oauth-client-id>
  PRIVO_GITHUB_CLIENT_SECRET_FILE=/run/secrets/privo_github_client_secret
  PRIVO_TRUSTED_PROXIES=10.0.0.0/8,172.16.0.0/12
  PRIVO_OAUTH_SERVER=on
  PRIVO_OAUTH_DYNAMIC_REGISTRATION=on
  ```
- **Watch Paths** (on both staging and production `privo-gateway`): add `!e2e/**` alongside `!test/**` so changes in `e2e/` never trigger unnecessary gateway redeploys.
- **Bootstrap personas:**
  1. Claim owner with `privo admin setup-link` using your primary GitHub account.
  2. Invite your second GitHub account as `member` (e.g., person `tester`).
  3. Create a staging owner control token (for `PRIVO_STAGING_OWNER_TOKEN`) and a member gateway token bound to the second account (for `PRIVO_STAGING_MEMBER_TOKEN`).

## 2. Quick Start

```sh
# 1. Install dependencies & Chromium for Playwright
make e2e-install

# 2. Export staging credentials (never commit secrets)
export PRIVO_STAGING_OWNER_TOKEN="privo_pat_..."
export PRIVO_STAGING_MEMBER_TOKEN="privo_pat_..."      # optional: enables bound toolset & 2-persona tests
export PRIVO_STAGING_MEMBER_PERSON="tester"            # optional: person slug of 2nd GitHub account
export PRIVO_TEST_GITHUB_PAT_OWNER="github_pat_..."    # optional: read-only PAT for GitHub MCP personal-key scenario

# 3. Check target health, real upstreams, and installed AI CLIs
make e2e-doctor TARGET=staging

# 4. (Optional, one-time) Capture browser sessions for Admin UI + OAuth consent + /connect/
make e2e-auth TARGET=staging PERSONA=owner
make e2e-auth TARGET=staging PERSONA=member
# Or, if GitHub uses Google SSO (blocked in automated browsers), import __Host-privo_session from DevTools:
npm --prefix e2e run probe -- auth --target staging --persona owner --cookie "<__Host-privo_session>"
# Secrets can also live in gitignored e2e/.state/staging/pat.env (chmod 600) and owner.token.

# 5. Run suites
make e2e-smoke   TARGET=staging    # ~2 min post-deploy smoke
make e2e-release TARGET=staging    # ~8-12 min full release gate (Scenarios 01-09)
make e2e-explore TARGET=staging    # open-ended UX & client gap hunter (Scenario 10)
make e2e-smoke   TARGET=prod       # non-destructive production smoke
```

Reports (Markdown, HTML, JSON) are written to `e2e/reports/`.

## 3. Running Specific Clients or Scenarios

```sh
# Run only Claude Code + Antigravity CLI (agy) on Scenarios 02 and 03
npm --prefix e2e run probe -- run --target staging --suite release --clients mcp-sdk,claude-code,agy --scenario 02,03

# Sweep any orphaned p-* / probe-* resources from an aborted run
make e2e-cleanup TARGET=staging
```
