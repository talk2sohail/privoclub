import { existsSync } from "node:fs";
import { chmod, mkdir, writeFile } from "node:fs/promises";
import http from "node:http";
import type { AddressInfo } from "node:net";
import path from "node:path";
import { chromium, type BrowserContext } from "playwright";
import type { ResolvedTarget } from "../config.js";
import type { ClientAvailability } from "./types.js";

export type Persona = "owner" | "member";

export type UiSmokeReport = {
  ok: boolean;
  visitedPages: Array<{ path: string; ok: boolean; title: string; error?: string }>;
  consoleErrors: string[];
  pageErrors: string[];
  serverErrors: string[];
  csrfRejected: boolean;
};

export function sessionStatePath(target: ResolvedTarget, persona: Persona): string {
  return path.join(target.stateDir, `${persona}.json`);
}

export function hasSavedSession(target: ResolvedTarget, persona: Persona): boolean {
  return existsSync(sessionStatePath(target, persona));
}

export class BrowserAdapter {
  readonly id = "browser" as const;
  readonly name = "Playwright Browser (Admin UI & OAuth)";

  async checkAvailability(target: ResolvedTarget): Promise<ClientAvailability> {
    const ownerReady = hasSavedSession(target, "owner");
    const memberReady = hasSavedSession(target, "member");
    return {
      id: this.id,
      name: this.name,
      installed: true,
      version: "playwright",
      authReady: ownerReady,
      note: ownerReady
        ? `saved sessions: owner=yes, member=${memberReady ? "yes" : "no"}`
        : `Run: npm --prefix e2e run probe -- auth --target ${target.name} --persona owner`,
    };
  }

  /**
   * Opens a visible browser so the operator can sign in once with GitHub
   * (including 2FA/passkeys) and saves Privo session cookies for automated runs.
   */
  async bootstrapSession(target: ResolvedTarget, persona: Persona): Promise<string> {
    await mkdir(target.stateDir, { recursive: true });
    const outPath = sessionStatePath(target, persona);
    const browser = await chromium.launch({ headless: false });
    try {
      const context = await browser.newContext();
      const page = await context.newPage();
      await page.goto(`${target.url}/admin/signin`, { waitUntil: "domcontentloaded" });

      console.log(
        `\n[privo-probe auth] Browser opened at ${target.url}/admin/signin.\n` +
          `Sign in as "${persona}" with GitHub. Waiting up to 3 minutes for /admin/*...\n`,
      );

      await page.waitForURL(
        (u) => u.origin === new URL(target.url).origin && u.pathname.startsWith("/admin/") && !u.pathname.startsWith("/admin/signin"),
        { timeout: 180_000 },
      );

      // Verify the session API answers 200
      const sessionRes = await page.evaluate(async () => {
        const r = await fetch("/admin/api/session", { credentials: "same-origin" });
        return { status: r.status, body: await r.text() };
      });
      if (sessionRes.status !== 200) {
        throw new Error(`Expected /admin/api/session 200 after sign-in, got ${sessionRes.status}`);
      }

      await context.storageState({ path: outPath });
      await chmod(outPath, 0o600).catch(() => {});
      console.log(`[privo-probe auth] Saved ${persona} session to ${outPath}`);
      return outPath;
    } finally {
      await browser.close();
    }
  }

  /**
   * Imports a Privo session cookie (`__Host-privo_session` on HTTPS, or
   * `privo_session` on HTTP) copied from the user's normal browser (e.g. when
   * GitHub sign-in uses Google SSO, which blocks automated Chromium windows).
   */
  async bootstrapFromCookie(target: ResolvedTarget, persona: Persona, rawCookieValue: string): Promise<string> {
    const value = rawCookieValue
      .trim()
      .replace(/^(__Host-)?privo_session=/, "")
      .trim();
    if (!value) {
      throw new Error("Empty session cookie value.");
    }
    const parsedUrl = new URL(target.url);
    const secure = parsedUrl.protocol === "https:";
    const cookieName = secure ? "__Host-privo_session" : "privo_session";

    // Validate the cookie against Privo's live /admin/api/session endpoint
    const check = await fetch(`${target.url}/admin/api/session`, {
      headers: {
        Accept: "application/json",
        Cookie: `${cookieName}=${value}`,
      },
      signal: AbortSignal.timeout(15_000),
    });
    if (check.status !== 200) {
      throw new Error(
        `Session cookie rejected by ${target.url}/admin/api/session (HTTP ${check.status}). Make sure you copied the full value of "${cookieName}" from an active sign-in.`,
      );
    }
    const session = (await check.json()) as { user?: { name?: string; person?: string; roles?: string[] } };

    await mkdir(target.stateDir, { recursive: true });
    const outPath = sessionStatePath(target, persona);
    const storageState = {
      cookies: [
        {
          name: cookieName,
          value,
          domain: parsedUrl.hostname,
          path: "/",
          expires: Math.floor(Date.now() / 1000) + 12 * 3600,
          httpOnly: true,
          secure,
          sameSite: "Lax",
        },
      ],
      origins: [],
    };
    await writeFile(outPath, JSON.stringify(storageState, null, 2), { mode: 0o600 });
    await chmod(outPath, 0o600).catch(() => {});
    console.log(
      `[privo-probe auth] Verified session for ${session.user?.person ?? "unknown"} (${(session.user?.roles ?? []).join(",")}) and saved to ${outPath}`,
    );
    return outPath;
  }

