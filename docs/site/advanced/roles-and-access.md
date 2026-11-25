# Roles and access

What each role may do, how roles combine, and the controls on who can sign in
at all. Inviting people and giving the everyday roles is in
[Inviting and managing people](../basics/people.md).

Several roles per person arrived in 0.11.0
([ADR 0025](https://github.com/talk2sohail/privoclub/blob/main/docs/adr/0025-several-roles-per-person.md)),
and sign-in limited to GitHub organisations in 0.19.0. Tokens belong to
members by their user id since 0.7.0
([ADR 0021](https://github.com/talk2sohail/privoclub/blob/main/docs/adr/0021-tokens-belong-to-members.md)).

## What each role may do

| | owner | admin | member | approver | auditor |
| --- | :-: | :-: | :-: | :-: | :-: |
| Use tools through an AI client | yes | yes | yes | | |
| Decide other people's approvals | yes | yes | | yes | |
| Read activity and the audit trail | yes | yes | | | yes |
| Manage the audit's retention and erasure | yes | yes | | | |
| Manage servers, teams, rules, people and tokens | yes | yes | | | |
| See the servers | yes | yes | yes | yes | yes |

## Combining roles

A person holds **owner** or **admin** alone, or any mix of **member**,
**approver** and **auditor**.

- A member who also approves keeps using their AI client, and decides
  everyone's calls but their own, in the browser.
- A person's own AI client token carries **member** and only uses the
  gateway, whatever other roles they hold. While none of their roles uses the
  gateway, it reaches nothing.
- Approvers who are not members only get control tokens, so a token that can
  approve is never in an AI client's hands.

## Who may change whose roles

- Owners change any set of roles.
- Admins change sets of member, approver and auditor, for people who are not
  owners or admins.
- Nobody changes their own roles.
- Invites never make owners; only owners invite admins.
- Whoever may change a person's roles may also restore them after removal.

Changes apply to the person's next request, and rules for `role:<role>`
follow them. With the admin API: `PATCH /api/v1/members/<person>` with
`{"roles": ["member", "approver"]}`. The single `"role"` field of the member
API is still read and returned, for one release.

## Who can sign in

People sign in with their GitHub account
([ADR 0016](https://github.com/talk2sohail/privoclub/blob/main/docs/adr/0016-admin-sign-in-with-github.md)); Privo ties a person
to their account by its numeric GitHub id, so a renamed GitHub login still
works.
Only invited people can sign in: any other account gets a plain refusal.

To also require membership of your GitHub organisations, set
`PRIVO_GITHUB_ORG_IDS` to their numeric ids, comma-separated
(`https://api.github.com/orgs/<name>` shows an organisation's `id`).

- Sign-in then asks GitHub for `read:org`, and refuses anyone in none of the
  organisations, invited or not.
- An organisation that restricts OAuth App access must allow Privo's app, or
  its members are refused.
- Membership is checked at each sign-in only. When someone leaves the
  organisation, remove them on **People**, which ends their sessions, tokens
  and connections at once.

!!! note
    Privo cannot require two-factor authentication at GitHub. Turn it on for
    everyone who approves calls; an organisation can require it.

For GitHub Enterprise Server, set `PRIVO_GITHUB_BASE_URL` and
`PRIVO_GITHUB_API_URL`.

## Sessions

A browser session ends after `PRIVO_SESSION_IDLE_TIMEOUT` without a request
(2 hours by default, 1 minute to 24 hours), or `PRIVO_SESSION_MAX_AGE` after
sign-in whatever happens (12 hours by default, 5 minutes to 720 hours).

All these settings are in the
[configuration reference](../installation/configuration.md).
