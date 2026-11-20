# 0026. Toolsets: a team can see only some of a server's tools

- Status: Proposed, 2026-11-20 (accept after the F08 review)
- Date: 2026-11-20
- Refines: 0020 (teams reach servers), 0002 (`toolsets` were planned per user or
  group)
- Related: 0001 (list and resolve stages), 0011 (who manages servers), 0025
  (roles)

## Context

ADR 0020 decides which **servers** a team reaches. A server often offers far
more tools than a team needs. GitHub offers dozens, and a designer needs two
of them. Every tool a person's AI client lists costs context. Every tool
listed is also one the model may call. Admins asked to hand a team "these
tools of GitHub", not all of GitHub.

ADR 0002 planned `toolsets`, `toolset_items` and `toolset_grants` as named
sets granted to users or groups. Teams now exist, and they are how access is
granted. So a toolset is simplest as a property of the pair (server, team)
rather than a third object to name and grant.

## Decision

1. **Per server and team, all tools or a chosen list.** The new table
   `server_team_tools` (`org_id`, `server_id`, `team`, `name`) lists the
   tools a team sees on a server. When a team has no rows for a server, it
   sees all of that server's tools, as it does today. The default and the
   migration keep everything as it is.
2. **A person sees a tool when any of their teams that reaches the server
   sees it.** Lists combine. A team that sees all tools, `everyone`
   included, opens all of them to its members. `everyone` can be narrowed
   like any team.
3. **Names, not definitions.** The list holds upstream tool names. A chosen
   tool whose definition changes stays chosen, but still needs review
   (pins, F11): toolsets narrow what pins allow and never widen it. A new
   tool on a server is hidden from a narrowed team until someone chooses it
   (fail closed). Names that the server does not offer (yet) may be
   chosen.
4. **Enforced in the gateway.** The list stage leaves out tools a caller
   does not see. The resolve stage answers a call on such a tool exactly
   like an unknown name, as for servers outside one's teams (0020 point 4),
   so nobody can probe for hidden tools. Access is read per request, so a
   change applies to the next request.
5. **Tools only.** Prompts and resources follow server access for now.
   Narrowing them is the same model with a `kind` column, added when
   someone needs it.
6. **Who changes it.** Changing it needs `servers.manage` (owners and
   admins), like a server's teams. Each change is one audit event,
   `server.team_tools:<alias>:<team>:<all|n>`. Removing a team from a
   server, or deleting the team, deletes its list in the same transaction.
7. **Per person.** No per-person toolsets: a team of one serves that need.
   This keeps one place to look when asking who sees what.

## Consequences

- An admin can give the designers GitHub's `search_code` and
  `get_file_contents` while the developers keep all of GitHub, and the
  designers' AI clients list two GitHub tools instead of all of them.
- Policies, approvals and pins still apply to every tool a person sees.
  Toolsets decide visibility; rules decide what a visible call may do.
- The start page and the context-cost report (F17) count what each person
  actually sees.
