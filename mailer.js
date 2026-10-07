// Welcome email for new waitlist members, sent through Resend's HTTP API.
// Without RESEND_API_KEY set, emails are skipped and logged instead.

const RESEND_API_KEY = process.env.RESEND_API_KEY || "";
const MAIL_FROM = process.env.MAIL_FROM || "Privo Club <support@privo.club>";
const MAIL_REPLY_TO = process.env.MAIL_REPLY_TO || "support@privo.club";
// Links in emails always use this, never the request's Host header.
const PUBLIC_URL = (process.env.PUBLIC_URL || "https://privo.club").replace(/\/$/, "");

export const mailEnabled = Boolean(RESEND_API_KEY);

// The useful part: something a member can act on today, before we launch.
const CHECKS = [
  ["Read the tool descriptions, not just the README.",
    "Your AI sees each tool’s description as instructions. Skim them for anything like “ignore previous instructions”, “don’t tell the user” or requests to read unrelated files."],
  ["Pin the version.",
    "Install a specific release (for example <code style="white-space:nowrap;">npx -y some-server@1.4.2</code>) rather than whatever is latest, so the server can’t change underneath you."],
  ["Give it the narrowest token you can.",
    "Use fine-grained, read-only tokens scoped to just the repos, folders or workspaces it needs. You can widen access later; you can’t un-leak a token."],
  ["Keep secrets out of shared config.",
    "Config files get synced, screenshotted and committed. Load keys from environment variables or a secrets manager instead of pasting them into the file."],
  ["Keep approvals on for anything that sends, pays or deletes.",
    "Most MCP clients can ask before a tool runs. Auto-approve reads if you like, but leave confirmation on for email, payments and deletes."],
];

