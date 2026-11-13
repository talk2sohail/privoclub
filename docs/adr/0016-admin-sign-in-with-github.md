# 0016. Admin sign-in with GitHub

- Status: Proposed, 2026-10-30. The owner chose GitHub on 2026-10-29; the design below is open to the F05 independent review and becomes Accepted after it.
- Amended: 2026-11-13, amendment 1 at the end (where a sign-in in flight is kept). It replaces the storage part of point 4 and the cap, and adds a cookie to point 6; both points are marked
- Date: 2026-10-30
- Amends: 0003 section 1 (names Google and Microsoft first), 0011 identity sequence point 1, 0014 point 5
- Related: 0005 (who decides, and how), 0008 (egress), 0009 (threat model)

## Context

Approvals can only be decided with a control token today. ADR 0005 says the
decision belongs to a person in an authenticated browser session, with CSRF
protection, and ADR 0014 point 5 says people will sign in instead of holding
control tokens. The first audience is developers who run Claude Code and
similar tools; all of them have a GitHub account, and the owner asked for
GitHub first because it is much less work than Google.

GitHub sign-in is plain OAuth 2.0: send the person to GitHub, exchange the
returned code for an access token, read `GET /user`. There is no ID token, key
set, nonce or discovery document to verify, and registering an OAuth App takes
minutes and needs no consent-screen review.

## Decision

1. **Provider.** GitHub OAuth App, authorization-code flow with PKCE S256
   (GitHub accepts only S256 and does not require it; we send it as defence in
   depth). No scope is requested, so the access token can only read public
   profile data, and `allow_signup=false`. The token is used for one
   `GET /user` and then dropped from memory; it is never stored or logged.
   GitHub Enterprise Server works by setting the two base URLs.
2. **Identity key.** `(provider, subject)` with provider `github` and subject
   the numeric GitHub user id. The login (`octocat`) is mutable and can be
   re-registered by someone else after a rename, so it is stored only as a
   display snapshot and never used to find or create a user. The email is not
   requested and never used. Accounts are never linked by name or email.
3. **First owner: a setup link.** `privo admin setup-link [--person NAME]` runs
   next to the data directory (the same trust as `privo token create`), prints
   `<public url>/admin/setup/<token>` once and stores the SHA-256 of the token.
   One hour, single use; issuing a new link retires older unclaimed ones. The
   page behind the link shows a POST button, "Continue with GitHub". The link is
   claimed only when GitHub has answered successfully, in one transaction that
   creates the owner user, binds the identity, consumes the link and writes the
   audit record. The owner's person name is the one given with `--person`, else
   the lowercased GitHub login. If the GitHub id is already a member, or the
   person name is taken, nothing is consumed. Every link that cannot be used
   (unknown, malformed, expired, used, replaced) shows the same page. This replaces the interim bootstrap token as the
   way to create the first person; the bootstrap token stays for automation.
4. **Sign-in.** *(Amendment 1 replaces where the state is kept: no row, no
   cap. The rest of this point stands.)* `POST /auth/github/start` generates a `state` (256 bits) and a
   second random nonce, stores the SHA-256 of both together (single use, 10
   minutes) with the PKCE verifier, and sets a short-lived cookie holding the
   nonce. The callback can therefore only be completed by the browser that
   started it: another browser has no cookie or the wrong one, finds no row, and
   does not use up the real one. `GET /auth/github/callback` looks the row up by
   the hash of the `state` parameter and the cookie's nonce, deleting it in the
   same statement that reads it, then exchanges the code with the client secret
   and the verifier, and reads the profile. What GitHub says in an error body is
   never shown or logged. The redirect URI is always
   `PRIVO_PUBLIC_URL` plus `/auth/github/callback`. After sign-in the browser is
   sent to a fixed page; there is no `next` parameter. The number of unexpired
   states is capped (500) and expired rows are pruned, so unauthenticated
   requests cannot fill the disk.
5. **Outbound calls.** The two GitHub requests go through the egress client of
   ADR 0008 with the operator's allow-list, bounded body size and timeout. The
   URLs are operator configuration, not user input, but a misconfigured or
   hijacked URL should not reach the metadata service. A GitHub Enterprise
   Server on a private network needs its CIDR in `PRIVO_EGRESS_ALLOW_CIDRS`.
