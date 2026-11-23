# Tokens and AI clients

Privo has two kinds of personal token
([ADR 0014](https://github.com/talk2sohail/privoclub/blob/main/docs/adr/0014-token-kinds-and-persons.md)):

- **Gateway tokens** go in AI clients (Claude Code, Cursor, Amp...) and work
  only on `/mcp`.
- **Control tokens** are for people administering Privo and work only on the
  admin API. Approvers only get control tokens.

AI clients that support it can sign in instead of using a token:
[AI clients that sign in with OAuth](oauth-clients.md).

## A token belongs to the member who made it

A gateway token **belongs to the member who made it** on the **Tokens** page,
signed in
([ADR 0021](https://github.com/talk2sohail/privoclub/blob/main/docs/adr/0021-tokens-belong-to-members.md)).
The page gives a ready Claude Code command. Only such a token uses that
member's teams and their own accounts on OAuth servers, and the audit names
the member with every call (`user`).

A token issued any other way (`POST /api/v1/tokens`, `privo token create`)
belongs to nobody, whatever `person` it names: it reaches the servers everyone
can use and never anyone's account. `person` is a label for approvals and the
audit. The lists show which is which (`bound`).

Token names are unique per person (0.17.0): two people may each call theirs
`claude-code`; tokens of nobody stay unique among themselves.

Tokens live `PRIVO_TOKEN_TTL` by default (90 days). Revoking one takes effect
within about two seconds.

## Tokens from the command line

For the first setup, where you have a shell on the server:

```sh
privo token create --name you --role owner --kind control       # admin API
privo token create --name ci --role member --kind gateway        # a script: servers everyone can use
```

Where you have no shell, see
[Deploy on Coolify](../installation/coolify.md#first-run-your-first-admin-token).
