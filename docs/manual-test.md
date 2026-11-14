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

