# Privo

The team gateway to AI work and the MCP world: one endpoint per organisation in
front of every MCP server, with policies, human approvals, encrypted
credentials and a tamper-evident audit trail.

Status: early development. Architecture is settled in `docs/adr/`; progress
per feature is in `docs/todo.md`.

## Quick start

```sh
make build                                        # bin/privo; needs Go 1.27+
openssl rand -base64 32 > master.key              # back this key up
PRIVO_MASTER_KEY_FILE=master.key PRIVO_DATA_DIR=./data bin/privo serve
curl localhost:8080/livez
```

Or with Docker (`localhost:8080`, no Go needed):

```sh
make image
mkdir -m 700 secrets && openssl rand -base64 32 > secrets/master.key && chmod 644 secrets/master.key
docker run -d --name privo -p 127.0.0.1:8080:8080 \
  -v privo_data:/data -v "$PWD/secrets/master.key:/run/secrets/master_key:ro" \
  -e PRIVO_PUBLIC_URL=http://localhost:8080 \
  -e PRIVO_MASTER_KEY_FILE=/run/secrets/master_key \
  privo:$(tr -d '[:space:]' < internal/buildinfo/VERSION)
```

## Documentation

Everything else is on [docs.privo.club](https://docs.privo.club), built from
[`docs/site/`](docs/site/index.md) with MkDocs (`make docs-serve` to read it
locally):

- [Build and run](docs/site/installation/build-and-run.md),
  [Deploy on Coolify](docs/site/installation/coolify.md),
  [Configuration reference](docs/site/installation/configuration.md)
- [The admin site](docs/site/using/admin-site.md),
  [servers](docs/site/using/servers.md), [teams](docs/site/using/teams.md),
  [rules and approvals](docs/site/using/rules.md),
  [tokens](docs/site/using/tokens.md), [the audit trail](docs/site/using/audit.md)
- [How a feature is made](docs/site/development/workflow.md),
  [architecture decisions](docs/site/development/decisions.md)

## Contributing, security and license

- [CONTRIBUTING.md](CONTRIBUTING.md): how a change is made.
- [SECURITY.md](SECURITY.md): report a vulnerability privately, never in an
  issue.
- Privo is open source under the [MIT License](LICENSE).
