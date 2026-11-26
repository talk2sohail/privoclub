# Per-team toolsets

A team that reaches a server sees all of its accepted tools, prompts,
resources and resource templates. A toolset narrows that: for one team and
one server, the team sees only the items you choose for each kind. Use it when
a team needs a server but not all of it, such as designers who read GitHub
issues or resource templates but never merge pull requests.

Toolsets arrived in 0.14.0
([ADR 0026](https://github.com/talk2sohail/privoclub/blob/main/docs/adr/0026-toolsets.md)),
and now cover **Tools**, **Prompts**, **Resources** and **Resource templates**
per kind.

## Choose what a team sees

1. Open **Servers** and the server.
2. Under **What each team sees**, for each team that can use the server and
   each kind (**Tools**, **Prompts**, **Resources**, **Resource templates**),
   choose:
    - **All**: every accepted item of that kind on the server;
    - **Only these**: tick the items of that kind the team may see.
3. Choose **Save**. The team sees the change at its next request.

Only reviewed items are listed. An item the server no longer offers is marked
"not offered now". Existing toolsets keep showing all prompts, resources and
templates until you narrow them.

## How toolsets combine

- A person in several teams sees what any of their teams sees for each kind.
- A new item of a narrowed kind stays hidden from that team until you tick it.
  Teams set to **All** for that kind see it as soon as it is accepted.
- Any item hidden from a person — tool, prompt, resource or resource template —
  is answered as if it did not exist, like an item on a server they do not
  reach.
- Rules, approvals and pin review still apply to everything a person sees. A
  toolset only hides items: it never allows a call or read that a rule asks
  about or denies.

## With the admin API

`PATCH /api/v1/servers/<alias>/teams/<slug>` accepts any combination of
`tools`, `prompts`, `resources` and `resource_templates`: `["a", "b"]` for only
those, `[]` for none, or `null` for all; omitted keys stay unchanged.
`GET /api/v1/servers` includes `team_tools`, `team_prompts`, `team_resources`
and `team_templates` for server managers. See
[Admin API and script tokens](api-and-automation.md).
