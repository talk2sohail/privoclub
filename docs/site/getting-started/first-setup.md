# First setup and sign-in

Privo runs; now make it yours. People sign in to Privo's admin site with their
GitHub account, so you connect a GitHub OAuth App, then claim Privo as its
owner with a one-time setup link. After that, everything happens in the
browser.

These steps match Privo 0.19.0 and later. The examples use
`https://privo.example.com`; put your own address in its place.

## 1. Create a GitHub OAuth App

1. In GitHub, open **Settings → Developer settings → OAuth Apps → New OAuth
   App**. An organisation's settings work too.
2. Fill in:
    - **Homepage URL**: `https://privo.example.com`
    - **Authorization callback URL**:
      `https://privo.example.com/auth/github/callback`
3. Leave device flow off, and register the application.
4. Copy the **Client ID**. Then choose **Generate a new client secret** and
   copy the secret at once: GitHub shows it only once.

Privo asks GitHub for no scope: it only learns who you are.

## 2. Give Privo the app

Set these three settings, then restart Privo:

- `PRIVO_PUBLIC_URL`: your address, with no path: `https://privo.example.com`.
- `PRIVO_GITHUB_CLIENT_ID`: the Client ID.
- `PRIVO_GITHUB_CLIENT_SECRET_FILE`: the path of a file that holds the client
  secret, such as `/run/secrets/privo_github_client_secret`.

Keep the secret in a file (on Coolify, a file mount), never in a plain
environment variable: a variable stays readable in `/proc/<pid>/environ`
while Privo runs. All three settings are in the
[configuration reference](../installation/configuration.md).

When the restart worked, Privo's log says `admin site is on`, and
`https://privo.example.com/admin` asks you to sign in.

## 3. Claim Privo as its owner

On the server, print a setup link next to Privo's data:

```sh
privo admin setup-link --person ada
```

On Coolify the image has no shell, so run the binary through Docker on the
host, over SSH:

```sh
docker exec <container> /privo admin setup-link --person ada
```

`--person` is the name your tokens carry (by default, your GitHub login in
lowercase). The command prints a link like
`https://privo.example.com/admin/setup/privo_setup_...`. Open it within an
hour, choose **Continue with GitHub**, and sign in with the GitHub account
that should own this Privo. The link works once, and a new link cancels the
old one.

!!! warning
    Treat the setup link as a secret until you have used it: whoever opens it
    first becomes the owner. Once used, it is worthless.

### No shell on the server at all

If you cannot run commands on the server, use the bootstrap token from
[Deploy on Coolify](../installation/coolify.md#first-run-your-first-admin-token)
instead. It gives you an owner token for the admin API for 24 hours. Use it to
invite yourself as an admin, then open the `url` in the reply and sign in with
GitHub:

```sh
curl -sS -X POST -H "Authorization: Bearer $BOOT" -H 'Content-Type: application/json' \
  -d '{"person":"ada","roles":["admin"]}' https://privo.example.com/api/v1/invites
```

Invites never make owners, so this makes you an admin. An admin can do
everything this guide needs; a setup link, run later from a shell, makes an
owner.

## 4. Find your way around

After you sign in, you land on **Servers**. The menu at the top holds every
page you may use:

| Page | What it is for |
| --- | --- |
| **Start** | Your own corner: your servers, a token for your AI client, and its setup. Every member lands here |
| **Servers** | Add servers, review their tools, and choose who can use them |
| **Teams** | Groups of people, and which servers each one reaches |
| **Rules** | Allow, ask or deny, by server, tool, team and argument |
| **People** | Invite people, give roles, remove and restore |
| **Tokens** | Tokens for AI clients and scripts, and the AI clients that signed in |
| **Clients** | AI clients that sign in with OAuth, when that is switched on |
| **Activity** | How calls went, and the audit trail |
| **Approvals** | Calls that wait for a person's decision |
| **Connections** | Each person's own accounts on servers |

People who are not admins see fewer pages: only the ones their roles allow.

!!! note "Limit sign-in to your GitHub organisation"
    Only people you invite can sign in. To also require membership of your
    GitHub organisations, set `PRIVO_GITHUB_ORG_IDS`. And turn on two-factor
    authentication at GitHub for everyone who approves calls: Privo cannot
    require it.

Next: [Your first server and AI client](first-call.md).
