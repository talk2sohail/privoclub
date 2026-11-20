# 0014. Token kinds and person identity

- Status: Accepted, 2026-10-28 (security fix; amends 0003 and 0011)
- Date: 2026-10-28
- Amended by: 0016 (point 5: people sign in with GitHub and get sessions),
  0021 (point 2: a person who approves may also hold member and use the
  gateway; their gateway token still cannot approve), 0022 (point 3: a token
  finds its member by user id; the person is a label)

## Context

The F10 security review showed that one token could both call tools through
`/mcp` and use the admin API. An AI agent holding an owner or admin token
could therefore mint itself an approver token, switch on self-approval, or
add an allow rule, and then approve its own calls. Self-approval was also
checked per token, so a person with two tokens could approve themselves.

## Decision

1. **Two token kinds.**
   - **Gateway tokens** are for AI clients. They work only on `/mcp`.
   - **Control tokens** are for people. They work only on the admin API.
   - Neither works on the other surface: the wrong kind gets 401.
2. **Approvers get control tokens only.** The approver role no longer has
   gateway access, so a token that can approve is never inside an AI client.
   Members get gateway tokens only.
3. **Every token belongs to a person** (`person`, defaulting to the token
   name). Approvals record the requester's person, and nobody approves a call
   made by their own person unless the organisation allows self-approval.
4. Only owners issue owner or admin tokens.
5. When the admin site arrives (F05), people sign in and receive sessions
   instead of control tokens. The person then comes from their verified
   identity, and control tokens remain for automation.

## Consequences

- A typical setup has two tokens per person: one in their AI client and one
  for administration (or, later, a browser session).
- The person field is set by whoever issues the token. That is an
  administrator, who is trusted; F05 replaces it with verified sign-in.
