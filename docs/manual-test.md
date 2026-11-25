# Manual test: Privo in the browser

Everything here is done in the browser, except printing the setup link once.
It checks what the automated tests cannot: the real GitHub, Coolify's proxy and
cookies over https, a real AI client and real MCP servers. The admin site is an
app (ADR 0019): moving between pages should feel instant, with no white flash;
note any page that does not. Write down anything
that differs from "expect" (step number, what you saw, the time) and tell
Claude.

## 0. Once: GitHub sign-in

Already done on app.privo.club (OAuth App, secret file mount, two variables).
For a fresh install see `docs/deploy-coolify.md` → "Sign in with GitHub".

## 1. Owner

1. Coolify → Servers → your server → **Terminal** (or SSH), print a link:
   `docker exec $(docker ps -qf name=f46equvu3pb9kmq0shusvojo) /privo admin setup-link --person sohail`
   (skip if you are already the owner: sign in at `/admin/signin`).
2. Open the link → **Continue with GitHub**. Expect: you land on **Servers**,
   your name and "owner" top right, and the menu Start, Approvals, Servers,
   Teams, Rules, People, Tokens, Activity, Connections.

## 2. A server

1. **Servers → Add a server**: name `deepwiki`, URL
   `https://mcp.deepwiki.com/mcp`, Streamable HTTP, No sign-in needed →
   **Add server**. (If deepwiki already exists, open it instead.)
2. Expect the server page: status `up`, and under **Waiting for review** its
   tools with descriptions (they appear by themselves within a few seconds).
   Read them, keep them ticked → **Accept N tools**. Expect: "Available to AI
   clients" lists them.
3. Wrong input shows the reason on the page: try adding a server named `Bad
   Name` → expect the browser to point at the name field; with a valid name
   but a URL that does not answer, expect the server's reason in a red box.

## 3. A rule

1. **Rules → Add a rule**: Server `deepwiki`, Tool `*`, Who everyone, Action
   ask for approval → **Add rule**. Expect it in the list.

## 4. A second person (needs a second GitHub account; skip to 5 otherwise)

1. **People → Invite someone**: Person `tester`, Role member → **Create invite
   link**. Expect a link with a **Copy** button, shown once.
2. Open it in a **private window**, Continue with GitHub with the second
   account. Expect: lands on **Start** (their teams, the servers they can use,
   and how to set up an AI client); the menu shows only Start, Tokens and
   Connections.

## 5. An AI client token

1. As the person who will call tools (the second account from 4, so that
   you, the owner, can approve; approving your own calls is refused):
   **Tokens → Your AI clients**, name `claude-code` → **Create token**.
   Expect: the token and a ready command, each with **Copy**, shown once
   (leave the page and come back: it is gone).
2. Run the command in your terminal (`claude mcp add --transport http privo …`).
   Without a second account: make the token as yourself and skip 6.2 (you
   cannot approve your own call; use **Rules** to set the action to allow).

## 6. A call that needs approval

1. In Claude Code ask: "use privo deepwiki to read the wiki structure of
   modelcontextprotocol/go-sdk". Expect: "Approval required…".
2. As the owner: **Approvals** shows the call and its arguments (click
   **refresh** or switch back to the tab if it was already open) → **Approve
   this call**. Ask Claude Code to try again. Expect: the real answer.
3. Again, and **Deny**. Expect: the retry is refused.
4. **Activity**: the calls with decisions and "by sohail"; **Admin changes**:
   your server, rule, invite and approvals, with you as actor.

## 7. Clean up

1. **People**: **Remove…** next to `tester` → **Remove tester**. Expect: "removed";
   their Claude Code gets "invalid token" within seconds.
2. **Rules**: delete the ask rule. **Tokens**: revoke tokens you made for the
   test.

## 8. Each person's own account (needs 0.4.0+, and an account at the provider)

1. **Servers → Add a server**: name `linear`, URL `https://mcp.linear.app/mcp`,
   "Each person's own account" → **Add server**. Expect: status down with
   "needs an administrator's connected account" and a connect link.
   (Notion `https://mcp.notion.com/mcp` or Atlassian work the same way. GitHub's
   MCP server needs its own OAuth App, callback
   `https://app.privo.club/connect/callback`, entered under "OAuth app".)
2. **Connections → Connect your account** → sign in at the provider. Expect:
   "Connected"; the server turns `up` with tools waiting for review; accept
   them.
3. Call one of its tools from Claude Code. Expect: it runs as your account.
4. Disconnect on **Connections**; call again. Expect: the answer is a link to
   connect.