function html({ position, shareUrl }) {
  const checks = CHECKS.map(([title, body], i) => `
    <tr><td style="padding:0 0 18px 0;">
      <table role="presentation" cellpadding="0" cellspacing="0" border="0" width="100%"><tr>
        <td valign="top" width="34" style="font-family:Georgia,serif;font-style:italic;font-size:22px;line-height:24px;color:#c9a66b;">${i + 1}</td>
        <td valign="top" style="font-family:-apple-system,'Segoe UI',Helvetica,Arial,sans-serif;font-size:15px;line-height:23px;color:#d9d3c5;">
          <strong style="color:#efe9dc;">${title}</strong><br>${body}
        </td>
      </tr></table>
    </td></tr>`).join("");

  return `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<meta name="color-scheme" content="dark light"><meta name="supported-color-schemes" content="dark light">
<title>Welcome to Privo Club</title></head>
<body style="margin:0;padding:0;background:#0c0e0b;">
<div style="display:none;max-height:0;overflow:hidden;">You’re No. ${position}. Plus five checks to run before you install any MCP server.</div>
<table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0" bgcolor="#0c0e0b" style="background:#0c0e0b;">
<tr><td align="center" style="padding:32px 16px;">
  <table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0" style="max-width:560px;">
    <tr><td style="padding:0 4px 24px 4px;font-family:Georgia,serif;font-size:24px;color:#efe9dc;">Privo<span style="color:#c9a66b;">.</span>club</td></tr>
    <tr><td bgcolor="#161a14" style="background:#161a14;border:1px solid #2a2e26;border-radius:18px;padding:32px 28px;">
      <p style="margin:0 0 6px 0;font-family:'SF Mono',Menlo,Consolas,monospace;font-size:12px;letter-spacing:2px;text-transform:uppercase;color:#c9a66b;">Founding member · No. ${position}</p>
      <h1 style="margin:0 0 16px 0;font-family:Georgia,serif;font-weight:normal;font-size:30px;line-height:36px;color:#efe9dc;">Your seat is saved.</h1>
      <p style="margin:0 0 16px 0;font-family:-apple-system,'Segoe UI',Helvetica,Arial,sans-serif;font-size:15px;line-height:24px;color:#d9d3c5;">
        Thanks for joining Privo Club. We’re building a private home for MCP: vetted servers behind one endpoint, your keys in a vault, and every call on the record. Invites go out to founding members in small groups, in list order.
      </p>
      <p style="margin:0 0 28px 0;font-family:-apple-system,'Segoe UI',Helvetica,Arial,sans-serif;font-size:15px;line-height:24px;color:#d9d3c5;">
        You don’t have to wait to get something out of it, though. Here’s the checklist we use ourselves.
      </p>

      <h2 style="margin:0 0 18px 0;font-family:Georgia,serif;font-weight:normal;font-size:22px;line-height:28px;color:#e6c891;">Five checks before you install any MCP server</h2>
      <table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0">${checks}</table>

      <table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0" style="margin-top:12px;border-top:1px solid #2a2e26;">
        <tr><td style="padding-top:24px;font-family:-apple-system,'Segoe UI',Helvetica,Arial,sans-serif;font-size:15px;line-height:24px;color:#d9d3c5;">
          <strong style="color:#efe9dc;">Want an earlier invite?</strong> Members who bring friends move to the front of the list. Your personal link:
        </td></tr>
        <tr><td style="padding:14px 0 6px 0;">
          <a href="${shareUrl}" style="display:inline-block;padding:13px 22px;border-radius:999px;background:#c9a66b;color:#1a1508;font-family:-apple-system,'Segoe UI',Helvetica,Arial,sans-serif;font-size:15px;font-weight:600;text-decoration:none;">Share your invite link</a>
        </td></tr>
        <tr><td style="padding:4px 0 0 0;font-family:'SF Mono',Menlo,Consolas,monospace;font-size:13px;color:#9a9587;word-break:break-all;">${shareUrl}</td></tr>
        <tr><td style="padding-top:24px;font-family:-apple-system,'Segoe UI',Helvetica,Arial,sans-serif;font-size:15px;line-height:24px;color:#d9d3c5;">
          <strong style="color:#efe9dc;">One question:</strong> which tool would you connect to your AI first, and what’s stopped you so far? Just hit reply. A real person reads every answer, and it decides which servers we vet first.
        </td></tr>
        <tr><td style="padding-top:20px;font-family:-apple-system,'Segoe UI',Helvetica,Arial,sans-serif;font-size:15px;line-height:24px;color:#d9d3c5;">
          See you inside,<br>The Privo Club team
        </td></tr>
      </table>
    </td></tr>
    <tr><td style="padding:20px 4px 0 4px;font-family:-apple-system,'Segoe UI',Helvetica,Arial,sans-serif;font-size:12px;line-height:19px;color:#6c695f;">
      You’re getting this because this address joined the waitlist at <a href="${PUBLIC_URL}" style="color:#9a9587;">privo.club</a>. Reply “remove” and we’ll delete it. No trackers in this email.
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
  if (!mailEnabled) {
    console.log(`[mail] RESEND_API_KEY not set; skipped welcome email for signup No. ${position}`);
    return false;
  }
  const { subject, html, text } = renderWelcome({ position, refCode });
  for (let attempt = 1; attempt <= 3; attempt++) {
    try {
      const res = await fetch("https://api.resend.com/emails", {
        method: "POST",
        headers: { Authorization: `Bearer ${RESEND_API_KEY}`, "Content-Type": "application/json" },
        body: JSON.stringify({
          from: MAIL_FROM,
          to: [email],
          reply_to: MAIL_REPLY_TO,
          subject,
          html,
          text,
          headers: { "List-Unsubscribe": `<mailto:${MAIL_REPLY_TO}?subject=remove>` },
        }),
        signal: AbortSignal.timeout(10_000),
      });
      if (res.ok) return true;
      const detail = (await res.text()).slice(0, 300);
      // 4xx other than rate limiting won't succeed on retry (bad key, unverified domain, invalid address).
      if (res.status < 500 && res.status !== 429) {
        console.error(`[mail] Resend rejected welcome email (${res.status}): ${detail}`);
        return false;
      }
      console.error(`[mail] Resend error ${res.status} on attempt ${attempt}: ${detail}`);
    } catch (err) {
      console.error(`[mail] Welcome email attempt ${attempt} failed: ${err.message}`);
    }
    await new Promise((r) => setTimeout(r, attempt * 2000));
  }
  return false;
}
