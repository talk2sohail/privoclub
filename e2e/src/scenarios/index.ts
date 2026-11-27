import type { SuiteName } from "../config.js";
import { scenario01HealthDiscovery } from "./01-health-discovery.js";
import { scenario02ConnectPinning } from "./02-connect-pinning.js";
import { scenario03ArgumentSchema } from "./03-argument-schema.js";
import { scenario04PolicyConditions } from "./04-policy-conditions.js";
import { scenario05ApprovalsTwoPersona } from "./05-approvals-two-persona.js";
import { scenario06TeamsToolsetsRbac } from "./06-teams-toolsets-rbac.js";
import { scenario07AuthOAuthRevocation } from "./07-auth-oauth-revocation.js";
import { scenario08PersonalKeyUpstream } from "./08-personal-key-upstream.js";
import { scenario09AuditTelemetryUi } from "./09-audit-telemetry-ui.js";
import { scenario10ExploratoryGapFinder } from "./10-exploratory-gap-finder.js";
import type { Scenario } from "./types.js";

export const ALL_SCENARIOS: Scenario[] = [
  scenario01HealthDiscovery,
  scenario02ConnectPinning,
  scenario03ArgumentSchema,
  scenario04PolicyConditions,
  scenario05ApprovalsTwoPersona,
  scenario06TeamsToolsetsRbac,
  scenario07AuthOAuthRevocation,
  scenario08PersonalKeyUpstream,
  scenario09AuditTelemetryUi,
  scenario10ExploratoryGapFinder,
];

export function selectScenarios(suite: SuiteName, filter?: string[]): Scenario[] {
  let list = ALL_SCENARIOS.filter((s) => s.suites.includes(suite));
  if (filter && filter.length > 0) {
    const wanted = new Set(filter.map((f) => f.toLowerCase()));
    list = ALL_SCENARIOS.filter((s) => [...wanted].some((w) => s.id.toLowerCase().includes(w)));
  }
  return list;
}

export * from "./types.js";
