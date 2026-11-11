# Project audit, 2026-11-11

What was asked: read the whole project, find what is broken, inconsistent,
inefficient or no longer needed, fix what is certain and list the rest.

Scope: the repository at `main` `7e434a2` (0.6.0), production
(`app.privo.club`), CI, and the Claude Project's six documents. This is a
consistency and health audit, not a sixth security review: the five reports of
2026-11-03 were read and their open findings re-checked in the code, not redone.

## 1. What was run

| Check | Result |
|---|---|
| `gofmt -l .` | clean |
| `go build ./...`, `go test -race -count=1 ./...` | all 25 packages ok |
| Acceptance suite under `-race` | ok (92 s) |
| golangci-lint 2.15.0 (what CI's `latest` installs), with the `acceptance` tag | 0 issues |
| govulncheck | no vulnerabilities |
| `go mod tidy` | changes nothing |
| Admin app: `npm ci`, `tsc`, Vitest, `vite build` | 30/30 tests; the build is byte-identical to the committed `web/dist` |
| `npm audit`, admin app | 2 moderate, both in `vitest` (development only), fixed in 4.1.12 |
| `npm audit`, landing page | 1 moderate in `nodemailer` 10.0.16 (address parsing), fixed in 10.5.0 |
| Dead-code scan (`deadcode`) | 2 functions nothing calls, 4 that only their own tests call (and `vault.StaticKeyProvider`, a deliberate test helper) |
| Production | `/version` 0.6.0, `/readyz` ok |
| CI on `main` | green on `7e434a2` |

Nothing is broken in the sense of failing: the code builds, every suite
passes, production is healthy. What is wrong is mostly that the written record
has fallen behind the code, and that review findings were parked without being
tracked.

The same checks were run again on the tree with the fixes below: all green.

## 2. Fixed in this change (certain)

1. **`docs/todo.md` said four independent reviews were still to do; they were
   done on 2026-11-03 and their findings existed only in the report files.**
   F05, F07, F14 and F24 now show the review as done and list every finding
   as an open item: 1 High, 7 Medium (one of them is the High as seen from the
   app), 16 Low, 6 Info. Each Medium and High
   was re-checked against the code on 2026-11-11 and is still present.
2. **Stale statuses in `docs/todo.md`:** F00 and F01 were "In progress" with
   everything done (three F01 items described a repository and CI that have
   existed since 2026-10-29); F15 was "Not started" with backups shipped and
   Coolify in use; F16's health and drain item was built in F01; F05 said the
   Coolify run was open (the owner signed in on 2026-11-03); F02 said "not yet
   redeployed" and listed two items as open that were closed as R-04 and R-07;
   the version was given as 0.5.0; F21 said `tools/call` was unconfirmed in
   Claude Code; F12 had two ADR lines; F06 left the approver role out; F26's
   status still said its review was open.
3. **The three repo checkpoints all said "read this first" and described
   production as 0.2.0 or 0.3.0 with work uncommitted.** New
   `docs/checkpoint-2026-11-11.md` with the state as checked; the older ones
   carry a "superseded" line.
4. **`README.md`:** `make acceptance` was described as failing "until F02
   lands"; the endpoint list left out `/admin`, `/auth`, `/connect` and the
   client metadata document; the admin menu left out Start and Teams; `/auth`
   and `/connect` also answer 503 when sign-in is off.
5. **`Makefile`:** said "Requires Go 1.25+"; `go.mod` requires 1.26.
6. **`CLAUDE.md`:** the "Layout (planned)" section named packages that do not
   exist (`internal/pipeline`, `internal/protocol`, `deploy/coolify`) and left
   out fourteen that do. It now lists what exists and names what is planned.
7. **`cmd/privo`:** the usage line and the command list did not mention
   `privo admin invite`; a doc comment for `adminSite` sat on the wrong
   declaration and pointed at the superseded ADR 0018.
8. **`internal/auth`:** `WithPrincipal`, `PrincipalFrom` and their key type
   were called by nothing, tests included. Removed.
9. **`site/README.md`:** claimed "zero dependencies"; the site depends on
   `nodemailer`. `MAIL_REPLY_TO` was in `.env.example` and undocumented.

None of this changes behaviour. Items 7 and 8 touch Go files, so pushing them
redeploys the gateway (the usual few seconds); the rest is outside the
gateway's watch paths. No version bump: nothing public changed.

## 3. For the owner to review

### 3.1 Security findings that are open and were untracked until today

Now in `docs/todo.md`; listed here so the order is clear.

1. **High, F07 R-01 = F14 R-02.** An owner or admin can issue a gateway token
   carrying another member's `person` (the Tokens page offers the field; an
   omitted person defaults to the token name) and call OAuth servers with that
   member's grant. Approvals, team membership and the audit follow the same
   string. One fix covers all of it: `api_tokens.user_id`.
