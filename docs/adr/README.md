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
| [0003](0003-secrets-and-authentication.md) | Secrets and authentication | Accepted (rev 2). Changed by the owner on 2026-11-12 in section 2, through ADR 0022: token revocation within 2 seconds, not 5 (point 10); no 30-second grace for a rotated refresh token, its reuse revokes the grant at once (point 9); the library is the maintained fosite fork (point 16) |
| [0004](0004-protocol-versions.md) | Protocol versions, statelessness and notifications | Accepted |
| [0005](0005-approvals.md) | Approvals | Accepted |
| [0006](0006-audit.md) | Audit delivery and integrity | Accepted |
| [0007](0007-stdio-runner.md) | Stdio runner isolation and package supply chain | Accepted |
| [0008](0008-egress-and-ssrf.md) | Egress and SSRF protection | Accepted |
| [0009](0009-threat-model.md) | Threat model and trust boundaries | Accepted |
| [0010](0010-operations.md) | Operations: topology, upgrades, backup, observability | Accepted |
| [0011](0011-enterprise-identity-and-roles.md) | Enterprise identity and roles | Accepted |
| [0012](0012-oauth-server-library.md) | OAuth authorization server library | Superseded by 0022 point 16 (2026-11-12); was never accepted |
| [0013](0013-key-custody.md) | Key custody and shared responsibility | Accepted |
| [0014](0014-token-kinds-and-persons.md) | Token kinds and person identity | Accepted (security fix) |
| [0015](0015-configuration.md) | Configuration: environment and an optional file | Accepted |
| [0016](0016-admin-sign-in-with-github.md) | Admin sign-in with GitHub | Proposed (accept after the F05 review); amendment 1 of 2026-11-13: a sign-in in flight lives in a sealed cookie |
| [0017](0017-upstream-oauth-broker.md) | Upstream OAuth broker: each person connects their own account | Proposed (accept after the F07 review) |
| [0018](0018-admin-ui-server-rendered.md) | Admin UI: server-rendered pages over the admin API | Superseded by 0019 |
| [0019](0019-admin-app-preact.md) | Admin app: Preact and Vite, embedded in the binary | Accepted |
| [0020](0020-teams-and-server-catalog.md) | Teams decide which servers a person reaches; a built-in catalog adds them | Proposed (accept after the F26 review) |
| [0021](0021-tokens-belong-to-members.md) | A token belongs to a member by user id, never by the name it carries | Accepted (merged 2026-11-11) |
| [0022](0022-gateway-sign-in-with-oauth.md) | AI clients sign in to the gateway with OAuth; Privo is the authorization server | Accepted 2026-11-12; points 9 and 16 amended by the owner the same day; built in 0.8.0, off unless `PRIVO_OAUTH_SERVER=on` ("As built") |
| [0023](0023-each-server-as-it-presents-itself.md) | Each upstream server is reached the way it presents itself: each person's own API key, OAuth settings entered by hand | Accepted 2026-11-14; built in 0.9.0 |
