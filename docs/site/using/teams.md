# Teams and the catalog

Each team sees only the servers given to it
([ADR 0020](https://github.com/talk2sohail/privoclub/blob/main/docs/adr/0020-teams-and-server-catalog.md)).
On the admin site:

- **Teams → New team**: start from a profile (Developers, Designers, Product
  managers, Sales and support) or a blank team, then add its people. A team
  made from a profile shows the catalog's servers for it, each one click away.
- **Servers → Add from the catalog**: 25 hosted MCP servers (GitHub, Linear,
  Sentry, Figma, Canva, Notion, Asana, Slack…), checked with Privo's own
  discovery on 2026-11-03. Pick the teams that may use it. Most register Privo
  by themselves; GitHub, Asana, Slack and HubSpot need an OAuth app of yours,
  and the form shows the callback URL to give them. Every tool is still
  reviewed before anyone sees it.
- **A server's page → Who can use it**: the teams that reach it.
  **Everyone** is built in (every member is in it). A server with no team is
  reachable by nobody.
- **Start** (where members land): their teams, their servers with a Connect
  link where their own account is needed, and the setup of Claude Code,
  Cursor, VS Code, Codex or Windsurf with a token made on the spot.

The gateway lists only the tools of servers the caller's teams reach, and of
those only the ones their teams see, and answers a call on any other tool as
if the name did not exist. Rules can name a team: subject `team:<slug>`.
Existing servers, and servers added through the API without `"teams"`, are
for everyone.

## Which tools each team sees

Toolsets, 0.14.0,
[ADR 0026](https://github.com/talk2sohail/privoclub/blob/main/docs/adr/0026-toolsets.md).
**A server's page → Which tools each team sees**: per team, all of the
server's tools or only the ones you tick. A person in several teams sees what
any of them sees; a new tool stays hidden from a team that sees only some
until you tick it. Rules, approvals and the tool review still apply to every
tool a person sees.

API: `PATCH /api/v1/servers/<alias>/teams/<slug>` with
`{"tools": ["a", "b"]}`, or `{"tools": null}` for all.

## The API

| Call | What it does |
| --- | --- |
| `GET`, `POST /api/v1/teams` | List, create |
| `PATCH`, `DELETE /api/v1/teams/<slug>` | Change, delete |
| `POST /api/v1/teams/<slug>/members` | Add a person: `{"person": "ada"}` |
| `DELETE /api/v1/teams/<slug>/members/<person>` | Remove a person |
| `POST /api/v1/teams/<slug>/servers` | Give a server: `{"alias": "figma"}` |
| `DELETE /api/v1/teams/<slug>/servers/<alias>` | Take a server away |
| `"teams": [...]` on `POST` and `PATCH /api/v1/servers` | The teams that reach a server |
| `GET /api/v1/catalog` | The catalog |
| `GET /api/v1/me/tools` | One's own servers and tools |
