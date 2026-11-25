# 0030. Distribution and deployment: one instance per organisation, from signed releases

- Status: Proposed, 2026-11-24
- Date: 2026-11-24
- Amends: 0010 (upgrades run migrations on start; no separate `privo migrate`)
- Related: 0002 (data model, SQLite and Postgres), 0004 (one gateway replica
  in v1), 0007 (stdio runner), 0010 (operations), 0011 (identity), 0013 (key
  custody), 0016 (sign-in with GitHub), 0034 (sign-in with OpenID Connect),
  F15, F20, F31

## Context

Privo is open source under MIT (F31), for teams and organisations of any
size. Today the only way to run it is to build it from the repository, and
the only written deployment is our own Coolify app. We need a local evaluation
path that runs in two minutes without a domain, and one team deployment that
an operator can follow in five minutes and that we can support, chosen to fit
what the architecture is today, not what it will be.

What the architecture is today:

- **One static Go binary** (`CGO_ENABLED=0`): it builds for any OS and CPU
  without a toolchain on the target, and the image is distroless and
  non-root (`65532:65532`).
- **One writer.** SQLite with a lease row: a second process refuses to start
  (0002). Calls parked for an approval or an elicitation and several caches
  live in memory, so **v1 runs one gateway replica** (0004). Postgres and
  several replicas are designed (0002, 0010) and not built.
- **Plain HTTP, with long-lived MCP streams.** The binary serves no TLS, and
  `PRIVO_PUBLIC_URL` must be https unless the host is loopback (`localhost` or
  `127.0.0.1`), so a team deployment needs a proxy in front that ends TLS,
  does not buffer `text/event-stream` replies, and allows long tool calls
  (`PRIVO_UPSTREAM_CALL_TIMEOUT`, 10m by default).
- **State is one directory plus one key.** Everything is in the data
  directory, except the master key, which must live apart from it (0013) and
  be backed up apart from it.
- **Remote MCP servers only in production.** The stdio runner is local and
  for development (0007); the isolated runner is not built.
- **People sign in with GitHub (0016).** Generic OpenID Connect (Google
  Workspace, Microsoft Entra, Okta, Keycloak) is accepted in 0034 and is not
  built yet (F20 A).
- **Several organisations in one SQLite install are isolated by code only**
  (0002); large customers get their own install.

## Options considered

1. **Source only** (today). Every operator needs Go and our Makefile; there is
   no way to know which build they run. Rejected.
2. **Kubernetes and Helm first.** One replica on a single read-write-once
   volume gains nothing from Kubernetes, and most of the teams we aim at do
   not run it. Rejected as the first path; a plain manifest is a recipe.
3. **Several replicas on Postgres** for every install. Not built, and more
   than a small team should run. Later, for those who need it.
4. **Hosted only (Privo Cloud).** Not the goal of open source, and needs the
   cell design of 0010. Later.
5. **One instance per organisation, from versioned, signed artifacts, with
   Docker Compose as the reference deployment.** Chosen.

## Decision

1. **The unit of deployment is one organisation: one gateway process, one
   data volume, one master key file outside the volume.** An organisation
   that wants another org isolated runs another instance.

