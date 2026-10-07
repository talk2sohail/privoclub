// Welcome email for new waitlist members, sent over SMTP from the support@privo.club
// mailbox (Spacemail). Without SMTP_PASS set, emails are skipped and logged instead.

import nodemailer from "nodemailer";

const SMTP_HOST = process.env.SMTP_HOST || "mail.spacemail.com";
const SMTP_PORT = Number(process.env.SMTP_PORT || 465);
const SMTP_USER = process.env.SMTP_USER || "support@privo.club";
const SMTP_PASS = process.env.SMTP_PASS || "";
// Mailbox providers cap how fast one mailbox can send; stay well under it.
const SMTP_MAX_PER_MINUTE = Number(process.env.SMTP_MAX_PER_MINUTE || 20);
const MAIL_FROM = process.env.MAIL_FROM || `Privo Club <${SMTP_USER}>`;
const MAIL_REPLY_TO = process.env.MAIL_REPLY_TO || SMTP_USER;
// Links in emails always use this, never the request's Host header.
const PUBLIC_URL = (process.env.PUBLIC_URL || "https://privo.club").replace(/\/$/, "");

export const mailEnabled = Boolean(SMTP_PASS);

const transport = mailEnabled
  ? nodemailer.createTransport({
      host: SMTP_HOST,
      port: SMTP_PORT,
      secure: SMTP_PORT === 465, // 465 = TLS from the start; 587 = STARTTLS
      requireTLS: SMTP_PORT !== 465,
      auth: { user: SMTP_USER, pass: SMTP_PASS },
      pool: true,
      maxConnections: 1,
      rateDelta: 60_000,
      rateLimit: SMTP_MAX_PER_MINUTE,
      connectionTimeout: 15_000,
      socketTimeout: 30_000,
    })
  : null;

// Checks the SMTP login once at startup so a bad password shows up in the logs immediately.
export async function verifyMail() {
  if (!transport) return console.warn("[mail] SMTP_PASS is not set; welcome emails are disabled.");
  try {
    await transport.verify();
    console.log(`[mail] SMTP ready: ${SMTP_USER} via ${SMTP_HOST}:${SMTP_PORT}`);
  } catch (err) {
    console.error(`[mail] SMTP login failed (${SMTP_HOST}:${SMTP_PORT} as ${SMTP_USER}): ${err.message}`);
  }
}

// The useful part: something a member can act on today, before we launch.
const CHECKS = [
  ["Read the tool descriptions, not just the README.",
    "Your AI sees each tool’s description as instructions. Skim them for anything like “ignore previous instructions”, “don’t tell the user” or requests to read unrelated files."],
  ["Pin the version.",
    "Install a specific release (for example <code style='white-space:nowrap;'>npx -y some-server@1.4.2</code>) rather than whatever is latest, so the server can’t change underneath you."],
  ["Give it the narrowest token you can.",
    "Use fine-grained, read-only tokens scoped to just the repos, folders or workspaces it needs. You can widen access later; you can’t un-leak a token."],
  ["Keep secrets out of shared config.",
    "Config files get synced, screenshotted and committed. Load keys from environment variables or a secrets manager instead of pasting them into the file."],
  ["Keep approvals on for anything that sends, pays or deletes.",
    "Most MCP clients can ask before a tool runs. Auto-approve reads if you like, but leave confirmation on for email, payments and deletes."],
];

