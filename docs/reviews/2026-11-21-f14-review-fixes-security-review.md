# Security review: the F14 review fixes (2026-11-21)

Scope: the fixes for R-03 and R-05 to R-08 of the independent review of
2026-11-03, released as 0.17.1. A separate read-only review pass was run, as
CLAUDE.md requires for changes that touch sign-in sessions. It covered
three areas:

- when a session is kept alive;
- the order of the app bridge's checks;
- the confirmation for an owner or admin token made in the browser.

**Result: nothing at Medium or above. One Low, fixed in the same change.**

## Finding

1. **Low: a missing `Sec-Fetch-Site` still kept the session alive.**
   - **Scenario:** a browser that sends no Fetch Metadata follows a link
     from another site.
   - **Effect:** that request still kept the session alive, so R-03 came
     back on such browsers.
   - **Fixed:** pages keep a session alive only when the request is
     `same-origin` or `none`. Without the header, it counts only with no
     `Referer` or one from this site.
   - **Test:** `TestOnlyAcceptedRequestsKeepTheSessionAlive` (cross-site,
     same-site, and a foreign `Referer` without Fetch Metadata).

## Checked, no issue

- **Forms:** sign-out, the OAuth consent POST and the connect POSTs all go
  through `guarded()`, which keeps the session alive only after the origin,
  form and CSRF checks pass.
- **The app bridge:** `appAuth()` keeps the session alive only after the
  session, origin and CSRF-header checks pass.
- **Pages:** not touching never changes whether a request is allowed.
- **Bridge order:** the bridge's content-type and body checks run before
  authentication, but they only answer 400 or 415. Nothing is parsed or
  dispatched before authentication.
- **The confirmation:** it applies to every session principal. No other
  route mints an owner or admin control token. It guards against mistakes;
  it is not a boundary against an attacker who can already run script in
  the page.
- **CI and npm:** pinning actions by SHA and the new audit steps only add
  checks. Each SHA was resolved from its tag with the GitHub API on
  2026-11-21.