## 9. Teams and the catalog (needs 0.6.0+)

1. **Teams → New team**: Start from **Designers** (the name fills in) →
   **Create team**. Expect: the team opens below, with **Suggested servers**
   (Figma, Canva, Webflow, Miro).
2. Add the second account from 4 to the team (**Add to team**).
3. **Servers → Add from the catalog**: click **Designers**, then **Add** next to
   Figma. Under **Who can use it** untick Everyone, tick Designers → **Add
   Figma**. Expect: the server page, waiting for an admin's connected account;
   connect yours and accept its tools (as in 8).
4. Try **Add** on GitHub: expect the OAuth app help, a link to GitHub and the
   callback URL with a **Copy** button. Cancel.
5. As the second account, open **Start**. Expect: Designers among the teams,
   Figma listed with **Connect**, and the client tabs (Claude Code, Cursor,
   VS Code, Codex, Windsurf, Other). Make a token there: every tab fills it in.
6. In that person's AI client: Figma's tools show up (within 5 seconds of a
   refresh). Take them out of Designers (× next to their name): Figma's tools
   are gone at the next refresh, and a call is "not available".
7. Open Figma's page as the owner: **Who can use it** shows Designers. Untick
   everything: expect the warning that nobody can use it. Put Designers back
   (Save stays disabled while nothing changed), tick Everyone too → **Save**:
   expect "Saved". Untick Everyone → **Save**.
8. **Activity → Admin changes**: team.create, team.member.add, server.create,
   server.teams (one per Save) with you as actor.

## 10. An AI client signs in with OAuth (needs 0.8.0+ and `PRIVO_OAUTH_SERVER=on`)

1. In Claude Code: `claude mcp add --transport http privo https://app.privo.club/mcp`
   (no token), then `/mcp` and **Authenticate**. Expect: a browser opens on
   Privo; signed out, you go through GitHub first and land back on the page.
2. Expect the page to say **Let Claude Code use Privo as you?**, name
   `claude.ai` as the client, `localhost:<port>` as where you go back, you as
   the person, and warn that it runs on your computer. Press **Deny**: Claude
   Code reports the refusal. Authenticate again and press **Allow**: Claude
   Code says it is connected.
3. Ask Claude Code to call a tool of a server you connected your own account
   to (8): it answers as you.
4. **Tokens → AI clients that signed in**: Claude Code is listed. As the
   owner, **Clients → Connected clients** shows it under your name.
5. **Take access away**. Within seconds Claude Code's next call fails and it
   asks to authenticate again.
6. **Activity → Admin changes**: `oauth.grant.allow`, `oauth.token.exchange`,
   `oauth.grant.revoke:…` with the client and "for <you>".
7. Optional: Claude on the web (Settings → Connectors → add
   `https://app.privo.club/mcp`) and VS Code, the same way. Cursor needs
   `PRIVO_OAUTH_DYNAMIC_REGISTRATION=on` and is shown as "registered itself,
   not verified".

## 11. A server that takes each person's own key (needs 0.9.0+)

Needs a server that takes an API key in a header; any MCP server behind a
proxy that checks a header will do.

1. **Servers → Add another server**: under **Whose account does this server
   see?** choose **Each person's own account, by their own API key**. Header
   `X-Api-Key` (or leave `Authorization`), a line on where to get a key, a
   link. **Add server**. Expect: the server page says people enter their own
   key at `/connect/<name>`, sent as that header.
2. Open that link: a page with your server's name, the help text, the link
   and a password field. Enter your key → **Save my key**. Expect: back on
   Connections, the server marked connected; the server page comes up and
   lists its tools for review.
3. As the second account: the AI client's first call says to enter a key,
   with the link. Enter a wrong one: the next call says the server refused
   your API key, with the link. Enter the right one: the call works as that
   account.
4. **Activity → Admin changes**: `server.personal_key:<name>:<header>` and
   `upstream.connect:<name>` for each person.

## 12. Activity statistics and the log (needs 0.10.0+)

1. Make a few calls from an AI client (one that works, and one to a tool a
   rule denies, if you have one).
2. **Activity**: the Overview is the first tab. Expect the calls counted,
   the refused one under Refused, a bar in the chart, and the server in the
   table with its median and p95. Switch **by tool** and **by member**: your
   name appears, not an id.
3. **Log**: expect "Privo 0.10.0 started" under Start and stop. Stop an
   upstream server (or give it a wrong URL) and wait a minute: expect
   "connection failed" with a count, and "connected again" once it is back.
