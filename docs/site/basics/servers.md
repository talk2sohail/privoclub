# Servers and the catalog

A server is an MCP server Privo reaches for your team: GitHub, Linear,
Sentry, your own. You add it once on **Servers**; its tools then reach the AI
clients of the teams you choose, after you have reviewed them.

The catalog arrived in 0.6.0
([ADR 0020](https://github.com/talk2sohail/privoclub/blob/main/docs/adr/0020-teams-and-server-catalog.md)), **Block** in tool
review in 0.12.0, and editing a server in 0.17.0
([ADR 0029](https://github.com/talk2sohail/privoclub/blob/main/docs/adr/0029-editing-a-server.md)).

## Add a server from the catalog

The catalog holds popular hosted MCP servers with their settings filled in.

1. Open **Servers**. Under **Add from the catalog**, pick a tab (**All**, or a
   kind of team such as Developers) and choose **Add** next to the server.
2. **Name in Privo** is the server's alias. AI clients see its tools as
   `<alias>__<tool>`, so keep it short: `github`, `linear`.
3. **Who can use it**: the teams that reach the server. **Everyone** means
   every member.
4. Some providers, such as GitHub, Asana, Slack and HubSpot, need an OAuth app
   of yours. The form says so, shows the callback URL to give the provider,
   and asks for the app's client ID and secret.
5. Choose **Add** with the server's name, such as **Add Linear**. Privo opens
   the server's page, where its tools wait for your review.

## Add a server that is not in the catalog

Under **Add another server**:

1. **Name**: the alias, as above.
2. **Server URL**: the server's MCP address, such as
   `https://mcp.example.com/mcp`.
3. **Transport**: **Streamable HTTP** for most servers, **SSE** for older
   ones.
4. **Whose account does this server see?**
    - **Nobody's**: the server needs no sign-in.
    - **One shared account**: a key every call uses. Enter the **Header
      name** (`Authorization` by default) and the **Header value**, such as
      `Bearer sk-…`. The value is stored encrypted and never shown again.
    - **Each person's own account, by signing in**: each person connects
      their own account (OAuth). See [Connecting your accounts](connections.md).
    - **Each person's own account, by their own API key**: each person enters
      their own key once.
5. Choose **Add server**. Its tools show up for review within a few seconds.

The two "each person's own account" choices need GitHub sign-in and
`PRIVO_PUBLIC_URL` set up: see the
[configuration reference](../installation/configuration.md).

## Review its tools

Nothing reaches an AI client before a person accepts it. On the server's page,
**Waiting for review** lists the new tools:

1. Read what each one says it does. An AI client reads every word of a tool's
   description as instructions, so look for anything you would not want it to
   do. Privo marks tools with warnings, such as hidden instructions or
   characters a person cannot see.
2. Tick the tools you trust, or **Choose all N shown**.
3. Choose **Accept N tools**. They move to **Available to AI clients**.

**Block N** keeps the ticked tools hidden for good. When a server changes a
tool later, the tool disappears from every AI client again and waits here,
showing what changed, until someone accepts the new version.

!!! warning
    Accept only tools you have read. A tool's description is instructions to
    every AI client that sees it.

## Choose who can use it

**Who can use it** on the server's page lists the teams that reach it. A
server with no team is reachable by nobody. See
[Organising with teams](teams.md).

## Edit, turn off or delete a server

On the server's page:

- **Edit** changes the address, the headers sent with every request, and the
  OAuth client of a server whose app you entered. Header values are never
  shown again, only which headers are set.
- **Turn off** stops all calls to the server without losing anything; **Turn
  on** brings it back.
- **Delete server** removes the server, its rules and everyone's connections
  to it. Type its alias to confirm.

!!! warning "A new address is a new server"
    Changing the address ends everyone's connection to the server, drops
    header values you do not enter again, and sends every tool back to
    review. A server where each person uses their own account keeps its
    address: delete it and add it again instead.

## Going further

- [Per-team toolsets](../advanced/toolsets.md): a team can see only some of a
  server's tools.
- [Custom upstream sign-in](../advanced/upstream-auth.md): OAuth settings
  entered by hand, and each person's own API key in detail.
- [Admin API and script tokens](../advanced/api-and-automation.md): adding and
  editing servers with the API.
- [Local stdio servers](../advanced/stdio-runner.md), for development.
