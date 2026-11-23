# 0030. Distribution and deployment: one instance per organisation, from signed releases

- Status: Proposed, 2026-11-24
- Date: 2026-11-24
- Related: 0002 (data model, SQLite and Postgres), 0004 (one gateway replica
  in v1), 0007 (stdio runner), 0010 (operations), 0011 (identity), 0013 (key
  custody), 0016 (sign-in with GitHub), F15, F31

## Context

Privo becomes open source under MIT (F31), for teams and organisations of
any size. Today the only way to run it is to build it from the repository,
and the only written deployment is our own Coolify app. We need one way to
deploy that a team can follow in minutes and that we can support, chosen to
fit what the architecture is today, not what it will be.

What the architecture is today:

- **One static Go binary** (`CGO_ENABLED=0`): it builds for any OS and CPU
  without a toolchain on the target, and the image is distroless and
  non-root.
- **One writer.** SQLite with a lease row: a second process refuses to start
  (0002). Calls parked for an approval or an elicitation and several caches
  live in memory, so **v1 runs one gateway replica** (0004). Postgres and
  several replicas are designed (0002, 0010) and not built.
- **Plain HTTP.** The binary serves no TLS, and `PRIVO_PUBLIC_URL` must be
  https unless it is loopback, so something in front must end TLS.
- **State is one directory plus one key.** Everything is in the data
  directory, except the master key, which must live apart from it (0013) and
  be backed up apart from it.
- **Remote MCP servers only in production.** The stdio runner is local and
  for development (0007); the isolated runner is not built.
- **People sign in with GitHub** (0016). Generic OIDC is planned (0011).
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
     `<major>.<minor>`, for `linux/amd64` and `linux/arm64`, with SBOM and
     provenance, **signed with cosign keyless** (GitHub's OIDC identity of the
     release workflow). Never `latest` (0010);
   - binaries for Linux and macOS on amd64 and arm64, with `SHA256SUMS`,
     signed the same way, for hosts without containers (systemd);
   - release notes, with a **Breaking** section whenever one applies.
   The release job runs only on a tag pushed by a maintainer, has write
   access to packages and nothing else, and pins every action by commit.

3. **The reference deployment is Docker Compose** in `deploy/compose/`:
   - `compose.yaml` with two services, Privo (from the signed image, by
     version) and **Caddy**, which gets and renews the TLS certificate for
     the domain by itself, so a team needs only a DNS record;
   - the master key as a Compose secret file (`secrets/master_key`), never in
     the environment;
   - `PRIVO_TRUSTED_PROXIES` set to the Compose network, a named data volume,
     and **daily snapshots on by default** (`PRIVO_BACKUP_INTERVAL=24h`) for
     new installs;
   - first run without a shell or Go: `docker compose exec privo /privo admin
     setup-link` (the binary runs without a shell), or `docker run --rm
     <image> token generate` for the bootstrap token.

   The quick start fits on one screen: get the files, set the domain and the
   GitHub OAuth app, generate the key with `openssl rand -base64 32`,
   `docker compose up -d`, open the setup link.

4. **Other platforms are recipes over the same image**, documented and
   tested by hand at each minor release, not maintained as separate
   products:
   - Coolify (the existing guide);
   - a plain `docker run`;
   - Kubernetes as plain manifests: one replica, `strategy: Recreate`, a
     read-write-once volume, the master key as a Secret mounted as a file,
     an Ingress for TLS. A Helm chart comes with several replicas, not before;
   - the binary with a systemd unit and a reverse proxy;
   - platforms with a persistent volume and one instance. Network file
     systems are refused (0002).

5. **Upgrades**: back up, change the pinned version, `docker compose up -d`.
   Migrations run on start; a few seconds of downtime are stated. The
   current and the previous minor release are supported (0010).

6. **Size, said honestly.** One instance grows by giving it more CPU and
   memory. Before 1.0 we publish measured figures from a load test on the
   reference deployment (calls per second, people, servers), not estimates.
   An organisation that needs no downtime or more than one machine waits for
   the HA milestone: Postgres and several replicas (0002, 0010), the same
   image, chosen by setting, with an export from SQLite and an import into
   Postgres.

7. **Sign-in beyond GitHub is the first gap to close for "any
   organisation"**: generic OIDC (Google Workspace, Microsoft Entra, Okta,
   Keycloak), as 0011 plans, comes before HA.

8. **The documentation's Installation section starts with the Compose quick
   start**; Coolify, Kubernetes and the binary follow it; building from source
   moves to Development.

## Consequences

- A team runs Privo with Docker, a domain and a GitHub OAuth app, and can
  check that the image is ours (`cosign verify`).
- One supported shape to test and to debug; reports come with a version and
  an image digest.
- No high availability yet, and seconds of downtime per upgrade; stated on
  the site.
- GitHub sign-in stays a requirement until generic OIDC lands; that limits
  adoption by organisations that do not use GitHub.
- Caddy is a second container to keep up to date (pinned by version, updated
  with releases).
- Releases need discipline: a tag is a release, and its notes are written
  from the tracker.
- `go install privo.club/privo/cmd/privo@<version>` works only if privo.club
  answers `?go-get=1` with a `go-import` tag for the repository; optional, a
  small addition to the landing page.

## Tasks (tracker F15)

1. Release workflow: multi-arch image to ghcr.io, binaries, `SHA256SUMS`,
   cosign signatures, SBOM and provenance; a dry run on a pre-release tag.
2. `deploy/compose/` (`compose.yaml`, `Caddyfile`, `.env.example`) and an
   acceptance check that brings it up and signs in on a fresh host.
3. Installation pages: Quick start (Compose) first, then Coolify,
   Kubernetes manifests, binary with systemd; Verify a release (cosign).
4. The load test and the published figures.
5. Generic OIDC sign-in (0011), before HA.