4. At the bottom of the Overview: "Kept for 30 days", and no "not recorded".
5. Optional: with `PRIVO_METRICS_ADDR=127.0.0.1:9464`, `curl
   127.0.0.1:9464/metrics` in the container shows `privo_mcp_calls_total`;
   `https://<your domain>/metrics` does not.

## 13. A person with several roles (needs 0.11.0+ and the second account from 4)

1. **People**: next to the second account, **change** → tick **member** and
   **approver** → **Save roles**. Expect: "approver, member" in the list.
   Tick **admin** in the picker: the other boxes clear (admin stands alone);
   **Cancel**.
2. As the second account, reload: they still land on **Start**, and
   **Approvals** is now in the bar.
3. In their AI client, call a tool that a rule sends to approval (as in 6).
   On **Approvals** they see their own request marked "(you)" with "This is
   your own request. Another approver decides it." and no buttons. Approve it
   as the owner instead; the retried call goes through.
4. Make a call from the owner's AI client that needs approval: the second
   account approves it.
5. Paste the second account's AI client token into a request to the admin API
   (`curl -H "Authorization: Bearer <token>" <URL>/api/v1/approvals`): expect
   401.
6. Change their roles to **approver** only. Their AI client's tool list is
   empty at the next refresh, a call is "not available", and **Tokens** offers
   them no new token. Put **member** back.
7. **Activity → Admin changes**: `member.roles:<person>:<before>-><after>`,
   one per Save, with you as actor.

## 14. Home page and tool review (needs 0.12.0+)

1. Open the address itself (`https://app.privo.club/`), signed out. Expect:
   **Sign in** (it goes to the admin site), the MCP address
   `https://app.privo.club/mcp` for AI clients, and **About Privo**.
2. Add a server whose tools you have not accepted (any catalog server, or
   remove and add DeepWiki again). On its page, under **Waiting for
   review**, expect each tool on its own card: its whole description as
   text (more than the first line), its inputs with their descriptions, and
   **The whole definition** to open. Expect **Accept 0 tools** disabled:
   nothing is chosen until you choose it.
3. Choose one tool → **Accept 1 tool**. Choose another → **Block 1**. Expect:
   the blocked one under **Blocked (1)**, and it does not appear in your AI
   client's tool list. **Accept after all** on it: it appears at the next
   refresh.
4. A tool that changes: if a server you use changes a tool's description,
   its page shows the tool as **changed** with **What changed since the
   accepted version** (old lines in red, new in green). Until you accept,
   the AI client does not see the tool.
5. Warnings: a tool whose description talks to the model ("<IMPORTANT>",
   "do not tell the user", "~/.ssh") or hides characters shows a red
   **N warnings** badge and the lines that triggered it; hidden characters
   appear as ⟦U+200B⟧. No warnings on ordinary tools such as DeepWiki's.
6. **Activity → Admin changes**: `pins.accept:1:tool/<name>@<hash>`,
   `pins.block:1:…` and `pins.accept:1:…` again, each naming the tool and the
   start of its hash, with the server.

## 15. Approvals people live with (needs 0.13.0+ and the second account)

1. Make sure a rule asks before a tool (as in 3). From the second account's
   AI client, call it. As the second account, open **Start**: expect
   **Waiting for approval** with that call. **Withdraw** it. Expect the AI
   client's next identical call to ask again (a new request).
2. As the owner, open **Approvals**. Next to **Approve**, choose **Allow:
   this tool for 1 hour** → **Approve**. Expect "Approved, and <person> may
   use this tool for 1 hour without asking." The AI client's call goes
   through, and so do its next calls of that tool with other arguments.
3. **Rules**: a new allow rule for that person, "until" an hour from now,
   with the note "Allowed for … from approval …". Delete it: the next call
   asks again.
4. **Approvals → History**: the approved and the withdrawn requests, with
   who decided, and no arguments. With more than 50 waiting, **Load more**
   appears at the bottom of **Waiting**.
5. **Activity → Admin changes**: `approval.withdraw:…` by the second
   account, `approval.approve:…` and `policy.grant:…` by you.

## 16. Toolsets (needs 0.14.0+ and the second account)

1. Give a server with several tools to a team the second account is in (for
   example GitHub to Developers). On the server's page, under **Which tools
   each team sees**, that team shows **All tools**.
2. Choose **Only these**, tick two tools → **Save**. Expect "Saved". In the
   second account's AI client, after a refresh, the server lists only those
   two tools; a call on another tool of that server says it is not
   available, exactly as for a name that does not exist.
3. As the second account, **Start** counts only those two tools for that
   server.