2. **Medium, F05 R-01 and F07 R-03.** The 500-state cap is shared by sign-in,
   setup, invite and connect; a script (or any member) can fill it and lock
   everyone out of signing in, ten minutes at a time. app.privo.club is
   exposed to this today.
3. **Medium, F14 R-01.** The tool review shows 300 characters of the first
   line; the accepted hash covers the full description and schema. This is the
   gate against tool poisoning, so it matters before more catalog servers are
   accepted.
4. **Medium, F07 R-02.** After a re-registration the running manager keeps the
   old client id and deletes grants on refresh.
5. **Medium, F24 R-01 and R-02.** A bootstrap token installs on an org set up
   only in the browser; `privo token create` leaves no audit row.

### 3.2 Decisions that are waiting

6. **ADRs 0016, 0017 and 0020 are still "Proposed"**, each "until its
   review"; all three reviews are done ("approve with changes"). Accept them,
   or accept after the High and Medium fixes.
7. **ADR 0003 says a 5-second token cache and revocation within 5 seconds;**
   the code (`auth.CacheTTL`) and every other document say 2 seconds. Accepted
   ADRs are not edited, so this needs a one-line amendment by you.
8. **No release tags.** `docs/versioning.md` says the owner tags `v<version>`;
   0.2.0 to 0.6.0 were deployed untagged. Tag from here on, or drop the rule.
9. **`docs/deploy-coolify.md`, "Status (2026-10-29)": "Still to do by hand:
   add the webhook on GitHub".** I could not read the repository's webhooks.
   If pushes deploy by themselves now, that paragraph and the open F24 item
   are stale; if you deploy by hand, they are still true.