6. **Sessions.** Server-side rows. The cookie holds a 256-bit random secret;
   the table holds its SHA-256, so a database backup is not a set of live
   sessions. A new secret at every login (no fixation). `__Host-privo_session`
   (and `__Host-privo_oauth` for the sign-in nonce; since amendment 1
   `__Host-privo_signin` for the sealed sign-in, and `__Host-privo_oauth` only
   for connecting an account) with Secure and Path=/ when
   the public URL is https; the same names without the prefix and without Secure
   only when the public URL is loopback http (development and tests).
   HttpOnly and SameSite=Lax (Strict would drop the cookie on the redirect back
   from GitHub). Idle timeout 2 h and absolute lifetime 12 h, both settings.
   Every request reads the live user row and its role, so disabling a user or
   changing a role takes effect at once with no cache. `last_seen` is written at
   most once a minute.
7. **CSRF and origin.** Every state-changing request needs the session's CSRF
   token in the form and a same-origin `Origin` header equal to the public
   origin (or, when `Origin` is absent, `Sec-Fetch-Site: same-origin`). With
   neither header the request is refused. The CSRF token is read from the body
   only, never the URL. The origin comes from configuration, never from the Host
   header, which a proxy may rewrite. The sign-in start
   endpoints apply the same origin rule (they carry no session yet).
8. **Authorization.** A session becomes an `auth.Principal` with kind
   `session`, the user's role and person, so the existing permission table
   decides (`approvals.decide`: owner, admin, approver). Sessions are accepted
   only by the web pages in this slice; `/api/v1` still takes control tokens
   only, and `/mcp` takes gateway tokens only, so no agent token can use a
   browser session and no browser session reaches the gateway.
9. **Approvals.** The page calls `approval.Service.Decide`, so self-approval
   (by person), single use and "not pending" are enforced in one place for the
   API and the web. `Decider` gains a `UserID`; the recorded approver reads
   `name (user <id>)` where a token reads `name (<token id>)`. Arguments are
   shown while an approval is open, as on the API, and every view and decision
   is an admin audit event with the user as actor. Unlike the API, the page
   offers Approve only when the whole call is on the screen: arguments that
   cannot be decrypted or are longer than 4 KiB can only be denied here, because
   an approver must see what they allow.
10. **Pages.** Server-rendered with `html/template` (contextual escaping), one
    embedded stylesheet served from `/admin/assets/app.css`, no script. CSP
    `default-src 'none'; style-src 'self'; form-action 'self' <GitHub base
    origin>; frame-ancestors 'none'; base-uri 'none'`, `Cache-Control:
    no-store`, `Referrer-Policy: same-origin`, `X-Content-Type-Options:
    nosniff`, `X-Frame-Options: DENY`. The policy is `same-origin` and not
    `no-referrer` because a browser posts a form with `Origin: null` under
    `no-referrer`, which the origin check refuses; `same-origin` still sends
    nothing to GitHub. The page address of a setup link therefore reaches Privo
    itself in `Referer` (never another site), and the link works once. `form-action` has to name GitHub because
    Chrome applies it to the redirect that follows the sign-in form's POST.
    Request bodies are capped at 8 KiB, and the page shows at most 50 pending
    approvals.
11. **Fail closed.** Without client id, client secret and public URL, all
    `/admin` and `/auth` routes answer 503 with a page that names the missing
    settings. A public URL with a path also turns the site off (the callback and
    the links are at the root of the host). A secret may come from `PRIVO_GITHUB_CLIENT_SECRET_FILE` (a
    Coolify file mount, like the master key) and is refused in the
    configuration file (ADR 0015).
