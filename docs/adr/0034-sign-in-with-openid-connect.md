# 0034. Sign-in with OpenID Connect (Google, Microsoft, any provider)

- Status: Accepted, 2026-11-22 by the owner (proposed the same day)
- Date: 2026-11-22
- Amends: 0016 (GitHub becomes one sign-in provider among others), 0011
  identity sequence points 1 and 2 (generic OIDC before Google and Microsoft
  presets; magic links stay later)
- Related: 0003 (secrets), 0008 (egress), 0015 (configuration), 0021 (a
  token belongs to a member by user id), 0022 (AI clients sign in through
  Privo's own sign-in), F05 A (GitHub organisation limit), F20 A (this slice)

## Context

Every sign-in goes through GitHub today (ADR 0016). That suited the first
audience, developers. It does not suit the companies Privo is for: agencies
and mid-size firms whose people live in Google Workspace or Microsoft Entra
ID, and whose approvers (a manager, someone in finance) have no GitHub
account. Since ADR 0022, sign-in is also the first step of every AI client
that connects with OAuth, so it decides who can use the gateway at all.

OpenID Connect covers Google, Microsoft and every other identity provider
(Okta, Auth0, Keycloak, JumpCloud) with one protocol. Unlike GitHub's plain
OAuth, it gives a signed ID token. That brings checks of its own (issuer,
audience, nonce, keys), and well-known ways to get them wrong:

- **Matching by email.** Linking a new sign-in to an existing person because
  the email matches lets anyone who controls that email at some provider
  take over the account.
- **Trusting an unverified email.** Microsoft's `email` claim can be set by
  a tenant's admin and is not verified. "nOAuth" (2023) took over accounts in
  apps that keyed on it.
- **Multi-tenant issuers.** Microsoft's `common` endpoint answers for every
  tenant, so the issuer varies and a naive check accepts any tenant.

This ADR fixes those choices before any code.

## Decision

1. **One OIDC provider per instance, beside GitHub.** An operator sets
   `PRIVO_OIDC_ISSUER`, `PRIVO_OIDC_CLIENT_ID` and
   `PRIVO_OIDC_CLIENT_SECRET` (or `PRIVO_OIDC_CLIENT_SECRET_FILE`; refused in
   the configuration file, as GitHub's is). Optional settings:
   - `PRIVO_OIDC_NAME`, the button label. The default is "Google" or
     "Microsoft" for those issuers, else "single sign-on".
   - `PRIVO_OIDC_ALLOWED_DOMAINS`, see point 6.

   Several OIDC providers at once can come later. Nothing below depends on
   there being one.

2. **Who a person is: the issuer and the subject.** An identity is keyed
   `(provider, subject)` in the existing `identities` table (ADR 0011
   planned this; no migration).
   - The provider is the `iss` of the ID token, exactly as written, which
     is the configured issuer (point 4).
   - The subject is `sub`.
   - Email, name and username are a display snapshot in `login` and are
     never used to find, create or link a person.

   Changing the issuer later, for example by moving Microsoft tenants, makes
   every identity of the old issuer unusable. People then link again
   (point 7). That is the price of never guessing.

3. **The flow: authorization code with PKCE (S256), `state` and `nonce`.**
   It follows ADR 0016 amendment 1 exactly.
   - Starting writes nothing. The verifier, the nonce, the purpose (sign-in,
     setup link, invite, link), the link or invite id and the expiry are
     sealed into `__Host-privo_signin`, bound to the `state`.
   - The callback is `PRIVO_PUBLIC_URL` plus `/auth/oidc/callback`. It reads
     only `state` and `code` from the request.
   - Scopes are `openid email profile`. Only the response type `code` is
     accepted, with no `response_mode` other than `query`.
   - The ID token is read from the token response, over TLS from the token
     endpoint, and nowhere else. The access token is not used, and userinfo
     is not called. So `at_hash` has nothing to protect and is not
     required; when present it is checked anyway.

4. **What is checked, and refused otherwise.** The checks are made with
   `github.com/coreos/go-oidc/v3`, pinned at the release current when this
   is built. A spike, as in ADR 0022 task 1, confirms each check with a
   test before it is relied on. Privo makes the checks the library leaves
   to the caller.
   - **Discovery.** `<issuer>/.well-known/openid-configuration` is fetched
     through the egress client (ADR 0008). Its `issuer` must equal the
     configured one character for character. Discovery and the key set are
     cached; they are fetched again on an unknown key id, at most once a
     minute.
   - **Signature.** The token must be signed with a key from the issuer's
     key set, by RS256, PS256 or ES256 only. `none` and every HMAC algorithm
     are refused.
   - **Claims.**
     - `iss` must equal the configured issuer.
     - `aud` must contain the client id.
     - When `aud` has more than one entry, `azp` must equal the client id.
     - `exp` must not have passed, and `iat` must be within ten minutes,
       both with two minutes of leeway.
     - `nonce` must equal the sealed nonce.
   - **Issuer settings refused at startup.** Microsoft's multi-tenant
     issuers (`/common/`, `/organizations/`, `/consumers/`, or the
     `{tenantid}` template) are refused. The issuer must name one tenant:
     `https://login.microsoftonline.com/<tenant id>/v2.0`. An issuer that is
     not https is refused too, except loopback for development.

5. **Fail closed.**
   - **Missing settings.** If any of the three required settings is
     missing, or the public URL is, no OIDC button is shown. The OIDC routes
     then answer 503 with a page naming what is missing, as the GitHub
     routes do.
   - **Unreachable provider.** If the provider cannot be reached, its
     sign-in shows the generic failure page and a warning on Health. Other
     providers keep working. Nothing the provider sends in an error is shown
     or logged.

6. **Limiting who may sign in.** `PRIVO_OIDC_ALLOWED_DOMAINS` is a list of
   domains; without it, the provider's own access control decides. When it
   is set, the claim that counts depends on the provider:
   - **Google:** the `hd` claim (the Workspace domain) must be in the list.
     A consumer Google account has no `hd` and is refused, whatever its
     email.
   - **Microsoft:** the tenant in the issuer is the limit. The `email`
     claim is never trusted (nOAuth), so a domain list is refused at
     startup for a Microsoft issuer.
   - **Any other provider:** `email_verified` must be `true` and the email's
     domain must be in the list, compared exactly and in lower case.

   A refusal is audited as `signin.refused:not_allowed`, under the limiter
   of F05 R-02. As with the GitHub organisation limit, it is checked at each
   sign-in only, and removing a person on People ends their access at once.

7. **Linking a second sign-in to an existing person.**
   - **Only while signed in.** A signed-in member presses "Link Google"
     (or whichever provider) on their own page. The sealed cookie names
     their user id and is valid only with the same session at the callback.
   - **The identity must be unused.** If it already belongs to another
     person, the link is refused. The primary key enforces this.
   - **Never by email.** There is no automatic linking and no "this email
     already has an account".
   - **Unlinking** is allowed while another identity remains, so nobody
     unlinks themselves out.
   - **Audit.** Linking and unlinking are audited
     (`identity.link:<provider>`, `identity.unlink:<provider>`).

8. **Invites and setup links work with any configured provider.** The page
   behind the link shows a button per provider. The identity used to claim
   it is bound to the new person in the same transaction as today. After
   that, the person signs in with that identity or a linked one.

9. **GitHub can be switched off.** An operator who unsets the GitHub
   settings switches GitHub sign-in off; there is no separate flag. At
   startup Privo counts the active members who have no identity with a
   configured provider and puts a warning on Health. Those members cannot
   sign in until an admin restores access: an invite to a new person, or a
   temporary GitHub configuration while they link. Turning a provider off
   never deletes identities.

10. **Sessions, CSRF, roles and AI clients do not change.** An OIDC sign-in
    ends in the same session as a GitHub one (ADR 0016 points 6 to 8).
    Everything after sign-in follows from the user id, never from the
    provider (ADR 0021): roles, teams, tokens and OAuth clients. The sign-in
    page's CSP `form-action` gains the provider's authorization origin.

11. **What is not decided here.**
    - SAML and group mapping (F20 B) and SCIM (F20 C) are not decided here.
    - Step-up re-authentication (ADR 0030) is not decided here; with it,
      linking is one of the actions that will ask for a fresh sign-in.
    - Neither is ending a session from the provider (back-channel logout).
      A person disabled at the provider keeps a Privo session until its
      idle timeout or absolute lifetime (2 h and 12 h). The answer to that
      is SCIM, or removing them on People.

## Alternatives considered

- **Link by verified email.** It is convenient: GitHub and Google
  accounts with the same email would merge. It was rejected, because it
  makes every provider's email check part of Privo's security, and
  Microsoft's is not one (point 6).
- **Separate Google and Microsoft integrations.** Each provider would get
  its own code. Rejected: both are standard OIDC. What differs (`hd`, single
  tenants) fits in point 6 and needs no second code path.
- **Writing ID token verification ourselves.** It would mean JOSE parsing,
  algorithm choice and key rotation, where well-known bugs live. A
  maintained library is safer, with Privo's own checks around it and tests
  for each.
- **Multi-tenant Microsoft with a tenant allow-list.** Possible, but it
  validates the issuer against a template, the error point 4 rules
  out. It can come later, with its own review.

## Residual risks

- **The provider is trusted for who a person is.** A compromised identity
  provider, or a provider admin who reassigns a subject, can sign in as
  that person. The audit names the issuer and subject of every sign-in, so
  this can be investigated.
- **The client secret** is as sensitive as GitHub's: it is a file mount on
  Coolify, never logged or returned. `private_key_jwt` client
  authentication can replace it later.
- **Deprovisioning lag** of up to 12 hours without SCIM (point 11).
- **One provider at a time.** An organisation moving from Google to
  Microsoft runs a period where people link the new provider while
  signed in with the old one. That needs both configured, which point 1
  does not allow yet. Until several OIDC providers are supported, the move
  goes through GitHub or through new invites.

## Consequences

- A company signs in with its own identity provider, and approvers need no
  GitHub account.
- New settings: `PRIVO_OIDC_ISSUER`, `PRIVO_OIDC_CLIENT_ID`,
  `PRIVO_OIDC_CLIENT_SECRET(_FILE)`, `PRIVO_OIDC_NAME` and
  `PRIVO_OIDC_ALLOWED_DOMAINS`, in the registry, the README and the example
  file.
- A new package `internal/oidcauth` beside `internal/githubauth`. The web
  sign-in gains a provider choice and a linking page.
- The tests of F20 A: `TestIDTokenChecks`, `TestNonceBound`,
  `TestWrongAudienceRefused`, `TestLinkOnlyWhileSignedIn` and
  `TestNoEmailMatching`, plus:
  - `TestMultiTenantIssuerRefused`;
  - `TestGoogleNeedsHostedDomain`;
  - `TestMicrosoftEmailNeverTrusted`;
  - acceptance with a fake OIDC provider, which signs its tokens and can
    be made to send each wrong claim.
- The change touches sign-in, so it gets the independent security review
  before merge (CLAUDE.md).
