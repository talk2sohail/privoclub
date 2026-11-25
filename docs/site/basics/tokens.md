# Connecting AI clients

Each AI client reaches Privo with a token of its own. You make tokens on
**Start** or **Tokens** while you are signed in, so each token belongs to you:
it reaches your teams' servers and your own accounts, and the audit trail
names you with every call it makes.

Tokens that belong to the person who made them arrived in 0.7.0, and token
names unique per person in 0.17.0.

## Make a token

1. Open **Start**, and under **A token for your AI client** name the client,
   for example `claude-code-laptop`. (On **Tokens**, the same form is under
   **Your AI clients**.)
2. Choose **Create token**.
3. Copy the token now. It starts `privo_pat_` and is shown only once.

Make one token per AI client, so you can revoke one without the others. The
name is yours alone: two people can each have a `claude-code`.

A token works only on Privo's MCP address, `/mcp`, never on the admin site or
its API. It lasts `PRIVO_TOKEN_TTL` (90 days by default; see the
[configuration reference](../installation/configuration.md)); **Expires** on
**Tokens** shows when.

## Add Privo to your AI client

On **Start**, under **Add Privo to your AI client**, choose your client. Privo
fills your address and the token you just made into the right line or file:

| Client | Where it goes |
| --- | --- |
| Claude Code | One command in a terminal; then run `/mcp` in Claude Code to see Privo's tools |
| Cursor | `~/.cursor/mcp.json`, or `.cursor/mcp.json` in a project |
| VS Code | `.vscode/mcp.json`, or your user `mcp.json` (**MCP: Open User Configuration**) |
| Codex | `~/.codex/config.toml`, with the token in the `PRIVO_TOKEN` environment variable |
| Windsurf | `~/.codeium/windsurf/mcp_config.json` |
| Any other | Server URL `https://privo.example.com/mcp`, header `Authorization: Bearer <your token>` |

For Claude Code, the line looks like this:

```sh
claude mcp add --transport http privo https://privo.example.com/mcp --header "Authorization: Bearer privo_pat_..."
```

Any client that speaks MCP over Streamable HTTP works the same way.

!!! warning
    A token is a password for your AI work. Keep it in your client's
    configuration, not in a shared file or a chat. If one leaks, revoke it at
    once.

## Revoke a token

On **Tokens**, under **Your AI clients**, choose **Revoke** next to the
token. It stops working within seconds. Make a new one for the client if you
still use it.

A token listed as "not tied to your account" was made before 0.7.0 or by a
script: it reaches only what everyone can use. Make a new token and revoke
that one.

Admins also see **All tokens**, everyone's, and can revoke any of them.

## Going further

- [AI clients that sign in with OAuth](../using/oauth-clients.md): no token
  to paste at all.
- [Tokens for scripts](../using/tokens.md#tokens-from-the-command-line):
  API tokens and tokens that belong to nobody.