4. Untick both: expect the warning that the team sees none of the tools.
   Choose **All tools** → **Save**: everything is back after a refresh.
5. **Activity → Admin changes**: `server.team_tools:<alias>:<team>:2`, then
   `…:0` if you saved with none, then `…:all`.

## 17. Rules on arguments and rules as a file (needs 0.15.0+)

1. **Rules → Add a rule**: server DeepWiki (or any), tool
   `read_wiki_structure`, action **deny**, **Add a condition**: `repoName`
   **is** `facebook/react` → **Add rule**. Expect the rule listed with
   "when repoName is "facebook/react"".
2. From an AI client, call that tool for `facebook/react`: denied. Call it for
   `modelcontextprotocol/go-sdk`: it runs (or asks, if another rule says so).
3. Add a rule **ask** for every tool of that server. Call it for another
   repository: approval required. Approve with **Allow: this tool for 1
   hour**. Calls for other repositories now run; `facebook/react` is still
   denied: a grant never lifts a deny.
4. **Rules as a file → Export**: a YAML file with both rules and the
   condition. Paste it into **Import a rules file**, choose **Replace all
   rules with these**, **Check**: expect "This adds 2 rules and deletes 2".
   **Import**: the rules are the same as before, and the grant from step 3
   is still listed.
5. Paste a file with a mistake (for example `op: regex`) and **Check**:
   expect a message naming the rule, and nothing changed.
6. **Activity → Admin changes**: `policy.export:2`, `policy.import:replace:+2:-2`.

## 18. The audit trail (needs 0.16.0+)

1. Make a few calls from an AI client. **Activity → Tool calls → Check the record**:
   expect "Intact: N blocks and M events checked; last sealed …" within a few
   seconds of the calls.
2. On the host: `docker exec <container> /privo audit verify`. Expect JSON
   with `"ok": true` and exit code 0.
3. **Filter and export**: filter by a server and a date → the list narrows;
   **Export CSV** downloads the same rows (no recorded arguments).
   **Load more** appears with more than 100 rows.
4. As the owner: set **Keep events for** 10 days → **Save**: refused, at
   least 30 days. Set 365 → **Save**. Erase the recorded arguments of a
   person (only matters if recording is on); calls they made through an AI
   client that signed in with OAuth are erased too. **Check the record**
   again: still intact.
5. `curl -H "Authorization: Bearer <owner control token>"
   https://app.privo.club/api/v1/audit/checkpoints` lists signed block heads;
   `/api/v1/audit/keys` lists the public key. Keep both outside Privo.
6. **Activity → Admin changes**: `audit.verify:true`,
   `audit.retention_days=365`, `audit.erase:<person>`, `audit.export:<n>`.
7. Optional, on a copy of the database only (never production): with
   `sqlite3` delete the oldest rows of `audit_events`, or add a row with an
   old `ts`. **Check the record** must say "Not intact" and name the block,
   event or prune record.

## 19. Editing a server, restoring a member, token names (needs 0.17.0+ and the second account)

1. **Servers → a server with a header** (add one with `Authorization` if
   needed) **→ Edit**: the header shows as "set", never its value. Type a new
   value → **Save changes** → "Saved."; the server stays up and its tools stay
   accepted. **Activity → Admin changes**: `server.headers:Authorization`,
   with no value anywhere.
2. Change **Address** to another URL of the same server (for DeepWiki,
   `https://mcp.deepwiki.com/mcp` and back): a warning lists what ends;
   **Save changes** stays off until you tick "Change the address anyway".
   Save: the flash asks to enter the headers again; the server's tools are
   back in **Waiting for review**, and an AI client sees none of them until you
   accept. Admin changes: `server.url`, `server.review_again:N`,
   `server.connections_ended:N`.
3. A server that uses each person's own account: **Address** is greyed out
   with the reason. If its client was entered by hand, change the **Client
   id**: the page warns that everyone connects again; after saving,
   **Connections** is empty.
4. **People → Remove…** the second account, then **Restore**: they are back
   with the same roles. Signed in as them: no tokens from before; make one.
5. Signed in as each of the two accounts, make a token named `claude-code`:
   both work. A second `claude-code` for the same person is refused.
6. **Add a server** from the catalog with an address that does not answer
   (any catalog entry while offline, or a private address refused by the
   egress policy): the error shows under that catalog entry's form, not only
   at the top. A provider that hangs is named with "did not answer within 10
   seconds" and what Privo was doing, never "context deadline exceeded".

## 20. Sessions and tokens after the F14 review (needs 0.17.1+)