12. **Tables** (migration `00004_sign_in.sql`, each with `org_id`): `users`,
    `identities`, `sessions`, `setup_links`, `oauth_states`. One organisation
    serves the web pages in this slice (the instance's default); multi-org
    routing arrives with organisations.

## Trade-offs against Google and Microsoft sign-in

- No verified email and no `hd`-style domain restriction. Later, membership of
  a GitHub organisation can restrict sign-in (scope `read:org`).
- Privo cannot require MFA at GitHub. Operators who need it must enforce it in
  their GitHub organisation.
- Customers without GitHub accounts (non-developers who approve) need Google,
  Microsoft or magic links, which stay on the roadmap (0011).

## Residual risks

- **Setup token in a URL path.** It can land in reverse-proxy access logs.
  Single use, one hour and a claim that needs a GitHub login limit the damage;
  the deploy guide says to treat the link as a secret and to claim it at once.
- **Person labels.** A user's `person` is what self-approval compares with a
  gateway token's `person`, and an administrator chooses both. The setup link
  takes `--person` so the owner can state the name their tokens use; the
  default is the GitHub login. Verified linking between a person and their
  tokens arrives with members.
- **No step-up.** Approving a high-risk call needs only the session. Step-up
  re-authentication (ADR 0003) comes later.
- **A stolen session cookie** is a stolen approver. HttpOnly, Secure, the
  idle timeout and the origin rule limit it; there is no device binding.
- **Mutable logins.** Mitigated by keying on the numeric id; invites (later)
  must not bind by login.
- **Stored in the clear for a short time.** The CSRF token (for the life of the
  session) and the PKCE verifier (up to ten minutes). Neither is a credential on
  its own: the CSRF token is useless without the session cookie, and the
  verifier is useless without the code from the person's browser.
- **Setup page.** It repeats the setup token in a hidden form field, because the
  POST needs it; the page is `no-store` and has no third-party content.
- **Old sessions of a browser.** Signing in again does not end the browser's
  previous session; it ends by idle timeout, absolute lifetime or sign-out.

## Consequences

- People approve in a browser without a control token; control tokens remain
  for automation and break-glass.
- GitHub client id and secret join the deployment's settings; the secret is a
  file mount on Coolify.
- The next sign-in providers add rows to `identities` and routes; they do not
  change sessions, CSRF or the approvals page.

## Amendment 1 (2026-11-13): a sign-in in flight lives in a sealed cookie

**Why.** Point 4 stored every started sign-in as a row and capped the rows at
500, "so unauthenticated requests cannot fill the disk". The independent
review found what the cap costs (F05 R-01, Medium): `POST /auth/github/start`
needs no session, a script can send the right `Origin`, and 500 requests every
ten minutes lock every sign-in, setup link and invite. A member could do the
same through the connect page (F07 R-03). A rate limit or a larger cap moves
the number; it does not remove the lever. With ADR 0022, sign-in becomes the
first step of every AI client, so the lever goes.

**Decision.**

1. **Starting a sign-in writes nothing.** What the callback needs (the
   purpose: sign-in, setup link or invite; the id of the link being claimed;
   the PKCE verifier; where to return; the expiry) is sealed into the cookie
   `__Host-privo_signin` (ten minutes, HttpOnly, SameSite=Lax, the rules of
   point 6). No table has a row for a sign-in in flight and there is no cap.
2. **Sealed with the organisation's data key** (ADR 0003: AES-256-GCM, a
   subkey per value). The `state` sent to GitHub is part of the key derivation
   and of the authenticated data, so a cookie opens only together with the
   state it was made for, and every cookie has a key of its own. A cookie
   therefore cannot be read, changed or made by anyone but this Privo; it
   survives a restart (every deploy is one) and opens on another replica that
   shares the database and the master key. The key version is in front, so a
   rotated key still opens the cookies of the last ten minutes.
3. **The callback takes everything from the cookie**, nothing from the
   request but `state` and `code`. It is completed only by the browser that
   started it, as before: another browser has no cookie, and a cookie of
   another sign-in does not open for this state.
4. **A callback that is not this browser's sign-in leaves the cookie alone**
   (F05 R-03: a link planted on another site could cancel a sign-in in
   progress). The cookie is cleared when it has opened.
5. **"Works once" changes its footing, and this is the part to weigh.** The
   row was deleted when it was read, so Privo itself refused a second use.
   Without a row, three things stand in its place: GitHub accepts an
   authorization code once and the verifier is needed with it; the browser's
   cookie is cleared at the callback; and each process remembers the sign-ins
   it has finished for ten minutes and refuses them again. That memory is in
   the process on purpose (a row would be a write for someone who is not
   signed in): it is bounded (8192 entries, the oldest goes first), it does
   not reach another replica, and a restart empties it. So a replay of a
   captured callback **and** its HttpOnly cookie, on another replica or after
   a restart, reaches GitHub and is refused there. That is weaker than a
   deleted row and it is stated here so it is a decision, not an accident.
6. **Connecting an account keeps its row** (ADR 0017), because it needs a
   session anyway: `oauth_states` now holds only those, at most one per member
   and server, a new start replacing the member's older one (F07 R-03). It
   keeps its own cookie, `__Host-privo_oauth`; sign-in and connect callbacks
   each read only their own cookie, and the connect lookup takes only connect
   rows (F07 R-07).

**What stays as it was.** The origin check on the start, the fixed redirect
URI, no `next` parameter (a return path has one fixed shape and is checked
when written and when used), the generic failure page, and that a setup link
or invite is claimed in one transaction only after GitHub answered.

**What this does not fix.** A person with any GitHub account can still finish
a sign-in and be refused, and each refusal writes one audit row (F05 R-02,
Low, open). That is the one write left for someone who is not a member.

**Left in the database.** The columns `setup_link_id`, `invite_id` and
`return_to` of `oauth_states` are no longer written. They go with the next
migration (F06 task 3), together with a unique index on member and server,
which Postgres will need where SQLite's single writer is enough today.
