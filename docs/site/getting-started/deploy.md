# Deploy Privo

Privo is one program that keeps its state in one data folder. To run it for a
team, wherever it runs, you need the same four things. This page lists them,
then sends you to the steps for your platform.

These steps match Privo 0.19.0 and later.

## What a team install needs

| You need | Why |
| --- | --- |
| A public HTTPS address, such as `https://privo.example.com` | AI clients, GitHub sign-in and OAuth providers reach Privo there. It is `PRIVO_PUBLIC_URL`. Privo itself serves plain HTTP: the platform or a proxy in front of it provides HTTPS |
| A master key: 32 random bytes, base64 | It protects every credential Privo stores. Make one with `openssl rand -base64 32` and give it to Privo as a file, `PRIVO_MASTER_KEY_FILE` |
| A persistent data folder (`/data` in the container) | The database lives there. Without a persistent volume, every redeploy starts again from nothing |
| A GitHub OAuth App | People sign in to the admin site with their GitHub account. You create it in [First setup and sign-in](first-setup.md) |

The settings are the same on every platform; each one is in the
[configuration reference](../installation/configuration.md).

!!! warning "Back up the master key"
    Keep a copy of the master key in your password manager, and keep it out
    of the data folder and its backups. Without it, every stored credential
    is unreadable and no backup can be restored.

Run exactly one Privo for each data folder: a second one refuses to start
while the first holds the database.

## Where to run it

### Coolify

Coolify is the deployment Privo supports today. Follow
[Deploy on Coolify](../installation/coolify.md): it covers the application,
the environment, the master key as a file mount, and the domain. You can skip
its first-token and GitHub sections for now: [First setup and
sign-in](first-setup.md) walks through them.

### Other ways to deploy

More ways to run Privo, with packaged releases, are being decided (ADR 0030).
They will be listed here, and the rest of Getting Started stays the same:
whatever runs Privo, the next steps happen in the browser.

!!! note "Just trying it out?"
    To evaluate Privo on your own machine, [build and run it
    locally](../installation/build-and-run.md). GitHub sign-in also works on
    `http://localhost`, so you can follow the next pages there too.

## Check that it runs

Open these addresses on your Privo:

- `https://privo.example.com/livez` answers `ok` while the process runs.
- `https://privo.example.com/readyz` answers once the database is ready.
- `https://privo.example.com/version` shows the version.

A missing or wrong master key never gets this far: Privo stops at start, and
its log says what is wrong with the key.

Until GitHub sign-in is set up, `/admin` answers 503 and names the settings
that are missing. That is the next step.

Next: [First setup and sign-in](first-setup.md).
