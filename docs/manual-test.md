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

## 10. A person with several roles (needs 0.8.0+ and the second account from 4)

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

## 11. Home page and tool review (needs 0.9.0+)

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

## 12. Approvals people live with (needs 0.10.0+ and the second account)

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

## 13. Toolsets (needs 0.11.0+ and the second account)

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

## 14. Rules on arguments and rules as a file (needs 0.12.0+)

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
