// privo.club: the landing page and the Privo Cloud waitlist.
// Zero dependencies: Node's built-in http and sqlite modules only.

import http from "node:http";
import { DatabaseSync } from "node:sqlite";
import { createHash, randomBytes, timingSafeEqual } from "node:crypto";
import { readFile, stat, mkdir } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";

const ROOT = path.dirname(fileURLToPath(import.meta.url));
const PUBLIC_DIR = path.join(ROOT, "public");
const PORT = Number(process.env.PORT || 3000);
const DATA_DIR = process.env.DATA_DIR || path.join(ROOT, "data");
const ADMIN_TOKEN = process.env.ADMIN_TOKEN || "";
// Only show "N people are on the waitlist" once the list is big enough not to look empty.
const PUBLIC_COUNT_MIN = Number(process.env.PUBLIC_COUNT_MIN || 50);

// ---------- database ----------

await mkdir(DATA_DIR, { recursive: true });
const db = new DatabaseSync(path.join(DATA_DIR, "waitlist.db"));
// edit_token, role, clients, tools, pain, pay, note and updated_at (and welcome_sent_at, added later)
// belonged to the post-signup survey and the welcome email, both removed. The columns stay so
// existing databases keep what was collected; nothing writes them now, and the CSV export still
// includes them.
db.exec(`
  PRAGMA journal_mode = WAL;
  CREATE TABLE IF NOT EXISTS signups (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    email         TEXT NOT NULL UNIQUE,
    ref_code      TEXT NOT NULL UNIQUE,
    edit_token    TEXT NOT NULL,
    referred_by   TEXT,
    utm_source    TEXT,
    utm_medium    TEXT,
    utm_campaign  TEXT,
    referrer_host TEXT,
    role          TEXT,
    clients       TEXT,
    tools         TEXT,
    pain          TEXT,
    pay           TEXT,
    note          TEXT,
    created_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    updated_at    TEXT
  );
  CREATE INDEX IF NOT EXISTS idx_signups_referred_by ON signups(referred_by);
`);
const q = {
  byEmail: db.prepare("SELECT * FROM signups WHERE email = ?"),
  refExists: db.prepare("SELECT 1 FROM signups WHERE ref_code = ?"),
  insert: db.prepare(`INSERT INTO signups
    (email, ref_code, edit_token, referred_by, utm_source, utm_medium, utm_campaign, referrer_host)
    VALUES (?, ?, ?, ?, ?, ?, ?, ?)`),
  position: db.prepare("SELECT COUNT(*) AS n FROM signups WHERE id <= ?"),
  referrals: db.prepare("SELECT COUNT(*) AS n FROM signups WHERE referred_by = ?"),
  count: db.prepare("SELECT COUNT(*) AS n FROM signups"),
  all: db.prepare("SELECT * FROM signups ORDER BY id"),
};

// ---------- helpers ----------

const EMAIL_RE = /^[^\s@]+@[^\s@]+\.[^\s@]{2,}$/;
const clip = (v, n = 120) => (typeof v === "string" ? v.trim().slice(0, n) || null : null);

function newRefCode() {
  const alphabet = "abcdefghjkmnpqrstuvwxyz23456789";
  for (;;) {
    const bytes = randomBytes(7);
    const code = Array.from(bytes, (b) => alphabet[b % alphabet.length]).join("");
    if (!q.refExists.get(code)) return code;
  }
}

function standing(row) {
  return {
    position: q.position.get(row.id).n,
    refCode: row.ref_code,
    referrals: q.referrals.get(row.ref_code).n,
  };
}

function send(res, status, body, headers = {}) {
  const isString = typeof body === "string";
  res.writeHead(status, {
    "Content-Type": isString ? "text/plain; charset=utf-8" : "application/json; charset=utf-8",
    "Cache-Control": "no-store",
    ...headers,
  });
  res.end(isString ? body : JSON.stringify(body));
}

async function readJson(req, limit = 8 * 1024) {
  let size = 0;
  const chunks = [];
  for await (const chunk of req) {
    size += chunk.length;
    if (size > limit) throw Object.assign(new Error("Payload too large"), { status: 413 });
    chunks.push(chunk);
  }
  try {
    return JSON.parse(Buffer.concat(chunks).toString("utf8") || "{}");
  } catch {
    throw Object.assign(new Error("Invalid JSON"), { status: 400 });
  }
}