  private async openAuthenticatedContext(target: ResolvedTarget, persona: Persona): Promise<{
    context: BrowserContext;
    close: () => Promise<void>;
  }> {
    const stateFile = sessionStatePath(target, persona);
    if (!existsSync(stateFile)) {
      throw new Error(
        `No saved browser session for persona "${persona}" on target "${target.name}". ` +
          `Run: npm --prefix e2e run probe -- auth --target ${target.name} --persona ${persona}`,
      );
    }
    const browser = await chromium.launch({ headless: true });
    const context = await browser.newContext({ storageState: stateFile });
    return {
      context,
      close: async () => {
        await context.close().catch(() => {});
        await browser.close().catch(() => {});
      },
    };
  }

  /**
   * Walks every Admin UI page for the given persona, verifying zero console
   * errors, zero uncaught exceptions, zero 5xx responses, and CSRF enforcement.
   */
  async smokeAdminUi(target: ResolvedTarget, persona: Persona = "owner"): Promise<UiSmokeReport> {
    const { context, close } = await this.openAuthenticatedContext(target, persona);
    const consoleErrors: string[] = [];
    const pageErrors: string[] = [];
    const serverErrors: string[] = [];
    const visitedPages: UiSmokeReport["visitedPages"] = [];
    let csrfRejected = false;

    const ownerRoutes = [
      "/admin/start",
      "/admin/approvals",
      "/admin/servers",
      "/admin/teams",
      "/admin/rules",
      "/admin/people",
      "/admin/tokens",
      "/admin/clients",
      "/admin/activity",
      "/admin/connections",
    ];
    const memberRoutes = ["/admin/start", "/admin/tokens", "/admin/connections"];
    const routes = persona === "owner" ? ownerRoutes : memberRoutes;

    try {
      const page = await context.newPage();
      let collecting = true;
      page.on("console", (msg) => {
        if (collecting && msg.type() === "error") consoleErrors.push(msg.text());
      });
      page.on("pageerror", (err) => {
        if (collecting) pageErrors.push(err.message);
      });
      page.on("response", (res) => {
        if (collecting && res.status() >= 500 && res.url().startsWith(target.url)) {
          serverErrors.push(`${res.status()} ${res.url()}`);
        }
      });

      // Verify saved session is still live
      await page.goto(`${target.url}/admin/start`, { waitUntil: "networkidle", timeout: 25_000 });
      if (page.url().includes("/admin/signin")) {
        throw new Error(
          `Saved "${persona}" browser session expired on ${target.url}. Re-run: npm --prefix e2e run probe -- auth --target ${target.name} --persona ${persona}`,
        );
      }

      for (const route of routes) {
        try {
          await page.goto(`${target.url}${route}`, { waitUntil: "networkidle", timeout: 20_000 });
          if (page.url().includes("/admin/signin")) {
            visitedPages.push({ path: route, ok: false, title: await page.title(), error: "Redirected to /admin/signin" });
            continue;
          }
          const heading = await page.locator("main h1").first().textContent().catch(() => "");
          visitedPages.push({
            path: route,
            ok: !heading?.toLowerCase().includes("not found"),
            title: (await page.title()) || heading || "",
          });
        } catch (err) {
          visitedPages.push({
            path: route,
            ok: false,
            title: "",
            error: err instanceof Error ? err.message : String(err),
          });
        }
      }

      // Stop collecting navigation console errors before intentional negative CSRF 403 probe
      collecting = false;
      // F14 security check: bad X-Privo-CSRF must be refused with 403
      const csrfStatus = await page.evaluate(async () => {
        const res = await fetch("/admin/api/v1/servers", {
          method: "GET",
          credentials: "same-origin",
          headers: { Accept: "application/json", "X-Privo-CSRF": "invalid-csrf-probe" },
        });
        return res.status;
      });
      csrfRejected = csrfStatus === 403;

      const ok =
        visitedPages.every((p) => p.ok) &&
        consoleErrors.length === 0 &&
        pageErrors.length === 0 &&
        serverErrors.length === 0 &&
        csrfRejected;

      return { ok, visitedPages, consoleErrors, pageErrors, serverErrors, csrfRejected };
    } finally {
      await close();
    }
  }