1. **Tokens → Create an API token**, role **auditor**: no checkbox. Role
   **owner**: a checkbox says what the token can do; **Create API token**
   does not submit until it is ticked. **Activity → Admin changes**: the
   `token.create:control:owner:<name>` event shows client `web`.
2. The token shown once has **I copied it, hide it**: it disappears. Make
   another and leave the page open: it goes by itself after 10 minutes.
3. Revoke the owner token you just made.
4. Optional, with the browser's developer tools: a request to
   `/admin/api/v1/...` with a wrong `X-Privo-CSRF` header is refused (403),
   and does not keep you signed in: after the idle timeout (2 hours by
   default) without a real click, the app asks you to sign in again.

## 21. Control-plane review findings (needs 0.18.0+ and the second account)

1. **Self-approval in any case:** make an API token whose person is your
   name in capitals (`BOSS` for `boss`) with role member, call a tool with an
   **ask** rule through it, then open **Approvals** signed in as yourself:
   the request is marked yours and cannot be approved by you.
2. **Catalog, own app:** add GitHub from the catalog with an OAuth App's id
   and secret: it works as before (the catalog names the issuer). Through the
   API, the same request without `oauth.issuer` is refused with a message
   naming it.
3. **Outside the teams:** give a server to one team only. As the second
   account (not in it), call one of its tools three times: each answer reads
   like an unknown tool. **Activity → Tool calls**, filter decision
   `denied:not_in_team`: one row.
4. **What is served:** on a server's page, **Available to AI clients** lists
   only what the server offers now; a changed tool's diff compares with the
   version AI clients get today.
5. **A server turned off** asks its provider for nothing; turned on again,
   people's connections still work without reconnecting.

## 22. Grants revoked at the provider (needs 0.18.1+ and the second account)

1. As the second account, connect a server that signs people in with their
   own account (GitHub, Linear, Notion…), then check the provider's list of
   authorized apps (GitHub: Settings → Applications → Authorized OAuth Apps):
   Privo is there.
2. As the owner, **People → Remove** the second account. Within a minute the
   provider's list no longer shows Privo for them (where the provider offers
   revocation; GitHub does for OAuth Apps).
3. Restore them, connect again, then delete the server (or change its OAuth
   client id under **Edit**): the same happens.

## 23. Everyone's connections (needs 0.19.0+ and the second account)

1. With both accounts connected to a server that uses each person's own
   account or key, open **Connections** as the owner: **Everyone's
   connections** lists both, with server, person, kind and dates. No token
   anywhere on the page.
2. Filter by the second account's person name: only theirs.
3. **End…** then **End <person>'s <server>** on theirs: gone from the list;
   signed in as them, **Connections** asks them to connect again.
   **Activity → Admin changes**: `upstream.disconnect:<server>:<person>`.
4. Signed in as a member: no **Everyone's connections** section.

## 24. Sign-in limited to a GitHub organisation (needs 0.19.0+ and the second account)

1. Find your organisation's id: `curl https://api.github.com/orgs/<name>`,
   field `id`. Set `PRIVO_GITHUB_ORG_IDS=<id>` in Coolify and redeploy.
2. Sign out and in again: GitHub asks to let Privo read your organisations.
   If the organisation restricts OAuth App access, approve Privo's app for it
   (organisation settings → Third-party access) or you are refused.
3. Invite a GitHub account that is not in the organisation and open the
   link with it: "This GitHub account cannot sign in", naming the
   organisation. **Activity → Admin changes**: `signin.refused:not_in_org`.
4. Unset the variable and redeploy: sign-in asks for no scope again.

## 25. Arguments checked against the schema (needs 0.20.0+)

1. With a server whose tools you accepted, ask your AI client to call one of
   them with an argument of the wrong type or a made-up field (for example
   "call echo with text set to the number 5").
2. The client gets "The arguments do not match this tool's input schema, so
   Privo did not send the call", naming the field. It usually corrects itself
   and calls again.
3. **Activity**: the call shows `refused:invalid_arguments`; the arguments
   and the reason are not there. **Approvals** has no request for it, even
   under an ask rule.
4. A correct call still works, and still asks when a rule says so.
5. Add a server with a tool whose `inputSchema` has a `$ref` to a URL (or
   `"$schema": "https://json-schema.org/draft/2019-09/schema"`). The tool
   review shows the `input-schema` warning before you accept it. Accept it
   and disable then enable the server: **Health** warns that the tool's
   input schema cannot be read, before anyone calls it. A tool
   declaring `"$schema": "https://json-schema.org/draft-07/schema"` (no `#`)
   is read and checked normally.
