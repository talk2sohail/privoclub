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

## Getting started

New to Privo? Read these in order. They take a team from nothing to a first
tool call that works.

1. [How Privo works](getting-started/how-it-works.md): what happens to one
   tool call, stop by stop.
2. [Key concepts](getting-started/concepts.md): the words the rest of these
   pages use.
3. [Deploy Privo](getting-started/deploy.md): what a team install needs, and
   where to run it.
4. [First setup and sign-in](getting-started/first-setup.md): connect GitHub
   sign-in and become the owner.
5. [Your first server and AI client](getting-started/first-call.md): add a
   server, accept its tools, and make the first call.

## Basic features

What a team uses day to day, all in the browser:
[the admin site](basics/admin-site.md),
[servers and the catalog](basics/servers.md),
[connecting your accounts](basics/connections.md),
[inviting and managing people](basics/people.md),
[organising with teams](basics/teams.md),
[connecting AI clients](basics/tokens.md),
[rules and approvals](basics/rules-and-approvals.md) and
[viewing activity](basics/activity.md).

## Advanced features

For admins who need more control:
[which tools each team sees](using/teams.md#which-tools-each-team-sees),
[rules on arguments and rules as a file](using/rules.md#rules-on-arguments),
[OAuth settings entered by hand](using/connections.md#a-server-that-does-not-say-where-its-people-sign-in),
[AI clients that sign in with OAuth](using/oauth-clients.md),
[roles](using/roles.md), [the audit trail](using/audit.md) and
[metrics](using/activity.md#prometheus-metrics).

## Operations and reference

[Deploy on Coolify](installation/coolify.md),
[build and run](installation/build-and-run.md), the
[configuration reference](installation/configuration.md),
[the configuration file](installation/configuration-file.md), and
[backup, restore and upgrades](installation/operations.md).

## Security and development

[Report a vulnerability](security/disclosure.md) privately. To work on Privo:
[building Privo](development/building.md), [tests](development/tests.md),
[how a feature is made](development/workflow.md),
[architecture decisions](development/decisions.md) and
[versioning](development/versioning.md).

## The addresses Privo serves

| Path | What it is |
| --- | --- |
| `/` | Home page: **Sign in** to the admin site, and the MCP address for AI clients |
| `/mcp` | The MCP gateway |
| `/api/v1/...` | The admin API |
| `/admin/...` | The admin site, with `/auth/...`, `/connect/...` and `/oauth/client-metadata.json` |
| `/livez`, `/readyz` | Health |
| `/version` | The semantic version (see [Versioning](development/versioning.md)) |

## About this project

Privo is open source under the
[MIT License](https://github.com/talk2sohail/privoclub/blob/main/LICENSE).
Status: early development, before 1.0. The architecture is settled in the
[architecture decisions](development/decisions.md); progress per feature is in
the [tracker](https://github.com/talk2sohail/privoclub/blob/main/docs/todo.md).
Found a security problem? [Report it privately](security/disclosure.md).
