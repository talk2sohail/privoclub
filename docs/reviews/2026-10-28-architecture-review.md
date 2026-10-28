# Architecture review, 2026-10-28

Three independent reviewers read revision 1 of ADRs 0001 to 0003, each with
one lens: security and identity; scale, reliability and operations; MCP
protocol and extensibility. Each held the work to an enterprise bar.

## Headline finding (all three)

The ADRs were written for MCP 2025-11-25. The current version is
**2026-07-28**: no protocol sessions, no `initialize`, no SSE resumability,
multi round-trip requests instead of server-initiated requests, tasks moved to
an extension, client ID metadata documents preferred over dynamic client
registration. Revision 2 targets 2026-07-28 with 2025-11-25 compatibility.

## Verdicts on revision 1

| ADR | Security | Scale | Protocol |
| --- | --- | --- | --- |
| 0001 Pipeline | Rework | Rework | Rework |
| 0002 Data model | Accept with changes | Accept with changes | Accept with changes |
| 0003 Secrets and auth | Rework | Accept with changes | Accept with changes |

## Findings and what we did

| Severity | Finding | Raised by | Resolution |
| --- | --- | --- | --- |
| Critical | Hooks could rewrite arguments after approval | Security, Protocol | Transform hooks run before policy; arguments frozen and digest-bound (0001, 0005) |
| Critical | Session model obsolete; Valkey sessions break across replicas | All | Stateless gateway; self-contained legacy session IDs; Valkey dropped from v1 (0004) |
| Critical | Approval waits die on restart and replica change; duplicate approvals | Scale, Protocol | Durable, single-use approvals with hold-then-retry (0005) |
| Critical | Agent could approve its own call via the link | Security | View-only links; authenticated, CSRF-protected decisions; verified Slack (0005) |
| Critical | Shared stdio runner: cross-server and cross-tenant access, unpinned `npx -y` | Security, Scale | Per-process isolation, separate network, pinned packages, no arbitrary stdio on Cloud (0007) |
| Critical | SSRF through server URLs and metadata fetches | Security | Single egress client with resolved-IP checks (0008) |
| Critical | "Audit never drops" was false (in-memory queue after side effect) | Scale | Synchronous intent record, batched completion, bounded queue (0006) |
| Critical | Shared upstream connection leaks state between users | Protocol | `stateful` servers get per-user connections or processes (0004) |
| High | No protocol version strategy | Protocol, Scale | Dual-era support on both sides (0004) |
| High | Per-row hash chain is a lock hotspot and forkable | Scale, Security | Batch sealer with signed, exportable checkpoints (0006) |
| High | Tool name budget wrong; prototype name-splitting bug | Protocol | 46-character public names, alias table, lookup routing (0002) |
| High | Whole-server pin fails for per-user tool lists and blocks everything on one change | Protocol, Scale | Per-tool pins with scope keys; first pin reviewed (0001, 0002) |
| High | Prompts and resources bypass policy | Protocol | Decision and after stages on every operation (0001) |
| High | No defence against data moving between servers | Security | Source and sink tags escalate to "ask" (0001) |
| High | OAuth server not current with spec; revocation promise false with JWTs | Security, Scale | Library-based server, CIMD first, `iss`, resource checks, opaque tokens, 5-second revocation (0003) |
| High | Account-linking CSRF in the upstream broker | Security, Protocol | Session-bound, single-use state; Privo consent screen (0003) |
| High | Refresh-token rotation causes false lockouts and upstream refresh races | Security, Scale | 30-second grace window; single-flight refresh (0003) |
| High | Admin sign-in details (email keys, setup link lifetime) | Security | Issuer plus subject keys, 1-hour setup link, step-up auth (0003) |
| High | Master key in Coolify environment and backups | Security | Key file preferred, kept from child processes, separate backup (0003, 0010) |
| High | Upstream connection memory at scale | Scale | Shared transports, lazy LRU clients, no standing streams (0004) |
| High | SQLite writer discipline and double-writer on redeploy | Scale | WAL settings, single writer, lease (0002) |
| High | Migrations unsafe with old replicas running | Scale | Expand and contract, `privo migrate`, snapshots (0002) |
| Medium | Encryption associated data too narrow; nonce and DEK rotation | Security | Wider associated data, HKDF subkeys, DEK rotation (0003) |
| Medium | Row-level security can fail open with pools | Security, Scale | Transaction-local setting, forced RLS, non-owner role (0002) |
| Medium | Redaction overstated; can break output schemas | Security, Protocol | Called log hygiene; type-preserving and re-validated (0001, 0003) |
| Medium | No rate limits, backpressure or timeouts | Security, Scale | Limits at every layer (0001, 0010) |
| Medium | Roles too coarse | Security | Seven roles (0011) |
| Medium | Health checks and shutdown | Scale | `/livez`, `/readyz`, 30-second drain (0010) |
| Medium | Resource URI rewriting incomplete; notifications missing | Protocol | Percent-encoded URIs, subscriptions, list-change fan-out (0004) |
| Medium | Annotations trusted | Security, Protocol | Untrusted; only tighten defaults (0001) |
| Low | Personal token format and expiry | Security | `privo_pat_` format, mandatory expiry (0003) |

## Where reviewers differed, and the call made

- **Approval hold time:** 20 seconds (scale) versus 45 seconds (protocol).
  Chosen: 25 seconds, configurable.
- **Hash chain:** per-org serialised chain plus signatures (security) versus
  batch sealing (scale). Chosen: batch sealing with Ed25519-signed, exportable
  checkpoints, which meets both goals.
- **Deployer interface:** the protocol reviewer advised dropping it while there
  is one target. Chosen: no Go `Deployer` interface in v1. Deployment stays
  extensible through packaged artifacts (Compose now; Helm or Terraform later)
  and through the versioned **runner protocol** (0007), which is the boundary
  that actually changes between platforms.

## Gaps turned into new records

0004 protocol versions, 0005 approvals, 0006 audit, 0007 stdio runner,
0008 egress and SSRF, 0009 threat model, 0010 operations, 0011 enterprise
identity and roles.
