// Privo landing page: the live switchboard in the hero, the "follow one call" walkthrough,
// client snippets, and the Privo Cloud waitlist with its share link and survey.
// No dependencies and no inline code: the page's CSP allows scripts and styles from this site only.
(() => {
  document.documentElement.classList.add("js");
  const reduceMotion = window.matchMedia("(prefers-reduced-motion: reduce)").matches;

  // ---- nav: dark glass once scrolled; a menu on small screens ----
  const nav = document.querySelector("[data-nav]") || document.querySelector(".nav");
  const onScroll = () => nav?.classList.toggle("scrolled", window.scrollY > 8);
  window.addEventListener("scroll", onScroll, { passive: true });
  onScroll();

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

  // ---- tabs: arrow keys move between tabs (WAI-ARIA tabs, automatic activation) ----
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

  // ---- the switchboard: calls flow from AI clients through Privo to servers ----
  const board = document.querySelector("[data-board]");
  if (board && !reduceMotion) initBoard(board);

  function initBoard(svg) {
    const NS = "http://www.w3.org/2000/svg";
    const layer = svg.querySelector("[data-packets]");
    const core = svg.querySelector("[data-core]");
    const stages = [...svg.querySelectorAll("[data-stage]")];
    const nodes = [...svg.querySelectorAll(".node")]; // five clients, then six servers
    const rows = document.querySelector("[data-ticker] .tk-rows");
    const CY = [70, 150, 230, 310, 390];
    const SY = [50, 122, 194, 266, 338, 410];
    const inCurve = (c) => `M136 ${CY[c]}C188 ${CY[c]} 186 230 230 230`;
    const route = (c, s) => `${inCurve(c)}L370 230C414 230 412 ${SY[s]} 464 ${SY[s]}`;
    // The log in the page starts with the last four of these (newest first), so the first calls
    // the animation adds never repeat a line that is still showing.
    const SCRIPT = [
      { c: 1, s: 0, kind: "ok", who: "sam", tool: "github__create_pull_request", res: "allowed · 388 ms" },
      { c: 0, s: 0, kind: "ask", who: "ada", tool: "github__merge_pull_request", res: "approved by grace" },
      { c: 2, s: 2, kind: "ok", who: "lee", tool: "sentry__search_issues", res: "allowed · 141 ms" },
      { c: 4, s: 5, kind: "ok", who: "mia", tool: "context7__get-library-docs", res: "allowed · 96 ms" },
      { c: 3, s: 3, kind: "deny", who: "kai", tool: "supabase__execute_sql", res: "denied by a rule" },
      { c: 2, s: 1, kind: "ok", who: "lee", tool: "linear__update_issue", res: "allowed · 175 ms" },
      { c: 1, s: 4, kind: "ask", who: "sam", tool: "stripe__create_refund", res: "approved by grace" },
      { c: 0, s: 1, kind: "ok", who: "ada", tool: "linear__create_issue", res: "allowed · 212 ms" },
    ];
    const ease = (k) => (k < 0.5 ? 2 * k * k : 1 - (-2 * k + 2) ** 2 / 2);
    const packets = new Set();
    let next = 0;
    let nextAt = 0;
    let raf = 0;
    let pausedAt = null;

    function log(kind, who, tool, res) {
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
      const exit = enter + 140 / total; // the core box is 140 wide
      const dot = document.createElementNS(NS, "circle");
      dot.setAttribute("r", "5.5");
      dot.setAttribute("class", `packet ${e.kind}`);
      dot.setAttribute("filter", "url(#f-glow)");
      layer.append(dot);

      const mid = enter + (exit - enter) * 0.55;
      const at = enter + (exit - enter) * 0.62;
      const segs =
        e.kind === "ok" ? [{ from: 0, to: 1, ms: 2700 }]
        : e.kind === "ask" ? [{ from: 0, to: mid, ms: 1350 }, { from: mid, to: mid, ms: 1900, mode: "hold" }, { from: mid, to: 1, ms: 1350 }]
        : [{ from: 0, to: at, ms: 1250 }, { from: at, to: at, ms: 650, mode: "stop" }, { from: at, to: 0, ms: 1100, fade: true }];
      const p = { e, path, dot, total, enter, exit, segs, seg: 0, t0: now, f: 0, mode: null, row: null };
      packets.add(p);
      start(p);
      return e.kind === "ask" ? 4200 : e.kind === "deny" ? 3200 : 1500;
    }

    function start(p) {
      const s = p.segs[p.seg];
      p.mode = s.mode || null;
      if (s.mode === "hold") p.row = log("ask", p.e.who, p.e.tool, "waiting for approval");
      if (s.mode === "stop") p.row = log("deny", p.e.who, p.e.tool, p.e.res);
    }

    function end(p) {
      const s = p.segs[p.seg];
      if (s.mode === "hold" && p.row) {
        p.row.querySelector(".tk-res").textContent = p.e.res;
        p.row.classList.remove("new");
        void p.row.offsetWidth; // restart the entry highlight
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
        if (s.fade) p.dot.setAttribute("opacity", (1 - k * 0.85).toFixed(2));
        const q = (p.f - p.enter) / (p.exit - p.enter);
        if (q > 0 && q < 1) lit = Math.max(lit, Math.floor(q * 6));
        if (p.mode === "hold") hold = true;
        if (p.mode === "stop") stop = true;
        if (k >= 1 && p.seg === p.segs.length - 1) finish(p);
      }
      // Six lights on the core: identity, access, tool check, rules, approval, credentials.
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

  // ---- follow one call: the seven stops, three scenarios ----
  const flow = document.querySelector("[data-flow]");
  if (flow) initFlow(flow);

  function initFlow(root) {
    const tabs = [...root.querySelectorAll('[role="tab"][data-scenario]')];
    const panel = root.querySelector("[data-stage-panel]");
    const calls = [...root.querySelectorAll("[data-call]")];
    const lists = [...root.querySelectorAll(".flow-steps")];
    const stops = [...root.querySelectorAll(".stop")];
    const fill = root.querySelector("[data-fill]");
    const packet = root.querySelector("[data-packet]");
    const prevBtn = root.querySelector("[data-prev]");
    const nextBtn = root.querySelector("[data-next]");
    const playBtn = root.querySelector("[data-play]");
    const count = root.querySelector("[data-count-label]");
    const bar = root.querySelector("[data-progress]");
    const ORDER = ["ask", "deny", "allow"];
    const LAST = stops.length - 1;
    const DWELL = 4300;
    const names = stops.map((b) => b.querySelector(".stop-label").textContent.trim());

    let scenario = "ask";
    let step = 0;
    let playing = !reduceMotion;
    let inView = false;
    let holding = false; // keyboard focus inside the walkthrough
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
        // Past the stop where a rule said no, the call never arrives; only the audit still sees it.
        const state = stopAt >= 0 && i > stopAt && i < LAST ? "unreached" : states[i];
        li.className = "";
        if (i < step) li.classList.add("is-passed");
        if (i === step) li.classList.add("is-current");
        if (i <= step) li.classList.add(`st-${state}`);
        b.setAttribute("aria-current", i === step ? "step" : "false");
        const said = i > step ? "" : state === "ask" ? ", asks a person" : state === "deny" ? ", refused" : state === "skip" ? ", not needed" : state === "unreached" ? ", never reached" : ", passed";
        b.setAttribute("aria-label", `Stop ${i + 1}, ${names[i]}${said}`);
      });

      const color = states[reach] === "ask" ? "ask" : states[reach] === "deny" ? "deny" : "ok";
      fill.style.width = `${(reach / LAST) * 100}%`;
      packet.style.left = `${((2 * reach + 1) / (2 * stops.length)) * 100}%`;
      packet.className = `track-packet ${color}`;
      root.classList.toggle("is-out", step >= Number(listOf(scenario).dataset.outcomeAt));
      count.textContent = `Stop ${step + 1} of ${stops.length}`;
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
      const ms = step === LAST ? DWELL + 1600 : DWELL;
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

    // Anything a person does takes over from the tour.
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
      holding = false; // pressing Play means play, wherever the focus is
      if (playing && step === LAST) {
        scenario = ORDER[(ORDER.indexOf(scenario) + 1) % ORDER.length];
        step = 0;
        render();
      }
      schedule();
    });

    // A keyboard user moving through the walkthrough holds the tour, so nothing changes under them.
    root.addEventListener("focusin", (e) => {
      if (e.target.matches(":focus-visible") && e.target !== playBtn) { holding = true; schedule(); }
    });
    root.addEventListener("focusout", (e) => {
      if (!root.contains(e.relatedTarget)) { holding = false; schedule(); }
    });
    document.addEventListener("visibilitychange", schedule);
    new IntersectionObserver(([entry]) => { inView = entry.isIntersecting; schedule(); }, { threshold: 0.35 }).observe(root);

    render();
    schedule();
  }

  // ---- client snippets ----
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

  // attribution: remember ?ref= and utm_* for this visit
  const params = new URLSearchParams(location.search);
  const attribution = store.get("privo.attr", sessionStorage) || {};
  for (const key of ["ref", "utm_source", "utm_medium", "utm_campaign"]) {
    if (params.get(key)) attribution[key] = params.get(key);
  }
  if (!attribution.referrer && document.referrer && !document.referrer.startsWith(location.origin)) {
    attribution.referrer = document.referrer;
  }
  store.set("privo.attr", attribution, sessionStorage);

  // social proof: the server returns a number only once the list is big enough
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
  const survey = document.getElementById("survey");

  function showJoined(data, { already = false } = {}) {
    survey?.querySelectorAll("[data-pos]").forEach((el) => (el.textContent = data.position));

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
      if (data.welcomeEmail) node.querySelector("[data-inbox]").hidden = false;
      if (data.referrals > 0) {
        const refs = node.querySelector("[data-refs]");
        refs.textContent = `${data.referrals} ${data.referrals === 1 ? "friend has" : "friends have"} joined through your link.`;
        refs.hidden = false;
      }
      const openSurvey = node.querySelector("[data-open-survey]");
      if (data.editToken && !data.surveyed && survey) {
        openSurvey.hidden = false;
        openSurvey.addEventListener("click", () => survey.showModal());
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

        const record = {
          position: data.position,
          refCode: data.refCode,
          referrals: data.referrals,
          editToken: data.editToken || store.get(STORE_KEY)?.editToken || null,
        };
        // Only mention the inbox right after a fresh signup, not on later visits.
        showJoined({ ...record, welcomeEmail: data.welcomeEmail }, { already: data.alreadyJoined });
        store.set(STORE_KEY, record);
        if (!data.alreadyJoined && data.editToken && survey) setTimeout(() => survey.showModal(), 650);
      } catch (err) {
        msg.textContent = err.message;
        button.disabled = false;
        button.textContent = label;
      }
    });
  });

  // ---- survey ----
  if (survey) {
    const surveyForm = survey.querySelector("[data-survey]");
    const surveyDone = survey.querySelector(".survey-done");
    survey.querySelectorAll("[data-close]").forEach((b) => b.addEventListener("click", () => survey.close()));
    survey.addEventListener("click", (e) => { if (e.target === survey) survey.close(); });

    surveyForm.addEventListener("submit", async (event) => {
      event.preventDefault();
      const msg = surveyForm.querySelector("[data-survey-msg]");
      const button = surveyForm.querySelector("button[type=submit]");
      const record = store.get(STORE_KEY);
      const fd = new FormData(surveyForm);
      const payload = {
        editToken: record?.editToken,
        role: fd.get("role"),
        clients: fd.getAll("clients"),
        tools: fd.getAll("tools"),
        pain: fd.get("pain"),
        pay: fd.get("pay"),
        note: fd.get("note"),
      };

      button.disabled = true;
      try {
        const res = await fetch("/api/waitlist/profile", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify(payload),
        });
        if (!res.ok) throw new Error((await res.json().catch(() => ({}))).error || "Couldn’t save your answers.");
        store.set(STORE_KEY, { ...record, surveyed: true });
        document.querySelectorAll("[data-open-survey]").forEach((b) => (b.hidden = true));
        surveyForm.hidden = true;
        surveyDone.hidden = false;
      } catch (err) {
        msg.textContent = err.message;
      } finally {
        button.disabled = false;
      }
    });
  }

  // ---- reveal on scroll ----
  const reveals = document.querySelectorAll(".reveal");
  if ("IntersectionObserver" in window && !reduceMotion) {
    const io = new IntersectionObserver((entries) => {
      for (const entry of entries) {
        if (!entry.isIntersecting) continue;
        entry.target.classList.add("in");
        io.unobserve(entry.target);
      }
    }, { rootMargin: "0px 0px -8% 0px" });
    reveals.forEach((el) => io.observe(el));
  } else {
    reveals.forEach((el) => el.classList.add("in"));
  }
})();
