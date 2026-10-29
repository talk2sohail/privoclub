# 0009. Threat model and trust boundaries

- Status: Accepted, 2026-10-28
- Date: 2026-10-28

## Who is trusted

| Party | Self-hosted on Coolify | Privo Cloud |
| --- | --- | --- |
| Coolify or server operator | Trusted (can read the master key and data) | Privo staff, controlled by access policy and audit |
| Org owner and admins | Trusted for their org | Trusted for their org only; untrusted toward other orgs and Privo infrastructure |
| Members | Trusted to act as themselves within policy | Same |
| AI clients and the models behind them | **Untrusted**: may be prompt-injected | Same |
| Upstream MCP servers and their tool descriptions and results | **Untrusted** until pinned; results always untrusted | Same |
| Stdio server packages | Untrusted code, sandboxed (0007) | Curated only |

## Main threats and where they are handled

| Threat | Handled in |
| --- | --- |
| Tool poisoning, rug pulls (descriptions changed after review) | Per-tool pins, first pin reviewed (0001, 0002) |
| Prompt injection moving data between servers | Cross-server flow rule (0001) |
| Agent approving its own call | View-only links, authenticated approvals (0005) |
| Arguments changed after approval | Frozen arguments and digest binding (0001, 0005) |
| Token theft or passthrough | Audience-bound opaque tokens, no passthrough (0003) |
| Account-linking and consent attacks | Session-bound connect flow (0003) |
| SSRF | Egress client (0008) |
| Malicious stdio package | Runner isolation and pinned packages (0007) |
| Cross-tenant data access | Typed org ids and forced row-level security (0002) |
| Audit tampering | Sealed, signed checkpoints (0006) |
| Credential leak from storage | Envelope encryption with bound associated data (0003) |
| Abuse and floods | Rate limits on every endpoint (0001, 0010) |
| A hostile upstream breaking the protocol or exhausting the gateway (malformed messages, oversized replies, replies that never come, endless lists, change floods) | Recover wrappers on both sides of the gateway (`internal/safe`); limits on reply size, call time, list pages and bytes, refresh rate, and pruning of definitions no longer offered (`internal/upstream`, `PRIVO_UPSTREAM_*`); a reply that breaks a limit costs that server and no other (independent review R-01, R-02, R-04, 2026-10-29) |

## Security operations

- Published vulnerability disclosure policy and security contact.
- Dependency and container scanning in CI; signed releases with SBOMs.
- SOC 2 control mapping maintained from the start: access control, change
  management, admin-action logging, access reviews.
- Privo acts as a data processor for Cloud customers: DPA, subprocessor list
  (including Slack for approval messages).
