# Security review: F07 R-01, tokens bound to a user (2026-11-18)

Scope: commit `20c536f` (ADR 0022). A separate read-only review pass, run as
CLAUDE.md asks for every change to auth, tenancy or the request pipeline.
The reviewer read the code and ran nothing.

**Result: no High or Medium findings.**

## Paths checked

1. **Minting a token bound to someone else.**
   - `TokenSpec.UserID` is set only by `createMyToken`, from `p.UserID`.
   - `POST /api/v1/tokens` and the CLI never read a user id from their input.
   - Over HTTP the admin API admits only control tokens, and those never
     carry a user. The in-process path admits only sessions, whose user comes
     from the server's own session lookup.
2. **Gateway access, upstream grants and teams.**
   - `Access` and `session` read `p.UserID`, which comes from the token row of
     the verified org.
   - `AccessFor` and `UserByID` are scoped by `org_id`.
   - An unbound token gets "everyone" and no person session.
   - Errors are returned to the caller (fail closed).
3. **A removed member's bound token** reaches nothing:
   - `AccessFor` gives it no access at all, not even "everyone";
   - `session` answers "no account";
   - removing the member revokes it.
4. **Policy and approvals.**
   - Teams come from the bound user.
   - Self-approval still compares token id and person. A member's own token
     carries their person, and an unbound token with a person name can only
     make the check stricter.
   - `ActorID` stays the token id for tokens.
5. **The migration backfill.**
   - Every join is org-scoped, and person names are unique per org.
   - It binds a token only where the user's own `token.create` event, with
     the same role and name, is within 5 seconds of the token.
   - Every such token already acted as that user before the fix, so the
     backfill can only narrow access.
   - An unparsable time leaves the token unbound.
6. **The refusal of a member's name in `IssueToken`** keeps the token list and
   the audit honest. It is not a security boundary: an unbound token gains
   nothing from the name it carries, whatever its case.
7. **Data exposure.** `personal` and `account` appear only to holders of
   `tokens.manage` or `audit.read`, and carry no secret. The app renders them
   as text.
8. **Tenancy.** Every new or changed query filters by `org_id`. `TokenByID`
   is global by design, and `revokeMyToken` checks the org.

## Noted, not new

A bound gateway token keeps the role it was issued with if its member is
later demoted. This predates the change. ADR 0021 (F20) removes it: gateway
tokens are stored as `member`, and `role:` rules read the person's current
roles.