function html({ position, shareUrl }) {
  const font = "-apple-system,BlinkMacSystemFont,'Segoe UI',Helvetica,Arial,sans-serif";
  const colors = ["#ff7a59", "#e9a21b", "#2fb171", "#22b3c5", "#0a2540"];
  const checks = CHECKS.map(([title, body], i) => `
    <tr><td style="padding:0 0 18px 0;">
      <table role="presentation" cellpadding="0" cellspacing="0" border="0" width="100%"><tr>
        <td valign="top" width="40">
          <div style="width:26px;height:26px;border-radius:13px;background:${colors[i]};color:#ffffff;font-family:${font};font-size:13px;font-weight:600;line-height:26px;text-align:center;">${i + 1}</div>
        </td>
        <td valign="top" style="font-family:${font};font-size:15px;line-height:23px;color:#425466;">
          <strong style="color:#0a2540;">${title}</strong><br>${body}
        </td>
      </tr></table>
    </td></tr>`).join("");

  return `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<meta name="color-scheme" content="light"><meta name="supported-color-schemes" content="light">
<title>Welcome to Privo Club</title></head>
<body style="margin:0;padding:0;background:#f6f9fc;">
<div style="display:none;max-height:0;overflow:hidden;">You’re No. ${position}. Plus five checks to run before you install any MCP server.</div>
<table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0" bgcolor="#f6f9fc" style="background:#f6f9fc;">
<tr><td align="center" style="padding:32px 16px;">
  <table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0" style="max-width:560px;">
    <tr><td style="padding:0 4px 20px 4px;">
      <table role="presentation" cellpadding="0" cellspacing="0" border="0"><tr>
        <td width="28" style="width:28px;height:28px;border-radius:8px;background:#ff7a59;background-image:linear-gradient(135deg,#ff7a59,#ffc145);color:#ffffff;font-family:${font};font-size:16px;font-weight:700;text-align:center;line-height:28px;">P</td>
        <td style="padding-left:10px;font-family:${font};font-size:17px;font-weight:600;color:#0a2540;letter-spacing:-0.3px;">Privo Club</td>
      </tr></table>
    </td></tr>
    <tr><td bgcolor="#ffffff" style="background:#ffffff;border-radius:16px;box-shadow:0 2px 5px rgba(50,50,93,.10);overflow:hidden;">
      <div style="height:6px;background:#ff7a59;background-image:linear-gradient(90deg,#ff7a59,#ffc145,#2fb171,#22b3c5);"></div>
      <div style="padding:32px 28px;">
      <p style="margin:0 0 8px 0;font-family:${font};font-size:14px;font-weight:600;color:#11784a;">Founding member No. ${position}</p>
      <h1 style="margin:0 0 16px 0;font-family:${font};font-weight:700;font-size:28px;line-height:34px;letter-spacing:-0.6px;color:#0a2540;">Your seat is saved.</h1>
      <p style="margin:0 0 16px 0;font-family:${font};font-size:15px;line-height:24px;color:#425466;">
        Thanks for joining Privo Club. We’re building a private home for MCP: vetted servers behind one endpoint, your keys in a vault, and every call on the record. Invites go out to founding members in small groups, in list order.
      </p>
      <p style="margin:0 0 28px 0;font-family:${font};font-size:15px;line-height:24px;color:#425466;">
        You don’t have to wait to get something out of it, though. Here’s the checklist we use ourselves.
      </p>

      <h2 style="margin:0 0 18px 0;font-family:${font};font-weight:700;font-size:19px;line-height:26px;letter-spacing:-0.3px;color:#0a2540;">Five checks before you install any MCP server</h2>
      <table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0">${checks}</table>

      <table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0" style="margin-top:8px;border-top:1px solid #e3e8ee;">
        <tr><td style="padding-top:24px;font-family:${font};font-size:15px;line-height:24px;color:#425466;">
          <strong style="color:#0a2540;">Want an earlier invite?</strong> Members who bring friends move to the front of the list. Your personal link:
        </td></tr>
        <tr><td style="padding:14px 0 6px 0;">
          <a href="${shareUrl}" style="display:inline-block;padding:12px 22px;border-radius:999px;background:#0a2540;color:#ffffff;font-family:${font};font-size:15px;font-weight:600;text-decoration:none;">Share your invite link →</a>
        </td></tr>
        <tr><td style="padding:4px 0 0 0;font-family:Menlo,Consolas,monospace;font-size:13px;color:#697386;word-break:break-all;">${shareUrl}</td></tr>
        <tr><td style="padding-top:24px;font-family:${font};font-size:15px;line-height:24px;color:#425466;">
          <strong style="color:#0a2540;">One question:</strong> which tool would you connect to your AI first, and what’s stopped you so far? Just hit reply. A real person reads every answer, and it decides which servers we vet first.
        </td></tr>
        <tr><td style="padding-top:20px;font-family:${font};font-size:15px;line-height:24px;color:#425466;">
          See you inside,<br><strong style="color:#0a2540;">The Privo Club team</strong>
        </td></tr>
      </table>
      </div>
    </td></tr>
    <tr><td style="padding:20px 4px 0 4px;font-family:${font};font-size:12px;line-height:19px;color:#8792a2;">
      You’re getting this because this address joined the waitlist at <a href="${PUBLIC_URL}" style="color:#697386;">privo.club</a>. Reply “remove” and we’ll delete it. No trackers in this email.
    </td></tr>
  </table>
</td></tr></table>
</body></html>`;
}

