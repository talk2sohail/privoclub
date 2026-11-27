import { existsSync, readFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import YAML from "yaml";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
export const E2E_ROOT = path.resolve(__dirname, "..");

export type SampleCallSpec = {
  tool: string;
  args: Record<string, unknown>;
  invalidArgs: Record<string, unknown>;
  conditionArg: string;
  conditionDenyValue: string;
  conditionAllowValue: string;
};

export type NoAuthUpstreamSpec = {
  id: string;
  shortCode: string;
  name: string;
  url: string;
  transport: "http" | "sse";
  sampleCall: SampleCallSpec;
};

export type PersonalKeyUpstreamSpec = {
  id: string;
  shortCode: string;
  name: string;
  url: string;
  transport: "http" | "sse";
  header: string;
  prefix: string;
  help: string;
  helpUrl: string;
  identityTool: string;
};

export type PromptsResourcesUpstreamSpec = {
  id: string;
  shortCode: string;
  name: string;
  url: string;
  transport: "http" | "sse";
};

export type TargetYaml = {
  name: string;
  url: string;
  allowMutations: boolean;
  allowGlobalSettingsMutation: boolean;
  oauthExpected: boolean;
  dynamicRegistrationExpected: boolean;
  env: {
    ownerControlToken: string;
    memberGatewayToken: string;
    memberPerson: string;
    ownerPerson: string;
    githubPatOwner: string;
    githubPatMember: string;
    context7ApiKey: string;
  };
  upstreams: {
    noAuthPool: NoAuthUpstreamSpec[];
    personalKey?: PersonalKeyUpstreamSpec;
    promptsResources?: PromptsResourcesUpstreamSpec;
  };
};

export type ResolvedTarget = {
  name: string;
  url: string;
  mcpUrl: string;
  allowMutations: boolean;
  allowGlobalSettingsMutation: boolean;
  oauthExpected: boolean;
  dynamicRegistrationExpected: boolean;
  ownerControlToken: string;
  memberGatewayToken: string;
  ownerPerson: string;
  memberPerson: string;
  githubPatOwner: string;
  githubPatMember: string;
  context7ApiKey: string;
  upstreams: TargetYaml["upstreams"];
  stateDir: string;
  reportsDir: string;
};

export type SuiteName = "smoke" | "release" | "explore";

function parseDotEnvFile(filePath: string): Record<string, string> {
  if (!existsSync(filePath)) return {};
  const out: Record<string, string> = {};
  for (const rawLine of readFileSync(filePath, "utf8").split(/\r?\n/)) {
    const line = rawLine.trim();
    if (!line || line.startsWith("#")) continue;
    const cleaned = line.startsWith("export ") ? line.slice(7).trim() : line;
    const eq = cleaned.indexOf("=");
    if (eq <= 0) continue;
    const key = cleaned.slice(0, eq).trim();
    let val = cleaned.slice(eq + 1).trim();
    if ((val.startsWith('"') && val.endsWith('"')) || (val.startsWith("'") && val.endsWith("'"))) {
      val = val.slice(1, -1);
    }
    out[key] = val;
  }
  return out;
}

export function loadTarget(
  targetNameOrPath: string,
  overrides: { url?: string; allowMutations?: boolean } = {},
): ResolvedTarget {
  const candidatePath = existsSync(targetNameOrPath)
    ? targetNameOrPath
    : path.join(E2E_ROOT, "targets", `${targetNameOrPath}.yaml`);

  if (!existsSync(candidatePath)) {
    throw new Error(
      `Unknown target "${targetNameOrPath}". Expected "staging", "prod", or a YAML file path (looked for ${candidatePath}).`,
    );
  }

  const raw = YAML.parse(readFileSync(candidatePath, "utf8")) as TargetYaml;
  const stateDir = path.join(E2E_ROOT, ".state", raw.name);

  // Load gitignored local state files (pat.env, owner.token) as fallbacks behind explicit process.env
  const patEnv = parseDotEnvFile(path.join(stateDir, "pat.env"));
  const getVar = (name: string): string => process.env[name] || patEnv[name] || "";

  const ownerTokenFile = path.join(stateDir, "owner.token");
  const fileOwnerToken = existsSync(ownerTokenFile) ? readFileSync(ownerTokenFile, "utf8").trim() : "";

  const envUrl = getVar(`PRIVO_${raw.name.toUpperCase()}_URL`) || getVar("PRIVO_TARGET_URL");
  const baseUrl = (overrides.url || envUrl || raw.url).replace(/\/+$/, "");

  const ownerControlToken =
    getVar(raw.env.ownerControlToken) || getVar("PRIVO_OWNER_TOKEN") || fileOwnerToken;
  const memberGatewayToken =
    getVar(raw.env.memberGatewayToken) || getVar("PRIVO_MEMBER_TOKEN") || "";

  const allowMutations = overrides.allowMutations ?? raw.allowMutations;

  return {
    name: raw.name,
    url: baseUrl,
    mcpUrl: `${baseUrl}/mcp`,
    allowMutations,
    allowGlobalSettingsMutation: raw.allowGlobalSettingsMutation,
    oauthExpected: raw.oauthExpected,
    dynamicRegistrationExpected: raw.dynamicRegistrationExpected,
    ownerControlToken,
    memberGatewayToken,
    ownerPerson: getVar(raw.env.ownerPerson) || getVar("PRIVO_OWNER_PERSON") || "",
    memberPerson: getVar(raw.env.memberPerson) || getVar("PRIVO_MEMBER_PERSON") || "",
    githubPatOwner: getVar(raw.env.githubPatOwner) || "",
    githubPatMember: getVar(raw.env.githubPatMember) || "",
    context7ApiKey: getVar(raw.env.context7ApiKey) || "",
    upstreams: raw.upstreams,
    stateDir,
    reportsDir: path.join(E2E_ROOT, "reports"),
  };
}

/**
 * Safety guardrail preventing accidental mutating suites against production.
 */
export function assertTargetSafety(target: ResolvedTarget, suite: SuiteName): void {
  if (suite !== "smoke" && !target.allowMutations) {
    throw new Error(
      `Refusing to run mutating suite "${suite}" against target "${target.name}" (${target.url}). ` +
        `Target "${target.name}" is guarded for non-destructive smoke runs only. ` +
        `Use "--target staging" for release/explore suites, or pass "--allow-mutations" explicitly if intended.`,
    );
  }
}
