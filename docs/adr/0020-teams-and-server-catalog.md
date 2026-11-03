# 0020. Teams decide which servers a person reaches; a built-in catalog adds them

- Status: Proposed, 2026-11-03 (accept after the F26 review)
- Date: 2026-11-03
- Related: 0001 (pipeline: resolve and list stages), 0002 (data model:
  `groups`, `toolsets`), 0011 (roles), 0014 (persons), 0017 (each person's
  own account), 0019 (admin app)

## Context

Today every server an org adds is seen by every AI client of the org. A
company has developers, designers and product managers; each group wants a
few servers (GitHub and Sentry, Figma and Canva, Notion and Linear), and
nobody wants 200 tools from servers they never use in their model's context.
Adding a server also means finding its URL, its transport and whether it
needs an OAuth app, which every admin looks up again.

ADR 0002 planned `groups` and `toolsets` (tool-level visibility per user or
group). Tool-level toolsets are still wanted (F08), but the first need is
coarser and simpler: which *servers* a team reaches.

## Decision

1. **Teams.** An org has teams (`teams`: slug, name, description, profile).
   The slug is the key: lowercase, 2 to 32 characters, never changed after
   creation, so rules can name it. People belong to any number of teams
   (`team_members`, by user id). Owners and admins manage teams
   (`members.manage`). The ADR 0002 name `groups` becomes `teams`; identity
   provider group mapping (0011) will map onto teams.
2. **`everyone` is a built-in team** that every active member of the org is
   in. It has no row and cannot be created, renamed or deleted. A token whose
   person is no member (an automation token) is in `everyone` only.
3. **Server access is a set of teams** (`server_teams`). A person reaches a
   server when they are in one of its teams. A server added without teams gets
   `everyone`, which keeps the API and existing installs unchanged; the
   migration gives every existing server `everyone`. A server with no team is
   reachable by nobody (fail closed); the app warns about it.
4. **Enforced in the gateway, not in the client.** The list stages leave out
   items of servers the caller cannot reach, and the resolve stage refuses a
   call, prompt or read on them with the same "not available" answer as an
   unknown name, so a person cannot probe for servers outside their teams.
   Access is read per request (one indexed query), like pins and rules, so a
   change applies to the next request.
5. **Rules can name a team**: subject `team:<slug>` (0001's Actor groups).
   Deleting a team deletes its rules, as deleting a server does.
6. **A built-in catalog** of hosted MCP servers, embedded in the binary
   (`internal/catalog`), each with a URL, transport, sign-in kind, whether
   the provider needs an OAuth app of the admin's own (no dynamic
   registration), and the profiles it suits (developers, designers, product,
   everyone). The catalog only fills in the add-server form: adding still goes
   through `POST /api/v1/servers` and all of its checks (egress, discovery,
   review of every tool). Entries are checked by a probe before they are
   listed and carry the date of that check.
7. **Profiles** are named starting points for a team (`developers`,
   `designers`, `product`). A team made from a profile is shown the catalog
   entries for it; nothing is added without the admin's click.
8. **A start page for every person** who uses the gateway: their teams,
   the servers they reach (and whether they still need to connect their own
   account), and the setup of their AI client (Claude Code, Cursor, VS Code,
   Codex, Windsurf, others) with a token made on the spot.

## Consequences

- Admins see everything in the admin app; their own AI clients follow their
  teams like everyone else's.
- Team membership is resolved from the token's person, the same binding
  0017 uses for upstream grants. The independent review of 2026-11-03 (F07
  R-01) found that binding too loose (a free-text person); the fix binds
  tokens to a user id and applies here unchanged.
- Tool-level toolsets (F08) are a later refinement inside a server; they do
  not change this model.
- The catalog goes stale as providers move. It is data, reviewed with the
  code, and an entry is a suggestion: the server's own answers (discovery,
  its tool list) decide.
