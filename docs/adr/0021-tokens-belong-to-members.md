# 0021. A token belongs to a member by user id, never by the name it carries

- Status: Accepted, 2026-11-11 (the owner merged F27, pull request 2)
- Date: 2026-11-11
- Related: 0014 (token kinds and persons; this amends point 3), 0017 (each
  person's own account; amends "maps the token's person to the member"),
  0020 (teams; amends "membership is resolved from the token's person"),
  0016 (sign-in), 0011 (roles)

## Context

ADR 0014 gave every token a `person`, a free text set by whoever issues the
token, and said so: "The person field is set by whoever issues the token."
That was enough for its purpose, the self-approval check.

ADR 0017 and ADR 0020 then used the same text for two things it was never
meant to carry: whose upstream account a call runs as, and which teams'
servers a caller reaches. Both looked the member up by the token's person
name. So an owner or admin could issue a gateway token with `person: "ada"`
(or simply name the token `ada`, since an empty person defaults to the token
name) and call Linear, GitHub or Notion with Ada's own grant; a control token
that named Ada could also ask `POST /api/v1/me/tokens` for "its own" gateway
token. The audit named the token, not whose account was used. The independent
reviews of 2026-11-03 rated this High (F07 R-01, F14 R-02) and it was
re-checked on 2026-11-11: the call answered "you are ada@linear".

An administrator is trusted to run Privo. They are not trusted to act as
another person at that person's providers: the provider's own audit would say
Ada did it.

## Decision

1. **`api_tokens.user_id` says which member a token belongs to.** It is set in
   exactly one place: when a signed-in member makes a token for themselves
   (`POST /api/v1/me/tokens`, through the session). The member must be active
   and of the same org; the check and the insert are one statement.
2. **Every other way of issuing a token makes a token of nobody**
   (`user_id = ''`): `POST /api/v1/tokens`, `privo token create`, the
   bootstrap token. There is no request field that sets the owner, and an
   unknown field is refused.
3. **`person` is a label.** It still names the requester in approvals and in
   the self-approval check of ADR 0014. It is never looked up to find a
   member, a team or a grant.
4. **The caller's member is `Principal.UserID`**, for a session and for a
   token alike. Team access (`gateway.Access`), the per-person upstream
   session (`grantOwner`) and "my" endpoints read it and nothing else.
5. **A token of nobody** reaches the servers of the team "everyone" and
   shared-credential servers there. On a server where each person uses their
   own account it is told to sign in and make a token of its own. It is never
   in a team.
6. **Existing tokens are not given an owner by the migration.** Which of them
   a member made for themselves cannot be told from the rows, and guessing
   from the person name would keep the hole for every token already forged.
   People make a new token on the Tokens page; the old one is listed there as
   not tied to their account, and they can revoke it.
7. **The audit records whose account and teams a call ran as**
   (`audit_events.user_id`, shown as `user` and `user_id` in
   `GET /api/v1/audit/events`), next to the token that made it.
8. **Removing a member** revokes the tokens that belong to them and, as
   before, the tokens of nobody that name them.

## Consequences

- An administrator can no longer use a member's account or see a member's
  team servers with a token. What an administrator can still do, and what the
  audit shows: remove the member, disconnect their account, change teams.
- **Breaking for existing deployments:** gateway tokens made before this
  version keep working on shared servers that everyone reaches, and stop
  working on own-account servers and on servers given to a team, until the
  person makes a new token. Version 0.7.0, noted under "Breaking" in
  `docs/deploy-coolify.md`.
- A script that needs a server reaches it through "everyone" with a shared
  credential. A service account that is in a team is not possible yet; if it
  is wanted it becomes its own kind of member, not a name on a token.
- Signing AI clients in with OAuth (F06) fits this without change: the
  access token Privo issues there belongs to the member who signed in.
- Not changed here, and still open: the self-approval check compares person
  labels (ADR 0014, F05 R-04); a token keeps the role its member had when it
  was made.
