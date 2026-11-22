# Welcome to Privo

Privo is the team gateway to AI work and the MCP world: one endpoint per
organisation in front of every MCP server, with policies, human approvals,
encrypted credentials and a tamper-evident audit trail.

Your team's AI clients (Claude Code, Cursor, VS Code, Codex and others) talk
to one address, `https://<privo>/mcp`. Behind it, Privo reaches every MCP
server you add, shows each person only the servers and tools their teams may
use, runs every call with the right credential (shared, or the caller's own
account), applies your rules, holds a call for a person's approval when a rule
says so, and records every call and every admin change.

Status: early development. The architecture is settled in the
[architecture decisions](development/decisions.md); progress per feature is in
the project's tracker.

## Where to start

- **Run it:** [Build and run](installation/build-and-run.md), then
  [Deploy on Coolify](installation/coolify.md).
- **Configure it:** every setting is in the
  [configuration reference](installation/configuration.md), as an environment
  variable or in [a configuration file](installation/configuration-file.md).
- **Use it:** sign in on [the admin site](using/admin-site.md),
  [invite your team](using/people.md), [add servers](using/servers.md) and
  [make tokens](using/tokens.md) for your AI clients.
- **Keep it:** [backups, restore and upgrades](installation/operations.md).

## The addresses Privo serves

| Path | What it is |
| --- | --- |
| `/` | Home page: **Sign in** to the admin site, and the MCP address for AI clients |
| `/mcp` | The MCP gateway |
| `/api/v1/...` | The admin API |
| `/admin/...` | The admin site, with `/auth/...`, `/connect/...` and `/oauth/client-metadata.json` |
| `/livez`, `/readyz` | Health |
| `/version` | The semantic version (see [Versioning](development/versioning.md)) |
