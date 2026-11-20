# Security review: control-plane batch 1 (2026-11-20)

Scope: `6d5d8fb..fd8ab15`, covering the home page, the F11 pin review (scan,
diff, block), separate rooms for each kind of sign-in (F05 R-01, F07 R-03),
refreshing with the current registration (F07 R-02), and bootstrap and token
audit (F24 R-01, R-02). A separate read-only review pass, run as CLAUDE.md
asks for every change to auth, tenancy or the request pipeline. The reviewer
read the code and ran nothing.

**Result: no vulnerability at or above 0.8 confidence.**

## Paths checked

1. **Home page.**
   - It is built once from configuration and the request is ignored.
   - The MCP address is escaped and the page has no script.
   - The CSP is unchanged: one style hash, `base-uri`, `form-action` and
     `frame-ancestors` all `'none'`.
2. **Pin review.**
   - The gateway still serves accepted definitions only.
   - A decision matches the org, server, kind, name and hash exactly. Block
     moves pending definitions only; accept moves pending or blocked ones.
   - The review shows the stored definition, whose bytes are the hashed
     ones.
   - A blocked definition cannot become pending again.
   - The routes need `servers.manage`, and every query is scoped by org.
   - No unsafe HTML API is used; upstream text is rendered as text only.
3. **Sign-in states.**
   - Every delete is scoped by org and by purpose.
   - The replacement keys come from a validated secret or from the session.
   - A state is still bound to its cookie and used once. Each callback
     checks the purpose.
   - Dropping someone's sign-in in flight costs availability only.
4. **Refresh.**
   - The re-read of the server is scoped by org and uses the same id.
   - The client id and secret now always match.
   - `Sync` touches only that org.
5. **Bootstrap and tokens.**
   - "Administered" is scoped by org and stricter than before.
   - No secret or secret hash is written to the audit.
   - The API's permission checks are unchanged.

## Notes acted on

- **Pin decisions are audited by name and hash:** the action is
  `pins.accept:N:tool/name@hash12,…`, with at most 20 entries.
- **Code tidy-ups:** a shadowed variable on the root page, and a dead branch
  in a test.

## Notes left open (tracked in `docs/todo.md`)

- **`previous` and "Available to AI clients"** can show an older accepted
  version that the server no longer offers. They should follow the
  definition the gateway serves now.
- **Token events** go straight to the store, not through `audit.Writer`.
  Today the two are the same; a shared helper would keep a future audit sink
  complete.
- **A refresh of a disabled server's grant** is not refused. This is not a new
  exposure.
