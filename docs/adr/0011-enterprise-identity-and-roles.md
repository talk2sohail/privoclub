# 0011. Enterprise identity and roles

- Status: Accepted, 2026-10-28
- Date: 2026-10-28

## Decision

### Roles

| Role | Can |
| --- | --- |
| Owner | Everything, including billing and deleting the org |
| Admin | Members, groups, settings |
| Server admin | Add and change servers (stdio is code execution); review pins |
| Policy admin | Policies, toolsets |
| Approver | Decide approvals |
| Auditor | Read audit and settings; change nothing |
| Member | Use the gateway within policy |

Roles combine. Changes to roles, policies and audit settings are audit events.

### Identity (sequenced)

1. v1: Google and Microsoft OIDC, magic links for self-hosted (0003).
2. Next: generic OIDC and SAML 2.0 SSO, just-in-time provisioning, group
   mapping from the identity provider to Privo groups.
3. Then: SCIM 2.0. Deprovisioning revokes sessions, OAuth grants, personal
   tokens and upstream connections immediately.
4. Passkeys or TOTP for local accounts on self-hosted installs without SSO.

## Consequences

- The data model (0002) keys identities by issuer and subject from day one, so
  SSO and SCIM add providers, not migrations.
