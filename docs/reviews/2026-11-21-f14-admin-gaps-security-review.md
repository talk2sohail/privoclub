# Security review: F14 third slice, the admin gaps (2026-11-21)

Scope: the staged change for 0.17.0. A separate read-only review pass was
run, as CLAUDE.md requires for changes that touch secrets and per-person
credentials. It covered:

- editing a server (ADR 0029: address, static headers, OAuth client);
- restoring a removed member;
- token names unique per person;
- timeouts named by host;
- the admin app's edit form.

**Result: one Medium finding, three Low; all fixed in the same change.**

## Findings

1. **Medium (M1): a concurrent edit could send an old header to a new address.**
   - **Cause:** the edit was built from a read of the server and wrote the
     whole configuration back. Two admins editing at once could therefore
     leave a header credential that the new configuration did not name.
   - **Effect:** the upstream connection sent every stored `header:*`
     credential, so that value went to the new host. It was invisible in the
     app, and removing it failed.
   - **Fixed in three places:**
     - `EditServer` writes only if the server is unchanged since it was read
       (`updated_at`). Otherwise the API answers 409.
     - A new address deletes every static header credential that is not given
       again, inside the transaction.
     - The upstream connection sends only the headers and env the
       configuration names.
   - **Tests:** `TestEditServerRefusesAStaleEdit`,
     `TestNewAddressDropsEveryStaticHeader`,
     `TestSecretsSendOnlyWhatTheConfigurationNames`.
2. **Low (L1): a stale pooled session could reach the old address.**
   - **Cause:** a per-person session that finished connecting just as an
     edit committed could enter the pool after the edit had closed it.
   - **Effect:** that session kept the old address, and the person's
     re-entered key could go there.
   - **Fixed:** pooled sessions carry the server's version, and one from an
     older version is closed instead of used. A connection that finishes after
     an edit is not pooled.
   - **Test:** `TestPooledSessionOfAnOlderVersionIsNotReused`.
3. **Low (L2): a refused request could still apply the edit.**
   - **Cause:** the edit was written before the request's teams were checked.
   - **Effect:** a 422 for an unknown team still applied the address change.
   - **Fixed:** teams are checked before anything is written.
   - **Test:** `TestRefusedEditChangesNothing`.
4. **Low (L3): restoring a member could bring back tokens.**
   - **Cause:** removal revokes tokens after it commits, so a removal
     interrupted at that point left some live.
   - **Effect:** restoring the member gave those tokens back.
   - **Fixed:** restoring revokes, in its own transaction, every token of the
     member and every unbound token carrying their name.
   - **Test:** `TestRestoreRevokesLeftoverTokens`.

## Checked, no issue

- Changing only headers restarts the connection with the new values.
- Ending connections deletes every grant and its credentials, including the
  catalog grant, and closes pooled sessions.
- All new SQL is scoped by `org_id`.
- OAuth servers keep their address.
- Answers and audit events carry header names, the client id and
  `has_client_secret`, never values.
- Restoring keeps the self-change and owner-only rules.
- Nothing looks a token up by its name.
- `TimeoutError` carries the host only.
- The app never pre-fills or renders a secret.
