# Roles

A person holds a set of roles
([ADR 0021](https://github.com/talk2sohail/privoclub/blob/main/docs/adr/0021-tokens-belong-to-members.md),
[ADR 0025](https://github.com/talk2sohail/privoclub/blob/main/docs/adr/0025-several-roles-per-person.md)):
**owner** or **admin** alone, or any of:

| Role | What it does |
| --- | --- |
| **member** | Uses tools through an AI client |
| **approver** | Decides other people's approvals |
| **auditor** | Reads the activity |

A member who also approves keeps using their AI client and decides everyone's
calls but their own, in the browser.

## Changing roles

Change them on **People**, or with `PATCH /api/v1/members/<person>` and
`{"roles": ["member", "approver"]}`.

- Owners change any set.
- Admins change sets of member, approver and auditor for people who are not
  owners or admins.
- Nobody changes their own.

Changes apply to the person's next request, and rules for `role:<role>`
follow them.

A person's own AI client token carries `member` and only uses the gateway,
whatever roles they hold; while none of their roles uses the gateway it
reaches nothing. The single `"role"` of the member API is still read and
returned for one release.
