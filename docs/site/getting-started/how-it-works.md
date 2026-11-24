# How Privo works

Privo stands between your team's AI clients and the MCP servers they use.
Every client talks to one address, `https://privo.example.com/mcp`, as if
Privo were a single MCP server. Behind that address, Privo decides what each
person may see and do, runs each call with the right credential, and writes
down what happened.

```
AI clients                         Privo                          MCP servers
Claude Code ─┐                                                 ┌─ GitHub
Cursor ──────┼─▶ who · reach · tool check · rules · approval ─▶┼─ Linear
VS Code ─────┘      · credential · audit                       └─ Sentry …
```

## One tool call, stop by stop

Ada is on the Payments team and uses Claude Code. She asks it to merge a pull
request, and Claude Code calls the tool `github__merge_pull_request` on
Privo. This is what happens to that call.

1. **Who is asking.** The call carries Ada's token, or her OAuth sign-in.
   Privo looks it up: it belongs to Ada, a member of the Payments team. A
   token is never just "a token": it names a person, or, for a script, nobody.
2. **What she can reach.** Ada sees the servers her teams reach, and on each
   server only the tools her teams see. A call to any other tool is answered
   as if the name did not exist.
3. **Whether the tool is the one that was accepted.** Privo serves only tool
   definitions an admin accepted. A new tool, or one whose server changed it,
   stays hidden from every client until someone reviews it.
4. **What the rules say.** Rules allow, ask or deny. They match the server,
   the tool, who is calling, and even the call's own arguments, such as
   `repo` equals `payments-api`. When no rule matches, the organisation's
   default decides.
5. **Whether a person agrees.** When a rule says ask, the call waits while an
   approver decides in the browser, for up to `PRIVO_APPROVAL_HOLD`
   (see the [configuration reference](../installation/configuration.md)). If
   the decision takes longer, Claude Code is told that the request waits for
   a person, with a link, and to repeat the same call once it is approved.
   Nobody approves their own calls.
6. **Whose account the server sees.** Privo opens the credential for this one
   call: a key the whole team shares, or Ada's own account, by an OAuth grant
   or her own API key. GitHub sees Ada, as Ada. The AI client never holds the
   credential.
7. **What was recorded.** The call is written to the audit trail before it
   runs, and its outcome after: who asked, from which client, what ran, who
   approved it and how it ended. Every few seconds new records are sealed
   into a signed block.

Then the server runs the tool, and its answer goes back to Claude Code.

A call that a rule denies stops at stop 4: Claude Code is told
"Denied by your organisation's Privo policy.", no credential is opened, and
the refusal is recorded too.

## Where Privo runs

Privo is one program, `privo serve`, with its own database in a data folder.
A master key, kept outside that folder, protects every credential it stores.
You run one Privo per organisation, on your own servers:
[Deploy Privo](deploy.md) says what it needs.

## The design, in depth

- The request pipeline: [ADR 0001](https://github.com/talk2sohail/privoclub/blob/main/docs/adr/0001-request-pipeline.md).
- Protocol versions and the stateless gateway: [ADR 0004](https://github.com/talk2sohail/privoclub/blob/main/docs/adr/0004-protocol-versions.md).
- Each person's own account on a server: [ADR 0017](https://github.com/talk2sohail/privoclub/blob/main/docs/adr/0017-upstream-oauth-broker.md).
- The threat model: [ADR 0009](https://github.com/talk2sohail/privoclub/blob/main/docs/adr/0009-threat-model.md).

Next: [Key concepts](concepts.md).
