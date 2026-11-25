# Build and run

## Try locally with Docker (no Go needed)

1. Build the image and generate a master key readable by the container's
   `nonroot` user (`65532`). The key file is world-readable so the container
   can read it; the `0700` directory keeps other users of your machine out:

   ```sh
   make image
   mkdir -m 700 secrets && openssl rand -base64 32 > secrets/master.key && chmod 644 secrets/master.key
   ```

2. Start Privo on `http://localhost:8080` (for browser sign-in, also pass
   `PRIVO_GITHUB_CLIENT_ID` and `PRIVO_GITHUB_CLIENT_SECRET` from a GitHub
   OAuth App with callback `http://localhost:8080/auth/github/callback`):

   ```sh
   docker run -d --name privo -p 127.0.0.1:8080:8080 \
     -v privo_data:/data \
     -v "$PWD/secrets/master.key:/run/secrets/master_key:ro" \
     -e PRIVO_PUBLIC_URL=http://localhost:8080 \
     -e PRIVO_MASTER_KEY_FILE=/run/secrets/master_key \
     privo:$(tr -d '[:space:]' < internal/buildinfo/VERSION)
   ```

3. Claim the owner account in the browser (needs the GitHub settings from
   step 2), or issue a CLI token below:

   ```sh
   docker exec privo /privo admin setup-link
   ```

## Build and run the binary

Requires Go 1.27 or later; `go.mod` pins `go1.27.4` (GO-2026-6859). The admin
app is embedded in the binary, so no Node is needed ([Building Privo](../development/building.md)).

```sh
make build                                        # bin/privo
openssl rand -base64 32 > master.key
PRIVO_MASTER_KEY_FILE=master.key PRIVO_DATA_DIR=./data bin/privo serve
curl localhost:8080/livez
```

Every setting is in the [configuration reference](configuration.md) and can
also live in [a configuration file](configuration-file.md).

!!! warning
    Back up the master key. Without it, every stored credential in the data
    directory is unreadable, and a backup cannot be restored.

## Your first sign-in or token

With [sign-in with GitHub](../using/admin-site.md) configured, print a
one-time owner link and do everything else in the browser:

```sh
privo admin setup-link
```

For CLI-only or API scripts where you have a shell on the server:

```sh
privo token create --name you --role owner --kind control       # admin API
privo token create --name ci --role member --kind gateway        # a script: servers everyone can use
```

For headless containers with neither browser sign-in nor SSH, see
[Deploy on Coolify](coolify.md#first-run-your-first-admin-token).
[Tokens](../using/tokens.md) explains the two kinds.

## The command line

| Command | What it does |
| --- | --- |
| `privo serve` | Run the gateway |
| `privo version` | Print the version |
| `privo healthcheck` | The container's health check |
| `privo config show` | Every setting, its value and its source |
| `privo token generate` | Print a new token, for the first boot |
| `privo token create` | Make a token in the database |
| `privo admin setup-link` | The one-time link that makes the first owner |
| `privo admin invite` | A one-time invite link |
| `privo backup`, `privo restore` | See [Backup, restore and upgrades](operations.md) |
| `privo audit verify` | Check the audit trail (exit 1 on a problem) |
