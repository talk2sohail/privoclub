# Independent review: F26 teams, server access per team, built-in catalog, member Start page

- Date: 2026-11-03
- Reviewer: independent review agent
- Reviewed commit: `08fd097` ("F26: teams, the server catalog and the start page (0.6.0)") on branch `f26-teams`, diffed against `0b5098c`. The working tree was clean when I started; I ran no state-changing git command.
- Method: read all code in scope (store, gateway, policy, control, web bridge, catalog, new UI pages), then ran the suites and a few scratch probes under the session scratchpad (never in the repo). Findings marked READ were argued from code and not executed.
- Ran, all green: `go test -race -count=1 ./internal/store/... ./internal/control/... ./internal/gateway/... ./internal/policy/... ./internal/catalog/...`; `go test -tags acceptance -count=1 -run TestTeams ./test/acceptance/`; `cd web && npx vitest run` (28 tests).

## Verdict

**Approve with changes.** The core promise holds: every gateway path that returns or runs an item goes through `visible`, which filters by the caller's reachable servers, and "outside your teams" is indistinguishable from "does not exist" on tools, prompts and resources. Fix R-01 (a member-facing endpoint that still lists, and lets members connect to, servers outside their teams) before accepting ADR 0020. The rest is Low or Info.

Counts: Critical 0, High 0, Medium 1 (R-01), Low 6 (R-02 to R-07), Info 5 (R-08 to R-12).

## Findings

### R-01 Medium: `/admin/api/me/connections` and the connect flow ignore teams, so members see and can connect servers outside their teams

- Where: `internal/web/connect.go:125` `oauthServers` (lists every OAuth server of the org, no access check); used by `internal/web/app.go:323` `myConnections`; `internal/web/connect.go:155` `connectTarget` (and `app.go:377` `disconnectGrant`) look the server up by alias with no `Reaches` check. Nothing in `internal/web` calls `gateway.Access` (grep: only `control/control.go` and `control/teams.go` do).
- What is wrong: F26 point 4 and the threat note say a person never learns of servers outside their teams. `listServers` and `/me/tools` honour that; the Connections page does not. Any signed-in member gets alias plus issuer host of every per-person-OAuth server (for example `figma`, `hubspot`, `stripe` of other teams), and `GET /admin/connect/<alias>` answers differently for an existing OAuth server (connect page) and a missing one (404 "No such server"), so alias probing works for them too.
- Worse: `connectStart`/callback let the member complete the provider's OAuth flow for a server they cannot use. Privo then stores an encrypted upstream grant (refresh token) for that person on a server their teams do not reach. It is inert today (the gateway refuses the call first), but it is a standing credential held for no reason, and it becomes live the moment someone adds that person to a team or the server to `everyone`, with no consent step.
- How established: READ. The code has no filter on either path; the acceptance test `TestTeamsLimitWhatEachPersonSees` checks `/v1/servers` and `/v1/me/tools` only, not `/admin/api/me/connections`.
- Fix: filter `oauthServers` for the signed-in user with `gateway.Access` (admins may keep seeing all on the admin pages but the Connections page is a member page: filter it too, or by reach only), apply the same check in `connectTarget` and answer exactly like "no such server". Add a test that a member outside the team gets neither the alias in the list nor a connect page.

### R-02 Low: a rule can name a team that does not exist; creating that team later inherits the rule

- Where: `internal/policy/policy.go` `ValidSubject` (accepts any well-formed slug), `internal/control/control.go` `createPolicy` (no existence check). `DeleteTeam` deletes rules only for an existing team.
- Scenario: an admin (or a script) creates `deny`/`allow`/`ask` rules for `team:finance` before the team exists, or a rule for a team whose name is reused. Whoever later creates `finance` silently gets those rules, for example a high-priority `allow` that was meant for a different group. The "slug re-creation inherits nothing" guarantee in the ADR holds only for rules that existed at delete time.
- How established: READ.
- Fix: reject `team:<slug>` unless the team exists (or is `everyone`), checked in the same transaction as the insert.

### R-03 Low: attempts on servers outside a caller's teams leave no audit record

- Where: `internal/gateway/gateway.go` `callTool`, `getPrompt`, `readResource`: `find`/`visible` returns not found before `run`, so no intent or completion event is written.
- Scenario: a token holder probing for `payroll__*` tools (names are guessable from the Start page of another person or from docs) produces no trace, whereas a policy denial does (`recordRefusal`). Indistinguishable responses are right; but nothing records them for the operator either. This matches the pre-F26 behaviour for unknown names, yet F26 turns "not in your team" into a security boundary whose violations are now invisible.
- How established: READ.
- Fix: when a name does not resolve for the caller but exists for the org (an admin-side lookup that is not shown to the caller), write a refusal audit event (`denied:not_in_team`) while still returning the same caller-visible text. Needs a cheap org-wide lookup; rate-limit the audit noise.