function text({ position, shareUrl }) {
  const strip = (s) => s.replace(/<[^>]+>/g, "").replace(/&lt;/g, "<").replace(/&gt;/g, ">");
  return [
    `Your seat is saved. You're founding member No. ${position}.`,
    "",
    "Thanks for joining Privo Club. We're building a private home for MCP: vetted servers behind one endpoint, your keys in a vault, and every call on the record. Invites go out to founding members in small groups, in list order.",
    "",
    "You don't have to wait to get something out of it, though. Here's the checklist we use ourselves.",
    "",
    "FIVE CHECKS BEFORE YOU INSTALL ANY MCP SERVER",
    "",
    ...CHECKS.flatMap(([title, body], i) => [`${i + 1}. ${title}`, `   ${strip(body)}`, ""]),
    "WANT AN EARLIER INVITE?",
    `Members who bring friends move to the front of the list. Your personal link: ${shareUrl}`,
    "",
    "ONE QUESTION",
    "Which tool would you connect to your AI first, and what's stopped you so far? Just hit reply. A real person reads every answer.",
    "",
    "See you inside,",
    "The Privo Club team",
    "",
    "--",
    `You're getting this because this address joined the waitlist at ${PUBLIC_URL}. Reply "remove" and we'll delete it.`,
  ].join("\n");
}

export function renderWelcome({ position, refCode }) {
  const shareUrl = `${PUBLIC_URL}/?ref=${encodeURIComponent(refCode)}&utm_source=email`;
  return {
    subject: `You're No. ${position} at Privo Club (plus 5 checks before you install any MCP server)`,
    html: html({ position, shareUrl }),
    text: text({ position, shareUrl }),
  };
}

// Resolves true when sent, false when skipped or failed. Never throws.
export async function sendWelcome({ email, position, refCode }) {
  if (!transport) {
    console.log(`[mail] SMTP not configured; skipped welcome email for signup No. ${position}`);
    return false;
  }
  const { subject, html, text } = renderWelcome({ position, refCode });
  for (let attempt = 1; attempt <= 3; attempt++) {
    try {
      await transport.sendMail({
        from: MAIL_FROM,
        to: email,
        replyTo: MAIL_REPLY_TO,
        subject,
        html,
        text,
        headers: { "List-Unsubscribe": `<mailto:${MAIL_REPLY_TO}?subject=remove>` },
      });
      return true;
    } catch (err) {
      // 5xx replies are permanent (bad password, rejected address); retrying won't help.
      const permanent = err.responseCode >= 500 || err.code === "EAUTH" || err.code === "EENVELOPE";
      console.error(`[mail] Welcome email for No. ${position} failed (attempt ${attempt}${permanent ? ", permanent" : ""}): ${err.message}`);
      if (permanent) return false;
    }
    await new Promise((r) => setTimeout(r, attempt * 5000));
  }
  return false;
}
