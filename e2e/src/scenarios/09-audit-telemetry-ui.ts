import { hasSavedSession } from "../clients/browser.js";
import { assertAuditIntegrity } from "../privo/oracles.js";
import { makeCell, type CellResult, type Scenario } from "./types.js";

/**
 * Scenario 09: Tamper-evident Audit Trail integrity & CSV export, Activity
 * Telemetry, and full Playwright Admin UI smoke across all pages + CSRF check.
 */
export const scenario09AuditTelemetryUi: Scenario = {
  id: "09-audit-telemetry-ui",
  title: "Audit Chain Integrity, Telemetry & Admin UI Smoke",
  suites: ["smoke", "release"],
  mutatesTarget: false,

  async run(ctx): Promise<CellResult[]> {
    const results: CellResult[] = [];

    // ---- 1. Audit chain integrity + CSV export + Telemetry summary
    {
      const start = Date.now();
      const findings: CellResult["findings"] = [];
      try {
        const integrity = await assertAuditIntegrity(ctx.api);
        const csvExport = await ctx.api.exportAuditCsv(
          ctx.shared.primaryServerAlias ? { server: ctx.shared.primaryServerAlias } : {},
        );
        if (!csvExport.csv.startsWith("time,")) {
          findings.push({
            scenarioId: this.id,
            clientId: "control-plane",
            category: "P0_BLOCKER",
            summary: "Audit CSV export header malformed",
            detail: csvExport.csv.slice(0, 200),
          });
        }
        // Verify arguments are never exported in CSV
        const headerLine = csvExport.csv.split(/\r?\n/)[0] ?? "";
        if (headerLine.includes("arguments")) {
          findings.push({
            scenarioId: this.id,
            clientId: "control-plane",
            category: "P0_BLOCKER",
            summary: "SECURITY REGRESSION: Audit CSV export header includes arguments column",
          });
        }

        const summary = await ctx.api.telemetrySummary("24h", "server");
        const hasBlocker = findings.some((f) => f.category === "P0_BLOCKER");
        results.push(
          makeCell(this, "control-plane", {
            status: hasBlocker ? "FAIL" : "PASS",
            durationMs: Date.now() - start,
            summary: hasBlocker
              ? findings[0]!.summary
              : `Audit intact (${integrity.blocks} blocks, ${integrity.events} events), CSV (${csvExport.rows} rows), telemetry 24h calls=${summary.total.calls}`,
            findings,
          }),
        );
      } catch (err) {
        results.push(
          makeCell(this, "control-plane", {
            status: "FAIL",
            durationMs: Date.now() - start,
            summary: err instanceof Error ? err.message : String(err),
            findings: [
              {
                scenarioId: this.id,
                clientId: "control-plane",
                category: "P0_BLOCKER",
                summary: "Audit integrity or telemetry verification failed",
                detail: err instanceof Error ? err.message : String(err),
              },
            ],
          }),
        );
      }
    }

    // ---- 2. Playwright Admin UI smoke (all 10 pages + CSRF enforcement)
    if (!ctx.browserEnabled || !hasSavedSession(ctx.target, "owner")) {
      results.push(
        makeCell(this, "browser", {
          status: "SKIP",
          durationMs: 0,
          summary: `Skipped Admin UI smoke: no saved owner session (run: npm --prefix e2e run probe -- auth --target ${ctx.target.name} --persona owner)`,
        }),
      );
      return results;
    }

    const uiStart = Date.now();
    try {
      const ownerReport = await ctx.browser.smokeAdminUi(ctx.target, "owner");
      const findings: CellResult["findings"] = [];

      if (!ownerReport.csrfRejected) {
        findings.push({
          scenarioId: this.id,
          clientId: "browser",
          category: "P0_BLOCKER",
          summary: "Admin API accepted request with invalid X-Privo-CSRF header (expected 403)",
        });
      }
      if (ownerReport.serverErrors.length > 0) {
        findings.push({
          scenarioId: this.id,
          clientId: "browser",
          category: "P0_BLOCKER",
          summary: `Admin UI triggered ${ownerReport.serverErrors.length} HTTP 5xx error(s)`,
          detail: ownerReport.serverErrors.join("\n"),
        });
      }
      const cfBeaconRe = /static\.cloudflareinsights\.com\/beacon\.min\.js/i;
      const cfBeaconErrors = ownerReport.consoleErrors.filter((m) => cfBeaconRe.test(m));
      const appConsoleErrors = ownerReport.consoleErrors.filter((m) => !cfBeaconRe.test(m));

      if (cfBeaconErrors.length > 0) {
        findings.push({
          scenarioId: this.id,
          clientId: "browser",
          category: "UX_CLIENT_GAP",
          summary: `Cloudflare auto-injected RUM beacon blocked by Privo's strict CSP (${cfBeaconErrors.length}x) — disable Cloudflare Web Analytics auto-injection for app/staging subdomains`,
          detail: cfBeaconErrors[0],
        });
      }
      if (ownerReport.pageErrors.length > 0 || appConsoleErrors.length > 0) {
        findings.push({
          scenarioId: this.id,
          clientId: "browser",
          category: "P0_BLOCKER",
          summary: `Admin UI emitted console/page errors (${appConsoleErrors.length} console, ${ownerReport.pageErrors.length} uncaught)`,
          detail: [...ownerReport.pageErrors, ...appConsoleErrors].slice(0, 10).join("\n"),
        });
      }
      const badPages = ownerReport.visitedPages.filter((p) => !p.ok);
      if (badPages.length > 0) {
        findings.push({
          scenarioId: this.id,
          clientId: "browser",
          category: "P0_BLOCKER",
          summary: `${badPages.length} Admin UI page(s) failed to load cleanly`,
          detail: badPages.map((p) => `${p.path}: ${p.error || p.title}`).join("\n"),
        });
      }

      const hasUiBlocker = findings.some((f) => f.category === "P0_BLOCKER");
      const hasUiGap = findings.some((f) => f.category === "UX_CLIENT_GAP");
      results.push(
        makeCell(this, "browser", {
          status: hasUiBlocker ? "FAIL" : hasUiGap ? "UX_GAP" : "PASS",
          durationMs: Date.now() - uiStart,
          summary: hasUiBlocker
            ? findings.find((f) => f.category === "P0_BLOCKER")!.summary
            : hasUiGap
              ? `All ${ownerReport.visitedPages.length} Admin UI pages loaded cleanly (0 app errors, CSRF=403); Cloudflare RUM beacon blocked by CSP (${cfBeaconErrors.length}x)`
              : `All ${ownerReport.visitedPages.length} Admin UI pages loaded cleanly; 0 console/5xx errors; CSRF=403`,
          findings,
        }),
      );
    } catch (err) {
      results.push(
        makeCell(this, "browser", {
          status: "FAIL",
          durationMs: Date.now() - uiStart,
          summary: err instanceof Error ? err.message : String(err),
          findings: [
            {
              scenarioId: this.id,
              clientId: "browser",
              category: "P0_BLOCKER",
              summary: "Playwright Admin UI smoke threw error",
              detail: err instanceof Error ? err.message : String(err),
            },
          ],
        }),
      );
    }

    return results;
  },
};