10. **The deploy guide still leads with the bootstrap token** ("First run:
    your first admin token") although the setup link is the path you chose,
    and F24 R-01 makes the bootstrap path the riskier one. Reorder, or mark
    the bootstrap section as the fallback.
11. **golangci-lint is `version: latest` in CI** (open suggestion since
    2026-10-29): a linter release can turn CI red without a code change, as it
    did that day. Pin it and bump on purpose.

### 3.3 The landing page no longer matches the product

12. `site/` sells **"Privo Club: the private club for MCP"** to individuals
    (founding members, "a couple of coffees a month", "Since 2024", welcome
    email signed "The Privo Club team"). The gateway's own root page, the
    README and CLAUDE.md say **"Privo: the team gateway to AI work and the MCP
    world"** for companies, and the root page links to privo.club. A visitor
    who follows that link lands on a different product. Copy and positioning
    are yours to decide; the waitlist survey vocabulary would change with it.
13. `site/README.md` describes DNS as a proxied A record and "push this folder
    to a Git repo"; `docs/deploy-coolify.md` says privo.club runs through a
    Cloudflare Tunnel from `/site` of this repository. One of them is out of
    date (the deploy guide is the newer).
14. `site/server.js` line 2 repeats the "zero dependencies" claim. Not changed
    here because any change under `site/` (except `*.md`) redeploys the
    landing page; fix it with the next real change there.

### 3.4 Dependencies

15. **`nodemailer` 10.0.16 → 10.5.0** (`cd site && npm audit fix`, lock file
    only, within the `^10.0.16` range). Three moderate advisories in address
    parsing; the welcome email is sent to an address a visitor typed, so this
    is the code path concerned. I did not apply it because I cannot send a
    real email to prove the new version against Spacemail.
16. **`vitest` 4.1.11 → 4.1.12** in `web/` (development only, nothing ships).
    Exact pin in `package.json`, so it is a one-line change plus the lock file.
17. Newer majors exist for Preact (11), TypeScript (7), Vitest (5), jsdom
    (31). No reason to move now.

### 3.5 No longer needed (I delete nothing without your yes)

In the repository (you delete and commit; deletes are off for sessions on the
Mac):

18. `docs/manual-test-f05-f07.md`: three lines that say "replaced, can be
    deleted".
19. `privo-checkpoint-2026-10-29.md` at the repo root (36 KB), and
    `docs/checkpoint-2026-11-01.md`, `docs/checkpoint-2026-11-03.md`: history
    only, now marked superseded. Git keeps them if deleted. The ten backup
    review questions in the first one should move to `docs/todo.md` (F15)
    before it goes.
20. `docs/reviews/2026-10-29-f24-bootstrap-self-review.md`: superseded by two
    independent reviews of the same change. Keep it only as a record.
21. Four exported functions that only their own tests call:
    `catalog.ByID`, `config.Lookup`, `config.Web.Enabled`,
    `vault.LoadEnvKeyProvider`. Remove with their tests, or keep as API.

In the Claude Project (every document there is loaded into each new chat):

22. `privo-checkpoint-2026-10-29.md` and `claude/privo-checkpoint-2026-10-29.md`
    are the same 36 KB document twice.
23. `claude/privo-review-brief.md` (the brief for a review that was run) and
    `claude/privo-review-recovered-findings.md` (notes from the interrupted
    run; both High findings fixed in `a6742fa`, the finished report is in
    `docs/reviews/`).
24. `claude/privo-checkpoint-2026-10-30.md` and `-11-01.md` describe
    production as 0.2.0 with F05 "not in git". Still useful in them: the
    identity-provider proposal and Obot comparison (10-30). Everything else is
    superseded by `claude/privo-checkpoint-2026-11-11.md`.
25. The Project has no instructions. One line there ("start from the newest
    `claude/privo-checkpoint-*.md`, then `docs/checkpoint-*.md` in the repo")
    would replace the "read this first" header that every checkpoint carries.

### 3.6 Smaller inconsistencies, left as they are

26. `docs/todo.md` F05 acceptance point 7 and its test list describe the
    server-rendered approvals page (Deny only above 4 KiB, at most 50 shown;
    tests `TestApproveAndDeny`, `TestLongArgumentsAreShortened`,
    `TestSelfApprovalBlocked`, `TestMemberCannotSeeOrDecideApprovals`,
    `TestUnreadableArgumentsCanOnlyBeDenied`). Those tests went with the pages
    on 2026-11-03; the app shows arguments in full and offers Deny only when
    they cannot be decrypted. The same holds for two F07 test names
    (`TestMembersLandOnConnectionsApproversOnApprovals`,
    `TestNavShowsApprovalsOnlyToDeciders`). I left the sections as the record
    of the first slice; say if you want them rewritten to the current tests.
27. `docs/versioning.md` uses 0.5.0 in its examples. Examples, not claims.
28. Go comments still cite ADR 0018 for the in-process API (a few places).
    Correct as far as it goes: 0018 is superseded "except points 2, 4 and 5".
29. `docs/todo.md` F12 has two overlapping lines about argument recording
    (one ticked, one not, the second also covers GDPR erasure).
30. Known inefficiencies, already tracked and not new: a grant read, decrypt
    and write per upstream HTTP request (F07 R-08), rules read per call (F09),
    pins read once per server in `/me/tools` (F26 R-10).

## 4. Not covered

- No line-by-line security review of the Go code (five exist from 2026-11-03).
- Coolify itself: environment, file mounts, watch paths and the webhook were
  not looked at (no Coolify access was requested).
- No real browser run and no real provider.
- The working tree on the Mac was not compared with `main` before this audit.
