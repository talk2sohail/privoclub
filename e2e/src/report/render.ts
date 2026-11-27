import { mkdir, writeFile } from "node:fs/promises";
import path from "node:path";
import type { ClientAvailability, ClientId } from "../clients/index.js";
import type { ResolvedTarget, SuiteName } from "../config.js";
import type { WireRecord } from "../proxy/wire-proxy.js";
import type { CellResult, CellStatus, Finding } from "../scenarios/types.js";
import type { UpstreamPoolStatus } from "../upstreams/health.js";

export type ReleaseReport = {
  generatedAt: string;
  runId: string;
  target: { name: string; url: string; privoVersion?: string };
  suite: SuiteName;
  releaseReady: boolean;
  summary: {
    pass: number;
    fail: number;
    uxGap: number;
    upstreamUnavailable: number;
    skip: number;
    p0Blockers: number;
    protocolViolations: number;
    wireCalls: number;
    latencyP50Ms: number;
    latencyP95Ms: number;
  };
  clients: ClientAvailability[];
  upstreams: UpstreamPoolStatus;
  cells: CellResult[];
  findings: Finding[];
  cleanupWarnings: string[];
};

function percentile(values: number[], p: number): number {
  if (values.length === 0) return 0;
  const sorted = [...values].sort((a, b) => a - b);
  const idx = Math.min(sorted.length - 1, Math.max(0, Math.ceil((p / 100) * sorted.length) - 1));
  return sorted[idx] ?? 0;
}

export function buildReleaseReport(input: {
  runId: string;
  target: ResolvedTarget;
  privoVersion?: string;
  suite: SuiteName;
  clients: ClientAvailability[];
  upstreams: UpstreamPoolStatus;
  cells: CellResult[];
  wireRecords: WireRecord[];
  cleanupWarnings: string[];
  strictUx?: boolean;
}): ReleaseReport {
  const findings = input.cells.flatMap((c) => c.findings);
  const count = (status: CellStatus) => input.cells.filter((c) => c.status === status).length;
  const p0Blockers = findings.filter((f) => f.category === "P0_BLOCKER").length;
  const protocolViolations = input.cells.reduce((acc, c) => acc + c.violations.length, 0);
  const latencies = input.wireRecords.filter((r) => r.path.startsWith("/mcp")).map((r) => r.durationMs);

  const failCount = count("FAIL");
  const uxGapCount = count("UX_GAP");
  const releaseReady =
    failCount === 0 && p0Blockers === 0 && protocolViolations === 0 && (!input.strictUx || uxGapCount === 0);

  return {
    generatedAt: new Date().toISOString(),
    runId: input.runId,
    target: { name: input.target.name, url: input.target.url, privoVersion: input.privoVersion },
    suite: input.suite,
    releaseReady,
    summary: {
      pass: count("PASS"),
      fail: failCount,
      uxGap: uxGapCount,
      upstreamUnavailable: count("UPSTREAM_UNAVAILABLE"),
      skip: count("SKIP"),
      p0Blockers,
      protocolViolations,
      wireCalls: latencies.length,
      latencyP50Ms: percentile(latencies, 50),
      latencyP95Ms: percentile(latencies, 95),
    },
    clients: input.clients,
    upstreams: input.upstreams,
    cells: input.cells,
    findings,
    cleanupWarnings: input.cleanupWarnings,
  };
}

function badge(status: CellStatus): string {
  switch (status) {
    case "PASS":
      return "PASS";
    case "FAIL":
      return "**FAIL**";
    case "UX_GAP":
      return "UX_GAP";
    case "UPSTREAM_UNAVAILABLE":
      return "UPSTREAM_DOWN";
    case "SKIP":
      return "SKIP";
  }
}

