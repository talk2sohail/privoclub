# 0025. A person holds several roles; their gateway token only uses the gateway

- Status: Accepted, 2026-11-18 (by the owner; numbered 0021 on its branch, renumbered when main had taken 0021 to 0024)
- Date: 2026-11-18
- Amends: 0011 (roles combine, now for people), 0014 (point 2: a person who
  approves may also use the gateway)
- Related: 0005 (approvals, self-approval), 0016 (sessions), 0017 (catalog
  grants), 0020 (teams, `role:` and `team:` rules), 0021 (tokens belong to
  members by user id)

## Context

ADR 0011 says "roles combine", but each user and each token carries exactly
one role (`users.role`, `api_tokens.role`). Owners and admins hold every
permission, so they already act as administrators and as ordinary users. The
other roles do not combine with anything:

- A person cannot be an ordinary member who also approves requests. The
  approver role has no `gateway.use`, and `IssueToken` refuses an approver a
  gateway token (ADR 0014 point 2).
- A team lead who should approve their team's calls has to be made an admin,
  which also gives them servers, policies, tokens, members and audit
  settings.

ADR 0014 had a good reason for point 2: an AI agent must never hold a token
that can approve its own calls. Since then, people sign in with GitHub
(0016). Approving happens in a browser session, and gateway tokens work only
on `/mcp`. That reason is now covered by the token kinds, not by the
approver role, so the role restriction can be lifted without losing the
protection.

We considered three ways to let people do more than one kind of thing:

1. **Permission groups as a new concept** next to roles and teams. Rejected:
   it adds a third place to look when asking "who can do what". Roles and
   teams already answer that question.
2. **Teams carry roles** ("everyone in `leads` is an approver"). Deferred.
   Team membership is changed often and by anyone with `members.manage`.
   Keeping "who reaches which servers" (teams) apart from "who may do which
   admin action" (roles) keeps a review of either one simple. Mapping
   identity provider groups to roles (F20, SSO) is the place to revisit this.
3. **Several roles per person.** Chosen: it is what 0011 promised, it is a
   small change and it stays easy to audit.

## Decision

1. **A person holds a set of roles.** The new table `user_roles` has
   `org_id`, `user_id`, `role`, `source`, `granted_by` and `granted_at`, and
   the primary key `(org_id, user_id, role)`. The migration copies
   `users.role` into it and drops the column. `source` is `manual` for now;
   identity provider mapping (F20) will add `idp:<group>` so that
   deprovisioning removes exactly the roles it granted. Invites carry a role
   set the same way (`invite_roles`).
2. **Only these sets are valid:** `{owner}`, `{admin}`, or any non-empty
   subset of `{member, approver, auditor}`. Owner and admin already hold
   every permission, so combining them with anything else adds nothing and
   only makes rules harder to read. The store refuses an empty set; removing
   a person stays a separate action. The server admin and policy admin roles
   of 0011 will join the second group when they are built.
3. **Permissions are the union** of the set's roles. `Principal` carries the
   sorted set; `Can` checks every role in it. A principal with no role has no
   permission (fail closed). Code asks `p.Has(role)` or `p.Can(perm)`, never
   `p.Role == ...`. The roughly twenty direct comparisons today (members,
   invites, tokens, catalog grants, the app's offered roles) move to these
   helpers, and a test keeps new ones out.
4. **Roles belong to the person, not to the gateway token.**
   - A gateway token gets one capability: `gateway.use`. It is stored with
     role `member`, whatever roles its person holds. The migration rewrites
     existing gateway tokens of signed-in people to `member`.
   - A person may get a gateway token when one of their roles grants
     `gateway.use` (member, admin or owner). A person who is only an approver
     or only an auditor still gets none.
   - Policy subjects `role:<role>` match any of the person's **current**
     roles. The gateway reads them on every request with one indexed query,
     like teams (0020), so a change applies to the next request. A token
     whose person is not a user (automation) matches by its own role, as it
     does today.
   - Deciding approvals and every admin action stay on the admin surface: a
     signed-in session (0016) or a control token. The gateway token of a
     member who also approves gets 401 on the admin API, exactly as today.
5. **Control tokens keep one role.** They are for automation, and one role is
   the least privilege. Members still get no control token.
6. **Who may change roles.** These rules exist today; this ADR restates them
   for sets:
   - A change whose set contains owner or admin, before or after, needs an
     owner.
   - Admins change sets within `{member, approver, auditor}`.
   - Nobody changes their own roles.
   - The last active owner cannot lose owner.
   - Each change writes one audit event that records the set before and
     after, with the person as actor.
   - Losing owner or admin drops the person's catalog grants in the same
     transaction, as `SetUserRole` does today (0017).
7. **Self-approval is unchanged.** It is refused per person unless the org
   allows it (0005). More people will now hold member and approver together,
   so the approvals list marks a person's own requests and turns off their
   decide buttons, with the reason shown.

## Consequences

- "Roles combine" in 0011 now holds for people. Control tokens and
  automation still carry one role.
- 0014's protection holds through token kinds and sessions instead of the
  approver role: no single credential both calls tools and decides
  approvals.
- The binding of a gateway token to its person matters more: a `role:`
  rule follows the member found by it. ADR 0021 binds tokens to their member
  by user id (merged 2026-11-11), so no token can borrow someone else's
  roles.
- Teams still decide which servers a person reaches. Roles decide what a
  person may administer. Neither one grants the other.
- Approvers limited to a team or a server ("the designers' lead approves
  Figma calls") are a likely next step. They need their own ADR. This model
  leaves room for them: a scope on `user_roles`, or approver rules that name
  a team.
