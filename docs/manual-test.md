# Manual test: Privo in the browser

Everything here is done in the browser, except printing the setup link once.
It checks what the automated tests cannot: the real GitHub, Coolify's proxy and
cookies over https, a real AI client and real MCP servers. Write down anything
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
   your name and "owner" top right, and the menu Approvals, Servers, Rules,
   People, Tokens, Activity, Connections.

## 2. A server

1. **Servers → Add a server**: name `deepwiki`, URL
   `https://mcp.deepwiki.com/mcp`, Streamable HTTP, No sign-in needed →
   **Add server**. (If deepwiki already exists, open it instead.)
2. Expect the server page: status `up`, and under **Waiting for review** its
   tools with descriptions. Read them, keep them ticked → **Accept the ticked
   ones**. Expect: "Available to AI clients" lists them.
3. Wrong input shows the reason on the page: try adding a server named `Bad
   Name` → expect an error, nothing added.

## 3. A rule

1. **Rules → Add a rule**: Server `deepwiki`, Tool `*`, Who everyone, Action
   ask for approval → **Add rule**. Expect it in the list.

## 4. A second person (needs a second GitHub account; skip to 5 otherwise)

1. **People → Invite someone**: Person `tester`, Role member → **Create invite
   link**. Expect a link shown once.
2. Open it in a **private window**, Continue with GitHub with the second
   account. Expect: lands on **Tokens**, menu shows only Tokens and Connections.

## 5. An AI client token

1. As the person who will call tools (the second account from 4, so that
   you, the owner, can approve; approving your own calls is refused):
   **Tokens → Your AI clients**, name `claude-code` → **Create token**.
   Expect: the token and a ready command, shown once.
2. Run the command in your terminal (`claude mcp add --transport http privo …`).
   Without a second account: make the token as yourself and skip 6.2 (you
   cannot approve your own call; use **Rules** to set the action to allow).

## 6. A call that needs approval

1. In Claude Code ask: "use privo deepwiki to read the wiki structure of
   modelcontextprotocol/go-sdk". Expect: "Approval required…".
2. As the owner: **Approvals** shows the call and its arguments → **Approve
   this call**. Ask Claude Code to try again. Expect: the real answer.
3. Again, and **Deny**. Expect: the retry is refused.
4. **Activity**: the calls with decisions and "by sohail"; **Admin changes**:
   your server, rule, invite and approvals, with you as actor.

## 7. Clean up

1. **People**: tick "sure" next to `tester` → **Remove**. Expect: "removed";
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
