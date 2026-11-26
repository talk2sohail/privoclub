# Threat model

What Privo protects, whom it trusts, what it defends against, and what it does
not try to do: a summary of
[ADR 0009](https://github.com/talk2sohail/privoclub/blob/main/docs/adr/0009-threat-model.md)
for operators and security reviewers. Each protection says the version it
arrived in, or that it is planned. This page describes Privo 0.20.0.

## What Privo protects

- **The credentials it holds** for your servers: shared keys, and each
  person's own account or API key. See [Secrets and keys](secrets.md).
- **Control over what AI clients do**: which servers and tools they reach, and
  the rules and approvals in front of them.
- **The record** of who called what, what was decided, and every admin change.
- **People's access**: their sessions, tokens and connections.

## Who is trusted

| Party | Trusted | What that means |
| --- | --- | --- |
| Anyone with access to the host, the container or Coolify | Fully | They can read the master key and the database; Privo cannot protect against them |
| Owners and admins | For your organisation | They manage servers, rules, people and tokens; every change is recorded |
| Members | To act as themselves, within the rules | Their calls run with their own accounts |
| Approvers and auditors | For their task | Deciding other people's calls; reading the record |
| AI clients and the models behind them | **No** | A model can be steered by anything it reads (prompt injection) |
| Upstream MCP servers | **No** | Their tool definitions are reviewed before use; their replies are never trusted |
| Packages a stdio server runs | **No** | Refused in production until the isolated runner exists |

## Where the boundaries are

- **AI client to gateway.** Every call to `/mcp` carries a Privo token. It
  passes the tool review, the argument check, the rules and any approval
  before it reaches a server. The server's credentials never travel back to
  the client.
- **Browser to admin site.** People sign in with GitHub. Sessions live on the
  server, in HTTP-only, SameSite cookies (`__Host-` cookies over HTTPS). Every
  change checks a CSRF token and the origin, which comes from
  `PRIVO_PUBLIC_URL`, never from the request.
- **Gateway to servers.** Every outbound request goes through one client that
  refuses internal addresses ([Outbound requests](egress.md)). A call runs
  with that person's own credential, or the server's shared one, and the reply
  is checked before it is relayed.
- **Privo to its host.** The database holds credentials only encrypted and
  tokens only hashed. The master key lives outside it.

## Defaults that fail closed

- Privo does not start without a master key, or with one that cannot open its
  database.
- A new or changed tool is hidden from every AI client until an admin accepts
  its exact definition.
- A call whose arguments do not match the accepted schema, or whose schema
  cannot be read, is refused (0.20.0).
- A call is refused when its audit record cannot be written.
- Internal addresses are refused for servers unless an operator allows a
  range.
- Stdio servers are refused unless the development-only runner is switched on.
- AI clients sign in with OAuth, or register themselves, only when an operator
  switches it on (`PRIVO_OAUTH_SERVER`, `PRIVO_OAUTH_DYNAMIC_REGISTRATION`).
- Only invited people can sign in.
- Personal tokens always expire (`PRIVO_TOKEN_TTL`).
- Nobody approves their own calls unless an admin turns that on.
- A tool's own annotations can make a decision stricter, never looser.

The settings are in the [configuration reference](../installation/configuration.md).

## The threats

### Prompt injection

A model can be steered by text it reads, such as a web page, an issue or a tool
result, into calling tools it should not, or with arguments it should not.

- Every call passes the rules first: allow, ask or deny, by server, tool, who
  is calling and the call's arguments (conditions since 0.15.0). See
  [Rules and approvals](../basics/rules-and-approvals.md).
- An approver sees the call's arguments, and what they approve is exactly what runs.
  The arguments are frozen into one canonical form, and the approval is bound
  to their digest and used once (from the first release).
- A team's toolset keeps tools out of its AI clients' sight (0.14.0).
- **Planned:** a rule that stops data read from one server flowing to another
  (ADR 0001). Privo does not read results for injected instructions: they
  reach the model as the server sent them, after protocol checks.

### Tools that mislead or change

A server can describe a tool so that it misleads the model (tool poisoning),
or change a tool after you reviewed it (a rug pull).

- Each tool definition is pinned by its hash. New and changed tools stay
  hidden from every AI client until an admin accepts the exact definition
  (from the first release).
- The review shows the whole definition, with characters a person cannot see
  made visible, and what changed since the accepted version (0.8.1).
- A scan flags hidden instructions and similar text for the reviewer (0.12.0).
  It is a hint, never a gate: finding nothing proves nothing.
- **Block** keeps a definition hidden for good (0.12.0).

### Credential theft

- Credentials never reach an AI client: Privo adds them to its own request to
  the server. The client's token is never passed through.
- Credentials are encrypted at rest, write-only through the admin API, and
  never logged. See [Secrets and keys](secrets.md).
- Each person's account or API key is used only for that person's calls.
- A server's redirect to another host is refused, so a credential is never
  sent there.
- When a member is removed or a server deleted, their grants are revoked at
  the provider (0.18.1).

### Stolen Privo tokens

- Tokens are stored only as hashes. Personal tokens start `privo_pat_` and
  carry a checksum, so secret scanners can find leaked ones.
- Gateway tokens work only on `/mcp`, and control tokens only on the admin
  API. An AI client's OAuth token never works on the admin API.
- Personal tokens always expire; revoking one takes effect within about two
  seconds.
- OAuth access tokens last an hour and are issued only for this Privo's MCP
  address. A refresh token used twice ends the grant (0.8.0).

### Privilege escalation

- Every request is checked against a fixed table of what each role may do
  ([Roles and access](../advanced/roles-and-access.md)). A role change applies
  at the next request.
- Nobody changes their own roles. Admins cannot make owners or admins; only
  owners invite admins, or make owner and admin tokens.
- An AI client's token acts as a member and only uses the gateway, whatever
  its person's other roles (0.11.0). So a model that has been turned against
  you cannot approve calls, change rules or read the record.
- Sign-in is by GitHub account id, for invited people only, and optionally
  only members of your GitHub organisations (0.19.0).

### Requests to internal addresses (SSRF)

Privo fetches addresses that others supply: servers, their sign-in metadata,
and AI clients' metadata documents. The outbound client checks the address
each name resolves to when it connects, which also defeats DNS rebinding, and
refuses private, loopback, link-local and cloud-metadata ranges (from the
first release). See [Outbound requests](egress.md).

### Tampering

- Audit records are written before the call runs, then sealed into chained,
  signed blocks every few seconds (0.16.0). A changed, removed or reordered
  record breaks the chain. Checkpoints copied outside Privo prove that nothing
  before them was rewritten. See
  [The tamper-evident audit trail](../advanced/audit.md).
- Each credential is bound to its row. A ciphertext moved to another person,
  server or older version fails to decrypt.

### Hostile servers

A server can break the protocol, send huge or endless replies, or never
answer.

- Replies are checked inside the call. One that Privo cannot relay is
  recorded as an error.
- Reply size, call time, list pages and refresh rate are limited
  (`PRIVO_UPSTREAM_*`). A server that breaks a limit costs only itself.
- Fields that belong to the protocol or to Privo, such as `requestState` and
  reserved `_meta` keys, are removed from what a server sends.

### Floods

Today, AI clients that register themselves are limited per address; behind a
reverse proxy, set `PRIVO_TRUSTED_PROXIES` so the limit counts the client
([Outbound requests](egress.md#behind-a-reverse-proxy)). **Planned:** rate
limits, in-flight limits and circuit breakers on the gateway.

### Untrusted packages (stdio servers)

Stdio servers are refused in production. **Planned:** an isolated runner, with
pinned packages and its own network limits (ADR 0007). The
[local runner](../advanced/stdio-runner.md) is for development only and runs
packages unisolated.

### Data across organisations

One install serves one organisation. Every table still carries the
organisation, every store method takes its typed id, and tests check that one
organisation cannot read another's rows. **Planned:** row-level security in
the Postgres store, for Privo Cloud.

## What Privo does not protect against

- **Whoever controls the host**, the container or Coolify. They can read the
  master key and the database.
- **Someone running code inside a live Privo.** They see the credentials of
  the calls that pass while they are in. Key custody limits this, and does not
  prevent it
  ([ADR 0013](https://github.com/talk2sohail/privoclub/blob/main/docs/adr/0013-key-custody.md)).
- **What a server does** with the access you give it, and what its results
  say.
- **What a model does within your rules.** The rules and approvals are the
  control; write them for the worst case.
- **A token on a compromised machine**, until it expires or is revoked.
- **Secrets inside results.** Removing known secrets from results is not built,
  and would never be a security boundary: encoded or split secrets slip
  through.

## Security operations

- Report a vulnerability privately: [Reporting a vulnerability](disclosure.md).
- Every change runs the tests with the race detector, `go vet` and
  `govulncheck`. Every change that touches sign-in, secrets, tenancy or the
  request pipeline gets a separate security review
  ([How a feature is made](../development/workflow.md)).
- The security and independent reviews are published, with what they found
  and what was fixed, in
  [`docs/reviews/`](https://github.com/talk2sohail/privoclub/tree/main/docs/reviews).
- `/version` tells anyone which release runs: its version and Go version,
  nothing else. This is an accepted risk.
- **Planned:** signed releases with a software bill of materials, and
  container scanning.