// In-memory rate limit. IPs are never written to disk.
// Behind Cloudflare, CF-Connecting-IP is the visitor (Cloudflare overwrites any client-sent value).
// Otherwise the real client IP is the last X-Forwarded-For entry, added by Coolify's proxy
// (earlier entries are whatever the client sent).
const hits = new Map();
function rateLimited(req, bucket, max, windowMs = 10 * 60 * 1000) {
  const forwarded = (req.headers["x-forwarded-for"] || "").split(",").map((s) => s.trim()).filter(Boolean);
  const ip = req.headers["cf-connecting-ip"] || forwarded.at(-1) || req.socket.remoteAddress;
  const key = `${bucket}:${ip}`;
  const now = Date.now();
  const recent = (hits.get(key) || []).filter((t) => now - t < windowMs);
  recent.push(now);
  hits.set(key, recent);
  return recent.length > max;
}
setInterval(() => {
  const cutoff = Date.now() - 10 * 60 * 1000;
  for (const [ip, times] of hits) if (times.every((t) => t < cutoff)) hits.delete(ip);
}, 60 * 1000).unref();

function isAdmin(req) {
  if (!ADMIN_TOKEN) return false;
  const given = Buffer.from((req.headers.authorization || "").replace(/^Bearer\s+/i, ""));
  const want = Buffer.from(ADMIN_TOKEN);
  return given.length === want.length && timingSafeEqual(given, want);
}

// ---------- API handlers ----------

async function joinWaitlist(req, res) {
  if (rateLimited(req, "join", 30)) return send(res, 429, { error: "Too many attempts. Try again in a few minutes." });
  const body = await readJson(req);

  // Honeypot: real people never see or fill this field.
  if (body.website) return send(res, 200, { ok: true, position: 0, refCode: "", referrals: 0 });

  const email = typeof body.email === "string" ? body.email.trim().toLowerCase() : "";
  if (email.length > 254 || !EMAIL_RE.test(email)) {
    return send(res, 400, { error: "That email doesn't look right." });
  }

  const existing = q.byEmail.get(email);
  if (existing) return send(res, 200, { ok: true, alreadyJoined: true, ...standing(existing) });

  const ref = clip(body.ref, 16);
  const referredBy = ref && q.refExists.get(ref) ? ref : null;
  let referrerHost = null;
  try {
    referrerHost = body.referrer ? new URL(body.referrer).hostname.slice(0, 120) : null;
  } catch {}

  // edit_token is NOT NULL in existing databases; it was the survey's key and is unused now.
  q.insert.run(
    email, newRefCode(), "", referredBy,
    clip(body.utm_source), clip(body.utm_medium), clip(body.utm_campaign), referrerHost,
  );
  send(res, 201, { ok: true, alreadyJoined: false, ...standing(q.byEmail.get(email)) });
}

function publicCount(_req, res) {
  const n = q.count.get().n;
  send(res, 200, { count: n >= PUBLIC_COUNT_MIN ? n : null });
}

function adminStats(req, res) {
  if (!isAdmin(req)) return send(res, 401, { error: "Unauthorized" });
  const rows = q.all.all();
  const now = Date.now();
  const tally = (key) => {
    const out = {};
    for (const r of rows) if (r[key]) out[r[key]] = (out[r[key]] || 0) + 1;
    return Object.fromEntries(Object.entries(out).sort((a, b) => b[1] - a[1]));
  };
  const daily = {};
  for (const r of rows) daily[r.created_at.slice(0, 10)] = (daily[r.created_at.slice(0, 10)] || 0) + 1;
  const referrers = {};
  for (const r of rows) if (r.referred_by) referrers[r.referred_by] = (referrers[r.referred_by] || 0) + 1;
  const emailByRef = Object.fromEntries(rows.map((r) => [r.ref_code, r.email]));

  send(res, 200, {
    total: rows.length,
    last24h: rows.filter((r) => now - Date.parse(r.created_at) < 864e5).length,
    last7d: rows.filter((r) => now - Date.parse(r.created_at) < 7 * 864e5).length,
    referred: rows.filter((r) => r.referred_by).length,
    daily,
    sources: tally("utm_source"),
    referrerHosts: tally("referrer_host"),
    topReferrers: Object.entries(referrers)
      .sort((a, b) => b[1] - a[1])
      .slice(0, 10)
      .map(([code, n]) => ({ email: emailByRef[code], referrals: n })),
  });
}

