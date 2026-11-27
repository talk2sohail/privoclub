// Privo landing page: live gateway schematic, 7-stage request pipeline inspector,
// client configuration tabs, and Privo Cloud waitlist.
// Zero dependencies and strict CSP compliance (no inline scripts or style attributes).
(() => {
  document.documentElement.classList.add("js");
  const reduceMotion = window.matchMedia("(prefers-reduced-motion: reduce)").matches;

  // ---- mobile navigation ----
  const nav = document.querySelector("[data-nav]") || document.querySelector(".nav");
  const navToggle = document.querySelector("[data-nav-toggle]");
  const setMenu = (open) => {
    nav?.classList.toggle("open", open);
    navToggle?.setAttribute("aria-expanded", String(open));
  };
  navToggle?.addEventListener("click", () => setMenu(!nav.classList.contains("open")));
  nav?.querySelectorAll(".nav-links a").forEach((a) => a.addEventListener("click", () => setMenu(false)));
  document.addEventListener("keydown", (e) => {
    if (e.key === "Escape" && nav?.classList.contains("open")) {
      setMenu(false);
      navToggle?.focus();
    }
  });

  // ---- WAI-ARIA tabs keyboard helper ----
  function tabKeys(tabs, activate) {
    tabs.forEach((tab, i) => tab.addEventListener("keydown", (e) => {
      const keys = { ArrowRight: i + 1, ArrowDown: i + 1, ArrowLeft: i - 1, ArrowUp: i - 1, Home: 0, End: tabs.length - 1 };
      if (!(e.key in keys)) return;
      e.preventDefault();
      const next = tabs[(keys[e.key] + tabs.length) % tabs.length];
      activate(next);
      next.focus();
    }));
  }

  // ---- hero gateway schematic: live packet stream ----
  const board = document.querySelector("[data-board]");
  if (board && !reduceMotion) initBoard(board);

  function initBoard(svg) {
    const NS = "http://www.w3.org/2000/svg";
    const layer = svg.querySelector("[data-packets]");
    const core = svg.querySelector("[data-core]");
    const stages = [...svg.querySelectorAll("[data-stage]")];
    const nodes = [...svg.querySelectorAll(".node")]; // 5 clients, then 6 servers
    const rows = document.querySelector("[data-ticker] .tk-rows");
    const CY = [54, 122, 190, 258, 326];
    const SY = [40, 100, 160, 220, 280, 340];
    const inCurve = (c) => `M128 ${CY[c]}C182 ${CY[c]} 184 190 232 190`;
    const route = (c, s) => `${inCurve(c)}L368 190C416 190 418 ${SY[s]} 472 ${SY[s]}`;

    const SCRIPT = [
      { c: 1, s: 0, kind: "ok", who: "sam", tool: "github__create_pull_request", res: "allow · 388 ms" },
      { c: 0, s: 0, kind: "ask", who: "ada", tool: "github__merge_pull_request", res: "approved by grace" },
      { c: 2, s: 2, kind: "ok", who: "lee", tool: "sentry__search_issues", res: "allow · 141 ms" },
      { c: 4, s: 5, kind: "ok", who: "mia", tool: "context7__get-library-docs", res: "allow · 96 ms" },
      { c: 3, s: 3, kind: "deny", who: "kai", tool: "supabase__execute_sql", res: "denied by policy" },
      { c: 2, s: 1, kind: "ok", who: "lee", tool: "linear__update_issue", res: "allow · 175 ms" },
      { c: 1, s: 4, kind: "ask", who: "sam", tool: "stripe__create_refund", res: "approved by grace" },
      { c: 0, s: 1, kind: "ok", who: "ada", tool: "linear__create_issue", res: "allow · 212 ms" },
    ];
    const ease = (k) => (k < 0.5 ? 2 * k * k : 1 - (-2 * k + 2) ** 2 / 2);
    const packets = new Set();
    let next = 0;
    let nextAt = 0;
    let raf = 0;
    let pausedAt = null;

    function log(kind, who, tool, res) {
      if (!rows) return null;
      const li = document.createElement("li");
      li.className = `${kind} new`;
      for (const [tag, cls, text] of [["span", "tk-dot", ""], ["span", "tk-who", who], ["code", "tk-tool", tool], ["span", "tk-res", res]]) {
        const el = document.createElement(tag);
        el.className = cls;
        el.textContent = text;
        li.append(el);
      }
      rows.prepend(li);
      while (rows.children.length > 4) rows.lastElementChild.remove();
      return li;
    }

    function flash(node, cls) {
      if (!node) return;
      node.classList.add(cls);
      setTimeout(() => node.classList.remove(cls), 650);
    }

    function spawn(now) {
      const e = SCRIPT[next++ % SCRIPT.length];
      const path = document.createElementNS(NS, "path");
      path.setAttribute("d", route(e.c, e.s));
      path.setAttribute("class", "route");
      layer.append(path);
      const probe = document.createElementNS(NS, "path");
      probe.setAttribute("d", inCurve(e.c));
      probe.setAttribute("class", "route");
      layer.append(probe);
      const total = path.getTotalLength();
      const enter = probe.getTotalLength() / total;
      probe.remove();
      const exit = enter + 136 / total; // core box width = 136
      const dot = document.createElementNS(NS, "circle");
      dot.setAttribute("r", "4");
      dot.setAttribute("class", `packet ${e.kind}`);
      layer.append(dot);

      const mid = enter + (exit - enter) * 0.55;
      const at = enter + (exit - enter) * 0.62;
      const segs =
        e.kind === "ok" ? [{ from: 0, to: 1, ms: 2600 }]
        : e.kind === "ask" ? [{ from: 0, to: mid, ms: 1300 }, { from: mid, to: mid, ms: 1850, mode: "hold" }, { from: mid, to: 1, ms: 1300 }]
        : [{ from: 0, to: at, ms: 1200 }, { from: at, to: at, ms: 650, mode: "stop" }, { from: at, to: 0, ms: 1050, fade: true }];
      const p = { e, path, dot, total, enter, exit, segs, seg: 0, t0: now, f: 0, mode: null, row: null };
      packets.add(p);
      start(p);
      return e.kind === "ask" ? 4100 : e.kind === "deny" ? 3100 : 1500;
    }

    function start(p) {
      const s = p.segs[p.seg];
      p.mode = s.mode || null;
      if (s.mode === "hold") p.row = log("ask", p.e.who, p.e.tool, "pending approval");
      if (s.mode === "stop") p.row = log("deny", p.e.who, p.e.tool, p.e.res);
    }

    function end(p) {
      const s = p.segs[p.seg];
      if (s.mode === "hold" && p.row) {
        p.row.querySelector(".tk-res").textContent = p.e.res;
        p.row.classList.remove("new");
        void p.row.offsetWidth;
        p.row.classList.add("new");
      }
    }

    function finish(p) {
      if (p.e.kind === "ok") {
        log("ok", p.e.who, p.e.tool, p.e.res);
        flash(nodes[5 + p.e.s], "hit");
      } else if (p.e.kind === "ask") {
        flash(nodes[5 + p.e.s], "hit");
      } else {
        flash(nodes[p.e.c], "hit-deny");
      }
      p.dot.remove();
      p.path.remove();
      packets.delete(p);
    }

    function frame(now) {
      if (now >= nextAt) nextAt = now + spawn(now);
      let lit = -1;
      let hold = false;
      let stop = false;
      for (const p of packets) {
        let s = p.segs[p.seg];
        let k = Math.min(1, (now - p.t0) / s.ms);
        if (k >= 1 && p.seg < p.segs.length - 1) {
          end(p);
          p.seg += 1;
          p.t0 = now;
          start(p);
          s = p.segs[p.seg];
          k = 0;
        }
        p.f = s.from + (s.to - s.from) * ease(k);
        const pt = p.path.getPointAtLength(p.f * p.total);
        p.dot.setAttribute("cx", pt.x.toFixed(1));
        p.dot.setAttribute("cy", pt.y.toFixed(1));
        const q = (p.f - p.enter) / (p.exit - p.enter);
        const inCore = q > 0 && q < 1;
        if (inCore) p.dot.setAttribute("opacity", "0");
        else if (s.fade) p.dot.setAttribute("opacity", (1 - k * 0.85).toFixed(2));
        else p.dot.setAttribute("opacity", "1");
        if (inCore) lit = Math.max(lit, Math.floor(q * 6));
        if (p.mode === "hold") hold = true;
        if (p.mode === "stop") stop = true;
        if (k >= 1 && p.seg === p.segs.length - 1) finish(p);
      }
      stages.forEach((c, i) => {
        c.classList.toggle("hold", hold && i === 4);
        c.classList.toggle("stop", stop && i === 3);
        c.classList.toggle("on", i <= lit && !(hold && i === 4) && !(stop && i === 3));
      });
      core.classList.toggle("hold", hold);
      core.classList.toggle("stop", stop);
      raf = requestAnimationFrame(frame);
    }

    function play() {
      if (raf) return;
      if (pausedAt !== null) {
        const gap = performance.now() - pausedAt;
        nextAt += gap;
        for (const p of packets) p.t0 += gap;
        pausedAt = null;
      }
      raf = requestAnimationFrame(frame);
    }
    function pause() {
      if (!raf) return;
      cancelAnimationFrame(raf);
      raf = 0;
      pausedAt = performance.now();
    }

    let visible = false;
    const sync = () => (visible && !document.hidden ? play() : pause());
    new IntersectionObserver(([entry]) => { visible = entry.isIntersecting; sync(); }, { threshold: 0.15 }).observe(svg);
    document.addEventListener("visibilitychange", sync);
  }

  // ---- 7-stage request pipeline inspector ----
  const flow = document.querySelector("[data-flow]");
  if (flow) initFlow(flow);

  function initFlow(root) {
    const tabs = [...root.querySelectorAll('[role="tab"][data-scenario]')];
    const panel = root.querySelector("[data-stage-panel]");
    const calls = [...root.querySelectorAll("[data-call]")];
    const lists = [...root.querySelectorAll(".flow-steps")];
    const stops = [...root.querySelectorAll(".stop")];
    const fill = root.querySelector("[data-fill]");
    const prevBtn = root.querySelector("[data-prev]");
    const nextBtn = root.querySelector("[data-next]");
    const playBtn = root.querySelector("[data-play]");
    const count = root.querySelector("[data-count-label]");
    const bar = root.querySelector("[data-progress]");
    const ORDER = ["ask", "deny", "allow"];
    const LAST = stops.length - 1;
    const DWELL = 4300;
    const names = stops.map((b) => b.querySelector(".stop-label")?.textContent.trim() || "");

    let scenario = "ask";
    let step = 0;
    let playing = !reduceMotion;
    let inView = false;
    let holding = false;
    let timer = 0;

    const listOf = (s) => lists.find((l) => l.dataset.scenario === s);
    const statesOf = (s) => listOf(s).dataset.states.split(" ");

    function render() {
      const states = statesOf(scenario);
      const stopAt = states.indexOf("deny");
      const reach = stopAt >= 0 ? Math.min(step, stopAt) : step;
      root.dataset.scenario = scenario;

      tabs.forEach((t) => {
        const on = t.dataset.scenario === scenario;
        t.setAttribute("aria-selected", String(on));
        t.tabIndex = on ? 0 : -1;
      });
      panel.setAttribute("aria-labelledby", `tab-${scenario}`);
      calls.forEach((c) => { c.hidden = c.dataset.call !== scenario; });
      lists.forEach((l) => {
        const on = l.dataset.scenario === scenario;
        l.hidden = !on;
        [...l.children].forEach((li, i) => li.classList.toggle("is-current", on && i === step));
      });

      stops.forEach((b, i) => {
        const li = b.parentElement;
        const state = stopAt >= 0 && i > stopAt && i < LAST ? "unreached" : states[i];
        li.className = "";
        if (i < step) li.classList.add("is-passed");
        if (i === step) li.classList.add("is-current");
        if (i <= step) li.classList.add(`st-${state}`);
        b.setAttribute("aria-current", i === step ? "step" : "false");
        const said = i > step ? "" : state === "ask" ? ", asks a person" : state === "deny" ? ", refused" : state === "skip" ? ", not needed" : state === "unreached" ? ", never reached" : ", passed";
        b.setAttribute("aria-label", `Check ${i + 1}, ${names[i]}${said}`);
      });

      if (fill) fill.style.width = `${((reach + 1) / stops.length) * 100}%`;
      root.classList.toggle("is-out", step >= Number(listOf(scenario).dataset.outcomeAt));
      count.textContent = `${String(step + 1).padStart(2, "0")} / ${String(stops.length).padStart(2, "0")}`;
      prevBtn.disabled = step === 0;
    }

    function restartBar(ms) {
      bar.style.transition = "none";
      bar.style.width = "0";
      if (!ms) return;
      void bar.offsetWidth;
      bar.style.transition = `width ${ms}ms linear`;
      bar.style.width = "100%";
    }

    function schedule() {
      clearTimeout(timer);
      const go = playing && inView && !holding && !document.hidden;
      panel.setAttribute("aria-live", playing ? "off" : "polite");
      root.classList.toggle("is-paused", !playing);
      playBtn.setAttribute("aria-label", playing ? "Pause the tour" : "Play the tour");
      if (!go) return restartBar(0);
      const ms = step === LAST ? DWELL + 1500 : DWELL;
      restartBar(ms);
      timer = setTimeout(() => {
        if (step < LAST) step += 1;
        else {
          scenario = ORDER[(ORDER.indexOf(scenario) + 1) % ORDER.length];
          step = 0;
        }
        render();
        schedule();
      }, ms);
    }

    function take() {
      playing = false;
      render();
      schedule();
    }

    const choose = (tab) => {
      scenario = tab.dataset.scenario;
      step = 0;
      take();
    };
    tabs.forEach((t) => t.addEventListener("click", () => choose(t)));
    tabKeys(tabs, choose);
    stops.forEach((b, i) => b.addEventListener("click", () => { step = i; take(); }));
    prevBtn.addEventListener("click", () => { if (step > 0) step -= 1; take(); });
    nextBtn.addEventListener("click", () => {
      if (step < LAST) step += 1;
      else {
        scenario = ORDER[(ORDER.indexOf(scenario) + 1) % ORDER.length];
        step = 0;
      }
      take();
    });
    playBtn.addEventListener("click", () => {
      playing = !playing;
      holding = false;
      if (playing && step === LAST) {
        scenario = ORDER[(ORDER.indexOf(scenario) + 1) % ORDER.length];
        step = 0;
        render();
      }
      schedule();
    });

    root.addEventListener("focusin", (e) => {
      if (e.target.matches(":focus-visible") && e.target !== playBtn) { holding = true; schedule(); }
    });
    root.addEventListener("focusout", (e) => {
      if (!root.contains(e.relatedTarget)) { holding = false; schedule(); }
    });
    document.addEventListener("visibilitychange", schedule);
    new IntersectionObserver(([entry]) => { inView = entry.isIntersecting; schedule(); }, { threshold: 0.3 }).observe(root);

    render();
    schedule();
  }

  // ---- client configuration tabs ----
  document.querySelectorAll(".cc-tabs").forEach((list) => {
    const tabs = [...list.querySelectorAll('[role="tab"]')];
    const select = (tab) => tabs.forEach((t) => {
      const on = t === tab;
      t.setAttribute("aria-selected", String(on));
      t.tabIndex = on ? 0 : -1;
      document.getElementById(t.getAttribute("aria-controls")).hidden = !on;
    });
    tabs.forEach((t) => t.addEventListener("click", () => select(t)));
    tabKeys(tabs, select);
  });

  async function copyText(text, fallbackNode) {
    try {
      await navigator.clipboard.writeText(text);
    } catch {
      const range = document.createRange();
      range.selectNodeContents(fallbackNode);
      const sel = window.getSelection();
      sel.removeAllRanges();
      sel.addRange(range);
      document.execCommand?.("copy");
    }
  }

  document.querySelectorAll("[data-copy]").forEach((btn) => {
    const label = btn.querySelector("span");
    btn.addEventListener("click", async () => {
      const code = btn.closest(".codebox")?.querySelector("pre code");
      if (!code) return;
      await copyText(code.textContent, code);
      btn.classList.add("done");
      label.textContent = "Copied";
      setTimeout(() => { btn.classList.remove("done"); label.textContent = "Copy"; }, 1800);
    });
  });

  // ---- Privo Cloud waitlist ----
  const STORE_KEY = "privo.waitlist";
  const store = {
    get(key, area = localStorage) {
      try { return JSON.parse(area.getItem(key)); } catch { return null; }
    },
    set(key, value, area = localStorage) {
      try { area.setItem(key, JSON.stringify(value)); } catch {}
    },
  };

  const params = new URLSearchParams(location.search);
  const attribution = store.get("privo.attr", sessionStorage) || {};
  for (const key of ["ref", "utm_source", "utm_medium", "utm_campaign"]) {
    if (params.get(key)) attribution[key] = params.get(key);
  }
  if (!attribution.referrer && document.referrer && !document.referrer.startsWith(location.origin)) {
    attribution.referrer = document.referrer;
  }
  store.set("privo.attr", attribution, sessionStorage);

  const countEl = document.querySelector("[data-count]");
  if (countEl) {
    fetch("/api/waitlist/count")
      .then((r) => r.json())
      .then(({ count }) => {
        if (!count) return;
        const strong = document.createElement("strong");
        strong.textContent = count.toLocaleString();
        countEl.replaceChildren(strong, count === 1 ? " person is on the Privo Cloud waitlist." : " people are on the Privo Cloud waitlist.");
        countEl.hidden = false;
      })
      .catch(() => {});
  }

  const tpl = document.getElementById("joined-tpl");

  function showJoined(data, { already = false } = {}) {
    document.querySelectorAll(".join-slot").forEach((slot) => {
      const node = tpl.content.cloneNode(true);
      node.querySelector("[data-pos]").textContent = data.position;
      node.querySelector("[data-already]").hidden = !already;
      const url = `${location.origin}/?ref=${encodeURIComponent(data.refCode)}`;
      const input = node.querySelector("[data-share-url]");
      input.value = url;
      input.addEventListener("focus", () => input.select());
      const copy = node.querySelector("[data-copy-share]");
      copy.addEventListener("click", async () => {
        await copyText(url, input);
        copy.textContent = "Copied";
        setTimeout(() => (copy.textContent = "Copy"), 1800);
      });
      if (data.referrals > 0) {
        const refs = node.querySelector("[data-refs]");
        refs.textContent = `${data.referrals} ${data.referrals === 1 ? "colleague has" : "colleagues have"} joined through your link.`;
        refs.hidden = false;
      }
      slot.replaceChildren(node);
    });
  }

  const saved = store.get(STORE_KEY);
  if (saved?.refCode && tpl) showJoined(saved);

  document.querySelectorAll("[data-join]").forEach((form) => {
    form.addEventListener("submit", async (event) => {
      event.preventDefault();
      const msg = form.querySelector(".join-msg");
      const button = form.querySelector("button[type=submit]");
      const label = button.dataset.label || (button.dataset.label = button.textContent);
      const email = form.email.value.trim();
      msg.className = "join-msg";

      if (!/^[^\s@]+@[^\s@]+\.[^\s@]{2,}$/.test(email)) {
        msg.textContent = "Please enter a valid email address.";
        form.email.focus();
        return;
      }

      button.disabled = true;
      button.textContent = "Joining…";
      try {
        const res = await fetch("/api/waitlist", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ email, website: form.website.value, ...attribution }),
        });
        const data = await res.json().catch(() => ({}));
        if (!res.ok) throw new Error(data.error || "Something went wrong. Please try again.");

        const record = { position: data.position, refCode: data.refCode, referrals: data.referrals };
        showJoined(record, { already: data.alreadyJoined });
        store.set(STORE_KEY, record);
      } catch (err) {
        msg.textContent = err.message;
        button.disabled = false;
        button.textContent = label;
      }
    });
  });
})();
