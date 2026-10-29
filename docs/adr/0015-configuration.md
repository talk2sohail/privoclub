# 0015. Configuration: environment and an optional file

- Status: Accepted, 2026-10-29 (owner asked for the server's configuration to live outside the code, so later settings are added in one place)
- Date: 2026-10-29
- Related: 0003 secrets, 0010 operations, 0013 key custody

## Context

Every setting so far is an environment variable read in `internal/config`, in
`internal/vault` (the master key) and in `cmd/privo` (the bootstrap token).
That is fine for one container, but it does not scale to what is coming:
sign-in providers (F05) need structured values, an operator wants a file they
can review and keep in version control, and each new setting was an edit in
three places with nothing keeping the README honest.

## Decision

1. **Sources, highest first:** the environment, the configuration file, the
   built-in default. A variable that is empty or only spaces counts as unset,
   as before. A container can keep a shared file and override one value per
   deployment.
2. **The file is YAML**, one flat mapping, like the rest of the product's
   planned export format (F11). The key is the variable name in lower case
   without `PRIVO_`: `PRIVO_BACKUP_INTERVAL` is `backup_interval`. Values are
   single scalars (text, number, true/false). Durations stay text (`24h`).
3. **Nothing is read unless it is named.** `--config FILE` before the command,
   or `PRIVO_CONFIG_FILE`; the flag wins. There is no default location and no
   search path. A named file that is missing, not a regular file, over 64 KiB,
   not valid YAML or not a single document stops the command (exit 2). In
   containers use `PRIVO_CONFIG_FILE`, so the image's healthcheck reads the
   same file as the server.
4. **Strict.** Unknown keys, duplicate keys, aliases and values that are lists
   or maps are errors, all reported at once with the file name and the key,
   never a value. A typo must not silently leave a setting at its default.
5. **Secrets never sit in the file.** `master_key`, `master_key_previous` and
   `bootstrap_token` are refused with a message that names the `_file`
   setting to use instead. The file may name the paths of secret files. Secret
   values are never shown by `privo config show`.
6. **One registry.** `config.Settings` lists every variable the process reads.
   Tests fail the build when the code reads a `PRIVO_` name that is not
   registered, when a registered name is missing from the README table or from
   `docs/privo.example.yaml`, and when the example does not parse. Adding a
   setting is: register it, read it, document it; forgetting a step is a
   failing test.
7. **Read once, at start.** No reload: a setting that changes under a running
   gateway makes behaviour depend on when someone edited a file. Change it and
   restart (Coolify redeploys in seconds, and a restart is what the writer
   lease already assumes).
8. **`privo config show`** prints every setting with its effective value and
   where it came from (default, file, environment), and the problems found.
   It makes a deployment's configuration answerable without reading code.

## Not decided here

- Per-organisation settings (policies, audit options, approval options) stay in
  the database behind the admin API (0002). The file is for how the process
  runs, not for what an organisation allows.
- Structured sections (sign-in providers, notifiers) arrive with the feature
  that needs them, in the same file. Today a list or map is refused because
  nothing reads it.
- Reload, remote configuration, a secrets manager.

## Alternatives considered

- **TOML or JSON.** TOML needs a new dependency and has no advantage over YAML
  here; JSON has no comments, which is the point of a file an operator edits.
- **A `.env` file.** No dependency, but flat forever: it cannot carry the
  provider lists F05 needs, and it invites putting secrets in the file.
- **Environment only, with better docs.** Works for Coolify today; does not
  give a reviewable file, typo detection or room for structure.

## Consequences

- One new dependency, `go.yaml.in/yaml/v3` (the maintained fork of
  `gopkg.in/yaml.v3`; already in the module graph and in `go.sum`).
- The file can change behaviour, so it is part of the deployment's trust
  boundary like the environment: keep it writable only by the operator. It
  holds no secret, so it needs no special mode.
- `vault` and `cmd/privo` read through the same layered lookup as `config`, so
  a file can name `master_key_file` and `bootstrap_token_file`.
