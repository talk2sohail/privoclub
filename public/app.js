// Privo Club landing page: waitlist signup, share link and post-signup survey.
(() => {
  document.documentElement.classList.add("js");

  const STORE_KEY = "privo.waitlist";
  const store = {
    get(key, area = localStorage) {
      try { return JSON.parse(area.getItem(key)); } catch { return null; }
    },
    set(key, value, area = localStorage) {
      try { area.setItem(key, JSON.stringify(value)); } catch {}
    },
  };

  // ---- attribution: remember ?ref= and utm_* for this visit ----
  const params = new URLSearchParams(location.search);
  const attribution = store.get("privo.attr", sessionStorage) || {};
  for (const key of ["ref", "utm_source", "utm_medium", "utm_campaign"]) {
    if (params.get(key)) attribution[key] = params.get(key);
  }
  if (!attribution.referrer && document.referrer && !document.referrer.startsWith(location.origin)) {
    attribution.referrer = document.referrer;
  }
  store.set("privo.attr", attribution, sessionStorage);

  // ---- social proof (server only returns a number once the list is big enough) ----
  const countEl = document.querySelector("[data-count]");
  fetch("/api/waitlist/count")
    .then((r) => r.json())
    .then(({ count }) => {
      if (!count || !countEl) return;
      countEl.innerHTML = `<strong>${count.toLocaleString()}</strong> people have already asked for a seat.`;
      countEl.hidden = false;
    })
    .catch(() => {});

  // ---- joined state ----
  const tpl = document.getElementById("joined-tpl");
  const survey = document.getElementById("survey");

  function showJoined(data, { already = false } = {}) {
    const no = String(data.position).padStart(4, "0");
    document.querySelectorAll("[data-card-no]").forEach((el) => (el.textContent = no));
    survey.querySelectorAll("[data-pos]").forEach((el) => (el.textContent = data.position));

    document.querySelectorAll(".join-slot").forEach((slot) => {
      const node = tpl.content.cloneNode(true);
      node.querySelector("[data-pos]").textContent = data.position;
      node.querySelector("[data-already]").hidden = !already;
      const url = `${location.origin}/?ref=${data.refCode}`;
      const input = node.querySelector("[data-share-url]");
      input.value = url;
      input.addEventListener("focus", () => input.select());
      const copy = node.querySelector("[data-copy]");
      copy.addEventListener("click", async () => {
        try {
          await navigator.clipboard.writeText(url);
        } catch {
          input.select();
          document.execCommand?.("copy");
        }
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
      if (data.editToken && !data.surveyed) {
        openSurvey.hidden = false;
        openSurvey.addEventListener("click", () => survey.showModal());
      }
      slot.replaceChildren(node);
    });
  }

  const saved = store.get(STORE_KEY);
  if (saved?.refCode) showJoined(saved);

  // ---- signup ----
  document.querySelectorAll("[data-join]").forEach((form) => {
    form.addEventListener("submit", async (event) => {
      event.preventDefault();
      const msg = form.querySelector(".join-msg");
      const button = form.querySelector("button[type=submit]");
      const email = form.email.value.trim();
      msg.className = "join-msg";

      if (!/^[^\s@]+@[^\s@]+\.[^\s@]{2,}$/.test(email)) {
        msg.textContent = "Please enter a valid email address.";
        form.email.focus();
        return;
      }

      button.disabled = true;
      button.textContent = "Reserving your seat…";
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
        if (!data.alreadyJoined && data.editToken) setTimeout(() => survey.showModal(), 650);
      } catch (err) {
        msg.textContent = err.message;
        button.disabled = false;
        button.textContent = "Request an invite";
      }
    });
  });

  // ---- survey ----
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

  // ---- reveal on scroll ----
  const reveals = document.querySelectorAll(".reveal");
  if ("IntersectionObserver" in window) {
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