  /**
   * Drives Privo's OAuth 2.1 consent page (`/oauth/authorize?...`) as a real
   * signed-in member and clicks either Allow or Deny, capturing the redirect URL
   * via a real local loopback HTTP listener (identical to Claude Code / Codex).
   */
  async completeOAuthConsent(
    target: ResolvedTarget,
    authorizeUrl: string,
    persona: Persona,
    decision: "allow" | "deny",
    callbackServer?: { waitForCallback: (timeoutMs?: number) => Promise<string> },
  ): Promise<{ redirectUrl: string; pageText: string }> {
    const { context, close } = await this.openAuthenticatedContext(target, persona);
    try {
      const page = await context.newPage();
      await page.goto(authorizeUrl, { waitUntil: "domcontentloaded", timeout: 25_000 });
      if (page.url().includes("/admin/signin")) {
        throw new Error(`Saved "${persona}" session expired before OAuth consent.`);
      }

      const pageText = (await page.locator("body").textContent()) ?? "";
      const buttonLabel = decision === "allow" ? /^Allow$/i : /^Deny$/i;
      const btn = page.getByRole("button", { name: buttonLabel });
      await btn.waitFor({ state: "visible", timeout: 10_000 });

      if (callbackServer) {
        const [cbUrl] = await Promise.all([callbackServer.waitForCallback(15_000), btn.click()]);
        return { redirectUrl: cbUrl, pageText };
      }

      await Promise.all([
        page.waitForURL((u) => u.hostname === "127.0.0.1" || u.hostname === "localhost", { timeout: 15_000 }).catch(() => {}),
        btn.click(),
      ]);
      return { redirectUrl: page.url(), pageText };
    } finally {
      await close();
    }
  }

  /**
   * Starts a temporary local loopback HTTP server on 127.0.0.1:<ephemeral>/callback
   * to receive real browser OAuth 2.1 redirects from Privo.
   */
  async startLoopbackCallback(): Promise<{
    redirectUri: string;
    waitForCallback: (timeoutMs?: number) => Promise<string>;
    close: () => Promise<void>;
  }> {
    let resolver: ((url: string) => void) | null = null;
    let queuedUrl: string | null = null;

    const server = http.createServer((req, res) => {
      const addr = server.address() as AddressInfo;
      const fullUrl = `http://127.0.0.1:${addr.port}${req.url || "/"}`;
      res.writeHead(200, { "Content-Type": "text/plain" });
      res.end("OAuth callback received by privo-probe");
      if (resolver) {
        const fn = resolver;
        resolver = null;
        fn(fullUrl);
      } else {
        queuedUrl = fullUrl;
      }
    });

    await new Promise<void>((resolve, reject) => {
      server.once("error", reject);
      server.listen(0, "127.0.0.1", () => resolve());
    });
    const port = (server.address() as AddressInfo).port;

    return {
      redirectUri: `http://127.0.0.1:${port}/callback`,
      waitForCallback: (timeoutMs = 15_000) => {
        if (queuedUrl) {
          const u = queuedUrl;
          queuedUrl = null;
          return Promise.resolve(u);
        }
        return new Promise<string>((resolve, reject) => {
          const timer = setTimeout(() => {
            resolver = null;
            reject(new Error(`Timed out after ${timeoutMs}ms waiting for OAuth loopback callback`));
          }, timeoutMs);
          resolver = (u) => {
            clearTimeout(timer);
            resolve(u);
          };
        });
      },
      close: () => new Promise<void>((resolve) => server.close(() => resolve())),
    };
  }

  /**
   * Submits a person's personal API key on `/connect/<alias>` (ADR 0023).
   */
  async connectPersonalKey(
    target: ResolvedTarget,
    alias: string,
    apiKey: string,
    persona: Persona,
  ): Promise<{ ok: boolean; finalUrl: string; bodyText: string }> {
    const { context, close } = await this.openAuthenticatedContext(target, persona);
    try {
      const page = await context.newPage();
      await page.goto(`${target.url}/connect/${encodeURIComponent(alias)}`, {
        waitUntil: "domcontentloaded",
        timeout: 25_000,
      });
      if (page.url().includes("/admin/signin")) {
        throw new Error(`Saved "${persona}" session expired before /connect/${alias}.`);
      }

      const input = page.locator('input[type="password"], input[name="key"]').first();
      await input.waitFor({ state: "visible", timeout: 10_000 });
      await input.fill(apiKey);

      const submit = page.getByRole("button", { name: /save my key|save/i }).first();
      await Promise.all([
        page.waitForLoadState("networkidle", { timeout: 15_000 }).catch(() => {}),
        submit.click(),
      ]);

      const bodyText = (await page.locator("body").textContent()) ?? "";
      const ok = page.url().includes("/connections") || bodyText.toLowerCase().includes("connected");
      return { ok, finalUrl: page.url(), bodyText: bodyText.slice(0, 500) };
    } finally {
      await close();
    }
  }
}
