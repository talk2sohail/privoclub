# Your first server and AI client

From one server to a tool call that works, in the browser. The example uses
**DeepWiki**, a server in Privo's catalog that answers questions about public
GitHub repositories and needs no sign-in, so nothing stands between you and
the first call.

These steps match Privo 0.19.0 and later, signed in as the owner or an admin.

## 1. Add a server from the catalog

1. Open **Servers**. Under **Add from the catalog**, find **DeepWiki** and
   choose **Add**.
2. **Name in Privo** is the alias, `deepwiki`: AI clients see its tools as
   `deepwiki__<tool>`.
3. **Who can use it** says which teams reach the server. Keep **Everyone**.
4. Choose **Add DeepWiki**. Privo opens the server's page.

The catalog fills in the server's settings; Privo still checks the server, and
you still review every tool. Servers that use each person's own account work
the same way, and some, like GitHub, also need an OAuth app of yours: the form
shows the callback URL to give it. For a server that is not in the catalog,
use **Add another server**: see [Adding servers](../using/servers.md).

## 2. Review and accept its tools

Nothing reaches an AI client before a person accepts it. After a few seconds
the server's tools appear under **Waiting for review** (reload the page if
they have not yet).

1. Read what each tool says it does. An AI client reads every word of a tool's
   description and treats it as instructions, so look for anything you would
   not want it to do. Privo flags hidden instructions and characters a person
   cannot see.
2. Choose the tools you trust, or **Choose all N shown**.
3. Choose **Accept N tools**. They move to **Available to AI clients**.

**Block** keeps a tool hidden. If the server changes a tool later, the tool is
hidden from everyone again until someone accepts the new version.

## 3. Connect your account, if the server needs it

DeepWiki needs no account, so you can skip this step. For servers where each
person uses their own account (GitHub, Linear, Notion and the like), open
**Start**: under **1. Your tools**, choose **Connect** (or **Enter your key**)
next to the server, and sign in once. Privo then calls that server as you,
and only for your calls. See [Each person's own account](../using/connections.md).

## 4. Make a token for your AI client

1. Open **Start**. Under **2. A token for your AI client**, name the client,
   for example `claude-code-laptop`, and choose **Create token**.
2. Copy the token now: it is shown only once.

Each AI client gets its own token, so you can revoke one without the others.
A token made here belongs to you: it reaches your teams' servers and your own
accounts, and works only on `/mcp`.

## 5. Add Privo to your AI client

Under **3. Add Privo to your AI client**, choose your client. Privo fills the
token into the line or file for it. For Claude Code, it is one command:

```sh
claude mcp add --transport http privo https://privo.example.com/mcp --header "Authorization: Bearer privo_pat_..."
```

Then run `/mcp` in Claude Code: Privo is listed, with DeepWiki's tools. Cursor,
VS Code, Codex and Windsurf each get their own file and format on the same
page. More in [Tokens and AI clients](../using/tokens.md).

## 6. Make the first call

Ask your AI client something DeepWiki can answer, for example:

> Use DeepWiki to explain how the modelcontextprotocol/go-sdk repository is
> organised.

The client calls one of the `deepwiki__…` tools through Privo, and the answer
comes back from DeepWiki.

## 7. See it in Activity

Open **Activity**. **Tool calls** lists your call: who made it, from which
client, the server and tool, the decision and how it ended. **Overview**
counts it with its timings.

## If it does not work

- **The AI client shows no tools.** Accept them on the server's page (step 2),
  and check that the server is given to one of your teams (**Who can use it**
  on its page).
- **The client says it is not authorised.** The token was copied wrongly or
  revoked: make a new one on **Start**.
- **"Connect your … account first"**: the server uses your own account. Open
  the link in the message, connect, and ask again.

## Next

- [Invite your team](../using/people.md): each person signs in with GitHub
  and makes their own token on **Start**.
- [Organise teams](../using/teams.md) so each one reaches only its servers.
- [Add rules](../using/rules.md): ask a person before risky calls, or deny
  them outright.
