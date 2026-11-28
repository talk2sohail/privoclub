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

## Turning off tools for yourself

Within the tools their teams allow, each signed-in member can see the full
list on **Start** (choose **Tools** next to a server) and turn off tools they
do not need right now to save context in their AI clients:

- Turning a tool off hides it from `tools/list` for that person only; teammates
  are unaffected.
- If an AI client still tries to call a tool you turned off yourself, Privo
  refuses the call with a clear reminder and a link back to **Start** (audited
  as `refused:disabled_by_user`, without creating an approval request).
- Personal toggles only narrow access: a member can never enable an unreviewed
  tool or a tool hidden by an administrator's team toolset. If an administrator
  later hides a tool you turned off, it answers as unavailable like any other
  hidden tool.

## With the admin API

- Admin team toolsets: `PATCH /api/v1/servers/<alias>/teams/<slug>` with
  `{"tools": ["get_issue", "list_issues"]}` for some tools, or
  `{"tools": null}` for all of them.
- A member's own toggles: `PATCH /api/v1/me/servers/<alias>/tools` with
  `{"disabled": ["create_issue"]}` to turn tools off, or `{"disabled": []}`
  to enable all tools their teams grant.

See [Admin API and script tokens](api-and-automation.md).