2. **Every release is built by CI from a tag `v<version>`** (the version in
   `internal/buildinfo/VERSION`, docs/site/development/versioning.md) and
   published on GitHub:
   - a container image `ghcr.io/talk2sohail/privo:<version>`, also tagged
     `<major>.<minor>`, for `linux/amd64` and `linux/arm64`, with SBOM,
     provenance, `org.opencontainers.image.source` pointing at
     `https://github.com/talk2sohail/privoclub` (the package name `privo`
     differs from the repository name, so GHCR needs the label and a public
     visibility check on the first publish), and **signed with cosign keyless**
     (GitHub's OIDC identity of the release workflow). Never `latest` (0010);
   - binaries for Linux and macOS on amd64 and arm64, with `SHA256SUMS`,
     signed the same way, for hosts without containers (systemd);
   - release notes, with a **Breaking** section whenever one applies.
   The release job runs only on a tag pushed by a maintainer, has write
   access to packages and nothing else, and pins every action by commit.

3. **Two first-time paths, with Docker Compose as the reference team
   deployment:**
   - **Local evaluation (`localhost:8080`, two minutes, no domain or TLS):**
     because `PRIVO_PUBLIC_URL` accepts `http://localhost:8080`, an evaluator
     can try Privo on a laptop with one `docker run` command on
     `127.0.0.1:8080`, no reverse proxy and no DNS record, and claim ownership
     with `docker exec privo /privo admin setup-link` (or `token create` for
     CLI-only evaluation without an OAuth app).
   - **Team reference deployment in `deploy/compose/`:**
     - `compose.yaml` with two services: `privo` (pinned version, `:8080`
       **not** published on the host) and **Caddy** (pinned version, `80` and
       `443` published), which gets and renews the TLS certificate by itself
       and streams `text/event-stream` without buffering;
     - an explicit Compose bridge network with a pinned CIDR (for example
       `172.30.0.0/24`) passed to `PRIVO_TRUSTED_PROXIES`, because
       `PRIVO_TRUSTED_PROXIES` accepts IP CIDRs only and Docker's default pool
       allocates across `172.16.0.0/12` and `192.168.0.0/16`;
     - secrets (`secrets/master_key` and the sign-in client secret) as Compose
       secret files, never environment variables. Because `docker compose`
       bind-mounts secret files with their host UID and mode and the image
       runs as `65532:65532`, the setup keeps `secrets/` at mode `0700` and
       makes the files readable by UID `65532` (`chown 65532:65532` or mode
       `0644` inside the `0700` directory);
     - a named data volume and **daily snapshots on by default**
       (`PRIVO_BACKUP_INTERVAL=24h`) for new installs;
     - first run without a shell or Go: `docker compose exec privo /privo
       admin setup-link` is the single primary onboarding path. `docker run
       --rm <image> token generate` (`PRIVO_BOOTSTRAP_TOKEN_FILE`) is kept
       aside for headless, API-only installs.

   The team quick start fits on one screen: download the three files, set the
   domain and sign-in client (GitHub or OIDC), generate `secrets/master_key`
   with `openssl rand -base64 32`, `docker compose up -d`, open the setup link.

4. **Other platforms are recipes over the same image**, documented and
   tested by hand at each minor release, not maintained as separate
   products:
   - Coolify (deploying the signed image, rolling updates off);
   - a plain `docker run`;
   - Kubernetes as plain manifests: one replica, `strategy: Recreate`, a
     read-write-once volume, `securityContext.fsGroup: 65532`, the master key
     as a Secret mounted as a file, and an Ingress for TLS. A Helm chart comes
     with several replicas, not before;
   - the binary with a systemd unit and a reverse proxy;
   - platforms with a persistent volume and one instance. Network file
     systems are refused (0002).
   Every reverse proxy or Ingress recipe must turn response buffering off and
   allow upstream read timeouts of at least `PRIVO_UPSTREAM_CALL_TIMEOUT`
   (10m by default), so SSE streams, approval holds (`PRIVO_APPROVAL_HOLD`)
   and long tool calls are not cut off at 60 seconds.

5. **Upgrades**: take a live snapshot first (`docker compose exec privo /privo
   backup`, since the last 24h snapshot may be hours old), change the pinned
   version, `docker compose up -d`. Migrations run automatically on start
   (amending 0010's separate `privo migrate`); a few seconds of downtime are
   stated. The current and the previous minor release are supported (0010).

6. **Size, said honestly.** One instance grows by giving it more CPU and
   memory. Before 1.0 we publish measured figures from a load test on the
   reference deployment (calls per second, people, servers), not estimates.
   An organisation that needs no downtime or more than one machine waits for
   the HA milestone: Postgres and several replicas (0002, 0010), the same
   image, chosen by setting, with an export from SQLite and an import into
   Postgres.

7. **Sign-in beyond GitHub ships before HA**: generic OIDC (Google Workspace,
   Microsoft Entra, Okta, Keycloak; ADR 0034, F20 A) lands so organisations
   that do not use GitHub need no GitHub OAuth app, and `deploy/compose/`
   documents both GitHub and OIDC in `.env.example`.

8. **The documentation's Installation section starts with Quick start**
   (try locally in two minutes, then deploy for a team with Compose), followed
   by Coolify, Kubernetes and the binary; building from source moves to
   Development. Every installation guide uses `setup-link` as the primary
   first-run step and avoids inline prose where a command suffices.

## Consequences

- Anyone can try Privo locally on `localhost:8080` in two minutes with Docker
  alone, or deploy for a team with Docker Compose, a domain and a GitHub or
  OIDC app, and check that the image is ours (`cosign verify`).
- One supported shape to test and to debug; reports come with a version and
  an image digest.
- No high availability yet, and seconds of downtime per upgrade; stated on
  the site.
- Until F20 A (ADR 0034) merges, sign-in requires GitHub; once merged, any
  OIDC provider works in the same Compose setup.
- Caddy is a second container to keep up to date (pinned by version, updated
  with releases).
- Releases need discipline: a tag is a release, and its notes are written
  from the tracker.
- `go install privo.club/privo/cmd/privo@<version>` works only if privo.club
  answers `?go-get=1` with a `go-import` tag for the repository; optional, a
  small addition to the landing page.

## Tasks (tracker F15)

1. Release workflow: multi-arch image to `ghcr.io/talk2sohail/privo` (with
   `org.opencontainers.image.source` and public package visibility verified),
   binaries, `SHA256SUMS`, cosign signatures, SBOM and provenance; a dry run
   on a pre-release tag including an unauthenticated `docker pull`.
2. `deploy/compose/` (`compose.yaml` with internal pinned-CIDR network and
   unexposed `:8080`, `Caddyfile`, `.env.example` for GitHub and OIDC, secret
   files readable by UID `65532`, daily snapshots on) and an acceptance check
   that brings it up and signs in on a fresh host.
3. Installation pages: Quick start (local `docker run` + team Compose) first,
   then Coolify, Kubernetes manifests (one replica, `Recreate`, unbuffered
   Ingress), binary with systemd; Verify a release (cosign).
4. The load test and the published figures.
5. Merge generic OIDC sign-in (ADR 0034, F20 A), before HA.
