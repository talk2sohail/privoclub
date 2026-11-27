#!/usr/bin/env node
import {
  ALL_CLIENT_IDS,
  ALL_MCP_CLIENT_IDS,
  createBrowserAdapter,
  createMcpAdapter,
  McpSdkAdapter,
  type ClientAvailability,
  type ClientId,
  type McpClientAdapter,
} from "./clients/index.js";
import type { Persona } from "./clients/browser.js";
import { assertTargetSafety, loadTarget, type SuiteName } from "./config.js";
import { PrivoAdminApi } from "./privo/admin-api.js";
import { FixtureManager } from "./privo/fixture-manager.js";
import { WireInspectorProxy } from "./proxy/wire-proxy.js";
import { buildReleaseReport, renderMarkdown, writeReports } from "./report/render.js";
import { makeCell, selectScenarios, type CellResult, type ScenarioContext } from "./scenarios/index.js";
import { probeAllUpstreams } from "./upstreams/health.js";

type ParsedArgs = {
  command: string;
  target: string;
  suite: SuiteName;
  url?: string;
  clients?: ClientId[];
  scenarios?: string[];
  persona: Persona;
  cookie?: string;
  cookieFile?: string;
  allowMutations?: boolean;
  strictUx: boolean;
  noCleanup: boolean;
};

function parseCliArgs(argv: string[]): ParsedArgs {
  const command = argv[0] || "help";
  const out: ParsedArgs = {
    command,
    target: "staging",
    suite: "smoke",
    persona: "owner",
    strictUx: false,
    noCleanup: false,
  };

  for (let i = 1; i < argv.length; i++) {
    const arg = argv[i]!;
    const next = () => argv[++i] ?? "";
    if (arg === "--target" || arg === "-t") out.target = next();
    else if (arg === "--suite" || arg === "-s") out.suite = next() as SuiteName;
    else if (arg === "--url") out.url = next();
    else if (arg === "--clients" || arg === "-c") {
      out.clients = next()
        .split(",")
        .map((s) => s.trim() as ClientId)
        .filter(Boolean);
    } else if (arg === "--scenario") {
      out.scenarios = next()
        .split(",")
        .map((s) => s.trim())
        .filter(Boolean);
    } else if (arg === "--persona") {
      out.persona = next() as Persona;
    } else if (arg === "--cookie") {
      out.cookie = next();
    } else if (arg === "--cookie-file") {
      out.cookieFile = next();
    } else if (arg === "--allow-mutations") {
      out.allowMutations = true;
    } else if (arg === "--strict-ux") {
      out.strictUx = true;
    } else if (arg === "--no-cleanup") {
      out.noCleanup = true;
    }
  }
  return out;
}

function printHelp(): void {
  console.log(`privo-probe — Release-grade AI client wrapper & verification harness for Privo

Usage:
  privo-probe doctor  --target staging|prod [--url https://...]
  privo-probe auth    --target staging|prod --persona owner|member [--url https://...]
  privo-probe run     --target staging|prod [--suite smoke|release|explore] [--clients mcp-sdk,claude-code,agy,codex,gemini,browser] [--scenario 02,03] [--strict-ux]
  privo-probe cleanup --target staging|prod

Examples:
  npm --prefix e2e run probe -- doctor --target staging
  npm --prefix e2e run probe -- auth --target staging --persona owner
  npm --prefix e2e run probe -- run --target staging --suite release
  npm --prefix e2e run probe -- run --target prod --suite smoke
`);
}

