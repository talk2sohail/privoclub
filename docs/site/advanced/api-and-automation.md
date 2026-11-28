# Admin API and script tokens

Everything the admin site does is a call to Privo's admin API, under
`https://privo.example.com/api/v1/`. The admin site runs those calls in
process as the person signed in
([ADR 0019](https://github.com/talk2sohail/privoclub/blob/main/docs/adr/0019-admin-app-preact.md));
scripts and CI call the same API with a token.

## Tokens

Privo has two kinds of personal token
([ADR 0014](https://github.com/talk2sohail/privoclub/blob/main/docs/adr/0014-token-kinds-and-persons.md)),
both starting `privo_pat_`:

- **Gateway tokens** go in AI clients and work only on `/mcp`.
- **Control tokens** are for administering Privo and work only on the admin
  API. Approvers only get control tokens.

Each token carries one role, and lives `PRIVO_TOKEN_TTL` (see the
[configuration reference](../installation/configuration.md)). Revoking one
takes effect within about two seconds.

### Tokens that belong to nobody

A gateway token made on **Start** or **Tokens** belongs to the member who made
it, signed in
([ADR 0021](https://github.com/talk2sohail/privoclub/blob/main/docs/adr/0021-tokens-belong-to-members.md)).
A token made any other way, with `privo token create` or
`POST /api/v1/tokens`, belongs to nobody, whatever `person` it names:

- it reaches the servers everyone can use, and never anyone's own account;
- its `person` is only a label for approvals and the audit;
- token lists show which tokens are bound to a member (`bound`).

Use them for scripts and CI. Their names are unique among the tokens that
belong to nobody; members' token names are unique per person.

### Make one

Where you have a shell on the server:

```sh
privo token create --name ci --role member --kind gateway      # a script that calls tools
privo token create --name ops --role admin --kind control      # a script that manages Privo
```

With the admin API, as an owner or admin:

```sh
curl -sS -X POST -H "Authorization: Bearer $OWNER" -H 'Content-Type: application/json' \
  -d '{"name":"ci","role":"member","kind":"gateway","person":"ci"}' \
  https://privo.example.com/api/v1/tokens
```

The reply holds the token once; copy it then.

On the admin site, **Tokens → Create an API token (for scripts and
automation)** makes a control token for the admin API, with a **Person** label
and a role. Only owners make owner or admin tokens, and an owner or admin
control token made from the browser needs a confirmation (`"confirm": true`
in the API): it can do everything that role can, without signing in, for its
whole lifetime.

## Calling the API

```sh
curl -sS -H "Authorization: Bearer $TOKEN" https://privo.example.com/api/v1/servers
```

Requests and answers are JSON. An error answers with its HTTP status and
`{"error": "..."}`: 401 for a missing or invalid token, 403 when the token's
role may not do that, 422 for a request that does not make sense.

## The command line

| Command | What it does |
| --- | --- |
| `privo admin setup-link [--person NAME]` | A one-time link that makes the owner, valid for an hour |
| `privo admin invite --person ada [--role admin \| --role member,approver,auditor]` | A one-time invite link, valid for 72 hours |
| `privo token create --name NAME [--role ROLE] [--kind control\|gateway] [--person NAME]` | A token, stored in the database |
| `privo token generate` | A new token, for the first boot ([Deploy on Coolify](../installation/coolify.md#first-run-your-first-admin-token)) |
| `privo audit verify` | Check the audit trail; exit 1 on a problem |
| `privo config show` | Every setting, its value and where it comes from |

The commands that touch the database run on the server, next to Privo's data.

## Endpoints

**Who** is the roles that may call each one.

### Servers and tools

| Call | Who | What it does |
| --- | --- | --- |
| `GET /api/v1/servers` | everyone | List servers |
| `POST /api/v1/servers` | owner, admin | Add a server (below) |
| `PATCH /api/v1/servers/<alias>` | owner, admin | Change `url`, `headers` (`""` removes one), `oauth_client` (`client_id`, `client_secret`), `enabled` or `teams` |
| `DELETE /api/v1/servers/<alias>` | owner, admin | Delete a server, its rules and everyone's connections |
| `GET /api/v1/servers/<alias>/pins` | owner, admin | Tool definitions and their review state |
| `POST /api/v1/servers/<alias>/pins:accept` | owner, admin | Accept definitions |
| `POST /api/v1/servers/<alias>/pins:block` | owner, admin | Block definitions |
| `PATCH /api/v1/servers/<alias>/teams/<slug>` | owner, admin | A team's toolset: `{"tools": [...]}` or `{"tools": null}` |
| `POST /api/v1/servers/<alias>/policy:preset` | owner, admin | Rules from a preset: `read-only` or `ask-before-writes` |
| `GET /api/v1/catalog` | owner, admin | The built-in catalog |

Adding a server: `alias`, `transport` (`http`, `sse`, or `stdio` for
[local stdio servers](stdio-runner.md)), `url`, and how the server sees the
caller: `headers` for a shared key, `oauth` for each person's account, or
`personal_key` for each person's key (see
[Custom upstream sign-in](upstream-auth.md)). `teams` names the teams that
reach it; without it, the server is for everyone.

```sh
curl -sS -X POST -H "Authorization: Bearer $OWNER" -H 'Content-Type: application/json' -d '{
  "alias": "acme", "transport": "http", "url": "https://mcp.acme.example/mcp",
  "headers": {"Authorization": "Bearer <the key>"}, "teams": ["payments"]
}' https://privo.example.com/api/v1/servers
```

### Connections

| Call | Who | What it does |
| --- | --- | --- |
| `GET /api/v1/connections?person=&server=` | owner, admin | Everyone's connections, across servers |
| `GET /api/v1/servers/<alias>/connections` | owner, admin | One server's connections |
| `DELETE /api/v1/servers/<alias>/connections/<person>` | owner, admin | End one, revoking it at the provider |

### Teams

| Call | Who | What it does |
| --- | --- | --- |
| `GET`, `POST /api/v1/teams` | owner, admin | List; create with `name`, `slug`, `description` and `profile` |
| `PATCH`, `DELETE /api/v1/teams/<slug>` | owner, admin | Rename or describe; delete |
| `POST /api/v1/teams/<slug>/members` | owner, admin | Add a person: `{"person": "ada"}` |
| `DELETE /api/v1/teams/<slug>/members/<person>` | owner, admin | Take a person out |
| `POST /api/v1/teams/<slug>/servers` | owner, admin | Give a server: `{"alias": "figma"}` |
| `DELETE /api/v1/teams/<slug>/servers/<alias>` | owner, admin | Take a server away |

### Rules and approvals

| Call | Who | What it does |
| --- | --- | --- |
| `GET`, `POST /api/v1/policies` | owner, admin | List rules and the default; add a rule |
| `DELETE /api/v1/policies/<id>` | owner, admin | Delete a rule |
| `GET /api/v1/policies/export` | owner, admin | Every rule and the default, as YAML |
| `POST /api/v1/policies/import` | owner, admin | Import YAML: `mode` `merge` or `replace`, `dry_run` |
| `PATCH /api/v1/settings/policy` | owner, admin | The default: `{"default": "ask"}` |
| `PATCH /api/v1/settings/approvals` | owner, admin | `{"allow_self": true}` lets people approve their own calls |
| `GET /api/v1/approvals`, `GET /api/v1/approvals/<id>` | owner, admin, approver | Waiting approvals |
| `POST /api/v1/approvals/<id>/approve` | owner, admin, approver | Approve; `{"grant": "15m"}`, `"1h"` or `"8h"` for a grant |
| `POST /api/v1/approvals/<id>/deny` | owner, admin, approver | Deny |

A rule takes `server`, `tool`, `subject`, `action`, and optionally
`priority`, `note`, `expires_in` and `conditions` (see
[Conditional rules, grants and YAML](policy-engine.md)).

### People

| Call | Who | What it does |
| --- | --- | --- |
| `GET /api/v1/members` | owner, admin | Members and their roles |
| `PATCH /api/v1/members/<person>` | owner, admin | Change roles: `{"roles": ["member", "approver"]}` |
| `POST /api/v1/members/<person>/remove` | owner, admin | Remove: sessions, tokens and connections end |
| `POST /api/v1/members/<person>/restore` | owner, admin | Restore with their account and roles |
| `POST /api/v1/invites` | owner, admin | An invite link: `{"person": "ada", "roles": ["member"]}`; the link is `invite.url` in the reply |

### Tokens and OAuth clients

| Call | Who | What it does |
| --- | --- | --- |
| `GET`, `POST /api/v1/tokens` | owner, admin | List every token; make one that belongs to nobody |
| `DELETE /api/v1/tokens/<id>` | owner, admin | Revoke a token |
| `GET`, `POST /api/v1/oauth/clients` | owner, admin | AI clients that may sign in; approve one |
| `POST /api/v1/oauth/clients:remove` | owner, admin | Remove an approved client |
| `GET /api/v1/oauth/grants`, `DELETE /api/v1/oauth/grants/<id>` | owner, admin | Clients members let in; end one |

### Audit and telemetry

| Call | Who | What it does |
| --- | --- | --- |
| `GET /api/v1/audit/events` | owner, admin, auditor | The audit trail, filtered |
| `GET /api/v1/audit/export` | owner, admin, auditor | CSV, at most 10,000 rows |
| `GET /api/v1/audit/integrity` | owner, admin, auditor | Check the chain |
| `GET /api/v1/audit/checkpoints`, `GET /api/v1/audit/keys` | owner, admin, auditor | Signed checkpoints and public keys |
| `PATCH /api/v1/settings/audit` | owner, admin | `retention_days`, `record_arguments`, `record_results` |
| `POST /api/v1/audit/erase` | owner, admin | Erase a person's recorded arguments: `{"person": "ada"}` |
| `GET /api/v1/telemetry/summary`, `timeseries`, `calls`, `logs`, `storage` | owner, admin, auditor | The Activity figures |

### Your own

These act for whoever calls them, and need a role that uses tools (owner,
admin or member):

| Call | What it does |
| --- | --- |
| `GET /api/v1/me/tools` | Your servers and tools (with each tool's enabled state) |
| `PATCH /api/v1/me/servers/<alias>/tools` | Turn tools off or back on for yourself: `{"disabled": [...]}` |
| `GET`, `POST /api/v1/me/tokens`, `DELETE /api/v1/me/tokens/<id>` | Your AI clients' tokens |
| `GET /api/v1/me/approvals`, `POST /api/v1/me/approvals/<id>/withdraw` | Your waiting calls; withdraw one |
| `GET /api/v1/me/clients`, `DELETE /api/v1/me/clients/<id>` | AI clients you let in; take access away |