### R-04 Low: a server with no teams is shown without a warning to admins (omitempty on an empty list)

- Where: `internal/control/control.go` `serverView.Teams` is `json:"teams,omitempty"`; `view` sets `append([]string{}, teams...)`, an empty non-nil slice that `omitempty` drops. `web/src/pages/servers.tsx:68` only warns when `s.teams && s.teams.length === 0`.
- Scenario: the fail-closed state "reachable by nobody" (all teams removed, or `teams: []`) renders as a blank cell and no warning on the Servers list; the admin sees nothing wrong while every member loses the server. The ADR says the app warns about it.
- How established: READ (JSON encoding rule; the field is absent, not `[]`).
- Fix: drop `omitempty` for admins (emit `[]`), keep it absent for non-admins by using a pointer or a separate struct; add a UI test for the zero-team warning.

### R-05 Low: an admin who adds a server before teams load gets "everyone" silently

- Where: `web/src/pages/servers.tsx:153` `if (teams.length > 0) body.teams = chosen;` and the same pattern in the catalog add. If `/v1/teams` is still loading or failed, `teams` is empty, `teams` is omitted from the request, and the API default is `everyone`.
- Scenario: an admin picks a restricted server from the catalog during a slow or failed teams fetch; the picker is hidden, the form submits, and the server is open to every member. Fail-open on a UI failure.
- Related API default: `POST /api/v1/servers` without `teams` means `everyone` by design (ADR point 3, to keep existing clients working). That is a deliberate fail-open for token-API callers; consider an org setting for the default.
- How established: READ.
- Fix: disable the add forms until the teams request succeeded; on error show the error and block submission.

### R-06 Low: unknown-team and race errors fall through to a 500; unknown team is detected after outbound work

- Where: `internal/control/teams.go` `addTeamMember` (maps `ErrNotFound`, not `ErrUnknownTeam`) and `addTeamServer` (neither `ErrNotFound` nor `ErrUnknownTeam`); `internal/control/control.go` `createServer` validates team existence only inside `CreateServer`, after URL egress checks and `configureOAuth` (which may do discovery and dynamic client registration at the provider).
- Scenario: deleting a team or server between the handler's lookup and the store transaction returns a generic server error instead of 404/422. A create with a bogus team name registers an OAuth client at the provider and then fails, leaving an orphan registration at a third party. Safe (fails closed), but wrong and wasteful.
- How established: READ.
- Fix: map the two errors; check team existence before `configureOAuth` as a fast path (the store check stays authoritative).

### R-07 Low: team names accept invisible and line-breaking characters; limits count bytes

- Where: `internal/control/teams.go:94` `plainText`: rejects `unicode.IsControl` and category Cf only, and uses `len(s)` (bytes), while the UI says "characters".
- What passes (RAN, scratch program): U+2028 (line separator), U+3164 (Hangul filler) repeated, so a name made only of invisible characters survives `TrimSpace`; private-use characters; stacked combining marks. U+202E and U+200B are correctly rejected.
- Scenario: an admin creates a team that looks blank, or two teams named identically ("Everyone" is also free to use as the name of a normal team, `slug` `everyone-2`), which makes the access picker and the member's "Your teams" line misleading. Admin-only input, so Low; names are rendered as text by Preact, so there is no XSS.
- Fix: also reject `Zl`, `Zp`, `Co`, `Cn`, runs of `Mn`; require at least one letter or digit; count runes; make names unique case-insensitively and reject the reserved display name "Everyone".

### R-08 Info: the pre-existing token-to-person binding and the effect of teams

- ADR 0020 already records this. Verified: teams do not make it materially worse. Only admins can choose a token's `person` freely (`POST /api/v1/tokens`, admin already sees everything), and `POST /me/tokens` forces the caller's own person (`session.go:265`). A removed member's tokens reach nothing (checked in `AccessFor`), and removal is permanent (no re-enable path found), so old `team_members` rows cannot revive. A token whose person is not a member is in `everyone` only: confirmed in code and `access_test.go`.

### R-09 Info: SQL dialect