async function cmdDoctor(args: ParsedArgs): Promise<number> {
  const target = loadTarget(args.target, { url: args.url });
  const api = new PrivoAdminApi(target.url, target.ownerControlToken);
  const browser = createBrowserAdapter();

  console.log(`\n=== privo-probe doctor (target=${target.name}, url=${target.url}) ===\n`);

  // 1. Target Privo health
  const [livez, readyz, version, prm] = await Promise.all([
    api.livez().catch((e) => ({ status: 0, text: String(e), json: undefined })),
    api.readyz().catch((e) => ({ status: 0, text: String(e), json: undefined })),
    api.version().catch((e) => ({ status: 0, text: String(e), json: undefined })),
    api.oauthProtectedResource().catch((e) => ({ status: 0, text: String(e), json: undefined })),
  ]);
  const verStr =
    version.json && typeof version.json === "object" && "version" in version.json
      ? String((version.json as Record<string, unknown>).version)
      : "unknown";
  console.log(`Target Gateway:`);
  console.log(`  /livez:   HTTP ${livez.status}`);
  console.log(`  /readyz:  HTTP ${readyz.status}`);
  console.log(`  /version: HTTP ${version.status} (${verStr})`);
  console.log(`  OAuth PRM: HTTP ${prm.status}`);
  console.log(`  Owner control token: ${target.ownerControlToken ? "SET" : "MISSING"}`);
  console.log(`  Member gateway token: ${target.memberGatewayToken ? "SET" : "not set (auto-minted on staging)"}`);

  if (target.ownerControlToken) {
    const srvCheck = await api.raw("GET", "/api/v1/servers").catch((e) => ({ status: 0, text: String(e) }));
    console.log(`  Admin API auth check: HTTP ${srvCheck.status}`);
  }

  // 2. Client wrappers
  console.log(`\nAI Client Wrappers:`);
  const availabilities: ClientAvailability[] = [];
  for (const id of ALL_MCP_CLIENT_IDS) {
    const av = await createMcpAdapter(id).checkAvailability();
    availabilities.push(av);
  }
  availabilities.push(await browser.checkAvailability(target));
  for (const av of availabilities) {
    const icon = av.installed && av.authReady ? "[READY]" : av.installed ? "[NEEDS AUTH]" : "[NOT INSTALLED]";
    console.log(`  ${icon.padEnd(16)} ${av.id.padEnd(13)} ${av.version ?? ""} ${av.note ? `— ${av.note}` : ""}`);
  }

  // 3. Real upstream pool pre-flight
  console.log(`\nReal Upstream MCP Servers (direct pre-flight probe):`);
  const upstreams = await probeAllUpstreams(target);
  for (const u of upstreams.noAuthResults) {
    const icon = u.healthy ? "[UP]" : "[DOWN]";
    console.log(
      `  ${icon.padEnd(8)} ${u.name.padEnd(18)} ${u.latencyMs}ms  tools=${u.toolNames.length}  ${u.error ? `err=${u.error}` : ""}`,
    );
  }
  if (upstreams.personalKeyHealth) {
    const u = upstreams.personalKeyHealth;
    console.log(`  ${(u.healthy ? "[UP]" : "[DOWN]").padEnd(8)} ${u.name.padEnd(18)} ${u.latencyMs}ms  tools=${u.toolNames.length}`);
  } else {
    console.log(`  [SKIP]   GitHub MCP         set PRIVO_TEST_GITHUB_PAT_OWNER to probe personal-key upstream`);
  }
  if (upstreams.promptsResourcesHealth) {
    const u = upstreams.promptsResourcesHealth;
    console.log(
      `  ${(u.healthy ? "[UP]" : "[DOWN]").padEnd(8)} ${u.name.padEnd(18)} ${u.latencyMs}ms  prompts=${u.promptNames.length} resources=${u.resourceUris.length}`,
    );
  }

  console.log("");
  return livez.status === 200 && readyz.status === 200 ? 0 : 1;
}

async function cmdAuth(args: ParsedArgs): Promise<number> {
  const target = loadTarget(args.target, { url: args.url });
  const browser = createBrowserAdapter();
  if (args.cookieFile) {
    const { readFile } = await import("node:fs/promises");
    const raw = await readFile(args.cookieFile, "utf8");
    await browser.bootstrapFromCookie(target, args.persona, raw);
    return 0;
  }
  if (args.cookie) {
    await browser.bootstrapFromCookie(target, args.persona, args.cookie);
    return 0;
  }
  await browser.bootstrapSession(target, args.persona);
  return 0;
}

async function cmdCleanup(args: ParsedArgs): Promise<number> {
  const target = loadTarget(args.target, { url: args.url });
  if (!target.ownerControlToken) {
    throw new Error(`Owner control token env var is not set for target "${target.name}".`);
  }
  const api = new PrivoAdminApi(target.url, target.ownerControlToken);
  const swept = await FixtureManager.reapOrphans(api);
  console.log(`Reaped orphaned probe fixtures on ${target.name} (${target.url}):`, JSON.stringify(swept, null, 2));
  return 0;
}

