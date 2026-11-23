# The admin site and signing in

Everything after the first sign-in happens in the browser at
`<PRIVO_PUBLIC_URL>/admin/`:

- **Start**: each person's servers and client setup.
- **Servers**: add, edit, review tools, turn off.
- **Teams**.
- **Rules**: allow, ask, deny.
- **People**: invite, roles, remove, restore.
- **Tokens**: each person makes tokens for their AI clients, with a ready
  Claude Code command.
- **Activity**, **Approvals** and **Connections**.

It is a small Preact app built into the binary
([ADR 0019](https://github.com/talk2sohail/privoclub/blob/main/docs/adr/0019-admin-app-preact.md)):
moving between pages loads no page, and every action is a call to the admin
API, run in process as the signed-in person; the API stays for scripts.

## Sign in with GitHub

People sign in with their GitHub account and no token
([ADR 0016](https://github.com/talk2sohail/privoclub/blob/main/docs/adr/0016-admin-sign-in-with-github.md)).
It is off until you set `PRIVO_GITHUB_CLIENT_ID`,
`PRIVO_GITHUB_CLIENT_SECRET` (or `_FILE`) and `PRIVO_PUBLIC_URL`; until then
`/admin`, `/auth` and `/connect` answer 503 and name what is missing.

1. In GitHub, create an OAuth App (Settings > Developer settings). Its
   authorization callback URL is `<PRIVO_PUBLIC_URL>/auth/github/callback`.
   Privo asks for no scope.
2. Give Privo the client ID and the client secret, restart, and print a setup
   link next to the data directory:

   ```sh
   privo admin setup-link [--person NAME]
   ```

   Open the link within an hour, sign in with the GitHub account that should
   own this Privo, and you are in. The link works once. `--person` is the name
   your tokens carry (default: your GitHub login in lowercase); self-approval
   is refused between tokens and sessions of the same person.

Only people who were added can sign in; any other GitHub account sees a plain
refusal. [Deploy on Coolify](../installation/coolify.md#sign-in-with-github)
has the same steps on Coolify.

To limit sign-in to members of your GitHub organisations, set
`PRIVO_GITHUB_ORG_IDS` (0.19.0; see the
[configuration reference](../installation/configuration.md)).

Sessions end after `PRIVO_SESSION_IDLE_TIMEOUT` without a request (2 hours by
default) or `PRIVO_SESSION_MAX_AGE` after sign-in (12 hours).

## Approvals in the browser

`/admin/approvals` lists the calls waiting for a decision, with their
arguments. [Rules, approvals and grants](rules.md) says how a call comes to
wait and what approving does.

## Changing the app

The admin app's source is in `web/`. Changing it needs Node 22 and `make ui`;
building Privo does not ([Building Privo](../development/building.md)).
