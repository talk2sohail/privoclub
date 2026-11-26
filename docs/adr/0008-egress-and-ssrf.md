# 0008. Egress and SSRF protection

- Status: Accepted, 2026-10-28
- Date: 2026-10-28

## Context

Privo fetches URLs supplied by others: remote MCP server URLs registered by org
admins, upstream OAuth and resource metadata, and client metadata documents
from unknown AI clients. Each can point at internal addresses (cloud metadata
endpoints, the database, the runner, the Coolify host).

## Decision

- All outbound HTTP from the gateway goes through **one egress client**.
- Its dialer checks the **resolved** IP at connect time (which also defeats DNS
  rebinding) and denies loopback, RFC 1918, link-local (including
  169.254.169.254), unique local IPv6, IPv4-mapped IPv6 and other
  non-public ranges.
- Every redirect hop is re-checked; cross-host redirects are refused for
  metadata fetches; response size and time are capped.
- Self-hosted admins may allow specific internal CIDRs explicitly (for an
  in-house MCP server on their network). Privo Cloud never allows private
  ranges.
- The stdio runner's outbound traffic goes through an allow-listing proxy
  (0007); default deny.
- Blocked attempts are audit events.

## Consequences

- In-house servers on private networks need an explicit allow entry on
  self-hosted installs; the error message says how.

## As built (0.20.0, 2026-11-26)

For operators: [Outbound requests (egress)](https://docs.privo.club/security/egress/).

- Built as decided (`internal/egress`). It also refuses carrier-grade NAT,
  documentation and benchmarking ranges, NAT64, 6to4 and Teredo, and ignores
  environment proxies. Redirects stay on the same host, at most three, one
  for sign-in requests.
- AI clients' metadata documents use no allowed range, whatever
  `PRIVO_EGRESS_ALLOW_CIDRS` says (review of 2026-11-15, S-05), and follow
  no redirect.
- An allowed range is checked before the refused ones, so it can open
  loopback or link-local addresses; the page warns against that.
- Not built: blocked attempts as audit events, and the runner's proxy (ADR
  0007). The 422 for a refused server names the address and the reason, not
  how to allow it; the page does.
