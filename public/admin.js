// Waitlist dashboard. The token lives in sessionStorage only for this tab.
(() => {
  const LABELS = {
    role: { developer: "Developer / engineer", founder: "Founder or team lead", professional: "Non-technical professional", curious: "Just curious" },
    clients: { claude: "Claude", chatgpt: "ChatGPT", cursor: "Cursor", vscode: "VS Code / Copilot", windsurf: "Windsurf", other: "Other" },
    tools: { github: "GitHub", gmail: "Gmail", gdrive: "Google Drive", calendar: "Calendar", slack: "Slack", notion: "Notion", linear: "Linear", jira: "Jira", postgres: "Postgres", figma: "Figma", stripe: "Stripe", other: "Other" },
    pain: { trust: "Which servers to trust", keys: "Managing API keys", hosting: "Hosting servers", discovery: "Finding good servers", visibility: "Seeing what AI did", new: "Hasn’t tried MCP yet" },
    pay: { yes: "Yes", maybe: "Maybe", no: "No" },
  };
  const PANELS = [
    ["pain", "Biggest headache (problem validation)"],
    ["pay", "Would pay ~$15/mo"],
    ["role", "Who they are"],
    ["clients", "AI apps they use"],
    ["tools", "What they’d connect first"],
    ["sources", "UTM source"],
    ["referrerHosts", "Referring sites"],
  ];

  const $ = (s) => document.querySelector(s);
  const esc = (s) => String(s).replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[c]);
  const pct = (n, d) => (d ? Math.round((n / d) * 100) : 0);
  const getToken = () => { try { return sessionStorage.getItem("privo.admin"); } catch { return null; } };
  const setToken = (t) => { try { t ? sessionStorage.setItem("privo.admin", t) : sessionStorage.removeItem("privo.admin"); } catch {} };

  async function api(path, method = "GET") {
    const res = await fetch(path, { method, headers: { Authorization: `Bearer ${getToken()}` } });
    if (res.status === 401) throw new Error("That token didn’t work.");
    if (!res.ok) throw new Error("Couldn’t load the dashboard.");
    return res;
  }

  function bars(obj, labels = {}) {
    const entries = Object.entries(obj || {});
    if (!entries.length) return `<p class="empty">No answers yet.</p>`;
    const max = Math.max(...entries.map(([, v]) => v));
    const total = entries.reduce((s, [, v]) => s + v, 0);
    return `<ul class="bars">${entries.map(([k, v]) => `
      <li><span class="label">${esc(labels[k] || k)}</span><span class="val">${v} · ${pct(v, total)}%</span>
      <span class="track"><span class="fill" data-w="${pct(v, max)}"></span></span></li>`).join("")}</ul>`;
  }

  function render(s) {
    $("[data-kpis]").innerHTML = [
      ["Total signups", s.total, ""],
      ["Last 24 hours", s.last24h, ""],
      ["Last 7 days", s.last7d, ""],
      ["Answered survey", s.surveyed, `${pct(s.surveyed, s.total)}% of signups`],
      ["Came via referral", s.referred, `${pct(s.referred, s.total)}% of signups`],
      ["Would pay (yes)", s.pay.yes || 0, `${pct(s.pay.yes || 0, s.surveyed)}% of respondents`],
      ["Welcome emails", s.welcomed, s.mailEnabled ? `${pct(s.welcomed, s.total)}% of signups` : "Off: SMTP_PASS not set"],
    ].map(([label, n, note]) => `<div class="kpi"><span>${label}</span><strong>${n}</strong>${note ? `<small>${note}</small>` : ""}</div>`).join("");

    // daily bars, filling in empty days
    const days = Object.keys(s.daily).sort();
    const series = [];
    if (days.length) {
      for (let d = new Date(days[0]); d <= new Date(days.at(-1)); d.setUTCDate(d.getUTCDate() + 1)) {
        const key = d.toISOString().slice(0, 10);
        series.push([key, s.daily[key] || 0]);
      }
    }
    const peak = Math.max(1, ...series.map(([, n]) => n));
    $("[data-days]").innerHTML = series.length
      ? series.map(([d, n]) => `<div class="day" title="${d}: ${n}" data-h="${Math.round((n / peak) * 100)}"></div>`).join("") +
        `<span class="day-label">${series[0][0]}</span><span class="day-label end">${series.at(-1)[0]}</span>`
      : `<p class="empty">No signups yet.</p>`;

    $("[data-panels]").innerHTML = PANELS.map(([key, title]) =>
      `<div class="panel"><h2>${title}</h2>${bars(s[key], LABELS[key])}</div>`).join("") +
      `<div class="panel"><h2>Top referrers</h2>${s.topReferrers.length
        ? `<ul class="bars">${s.topReferrers.map((r) => `<li><span class="label">${esc(r.email)}</span><span class="val">${r.referrals}</span></li>`).join("")}</ul>`
        : `<p class="empty">No referrals yet.</p>`}</div>`;

    $("[data-notes]").innerHTML = s.notes.length
      ? s.notes.map((n) => `<li><p>${esc(n.note)}</p><small>${esc(n.email)} · ${esc(n.at.slice(0, 16).replace("T", " "))}</small></li>`).join("")
      : `<li class="empty">No free-text answers yet.</li>`;

    // CSP blocks inline style attributes, so size bars through the CSSOM.
    document.querySelectorAll("[data-w]").forEach((el) => (el.style.width = `${el.dataset.w}%`));
    document.querySelectorAll("[data-h]").forEach((el) => (el.style.height = `${el.dataset.h}%`));
  }

  async function load() {
    const stats = await (await api("/api/admin/stats")).json();
    $("[data-login]").hidden = true;
    $("[data-dash]").hidden = false;
    $("[data-export]").hidden = false;
    $("[data-logout]").hidden = false;
    render(stats);
    const missing = stats.total - stats.welcomed;
    const sendBtn = $("[data-send-pending]");
    sendBtn.hidden = !(stats.mailEnabled && missing > 0);
    sendBtn.textContent = `Send ${missing} missing welcome email${missing === 1 ? "" : "s"}`;
  }

  $("[data-send-pending]").addEventListener("click", async (e) => {
    e.target.disabled = true;
    try {
      const { queued } = await (await api("/api/admin/send-pending", "POST")).json();
      e.target.textContent = `Sending ${queued}… refresh in a minute`;
    } catch (err) {
      e.target.textContent = err.message;
    }
  });

  $("[data-login]").addEventListener("submit", async (e) => {
    e.preventDefault();
    const msg = e.target.querySelector(".join-msg");
    setToken(e.target.token.value.trim());
    try { msg.textContent = ""; await load(); } catch (err) { setToken(null); msg.textContent = err.message; }
  });

  $("[data-logout]").addEventListener("click", () => { setToken(null); location.reload(); });

  $("[data-export]").addEventListener("click", async () => {
    const blob = await (await api("/api/admin/export.csv")).blob();
    const a = Object.assign(document.createElement("a"), {
      href: URL.createObjectURL(blob),
      download: `privo-waitlist-${new Date().toISOString().slice(0, 10)}.csv`,
    });
    a.click();
    URL.revokeObjectURL(a.href);
  });

  if (getToken()) load().catch(() => setToken(null));
})();
