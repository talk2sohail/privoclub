# Privo Club — landing page + waitlist

The private club for MCP. This repo is the pre-launch landing page for **privo.club**: a waitlist with referral links, a short post-signup survey to validate the idea, and a private dashboard to read the results.

- One dependency (`nodemailer`, for the welcome email); everything else is Node's built-in `http` and `sqlite` modules
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
   - `SMTP_PASS`: the support@privo.club mailbox password; turns on welcome emails (see below)
   - Optional: `SMTP_HOST` (default `mail.spacemail.com`), `SMTP_PORT` (default `465`; use `587` if your host blocks 465), `SMTP_USER` (default `support@privo.club`), `SMTP_MAX_PER_MINUTE` (default `20`), `MAIL_FROM`, `MAIL_REPLY_TO` (default: `SMTP_USER`), `PUBLIC_URL` (default `https://privo.club`)
6. **Persistent storage:** add a volume mounted at **`/data`**. This is where `waitlist.db` lives. Without it, every redeploy wipes your signups.
7. Point the domain's DNS `A` record at your Coolify server, then deploy.

Health check: `GET /healthz` (already configured in the Dockerfile).

## Welcome emails

Sent from the **support@privo.club** Spacemail mailbox over SMTP to each **new** signup (never to a repeat signup, so the form can't be used to spam an address). Sent copies appear in the mailbox's Sent folder and replies land in its inbox.

- Set `SMTP_PASS` in Coolify to the mailbox password. The container logs `[mail] SMTP ready` on startup if the login works, or the exact error if it doesn't.
- Sending is throttled to `SMTP_MAX_PER_MINUTE` (default 20) so a launch-day spike stays under Spacemail's per-mailbox limits. Emails queue in memory while throttled.
- For good inbox placement the domain needs SPF, DKIM and DMARC records for Spacemail (see DNS below).
- Signups that arrived while email was off (or failed) can be caught up with **Send missing welcome emails** on the dashboard.

Without `SMTP_PASS` the site works normally and simply skips the email. Delivery failures are logged and never block a signup.

## DNS (Cloudflare)

| Type | Name | Content | Proxy |
|---|---|---|---|
| A | `@` | your Coolify server's IP | Proxied |
| CNAME | `www` | `privo.club` | Proxied |
| MX | `@` | `mx1.spacemail.com` (priority 0) | DNS only |
| MX | `@` | `mx2.spacemail.com` (priority 0) | DNS only |
| TXT | `@` | `v=spf1 include:spf.spacemail.com ~all` | DNS only |
| TXT | `spacemail._domainkey` | the DKIM key from Spacemail | DNS only |
| TXT | `_dmarc` | `v=DMARC1; p=none; rua=mailto:support@privo.club` | DNS only |

Use the same SSL/TLS mode as your other Coolify sites (Full or Full (strict)); "Flexible" causes redirect loops.

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
