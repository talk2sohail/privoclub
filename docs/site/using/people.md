# Inviting your team

## Invite

```sh
privo admin invite --person ada [--role admin | --role member,approver,auditor]
```

prints a link that works once, for 72 hours (also `POST /api/v1/invites` with
`{"person": "ada", "roles": ["member"]}`, or **People → Invite** on the admin
site). Ada opens it and signs in with GitHub; her account is bound by its
numeric GitHub id. She then makes a token for each of her AI clients on the
**Tokens** page ([Tokens and AI clients](tokens.md)).

[Roles](roles.md) explains what each role may do.

## Remove

**People → Remove** ends the person's sessions, tokens and connections at
once. When sign-in is limited to GitHub organisations
(`PRIVO_GITHUB_ORG_IDS`), membership is checked at each sign-in only: when
someone leaves the organisation, remove them here.

## Restore

**People → Restore** (or `POST /api/v1/members/{person}/restore`) brings back
a removed member with their GitHub account and roles; they sign in and make
new tokens. Whoever may change their roles may restore them.
