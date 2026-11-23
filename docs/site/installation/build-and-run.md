# Build and run

## Build

Requires Go 1.26 or later; `go.mod` pins the toolchain (1.27.4 or later:
GO-2026-6859).

```sh
make            # vet, lint, test, build
make build      # bin/privo
make image      # container image
```

The admin app is built into the binary, so building Privo needs no Node.
[Building Privo](../development/building.md) has every target.

## Run

```sh
PRIVO_LISTEN=:8080 PRIVO_DATA_DIR=./data bin/privo serve
curl localhost:8080/livez
```

`serve` needs a master key: 32 random bytes, base64 (`openssl rand -base64
32`), in a file named by `PRIVO_MASTER_KEY_FILE`. Every setting is in the
[configuration reference](configuration.md); they can also live in
[a configuration file](configuration-file.md).

!!! warning
    Back up the master key. Without it, every stored credential in the data
    directory is unreadable, and a backup cannot be restored.

## Your first token

Where you have a shell on the server:

```sh
privo token create --name you --role owner --kind control       # admin API
privo token create --name ci --role member --kind gateway        # a script: servers everyone can use
```

Where you have no shell (the distroless image on Coolify), generate a token on
your own machine and give it to the first boot instead; see
[Deploy on Coolify](coolify.md#first-run-your-first-admin-token):

```sh
go run ./cmd/privo token generate     # prints the token; keep it, it is not shown again
```

Then set up [sign-in with GitHub](../using/admin-site.md): everything after
the first sign-in happens in the browser. [Tokens](../using/tokens.md)
explains the two kinds.

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
