# Security review: F10 second slice, grants, withdraw and history (2026-11-20)

Scope: commit `0096994`. A separate read-only review pass, run as CLAUDE.md
asks for every change to auth or the request pipeline. The reviewer read the
code and ran nothing.

**Result: two Medium findings, both fixed in the next commit. Both made a
grant broader than the F10 spec allows.**

## Findings

### 1. Medium (0.85): a requester could allow a tool for themselves

- **What happened:** where the org allows self-approval, a person who is
  both member and approver could approve their own call with an 8-hour grant.
  Their AI client then ran the tool with no human step, and they had written
  themselves an allow rule without `policy.manage`.
- **Fixed:** a grant on one's own request is refused (403) before anything is
  decided, whatever the self-approval setting. The app hides the grant choice
  on one's own request.
- **Tests:** `TestNoGrantForOneself`. With the check removed, it fails.

### 2. Medium (0.8): a grant could outrank a deny added while the request waited

- **What happened:** the grant ranked one above the rule that asked when
  the request was made. A deny added at that rank afterwards wins today
  (deny beats ask at the same rank), but the grant outranked it.
- **Fixed:** the grant is checked before anything is decided, against the
  rules as they are now and for the requester as they are now: their roles,
  their teams, whether they still reach the server, and whether the
  definition they called is still the accepted one.
  - **Grant made:** only when the call would still ask. It ranks one above
    the rule that asks now.
  - **Grant refused (409):** in every other case. The approver can still
    approve the call alone.
  - **Pinned:** the grant is also pinned to the reviewed definition, so it
    stops matching once the tool changes.
- **Tests:** `TestGrantRefusedWhenRulesChanged` (with the check removed, it
  fails) and `TestGrantNeedsTheAcceptedDefinition`.

## Paths checked that held

- **No grant without a decision:** a grant is never made on a refused,
  decided or expired approval.
- **No mixing approvals:** the grant cannot attach to a different approval.
- **The right person:** the requester's token is checked to be of this org.
  A `user:` rule needs an active member.
- **Exact tool:** the escaping in `ExactTool` makes the pattern match only
  that name.
- **Bounded length:** at most 8 hours.
- **`user:` matches members only:** it never matches a caller without a
  member.
- **Withdraw and own lists:** limited to the caller's own member, by org,
  with no path for control tokens or for tokens that only name a person.
- **Pages:** the cursor is bound as a value and every page is scoped by org.
  History has no arguments.
- **Withdraw racing a decision:** both need `pending`, so only one wins.

## Notes kept (not vulnerabilities)

- **A grant covers all of the person's tokens.** It is "for this person"
  (ADR 0005), so a `token:` deny at or below the asking rule's rank is
  outranked for their other tokens. Now stated in the F10 threat note.
- **A failed grant after a successful approval** is logged, not audited.
  Moving the check before the decision makes this rare: only a storage
  error is left.
- **The old unpaged list** (`status` empty or a stored status) still shows
  arguments for approved, unused requests. This matches ADR 0005; the
  paged views are the ones the app uses.