export function renderMarkdown(report: ReleaseReport): string {
  const verdict = report.releaseReady ? "RELEASE READY" : "BLOCKED (P0 REGRESSIONS FOUND)";
  const lines: string[] = [
    `# Privo Release Verification Report — ${verdict}`,
    "",
    `- **Target:** \`${report.target.name}\` (${report.target.url})${report.target.privoVersion ? ` — Privo \`v${report.target.privoVersion}\`` : ""}`,
    `- **Suite:** \`${report.suite}\` | **Run ID:** \`${report.runId}\` | **Generated:** ${report.generatedAt}`,
    `- **Scorecard:** PASS=${report.summary.pass} | FAIL=${report.summary.fail} | UX_GAP=${report.summary.uxGap} | UPSTREAM_DOWN=${report.summary.upstreamUnavailable} | SKIP=${report.summary.skip}`,
    `- **Wire Oracle:** ${report.summary.wireCalls} MCP requests | **Protocol Violations:** ${report.summary.protocolViolations} | **Latency:** p50=${report.summary.latencyP50Ms}ms, p95=${report.summary.latencyP95Ms}ms`,
    "",
    "## Compatibility Matrix (Scenarios x Clients)",
    "",
  ];

  // Build unique column list in stable order
  const colOrder: Array<ClientId | "control-plane"> = [
    "control-plane",
    "mcp-sdk",
    "claude-code",
    "agy",
    "codex",
    "gemini",
    "browser",
  ];
  const presentCols = colOrder.filter((col) => report.cells.some((c) => c.clientId === col));
  const scenarioOrder = [...new Set(report.cells.map((c) => c.scenarioId))];

  lines.push(`| Scenario | ${presentCols.join(" | ")} |`);
  lines.push(`| --- | ${presentCols.map(() => "---").join(" | ")} |`);

  for (const sid of scenarioOrder) {
    const first = report.cells.find((c) => c.scenarioId === sid)!;
    const cols = presentCols.map((col) => {
      const cell = report.cells.find((c) => c.scenarioId === sid && c.clientId === col);
      return cell ? `${badge(cell.status)} (${cell.durationMs}ms)` : "—";
    });
    lines.push(`| \`${sid}\` ${first.scenarioTitle} | ${cols.join(" | ")} |`);
  }

  lines.push("", "## Findings by Category", "");
  const categories: Array<{ key: Finding["category"]; title: string }> = [
    { key: "P0_BLOCKER", title: "P0 Release Blockers" },
    { key: "UX_CLIENT_GAP", title: "UX & Client Friction Gaps" },
    { key: "UPSTREAM_UNAVAILABLE", title: "External Upstream Availability Warnings" },
  ];

  for (const cat of categories) {
    const items = report.findings.filter((f) => f.category === cat.key);
    lines.push(`### ${cat.title} (${items.length})`, "");
    if (items.length === 0) {
      lines.push("_None._", "");
      continue;
    }
    for (const item of items) {
      lines.push(`- **[${item.scenarioId} / ${item.clientId}]** ${item.summary}`);
      if (item.detail) {
        lines.push("  ```text", ...item.detail.split("\n").map((l) => `  ${l}`), "  ```");
      }
    }
    lines.push("");
  }

  lines.push("## Detailed Cell Results", "");
  for (const c of report.cells) {
    lines.push(`- \`${c.scenarioId}\` / \`${c.clientId}\` — **${c.status}** (${c.durationMs}ms): ${c.summary}`);
  }

  if (report.cleanupWarnings.length > 0) {
    lines.push("", "## Cleanup Warnings", "");
    for (const w of report.cleanupWarnings) {
      lines.push(`- ${w}`);
    }
  }

  return lines.join("\n") + "\n";
}

function escapeHtml(s: string): string {
  return s.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&quot;");
}

