# Key concepts

The words these pages use, in the order you meet them.

## The gateway

- **Gateway**: Privo's MCP endpoint, `https://privo.example.com/mcp`. AI clients connect to it as one MCP server, and reach every server behind it through it.
- **Server**: An MCP server Privo reaches for you, over Streamable HTTP or SSE: GitHub, Linear, Sentry and so on. Privo calls these upstream servers on behalf of the person asking. See [Servers and the catalog](../basics/servers.md).
- **Alias**: The short name you give a server in Privo, such as `github` or `linear`: 2 to 16 lowercase letters, digits and dashes, starting with a letter. AI clients see its tools as `<alias>__<tool>`, so `create_issue` on `linear` is `linear__create_issue`.
- **Catalog**: Privo's built-in list of popular hosted MCP servers, with their settings filled in. Adding one from the catalog still goes through tool review. See [Servers and the catalog](../basics/servers.md).
- **Tool review**: An admin accepts each tool before any AI client sees it. When a server changes a tool, the tool is hidden again until someone accepts the new version; **Block** keeps one hidden for good.

## People and access

- **Role**: What a person may do: **owner** or **admin**, or any mix of **member** (uses tools through an AI client), **approver** (decides other people's calls) and **auditor** (reads the activity). See [Inviting and managing people](../basics/people.md#roles).
- **Team**: A group of people. Teams decide which servers a person reaches. Everyone is in the built-in team **Everyone**. See [Organising with teams](../basics/teams.md).
- **Toolset**: For one team and one server: all of the server's tools, or only the ones you tick.
- **Personal token**: A secret that starts `privo_pat_`. A **gateway token** goes in an AI client and works only on `/mcp`; a **control token** works only on the admin API. A gateway token made on the **Start** or **Tokens** page while you are signed in belongs to you: it reaches your teams' servers and your own accounts. One made with the command line or the API belongs to nobody and is for scripts. See [Connecting AI clients](../basics/tokens.md).
- **Connection**: Your own account on a server that knows its users: an OAuth sign-in Privo keeps for you, or your own API key. You connect once; Privo uses it only for your calls. See [Connecting your accounts](../basics/connections.md).

## Rules and approvals

- **Rule**: Allow, ask or deny, for a server and a tool, for everyone, a role, a team or one person. A rule can also test the call's arguments. See [Rules and approvals](../basics/rules-and-approvals.md).
- **Default**: What happens to a call when no rule matches: allow, ask or deny.
- **Approval**: A call a rule marks "ask" waits for an approver, who decides in the browser with the call's arguments in front of them.
- **Grant**: When approving, an approver can allow the same tool for that person for 15 minutes, 1 hour or 8 hours. A grant only ever turns an "ask" into "allow", never a deny.

## The record and the keys

- **Audit trail**: Every call and every admin change, written down before it runs and sealed into signed blocks. See [The tamper-evident audit trail](../advanced/audit.md).
- **Activity**: How calls went: counts, failures, refusals and timings, by server, tool, person and client. See [Viewing activity](../basics/activity.md).
- **Master key**: The key that protects every credential Privo stores. It lives outside the database, and you keep a copy. See [Deploy Privo](deploy.md).
- **Public URL**: The HTTPS address people, AI clients and sign-in providers reach Privo at: `PRIVO_PUBLIC_URL` in the [configuration reference](../installation/configuration.md).

Next: [Deploy Privo](deploy.md).
