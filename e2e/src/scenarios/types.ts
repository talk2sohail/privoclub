import type { BrowserAdapter, ClientId, McpClientAdapter, McpSdkAdapter } from "../clients/index.js";
import type { ResolvedTarget, SuiteName } from "../config.js";
import type { PrivoAdminApi } from "../privo/admin-api.js";
import type { FixtureManager } from "../privo/fixture-manager.js";
import type { ProtocolViolation } from "../proxy/schema-validator.js";
import type { WireInspectorProxy, WireRecord } from "../proxy/wire-proxy.js";
import type { UpstreamPoolStatus } from "../upstreams/health.js";

export type CellStatus = "PASS" | "FAIL" | "SKIP" | "UX_GAP" | "UPSTREAM_UNAVAILABLE";

export type FindingCategory = "P0_BLOCKER" | "UX_CLIENT_GAP" | "UPSTREAM_UNAVAILABLE";

export type Finding = {
  scenarioId: string;
  clientId: ClientId | "control-plane";
  category: FindingCategory;
  summary: string;
  detail?: string;
};

export type CellResult = {
  scenarioId: string;
  scenarioTitle: string;
  clientId: ClientId | "control-plane";
  status: CellStatus;
  durationMs: number;
  summary: string;
  violations: ProtocolViolation[];
  wireSample?: WireRecord[];
  findings: Finding[];
};

export type ScenarioContext = {
  target: ResolvedTarget;
  suite: SuiteName;
  api: PrivoAdminApi;
  fixtures: FixtureManager;
  proxy: WireInspectorProxy;
  upstreams: UpstreamPoolStatus;
  sdk: McpSdkAdapter;
  cliClients: McpClientAdapter[];
  browser: BrowserAdapter;
  browserEnabled: boolean;
  /** Shared fixture state populated by early scenarios if running a full suite */
  shared: {
    primaryServerAlias?: string;
    primaryToolName?: string;
    primaryFullToolName?: string;
    memberGatewayToken?: string;
    memberTokenId?: string;
    approverControlToken?: string;
  };
};

export type Scenario = {
  id: string;
  title: string;
  suites: SuiteName[];
  mutatesTarget: boolean;
  run(ctx: ScenarioContext): Promise<CellResult[]>;
};

export function makeCell(
  scenario: Pick<Scenario, "id" | "title">,
  clientId: ClientId | "control-plane",
  partial: Omit<CellResult, "scenarioId" | "scenarioTitle" | "clientId" | "violations" | "findings"> & {
    violations?: ProtocolViolation[];
    findings?: Finding[];
  },
): CellResult {
  const violations = partial.violations ?? [];
  const findings = [...(partial.findings ?? [])];
  if (violations.length > 0) {
    findings.push({
      scenarioId: scenario.id,
      clientId,
      category: "P0_BLOCKER",
      summary: `${violations.length} MCP protocol schema violation(s) on wire`,
      detail: violations.map((v) => `[${v.protocolVersion}] ${v.method} ${v.rule}: ${v.detail}`).join("\n"),
    });
  }
  const status: CellStatus =
    violations.length > 0 && partial.status === "PASS" ? "FAIL" : partial.status;

  return {
    scenarioId: scenario.id,
    scenarioTitle: scenario.title,
    clientId,
    status,
    durationMs: partial.durationMs,
    summary: partial.summary,
    violations,
    wireSample: partial.wireSample,
    findings,
  };
}
