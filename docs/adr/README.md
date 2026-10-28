# Architecture decision records

Each record captures one decision that is hard to reverse. Status moves from
Proposed to Accepted once Md Sohail signs off. After acceptance, changes get a
new record that supersedes the old one.

Revision 2 of 0001 to 0003 and records 0004 to 0011 follow the independent
review of 2026-10-28 (see `../reviews/2026-10-28-architecture-review.md`).
Target protocol: MCP 2026-07-28, with 2025-11-25 compatibility.

| No. | Title | Status |
| --- | --- | --- |
| [0001](0001-request-pipeline.md) | Request pipeline and extension points | Accepted (rev 2) |
| [0002](0002-data-model-and-tenancy.md) | Data model and tenancy | Accepted (rev 2) |
| [0003](0003-secrets-and-authentication.md) | Secrets and authentication | Accepted (rev 2) |
| [0004](0004-protocol-versions.md) | Protocol versions, statelessness and notifications | Accepted |
| [0005](0005-approvals.md) | Approvals | Accepted |
| [0006](0006-audit.md) | Audit delivery and integrity | Accepted |
| [0007](0007-stdio-runner.md) | Stdio runner isolation and package supply chain | Accepted |
| [0008](0008-egress-and-ssrf.md) | Egress and SSRF protection | Accepted |
| [0009](0009-threat-model.md) | Threat model and trust boundaries | Accepted |
| [0010](0010-operations.md) | Operations: topology, upgrades, backup, observability | Accepted |
| [0011](0011-enterprise-identity-and-roles.md) | Enterprise identity and roles | Accepted |
| [0012](0012-oauth-server-library.md) | OAuth authorization server library | Proposed (accept after F06 prototype) |
| [0013](0013-key-custody.md) | Key custody and shared responsibility | Accepted |
| [0014](0014-token-kinds-and-persons.md) | Token kinds and person identity | Accepted (security fix) |
