# AI clients that sign in with OAuth

0.8.0, off unless switched on.
With `PRIVO_OAUTH_SERVER=on` an AI client needs no token at all
([ADR 0022](https://github.com/talk2sohail/privoclub/blob/main/docs/adr/0022-gateway-sign-in-with-oauth.md)).
Give it the gateway's address, `https://<privo>/mcp`. It then:

1. finds Privo's authorization server by itself and opens a browser;
2. the person signs in to Privo (with GitHub, as for the admin site);
3. they see a page that names the client and where it will send them back,
   and press **Allow**.

The client then calls as that member: their teams' servers and their own
accounts. Personal tokens keep working for clients without OAuth and for
scripts. [Deploy on Coolify](../installation/coolify.md#ai-clients-that-sign-in-with-oauth)
has the steps to switch it on.

## Which clients may ask

`PRIVO_OAUTH_CLIENTS` decides:

- `known` (default) accepts Privo's built-in list (Claude Code, Claude on the
  web and desktop, VS Code, ChatGPT, Codex, GitHub Copilot CLI, Zed,
  Antigravity) plus any client an administrator approves on **Clients** by
  the address of its metadata document;
- `approved` accepts only approved ones;
- `any` accepts every client that publishes a metadata document.

A member still allows each client on a page that names it, every time.

**Clients that register themselves** (Cursor, Gemini CLI, Mistral) need
`PRIVO_OAUTH_DYNAMIC_REGISTRATION=on`. They are shown as "registered itself,
not verified", limited per address and in total, and removed when unused.

## Tokens

- Access tokens live an hour and work only on `/mcp`, never on the admin API.
- A refresh token rotates on every use. A refresh token that was already
  used, presented again, ends that client's access at once (no grace window).
- A client that is not used for 30 days, or 90 days after the person allowed
  it, has to ask again.

## Taking access away

- Each person on **Tokens → AI clients that signed in**.
- An administrator for everyone on **Clients**.
- Removing a member, or giving them a role that cannot use the gateway, ends
  their clients' access.

Every case counts within 2 seconds.

## What else to know

- The protected resource metadata names the authorization server only while
  it is on. Off, `/oauth/authorize`, `/oauth/token`, `/oauth/revoke` and
  `/oauth/register` answer 503, and personal tokens are the only way in.
- The hosted Claude apps and ChatGPT must reach Privo from their own servers,
  and Privo must be allowed to fetch their metadata documents (public
  addresses only, through the egress policy).
