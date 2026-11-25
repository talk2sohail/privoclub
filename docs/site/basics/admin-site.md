# The admin site

Privo's admin site, at `https://privo.example.com/admin/`, is where people
sign in and where everything in this section happens: adding servers,
organising teams, setting rules, approving calls and making tokens for AI
clients. It runs in the browser; there is nothing to install.

GitHub sign-in arrived in 0.3.0, the admin site's pages in 0.5.0, and the
**Start** page in 0.6.0.

## Signing in

Open `https://privo.example.com/admin/`, or **Sign in** on Privo's home page,
and choose **Continue with GitHub**. Only people who were invited can sign
in; any other GitHub account is turned away. The very first owner signs in
with a setup link instead: see
[First setup and sign-in](../getting-started/first-setup.md).

To sign out, choose **Sign out** at the right of the menu.

A session ends after `PRIVO_SESSION_IDLE_TIMEOUT` without a request (2 hours
by default) or `PRIVO_SESSION_MAX_AGE` after you signed in (12 hours), and
you sign in again. Both are in the
[configuration reference](../installation/configuration.md).

## Where you land

After signing in, each person lands where their work starts:

- owners and admins on **Servers**;
- members on **Start**, also when they approve calls too;
- approvers who use no tools on **Approvals**;
- auditors on **Activity**.

## The pages

The menu shows only the pages your roles allow.

| Page | What it is for | Who sees it |
| --- | --- | --- |
| **Start** | Your servers, a token for your AI client, and its setup | Owners, admins, members |
| **Approvals** | Calls that wait for a person's decision, and their history | Owners, admins, approvers |
| **Servers** | Add servers, review their tools, choose who can use them | Owners, admins |
| **Teams** | Groups of people, and which servers each one reaches | Owners, admins |
| **Rules** | Allow, ask or deny calls | Owners, admins |
| **People** | Invite people, change roles, remove and restore | Owners, admins |
| **Tokens** | Your AI clients' tokens; for admins, everyone's | Owners, admins, members |
| **Clients** | AI clients that sign in with OAuth | Owners, admins |
| **Activity** | How calls went, and every admin change | Owners, admins, auditors |
| **Connections** | Your own accounts on servers that need them | Owners, admins, members |

## The Start page

**Start** is each person's own corner. It shows:

1. **Your tools**: the servers your teams reach, how many tools each offers,
   whether it is up, and whether it needs your own account. **Connect** (or
   **Enter your key**) appears where it does: see
   [Connecting your accounts](connections.md).
2. **A token for your AI client**: name it and choose **Create token**. See
   [Connecting AI clients](tokens.md).
3. **Add Privo to your AI client**: the line or file for Claude Code, Cursor,
   VS Code, Codex or Windsurf, with your token filled in.

When a call of yours waits for a person's approval, **Waiting for approval**
appears at the top of **Start**, and you can withdraw it there.

## Going further

- [Roles](../using/roles.md): what each role may do, and holding several.
- [AI clients that sign in with OAuth](../using/oauth-clients.md): the
  **Clients** page.
- Limiting sign-in to members of your GitHub organisations:
  `PRIVO_GITHUB_ORG_IDS` in the
  [configuration reference](../installation/configuration.md).
