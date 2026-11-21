# Security review: F07 R-04, grants revoked at the provider (2026-11-21)

Scope: revoking upstream grants at the provider when Privo deletes them,
released as 0.18.1. A separate read-only review pass was run, as CLAUDE.md
requires for changes that touch secrets. It covered member removal, server
deletion, an edit that ends connections, connecting again, and disconnect.

**Result: one Medium, fixed in the same change; two Low notes.**

## Finding

1. **Medium: connecting again could end the new grant.**
   - **Cause:** the first version revoked the grant a new connection
     replaces. Both grants belong to the same client and the same person.
   - **Effect:** a provider that revokes the whole authorization (Google),
     or that returns the same refresh token, ended the new grant with the
     old one. The person's next call then failed.
   - **Fixed:** connecting again revokes nothing. A new client id ends
     connections through an edit (ADR 0029), which revokes under the old
     client before the edit is saved.
   - **Test:** `TestReconnectDoesNotRevoke`.

## Notes

- **A refresh landing in between.** A refresh between reading the tokens
  and deleting the grant can leave one rotated refresh token valid at the
  provider until it expires. This is accepted: deletion never waits on a
  provider.
- **Sequential revocation (fixed).** Revocations were sent one by one, each
  up to 10 seconds, holding tokens in memory meanwhile. They are now sent
  all at once within 10 seconds.

## Checked, no issue

- **Org and person:** no token reaches another person, org or server.
  Every read is scoped by org, and every caller passes the grants of the
  server named.
- **Edits:** revocations are read before an edit is saved, so they use the
  settings the tokens were issued under. A refused edit revokes nothing.
- **Endpoint:** the revocation endpoint has the same source as the token
  endpoint (the provider's metadata, or the admin). It is reached over
  HTTPS through the egress client.
- **Logs:** revocation failures are logged with fixed text and the server
  alias only.
- **Failure paths:** when the delete or edit fails, nothing is revoked. A
  failed revocation never blocks a delete.
