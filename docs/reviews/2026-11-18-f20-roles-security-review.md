# Security review: F20, several roles per person (2026-11-18)

Scope: commit `b2e24f2` (ADR 0021). A separate read-only review pass, run as
CLAUDE.md asks for every change to auth, tenancy or the request pipeline.
The reviewer read the code and ran nothing.

**Result: no vulnerability at or above 0.8 confidence.**

## Paths checked

1. **Escalation through `PATCH /api/v1/members`.**
   - Roles are normalised before the owner check, so spaces, repeats and
     unknown names (case included) do not get through.
   - When both `roles` and `role` are sent, `roles` wins. `[]` is refused, and
     unknown fields are refused.
   - Owners and admins can be changed only by an owner. Nobody can edit their
     own roles: person names never change, and a control token cannot carry a
     member's name (ADR 0022).
2. **Invites.**
   - An admin cannot invite an admin. Owners can never be invited.
   - A claim reads `invite_roles` scoped by org and invite. An empty set fails
     closed.
3. **Gateway tokens.**
   - They never reach the admin API.
   - Only a person with `gateway.use` gets one.
   - Every gateway path reads the person's current roles through `AccessFor`
     (scoped by org, removed members included). A token reaches nothing when
     none of those roles uses the gateway.
4. **`role:` rules.** A bound token is matched by its person's current roles,
   and an unbound token by its own role. An unbound token cannot borrow a
   member's roles.
5. **Self-approval.** `Decide` still refuses a person's own request.
   `markOwn` only changes what the list shows.
6. **SQL.** Only `?` markers are built; values are bound, and an empty
   `NOT IN ()` cannot occur.
7. **Tenancy.** The new tables key on `org_id`, with composite foreign keys.
   Every new query and subquery is correlated on `org_id`.
8. **The migration.**
   - It copies each role one to one.
   - It rewrites only bound gateway tokens. Their access still comes from
     their person's roles.
9. **Fail closed.** Unknown or empty roles grant nothing, and errors are
   returned, never replaced by a default role.

## Notes acted on

- **Fixed:** personal gateway tokens are now stored as `member`, so an admin
  could have revoked an owner's or another admin's personal token. That costs
  availability, not access. Revoking is again protected by the person's roles.
  Test: `TestAdminCannotRevokeAnOwnersOwnToken`.
- **Fixed:** `store.Invite` parsed the roles before checking the scan error.

## Notes for the release

- **Rules for personal tokens follow the person now.** An owner's or admin's
  own token was matched by `role:member` rules. It is now matched by the
  person's roles, so a `role:member` rule no longer applies to an owner's or
  admin's AI client.
- **The deprecated `role` field** returns the strongest role, ranked owner,
  admin, approver, auditor, member. Approver and auditor cannot really be
  compared.
- **The down migration is lossy.** It keeps one role per person, so a
  member who also approves becomes an approver, which is the safe direction.
