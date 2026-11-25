# Per-team toolsets

A team that reaches a server sees all of its accepted tools. A toolset
narrows that: for one team and one server, the team sees only the tools you
tick. Use it when a team needs a server but not all of it, such as designers
who read GitHub issues but never merge pull requests.

Toolsets arrived in 0.14.0
([ADR 0026](https://github.com/talk2sohail/privoclub/blob/main/docs/adr/0026-toolsets.md)).

## Choose which tools a team sees

1. Open **Servers** and the server.
2. Under **Which tools each team sees**, for each team that can use the
   server, choose:
    - **All tools**: every tool of the server that was accepted in review;
    - **Only these**: tick the tools the team may see.
3. Choose **Save**. The team sees the change at its next request.

Only reviewed tools are listed. A tool the server no longer offers is marked
"not offered now".

## How toolsets combine

- A person in several teams sees what any of their teams sees.
- A new tool stays hidden from a team that sees only some tools until you
  tick it. Teams on **All tools** see it as soon as it is accepted.
- A tool hidden from a person is answered as if it did not exist, like a
  tool on a server they do not reach.
- Rules, approvals and tool review still apply to every tool a person sees.
  A toolset only hides tools: it never allows a call that a rule asks about
  or denies.

## With the admin API

`PATCH /api/v1/servers/<alias>/teams/<slug>` with
`{"tools": ["get_issue", "list_issues"]}` for some tools, or
`{"tools": null}` for all of them. See
[Admin API and script tokens](api-and-automation.md).
