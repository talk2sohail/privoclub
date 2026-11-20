# Security review: F08 toolsets (2026-11-20)

Scope: commit `daf2ed5` (ADR 0026, numbered 0023 when reviewed). A separate read-only review pass, run as
CLAUDE.md asks for every change to the request pipeline. The reviewer read
the code and ran nothing.

**Result: no vulnerability at or above 0.8 confidence.**

## Paths checked

1. **Lists and calls.** The list stage drops what a person does not see,
   before the pins and collision checks. Calls resolve through the same
   list, and there is no other path to an upstream call.
2. **Approvals, grants and rules.** None of them make a hidden tool
   visible. A grant is only an allow rule.
3. **Prompts and resources** are not narrowed, as ADR 0026 decides. The
   gateway has no tools of its own that would proxy them.
4. **Cached lists.** They are a hint to the client; calls never use them.
5. **"All" and union.** A server is narrowed only when every team that
   reaches it is narrowed. The names seen are the union of those teams'
   lists.
6. **Callers with no member, removed members, and people without
   `gateway.use`:** none of them gain anything.
7. **Lists follow their team row.** A list goes with its server–team row
   through a cascading foreign key. A team taken off a server and put back
   gets "all" again.
8. **Tenancy.** Every query is scoped by org, and the composite keys carry
   `org_id`.
9. **Changing toolsets.** It needs `servers.manage`. The target is resolved
   within the org; a team not on the server is 404.
10. **SQL.** Values are bound; only `?` markers are built.
11. **Probing.** A hidden tool answers exactly like an unknown name.

## Notes acted on

- **Members learned how many tools were hidden from them.** The server list
  counted every tool of a server for members. It now counts only the tools
  they see, as Start does. The acceptance test checks it.
- **A grant could be made for a tool hidden while the request waited.**
  Approving with a grant now needs the requester to still see the tool.
- **The page did not say that prompts and resources stay visible.** It now
  says so.

## Notes left as they are

- **Two reads, no transaction.** `AccessFor` reads the toolset state in two
  queries. A change in between can mix two states in one request; the
  reviewer found no case where that shows more than either state allows.
- **`/me/tools` and roles.** It does not apply the gateway's role check for
  a member whose roles no longer include the gateway. It only displays
  data, and this predates the change.
