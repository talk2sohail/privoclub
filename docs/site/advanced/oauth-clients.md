# AI clients that sign in with OAuth

With OAuth switched on, an AI client needs no token at all. You give it
Privo's address, and it signs the person in through the browser. Privo
becomes the authorization server for its own `/mcp`.

This arrived in 0.8.0
([ADR 0022](https://github.com/talk2sohail/privoclub/blob/main/docs/adr/0022-gateway-sign-in-with-oauth.md)),
and it is off unless you switch it on. Personal tokens keep working either
way, for clients without OAuth and for scripts.

## How a client signs in

Give the client the gateway's address, `https://privo.example.com/mcp`. Then:

1. the client finds Privo's authorization server by itself and opens a
   browser;
2. the person signs in to Privo with GitHub, as for the admin site;
3. Privo shows a page that names the client and where it will send them
   back, and the person chooses **Allow**.

The client then calls as that member: their teams' servers and their own
accounts. A member allows each client on that page, every time.

## Switch it on

1. GitHub sign-in must work, and `PRIVO_PUBLIC_URL` must be the public address
   with no path.
2. Set `PRIVO_OAUTH_SERVER=on` and restart. The log says
   `authorization server on`, with the issuer and the client policy.
3. Check: `https://privo.example.com/.well-known/oauth-authorization-server`
   answers JSON, and
   `https://privo.example.com/.well-known/oauth-protected-resource/mcp` lists
   `https://privo.example.com` under `authorization_servers`.

The settings are in the [configuration reference](../installation/configuration.md).

!!! warning
    Have the authorization server reviewed for security before you switch it
    on in production.

To switch it off, set `PRIVO_OAUTH_SERVER=off`. Off, the protected resource
metadata names no authorization server, `/oauth/authorize`, `/oauth/token`,
`/oauth/revoke` and `/oauth/register` answer 503, and personal tokens are the
only way in.

## Which clients may ask

`PRIVO_OAUTH_CLIENTS` decides:

- `known` (the default) accepts Privo's built-in list (Claude Code, Claude on
  the web and desktop, VS Code, ChatGPT, Codex, GitHub Copilot CLI, Zed and
  Antigravity) and the clients an administrator approves;
- `approved` accepts only the clients an administrator approves;
- `any` accepts every client that publishes a metadata document.

To approve a client, open **Clients**, enter the address of its metadata
document, such as `https://client.example/oauth/client-metadata.json`, and
choose **Approve client**. Privo reads the document at once and refuses one it
cannot use.

**Clients that register themselves** (Cursor, Gemini CLI, Mistral) need
`PRIVO_OAUTH_DYNAMIC_REGISTRATION=on` (RFC 7591). They are shown as
"registered itself, not verified", limited per address and in total, and
removed when unused.

## The tokens a client gets

- Access tokens live an hour and work only on `/mcp`, never on the admin API.
- A refresh token rotates on every use. A refresh token that was already
  used, presented again, ends that client's access at once: there is no grace
  window.
- A client that is not used for 30 days, or 90 days after the person allowed
  it, has to ask again.

## Taking access away

- Each person, on **Tokens → AI clients that signed in**: **Take access
  away**.
- An administrator, for everyone, on **Clients**: **End its connections** for
  a built-in client, **Remove** for an approved one, and each person's
  clients under **Connected clients**.
- Removing a member, or giving them roles that cannot use the gateway, ends
  their clients' access.

Every case takes effect within 2 seconds.

## Hosted clients

The hosted Claude apps and ChatGPT reach Privo from their own servers, so
Privo must be reachable from the internet, and must be allowed to fetch their
metadata documents. Those fetches go through Privo's outbound rules: public
addresses only.
