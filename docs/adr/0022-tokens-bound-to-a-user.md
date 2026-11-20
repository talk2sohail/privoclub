# 0022. A token is bound to its member by id, never by a person name

- Status: Proposed, 2026-11-18 (accept after the security review of the fix)
- Date: 2026-11-18
- Amends: 0014 (point 3: the person on a token), 0017 (which grant a call uses),
  0020 (whose teams a token follows)
- Fixes: F07 review R-01 (High) and F14 review R-02, 2026-11-03

## Context

A gateway token found its member by the free-text `person` it carries:
`UserByPerson(token.person)`. That one lookup decided whose upstream account a
call used (0017), which teams the token followed (0020), and, through them,
which rules applied. Any owner or admin could issue a token with any `person`,
and a token named `ada` without a person was Ada. The independent review of
2026-11-03 showed an admin calling Linear as a member that way. The audit named
the token and not whose account it used, so the trail did not show it either.

## Decision

1. **`api_tokens.user_id` binds a token to a member.** Only
   `POST /api/v1/me/tokens` sets it, for the signed-in person, from their
   session. The principal carries it as `UserID`, as a session does.
2. **Members are found by id only.** Upstream grants, teams, `/me/tools`
   and every "own token" route read `UserID`. Nothing finds a member by a
   token's person any more. A token with no user reaches `everyone` and no
   one's own account; on such a server it is told to make a token of its own.
3. **Other tokens may not carry a member's name.** `POST /api/v1/tokens` and
   `privo token create` refuse a `person` (or a defaulted name) that names a
   member, whatever the kind. Those tokens are for services and scripts.
   This keeps the token list and the audit honest about who a token is.
4. **Control tokens have no own tokens.** The `/me/tokens` routes answer 403
   to a principal without a user, whatever person it names.
5. **Calls record the account they used.** A call on a server where each
   person uses their own account writes `account` ("<person> (user <id>)")
   on its audit record; the API returns it and the Activity page shows it.
6. **Existing tokens.** The migration binds only the tokens that a person
   made for themselves while signed in. It recognises them by their
   `token.create` audit event: same role and name, the person's own user as
   actor, written within 5 seconds of the token. Tokens an administrator made
   for someone, and tokens made by the CLI, stay unbound. Their holders make
   new tokens on the Tokens page.
7. Removing a member revokes the tokens bound to them, and also the older
   unbound ones that carry their person.

## Consequences

- An owner or admin, or a stolen admin session or control token, can no
  longer act as a member upstream or inherit a member's teams. They can
  still change rules and teams, and that is audited.
- A person's name on a token is now a label. Self-approval still compares
  persons (0005), which only ever refuses more.
- Upgrading may stop a token an administrator made for someone from using
  that person's accounts. The fix is one click for the person: make their own
  token. The release notes say so.
- ADR 0021 (several roles per person) builds on this: `role:` rules follow
  the member found here.
