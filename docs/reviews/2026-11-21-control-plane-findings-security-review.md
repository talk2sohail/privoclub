# Security review: the control-plane findings batch (2026-11-21)

Scope: the fixes released as 0.18.0. A separate read-only review pass was
run, as CLAUDE.md requires for changes that touch approvals, secrets and
the request pipeline. The fixes cover:

- F05 R-04: self-approval by member, and by name in any case;
- F07 R-05: a client secret only goes to an issuer the admin named;
- F07 R-06: a rotated token is never lost to a failed save;
- F26 R-03: calls outside a person's teams are audited;
- F11: the review follows what is served now;
- the grants of a server turned off are never refreshed.

**Result: one Medium, two Low and one note; all fixed in the same change.**

## Findings

1. **Medium: the issuer requirement could be skipped by editing the client.**
   - **Scenario:** an admin adds a server with a client id and no secret, so
     no issuer is needed and the sign-in is found from the server. They then
     edit the client and add a secret.
   - **Effect:** the secret went to an authorization server the MCP server
     chose, which is what F07 R-05 prevents.
   - **Fixed:** a server records whether the admin named its issuer
     (`issuer_named`). Hand-entered settings count as named. Editing the
     client refuses a new secret otherwise; changing the id or removing a
     secret is still allowed.
   - **Test:** `TestEditedClientSecretNeedsANamedIssuer`.
2. **Low: timing could reveal hidden names.** Recording a call on a hidden
   name did extra work before answering. Timing could tell such a name from
   an unknown one.
   - **Fixed:** the record is made on its own after the answer.
3. **Low: a held token could outlive its grant.** A token held because it
   could not be saved could still be used once after its grant was deleted.
   - **Fixed:** if the grant is gone the answer is "not connected". If
     another refresh saved first, theirs is used.
4. **Note: hiding old definitions also hid them from admins.**
   - **Cause:** hiding accepted definitions the server no longer offers also
     hid them from the admin.
   - **Effect:** those definitions are served again without review if the
     server offers them again.
   - **Fixed:** they are listed under "Accepted earlier, not offered now",
     with that consequence spelled out.

## Checked, no issue

- **Self-approval:** `approval.Own` only ever blocks. Withdrawing is checked
  by user id in the store. Matching names case-insensitively can only refuse
  more; two members whose names differ only in case cannot approve each
  other's requests.
- **Held tokens:** they are keyed by org, server and member, and used only
  while the stored credential's id and version match. Only sealed
  ciphertext is held.
- **Hidden-call records:** they are keyed by member (or by token, for a
  token of nobody). Unknown names are never recorded, and the answer is
  unchanged.
- **Discovery path:** the issuer named must be one the server names. The
  hand-entered path is refused when discovery succeeds.
