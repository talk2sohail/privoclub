# Versioning

Privo follows [Semantic Versioning 2.0.0](https://semver.org/). The version of a
build is the single line in `internal/buildinfo/VERSION`, for example `0.1.0`.
It is compiled into the binary, so every build reports it the same way: `make
build`, the Dockerfile and a deploy on Coolify, which has no git history and
passes no build arguments. It is not derived from git and does not contain a
commit hash.

Where it shows:

- `GET /version` (unauthenticated, by decision of the owner on 2026-10-29) and
  `privo version` print `{"version": "0.3.0", "go_version": "go1.27.3"}`. They
  carry nothing else: no commit, build date, organisation or configuration.
- The `version` field of every log line, and the `serverInfo` that Privo reports
  to AI clients and sends to upstream servers.
- The image tag from `make image` (`privo:0.3.0`).

## What counts as public

The version covers what operators and clients depend on:

- the MCP surface: how tools, prompts and resources are named, listed, called and
  refused (`<server>__<name>`, `privo://<server>/...`), and the protocol
  versions served;
- the admin API under `/api/v1`: paths, fields, status codes, roles;
- configuration: environment variables and their defaults;
- the data directory: the SQLite schema and the migrations that change it.

## When to bump

While the major version is 0, anything may change, and the rules are:

| Change | Bump |
|---|---|
| A bug fix, a security fix, a hardening that changes no documented behaviour | PATCH (0.1.0 → 0.1.1) |
| A new feature or endpoint, a new setting with a safe default, a new migration that older data goes through on start | MINOR (0.1.1 → 0.2.0) |
| A change that breaks a client, a script or an existing deployment: a removed or renamed field, variable or endpoint, a changed default that alters behaviour, a migration that cannot be undone by restoring a backup | MINOR, and the change is under "Breaking" in the release notes |

From 1.0.0 a breaking change is a MAJOR bump, a feature is MINOR, a fix is PATCH.
Pre-release versions use a suffix (`1.0.0-rc.1`), and build metadata after `+` is
ignored when versions are compared.

## How it is changed

- The change that earns a new version bumps `internal/buildinfo/VERSION` in the
  same commit, so the deployed `/version` says which behaviour is running.
  Not every commit bumps it; the version moves when a release is meant to.
- The owner tags the commit `v<version>` (`v0.3.0`). The tag has the `v`, the
  file does not.
- `TestVersionIsSemver` in `internal/buildinfo` fails the build if the file is
  not a valid semantic version.
