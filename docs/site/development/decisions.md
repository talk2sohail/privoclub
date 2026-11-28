# Architecture decisions

Each decision that is hard to reverse is written down as an architecture
decision record (ADR) in [`docs/adr/`](https://github.com/talk2sohail/privoclub/tree/main/docs/adr).
A record moves from Proposed to Accepted when the owner signs it off. After
acceptance, a change gets a new record that supersedes the old one, so the
history stays readable.

The pages of this site describe what Privo does; each one links to the
records behind it. Where a page and a record disagree, the record's "As
built" section is the one to check, and the page is a bug.

Target protocol: MCP 2026-07-28, with 2025-11-25 compatibility.

| No. | Title | Status |
| --- | --- | --- |
| [0001](https://github.com/talk2sohail/privoclub/blob/main/docs/adr/0001-request-pipeline.md) | Request pipeline and extension points | Accepted (rev 2) |
| [0002](https://github.com/talk2sohail/privoclub/blob/main/docs/adr/0002-data-model-and-tenancy.md) | Data model and tenancy | Accepted (rev 2) |
| [0003](https://github.com/talk2sohail/privoclub/blob/main/docs/adr/0003-secrets-and-authentication.md) | Secrets and authentication | Accepted (rev 2). Changed by the owner on 2026-11-12 in section 2, through ADR 0022: token revocation within 2 seconds, not 5 (point 10); no 30-second grace for a rotated refresh token, its reuse revokes the grant at once (point 9); the library is the maintained fosite fork (point 16) |
| [0004](https://github.com/talk2sohail/privoclub/blob/main/docs/adr/0004-protocol-versions.md) | Protocol versions, statelessness and notifications | Accepted |
| [0005](https://github.com/talk2sohail/privoclub/blob/main/docs/adr/0005-approvals.md) | Approvals | Accepted |
| [0006](https://github.com/talk2sohail/privoclub/blob/main/docs/adr/0006-audit.md) | Audit delivery and integrity | Accepted |
| [0007](https://github.com/talk2sohail/privoclub/blob/main/docs/adr/0007-stdio-runner.md) | Stdio runner isolation and package supply chain | Accepted |
| [0008](https://github.com/talk2sohail/privoclub/blob/main/docs/adr/0008-egress-and-ssrf.md) | Egress and SSRF protection | Accepted |
| [0009](https://github.com/talk2sohail/privoclub/blob/main/docs/adr/0009-threat-model.md) | Threat model and trust boundaries | Accepted |
| [0010](https://github.com/talk2sohail/privoclub/blob/main/docs/adr/0010-operations.md) | Operations: topology, upgrades, backup, observability | Accepted |
| [0011](https://github.com/talk2sohail/privoclub/blob/main/docs/adr/0011-enterprise-identity-and-roles.md) | Enterprise identity and roles | Accepted |
| [0012](https://github.com/talk2sohail/privoclub/blob/main/docs/adr/0012-oauth-server-library.md) | OAuth authorization server library | Superseded by 0022 point 16 (2026-11-12); was never accepted |
| [0013](https://github.com/talk2sohail/privoclub/blob/main/docs/adr/0013-key-custody.md) | Key custody and shared responsibility | Accepted |
| [0014](https://github.com/talk2sohail/privoclub/blob/main/docs/adr/0014-token-kinds-and-persons.md) | Token kinds and person identity | Accepted (security fix) |
| [0015](https://github.com/talk2sohail/privoclub/blob/main/docs/adr/0015-configuration.md) | Configuration: environment and an optional file | Accepted |
| [0016](https://github.com/talk2sohail/privoclub/blob/main/docs/adr/0016-admin-sign-in-with-github.md) | Admin sign-in with GitHub | Proposed (accept after the F05 review); amendment 1 of 2026-11-13: a sign-in in flight lives in a sealed cookie |
| [0017](https://github.com/talk2sohail/privoclub/blob/main/docs/adr/0017-upstream-oauth-broker.md) | Upstream OAuth broker: each person connects their own account | Proposed (accept after the F07 review) |
| [0018](https://github.com/talk2sohail/privoclub/blob/main/docs/adr/0018-admin-ui-server-rendered.md) | Admin UI: server-rendered pages over the admin API | Superseded by 0019 |
| [0019](https://github.com/talk2sohail/privoclub/blob/main/docs/adr/0019-admin-app-preact.md) | Admin app: Preact and Vite, embedded in the binary | Accepted |
| [0020](https://github.com/talk2sohail/privoclub/blob/main/docs/adr/0020-teams-and-server-catalog.md) | Teams decide which servers a person reaches; a built-in catalog adds them | Proposed (accept after the F26 review) |
| [0021](https://github.com/talk2sohail/privoclub/blob/main/docs/adr/0021-tokens-belong-to-members.md) | A token belongs to a member by user id, never by the name it carries | Accepted (merged 2026-11-11) |
| [0022](https://github.com/talk2sohail/privoclub/blob/main/docs/adr/0022-gateway-sign-in-with-oauth.md) | AI clients sign in to the gateway with OAuth; Privo is the authorization server | Accepted 2026-11-12; points 9 and 16 amended by the owner the same day; built in 0.8.0, off unless `PRIVO_OAUTH_SERVER=on` ("As built") |
| [0023](https://github.com/talk2sohail/privoclub/blob/main/docs/adr/0023-each-server-as-it-presents-itself.md) | Each upstream server is reached the way it presents itself: each person's own API key, OAuth settings entered by hand | Accepted 2026-11-14; built in 0.9.0 |
| [0024](https://github.com/talk2sohail/privoclub/blob/main/docs/adr/0024-call-statistics-and-diagnostic-log.md) | Call statistics, a diagnostic log and Prometheus metrics, apart from the audit | Accepted 2026-11-14; built in 0.10.0 |
| [0025](https://github.com/talk2sohail/privoclub/blob/main/docs/adr/0025-several-roles-per-person.md) | A person holds several roles; their gateway token only uses the gateway | Accepted 2026-11-18; built in 0.11.0 |
| [0026](https://github.com/talk2sohail/privoclub/blob/main/docs/adr/0026-toolsets.md) | Toolsets: a team can see only some of a server's tools | Accepted 2026-11-21; built in 0.14.0 (numbered 0023 on its first branch) |
| [0027](https://github.com/talk2sohail/privoclub/blob/main/docs/adr/0027-rule-conditions-grants-and-yaml.md) | Rules look at arguments; grants only replace an ask; rules as YAML | Accepted 2026-11-21; built in 0.15.0 (numbered 0024 on its first branch) |
| [0028](https://github.com/talk2sohail/privoclub/blob/main/docs/adr/0028-audit-signing-key-and-checkpoints.md) | The audit signing key comes from the master key; checkpoints anyone can check | Accepted 2026-11-21; built in 0.16.0 (numbered 0025 on its first branch) |
| [0029](https://github.com/talk2sohail/privoclub/blob/main/docs/adr/0029-editing-a-server.md) | Editing a server: a new address ends connections, drops static secrets and sends tools back to review | Accepted 2026-11-21; built in 0.17.0 |
| [0030](https://github.com/talk2sohail/privoclub/blob/main/docs/adr/0030-distribution-and-deployment.md) | Distribution and deployment: one instance per organisation, from signed releases | Proposed 2026-11-24 |
| [0032](https://github.com/talk2sohail/privoclub/blob/main/docs/adr/0032-cross-server-flow-rule.md) | Cross-server data-flow rule (`untrusted_source` to `egress_sink`) | Proposed (pending owner sign-off) |
| [0034](https://github.com/talk2sohail/privoclub/blob/main/docs/adr/0034-sign-in-with-openid-connect.md) | Sign-in with OpenID Connect: identities by issuer and subject, linking only while signed in, never by email | Accepted 2026-11-22 |