function adminExport(req, res) {
  if (!isAdmin(req)) return send(res, 401, { error: "Unauthorized" });
  // Every column the table has (so survey answers collected before the survey was removed are not
  // lost), except edit_token.
  const cols = db.prepare("PRAGMA table_info(signups)").all().map((c) => c.name).filter((c) => c !== "edit_token");
  const esc = (v) => {
    if (v == null) return "";
    // Neutralise spreadsheet formulas in user-supplied text.
    let s = String(v).replace(/^([=+\-@\t\r])/, "'$1");
    return /[",\n]/.test(s) ? `"${s.replace(/"/g, '""')}"` : s;
  };
  const csv = [cols.join(","), ...q.all.all().map((r) => cols.map((c) => esc(r[c])).join(","))].join("\n");
  send(res, 200, csv, {
    "Content-Type": "text/csv; charset=utf-8",
    "Content-Disposition": `attachment; filename="privo-waitlist-${new Date().toISOString().slice(0, 10)}.csv"`,
  });
}

// ---------- static files ----------

const MIME = {
  ".html": "text/html; charset=utf-8",
  ".css": "text/css; charset=utf-8",
  ".js": "text/javascript; charset=utf-8",
  ".svg": "image/svg+xml",
  ".png": "image/png",
  ".ico": "image/x-icon",
  ".woff2": "font/woff2",
  ".txt": "text/plain; charset=utf-8",
  ".xml": "application/xml; charset=utf-8",
  ".webmanifest": "application/manifest+json",
};

// Fingerprint of the page assets. HTML references them as "?v=__V__", so every deploy that
// changes an asset gets fresh URLs and Cloudflare/browser caches can never serve stale CSS or JS.
const ASSET_VERSION = createHash("sha1")
  .update(await Promise.all(["styles.css", "app.js", "admin.css", "admin.js", "favicon.svg", "og.png"]
    .map((f) => readFile(path.join(PUBLIC_DIR, f)))).then((bufs) => Buffer.concat(bufs)))
  .digest("hex").slice(0, 10);

async function serveStatic(req, res, pathname, status = 200) {
  if (pathname.endsWith("/")) pathname += "index.html";
  const file = path.normalize(path.join(PUBLIC_DIR, decodeURIComponent(pathname)));
  if (!file.startsWith(PUBLIC_DIR + path.sep)) return send(res, 403, "Forbidden");
  try {
    const info = await stat(file);
    if (!info.isFile()) throw new Error("not a file");
    const ext = path.extname(file);
    let body = await readFile(file);
    let cache;
    if (ext === ".html") {
      body = Buffer.from(body.toString("utf8").replaceAll("__V__", ASSET_VERSION));
      cache = "no-cache"; // always revalidate, so new asset URLs reach visitors right after a deploy
    } else if (ext === ".woff2" || new URL(req.url, "http://localhost").searchParams.has("v")) {
      cache = "public, max-age=31536000, immutable";
    } else {
      cache = "public, max-age=300";
    }
    res.writeHead(status, {
      "Content-Type": MIME[ext] || "application/octet-stream",
      "Content-Length": body.length,
      "Cache-Control": cache,
    });
    if (req.method === "HEAD") return res.end();
    res.end(body);
  } catch {
    if (pathname !== "/404.html") return serveStatic(req, res, "/404.html", 404);
    send(res, 404, "Not found");
  }
}

// ---------- router ----------

const SECURITY_HEADERS = {
  "Content-Security-Policy":
    "default-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; font-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'self'; form-action 'self'",
  "X-Content-Type-Options": "nosniff",
  "Referrer-Policy": "strict-origin-when-cross-origin",
  "Permissions-Policy": "camera=(), microphone=(), geolocation=(), interest-cohort=()",
  "Strict-Transport-Security": "max-age=31536000; includeSubDomains",
};

const routes = {
  "POST /api/waitlist": joinWaitlist,
  "GET /api/waitlist/count": publicCount,
  "GET /api/admin/stats": adminStats,
  "GET /api/admin/export.csv": adminExport,
  "GET /healthz": (_req, res) => send(res, 200, "ok"),
};

const server = http.createServer(async (req, res) => {
  for (const [k, v] of Object.entries(SECURITY_HEADERS)) res.setHeader(k, v);
  const { pathname } = new URL(req.url, "http://localhost");
  const handler = routes[`${req.method} ${pathname}`];
  try {
    if (handler) return await handler(req, res);
    if (pathname.startsWith("/api/")) return send(res, 404, { error: "Not found" });
    if (req.method !== "GET" && req.method !== "HEAD") return send(res, 405, "Method not allowed");
    if (pathname === "/admin") return await serveStatic(req, res, "/admin.html");
    await serveStatic(req, res, pathname);
  } catch (err) {
    const status = err.status || 500;
    if (status === 500) console.error(err);
    if (!res.headersSent) send(res, status, { error: status === 500 ? "Something went wrong." : err.message });
  }
});

server.listen(PORT, () => {
  console.log(`privo.club listening on http://localhost:${PORT}`);
  if (!ADMIN_TOKEN) console.warn("ADMIN_TOKEN is not set — the admin dashboard is disabled.");
});

for (const sig of ["SIGINT", "SIGTERM"]) {
  process.on(sig, () => server.close(() => { db.close(); process.exit(0); }));
}