async function cmdRun(args: ParsedArgs): Promise<number> {
  const target = loadTarget(args.target, {
    url: args.url,
    allowMutations: args.allowMutations,
  });
  assertTargetSafety(target, args.suite);

  if (!target.ownerControlToken) {
    throw new Error(
      `Owner control token is required. Set PRIVO_${target.name.toUpperCase()}_OWNER_TOKEN (or PRIVO_OWNER_TOKEN).`,
    );
  }

  const api = new PrivoAdminApi(target.url, target.ownerControlToken);
  const fixtures = new FixtureManager(api);
  const proxy = new WireInspectorProxy(target.url);
  proxy.addSecretToScrub(target.ownerControlToken);
  if (target.memberGatewayToken) proxy.addSecretToScrub(target.memberGatewayToken);

  const selectedClients = args.clients ?? ALL_CLIENT_IDS;
  const browser = createBrowserAdapter();
  const sdk = new McpSdkAdapter();

  const clientAvailabilities: ClientAvailability[] = [];
  const cliClients: McpClientAdapter[] = [];

  for (const id of ALL_MCP_CLIENT_IDS) {
    const adapter = id === "mcp-sdk" ? sdk : createMcpAdapter(id);
    const av = await adapter.checkAvailability();
    clientAvailabilities.push(av);
    if (id !== "mcp-sdk" && selectedClients.includes(id) && av.installed && av.authReady) {
      cliClients.push(adapter);
    }
  }
  clientAvailabilities.push(await browser.checkAvailability(target));

  console.log(`\n[privo-probe] Target: ${target.name} (${target.url}) | Suite: ${args.suite} | Run ID: ${fixtures.runId}`);
  console.log(
    `[privo-probe] Active CLI AI clients: ${cliClients.length > 0 ? cliClients.map((c) => c.id).join(", ") : "(none installed/authenticated; running MCP SDK + Control Plane + Browser)"}`,
  );

  // Reap any stale probe-* fixtures on staging before starting
  if (target.allowMutations) {
    await FixtureManager.reapOrphans(api).catch(() => {});
  }

  const versionRes = await api.version().catch(() => null);
  const privoVersion =
    versionRes?.json && typeof versionRes.json === "object" && "version" in versionRes.json
      ? String((versionRes.json as Record<string, unknown>).version)
      : undefined;

  console.log(`[privo-probe] Probing real upstream MCP servers directly...`);
  const upstreams = await probeAllUpstreams(target);
  if (upstreams.primaryNoAuth) {
    console.log(
      `[privo-probe] Primary real upstream: ${upstreams.primaryNoAuth.spec.name} (${upstreams.primaryNoAuth.health.latencyMs}ms, ${upstreams.primaryNoAuth.health.toolNames.length} tools)`,
    );
  }

  await proxy.start();
  const cells: CellResult[] = [];
  let cleanupWarnings: string[] = [];

  try {
    const ctx: ScenarioContext = {
      target,
      suite: args.suite,
      api,
      fixtures,
      proxy,
      upstreams,
      sdk,
      cliClients,
      browser,
      browserEnabled: selectedClients.includes("browser"),
      shared: {},
    };

    const scenarios = selectScenarios(args.suite, args.scenarios);
    for (const sc of scenarios) {
      console.log(`[privo-probe] Running ${sc.id} — ${sc.title}...`);
      let scCells: CellResult[];
      try {
        scCells = await sc.run(ctx);
      } catch (err) {
        const msg = err instanceof Error ? err.message : String(err);
        scCells = [
          makeCell(sc, "control-plane", {
            status: "FAIL",
            durationMs: 0,
            summary: `Unhandled scenario error: ${msg}`,
            findings: [
              {
                scenarioId: sc.id,
                clientId: "control-plane",
                category: "P0_BLOCKER",
                summary: `Scenario ${sc.id} threw unhandled error`,
                detail: msg,
              },
            ],
          }),
        ];
      }
      for (const c of scCells) {
        console.log(`  [${c.status.padEnd(4)}] ${c.clientId.padEnd(13)} (${c.durationMs}ms) ${c.summary}`);
      }
      cells.push(...scCells);
    }
  } finally {
    await sdk.teardown();
    for (const c of cliClients) await c.teardown();
    await proxy.stop();
    if (!args.noCleanup && target.allowMutations) {
      cleanupWarnings = await fixtures.cleanup();
    }
  }

  const report = buildReleaseReport({
    runId: fixtures.runId,
    target,
    privoVersion,
    suite: args.suite,
    clients: clientAvailabilities,
    upstreams,
    cells,
    wireRecords: proxy.allRecords(),
    cleanupWarnings,
    strictUx: args.strictUx,
  });

  const paths = await writeReports(target.reportsDir, report);
  console.log(`\n${renderMarkdown(report)}`);
  console.log(`Reports written:\n  Markdown: ${paths.mdPath}\n  HTML:     ${paths.htmlPath}\n  JSON:     ${paths.jsonPath}\n`);

  if (process.env.GITHUB_STEP_SUMMARY) {
    const { appendFile } = await import("node:fs/promises");
    await appendFile(process.env.GITHUB_STEP_SUMMARY, renderMarkdown(report), "utf8");
  }

  return report.releaseReady ? 0 : 1;
}

async function main(): Promise<void> {
  const args = parseCliArgs(process.argv.slice(2));
  try {
    let code = 0;
    switch (args.command) {
      case "doctor":
        code = await cmdDoctor(args);
        break;
      case "auth":
        code = await cmdAuth(args);
        break;
      case "cleanup":
        code = await cmdCleanup(args);
        break;
      case "run":
        code = await cmdRun(args);
        break;
      default:
        printHelp();
        code = args.command === "help" || args.command === "--help" ? 0 : 1;
    }
    process.exit(code);
  } catch (err) {
    console.error(`[privo-probe] ERROR:`, err instanceof Error ? err.message : String(err));
    process.exit(1);
  }
}

void main();