- `INSERT OR IGNORE` (`internal/store/teams.go:197`, `:242`) is SQLite-only. The project plans Postgres for HA; the new store code needs `ON CONFLICT DO NOTHING` then. Also `server_teams.team` has no foreign key (needed for `everyone`), so on Postgres under READ COMMITTED a concurrent `DeleteTeam` and `AddServerTeam` could leave a dangling row that a re-created slug would inherit. On the single SQLite writer (`s.w`) transactions serialise and `checkTeams` runs in the same transaction, so I found no race there.

### R-10 Info: read-side behaviour changes worth knowing

- Auditors and approvers (roles with `servers.read` but no team membership) now get a team-filtered `GET /api/v1/servers`, the same as a member; they lose the org-wide server list. Approvals still show requests for any server to approvers (by design).
- `GET /api/v1/me/tools` does one `PinStatuses` query per reachable server (N+1) and `AccessFor` is evaluated twice per tool call (`find` and `decide`); both are cheap on SQLite but worth batching.
- List results are cacheable (`private`, 5 s). A client keeps a removed server in its list for up to the TTL; calls are refused at once (acceptance test covers this).
- `server.teams=...` on create is a second, non-atomic audit write after `CreateServer` (`adminEvent` logs and continues on failure); team operations proper (create, update, delete, member, server-team) write their audit event in the same transaction.

### R-11 Info: client setup snippets

- `web/src/clients.ts` interpolates `mcp_url` (from the configured public origin) and the token into shell, JSON (via `JSON.stringify`) and TOML. Token alphabet and origin are server-controlled; no injection path from user data. Codex uses `bearer_token_env_var`, so the token is not written into the TOML. The Claude Code line puts the token in shell history and argv (unchanged from before).

### R-12 Info: catalog

- Static data, no fetch at runtime, `app_url` values are fixed `https://` links rendered with `rel="noopener noreferrer"`. `connect_url` is built server-side from the public URL and a validated alias. Adding still goes through `POST /api/v1/servers` (egress check, discovery, tool review). Own-app flow sends `client_secret` in the body only and the UI never echoes it. No `dangerouslySetInnerHTML`, `innerHTML` or data-driven `href` found in the new pages.

## Checked and found sound

| Area | Result |
|---|---|
| Lists (`tools/list`, `prompts/list`, `resources/list`) | Filtered by `acc.Reaches(it.ServerID)` in `visible`; `resources/templates/list` returns empty. |
| `tools/call`, `prompts/get`, `resources/read` | All resolve through `visible`/`find`; hidden and unknown names return identical text and code (acceptance test compares them). No timing channel: `PinStatuses` runs only for reachable servers in both cases. |
| Other methods (completion, subscribe, logging) | Fall to the SDK server, which registers no such features; nothing routes to an upstream. |
| Approvals after access is removed | A retried call re-enters `find` first, so it is refused before `decide`; the approval just expires. |
| Fail-closed errors | `Access` returns any lookup error; `visible`, `decide`, `listServers`, `/me/tools` propagate it (`access_test.go`). Disabled or unknown user id reaches nothing; empty user id gets `everyone` only. |
| Principals | Session principal uses `UserID`; token principal resolves `Person` via `UserByPerson`; `/me/tools` `connected` uses the same resolution and only the caller's own grants. |
| Org isolation | Every new query carries `org_id`; composite FKs include `org_id`; `TestTeamsAreIsolatedPerOrg`; typed `OrgID` on every method. |
| Policy | `team:` matches against server-derived `acc.Teams`, not request data; `team:everyone` valid; team delete removes its rules; slug re-creation after delete inherits nothing (but see R-02). |
| Permissions | Team CRUD and membership need `members.manage`, team-server and catalog need `servers.manage`, `/me/tools` needs `gateway.use`; both token API and session bridge go through the same `wrap`. |
| Migration | Existing servers get `everyone` (`TestMigrationGivesExistingServersEveryone` runs down to 5 and up); down drops the three tables in FK order; foreign keys are ON. |
| Server delete | Removes `server_teams`; a re-created alias gets a new id and inherits no access. |
| Audit | All team writes audited in the same transaction with actor id; no secret in events. |
| XSS | Team names, descriptions and aliases rendered as text by Preact; Cf/bidi controls rejected (see R-07 for gaps). |

## Limits

R-01, R-02, R-03, R-04, R-05 and R-06 are from reading, not executed; I did not write an end-to-end probe for the Connections endpoint (the acceptance fixture could do it in a few lines). The Unicode result in R-07 was run. No production deployment was contacted.
