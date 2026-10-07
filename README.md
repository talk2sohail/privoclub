# Privo Club — landing page + waitlist

The private club for MCP. This repo is the pre-launch landing page for **privo.club**: a waitlist with referral links, a short post-signup survey to validate the idea, and a private dashboard to read the results.

- Zero dependencies: Node's built-in `http` and `sqlite` modules only
- No third-party requests: fonts are self-hosted, no analytics, strict CSP
- IP addresses are used for rate limiting in memory and never stored
- New members get a welcome email from support@privo.club with their list number, referral link and a 5-point MCP safety checklist

## Run locally

Requires Node 22.13+.

```sh
cp .env.example .env          # then set ADMIN_TOKEN (openssl rand -hex 32)
npm start                     # http://localhost:3000 (or the PORT in .env)
```

- Landing page: `/`
- Dashboard: `/admin` (sign in with `ADMIN_TOKEN`)

## Deploy on Coolify

1. Push this folder to a Git repo (GitHub, GitLab or Gitea).
2. In Coolify: **New Resource → Application → your repo**, build pack **Dockerfile**.
3. **Domains:** `https://privo.club` (and `https://www.privo.club` if you want it). Coolify issues the TLS certificate.
4. **Ports exposed:** `3000`.
5. **Environment variables:**
   - `ADMIN_TOKEN`: long random string (`openssl rand -hex 32`)
   - `PUBLIC_COUNT_MIN` (optional, default `50`): the page only shows "N people have asked for a seat" once the list reaches this size
   - `RESEND_API_KEY`: turns on welcome emails (see below)
   - `MAIL_FROM` (optional, default `Privo Club <support@privo.club>`), `MAIL_REPLY_TO` (default `support@privo.club`), `PUBLIC_URL` (default `https://privo.club`)
6. **Persistent storage:** add a volume mounted at **`/data`**. This is where `waitlist.db` lives. Without it, every redeploy wipes your signups.
7. Point the domain's DNS `A` record at your Coolify server, then deploy.

Health check: `GET /healthz` (already configured in the Dockerfile).

## Welcome emails

Sent through [Resend](https://resend.com) to each **new** signup (never to a repeat signup, so the form can't be used to spam an address).

1. Create a Resend account and add the domain `privo.club`.
2. Add the DNS records Resend shows you (SPF, DKIM and optionally DMARC) at your domain registrar, and wait for it to verify.
3. Create an API key with "sending access" and set it as `RESEND_API_KEY` in Coolify.
4. Make sure `support@privo.club` can **receive** mail (for example with your registrar's email forwarding or Cloudflare Email Routing). The email invites people to reply.

Without `RESEND_API_KEY` the site works normally and simply skips the email. Delivery failures are logged and never block a signup. The dashboard shows how many welcome emails were sent.

## What you'll learn from the dashboard

| Signal | Where it comes from | What it tells you |
|---|---|---|
| Signups per day | Waitlist form | Is the positioning pulling people in? |
| Biggest headache | Survey | Which problem resonates: trust, keys, hosting, discovery or visibility |
| Would pay ~$15/mo | Survey | Willingness to pay, the strongest validation signal |
| What they'd connect first | Survey | Which servers to vet first |
| AI apps they use | Survey | Which clients to support first |
| Came via referral | `?ref=` links | Whether people care enough to share it |
| UTM source / referring sites | `?utm_source=` and the HTTP referrer | Which channel works (tag every link you post) |

Tag links per channel so you can compare them, e.g. `https://privo.club/?utm_source=reddit`, `?utm_source=x`, `?utm_source=hn`.

Export everything as CSV from the dashboard (spreadsheet formulas in answers are neutralised).

## API

| Method | Path | Auth | Purpose |
|---|---|---|---|
| POST | `/api/waitlist` | — | Join: `{ email, ref?, utm_* }` → position, share code, one-time edit token |
| POST | `/api/waitlist/profile` | edit token | Save survey answers |
| GET | `/api/waitlist/count` | — | Public count, `null` until `PUBLIC_COUNT_MIN` |
| GET | `/api/admin/stats` | Bearer `ADMIN_TOKEN` | Dashboard data |
| GET | `/api/admin/export.csv` | Bearer `ADMIN_TOKEN` | Full export |

Signups are rate-limited to 30 per IP per 10 minutes and include a honeypot field for bots.

## Backups

The whole waitlist is one SQLite file at `/data/waitlist.db`. Copy it off the server occasionally, or use Coolify's scheduled backups for the volume.