export function renderHtml(report: ReleaseReport): string {
  const statusColor = (s: CellStatus) =>
    s === "PASS"
      ? "#15803d"
      : s === "FAIL"
        ? "#b91c1c"
        : s === "UX_GAP"
          ? "#b45309"
          : s === "UPSTREAM_UNAVAILABLE"
            ? "#6d28d9"
            : "#6b7280";

  const rows = report.cells
    .map(
      (c) =>
        `<tr>
          <td><code>${escapeHtml(c.scenarioId)}</code><br/><small>${escapeHtml(c.scenarioTitle)}</small></td>
          <td><code>${escapeHtml(c.clientId)}</code></td>
          <td style="font-weight:600;color:${statusColor(c.status)}">${escapeHtml(c.status)}</td>
          <td>${c.durationMs}ms</td>
          <td>${escapeHtml(c.summary)}</td>
        </tr>`,
    )
    .join("\n");

  const findingsHtml =
    report.findings.length === 0
      ? "<p>No findings.</p>"
      : `<ul>${report.findings
          .map(
            (f) =>
              `<li><strong>[${escapeHtml(f.category)}]</strong> <code>${escapeHtml(f.scenarioId)}</code> / <code>${escapeHtml(f.clientId)}</code>: ${escapeHtml(f.summary)}${
                f.detail ? `<pre>${escapeHtml(f.detail)}</pre>` : ""
              }</li>`,
          )
          .join("")}</ul>`;

  return `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8" />
  <title>Privo Release Verification — ${escapeHtml(report.target.name)} (${escapeHtml(report.suite)})</title>
  <style>
    body { font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif; margin: 2rem; color: #111827; background: #fafafa; }
    h1 { margin-bottom: .25rem; }
    .badge { display: inline-block; padding: .25rem .6rem; border-radius: 6px; color: #fff; font-weight: 700; background: ${report.releaseReady ? "#15803d" : "#b91c1c"}; }
    table { width: 100%; border-collapse: collapse; background: #fff; margin-top: 1rem; box-shadow: 0 1px 2px rgba(0,0,0,.06); }
    th, td { padding: .6rem .8rem; border-bottom: 1px solid #e5e7eb; text-align: left; vertical-align: top; }
    th { background: #f3f4f6; }
    pre { background: #111827; color: #f9fafb; padding: .75rem; border-radius: 6px; overflow-x: auto; font-size: .85rem; }
  </style>
</head>
<body>
  <h1>Privo Release Verification <span class="badge">${report.releaseReady ? "RELEASE READY" : "BLOCKED"}</span></h1>
  <p>Target: <strong>${escapeHtml(report.target.name)}</strong> (${escapeHtml(report.target.url)}) · Suite: <code>${escapeHtml(report.suite)}</code> · Run: <code>${escapeHtml(report.runId)}</code> · Generated: ${escapeHtml(report.generatedAt)}</p>
  <p>PASS=${report.summary.pass} · FAIL=${report.summary.fail} · UX_GAP=${report.summary.uxGap} · UPSTREAM_DOWN=${report.summary.upstreamUnavailable} · SKIP=${report.summary.skip} · Wire calls=${report.summary.wireCalls} (p50=${report.summary.latencyP50Ms}ms, p95=${report.summary.latencyP95Ms}ms)</p>
  <h2>Results</h2>
  <table>
    <thead><tr><th>Scenario</th><th>Client</th><th>Status</th><th>Duration</th><th>Summary</th></tr></thead>
    <tbody>${rows}</tbody>
  </table>
  <h2>Findings</h2>
  ${findingsHtml}
</body>
</html>
`;
}

export async function writeReports(
  reportsDir: string,
  report: ReleaseReport,
): Promise<{ mdPath: string; htmlPath: string; jsonPath: string }> {
  await mkdir(reportsDir, { recursive: true });
  const stamp = report.generatedAt.replace(/[:.]/g, "-");
  const base = `report-${report.target.name}-${report.suite}-${stamp}`;

  const mdPath = path.join(reportsDir, `${base}.md`);
  const htmlPath = path.join(reportsDir, `${base}.html`);
  const jsonPath = path.join(reportsDir, `${base}.json`);

  const md = renderMarkdown(report);
  const html = renderHtml(report);
  const json = JSON.stringify(report, null, 2);

  await Promise.all([
    writeFile(mdPath, md, "utf8"),
    writeFile(htmlPath, html, "utf8"),
    writeFile(jsonPath, json, "utf8"),
    writeFile(path.join(reportsDir, "latest.md"), md, "utf8"),
  ]);

  return { mdPath, htmlPath, jsonPath };
}
