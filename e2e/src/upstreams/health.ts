import { Client } from "@modelcontextprotocol/sdk/client/index.js";
import { StreamableHTTPClientTransport } from "@modelcontextprotocol/sdk/client/streamableHttp.js";
import type { NoAuthUpstreamSpec, ResolvedTarget } from "../config.js";

export type UpstreamHealthResult = {
  id: string;
  name: string;
  url: string;
  healthy: boolean;
  latencyMs: number;
  toolNames: string[];
  promptNames: string[];
  resourceUris: string[];
  error?: string;
};

/**
 * Probes a real remote MCP server directly (bypassing Privo) to verify it is
 * reachable before running scenarios and to distinguish third-party outages
 * from Privo regressions.
 */
export async function probeRemoteMcpServer(spec: {
  id: string;
  name: string;
  url: string;
  headers?: Record<string, string>;
  timeoutMs?: number;
}): Promise<UpstreamHealthResult> {
  const start = Date.now();
  const client = new Client({ name: "privo-probe-health", version: "0.1.0" });
  const transport = new StreamableHTTPClientTransport(new URL(spec.url), {
    requestInit: {
      headers: spec.headers,
      signal: AbortSignal.timeout(spec.timeoutMs ?? 12_000),
    },
  });

  try {
    await client.connect(transport);
    const toolsRes = await client.listTools().catch(() => ({ tools: [] }));
    const promptsRes = await client.listPrompts().catch(() => ({ prompts: [] }));
    const resourcesRes = await client.listResources().catch(() => ({ resources: [] }));

    return {
      id: spec.id,
      name: spec.name,
      url: spec.url,
      healthy: true,
      latencyMs: Date.now() - start,
      toolNames: toolsRes.tools.map((t) => t.name),
      promptNames: promptsRes.prompts.map((p) => p.name),
      resourceUris: resourcesRes.resources.map((r) => r.uri),
    };
  } catch (err) {
    return {
      id: spec.id,
      name: spec.name,
      url: spec.url,
      healthy: false,
      latencyMs: Date.now() - start,
      toolNames: [],
      promptNames: [],
      resourceUris: [],
      error: err instanceof Error ? err.message : String(err),
    };
  } finally {
    await client.close().catch(() => {});
  }
}

export type UpstreamPoolStatus = {
  noAuthResults: UpstreamHealthResult[];
  primaryNoAuth: { spec: NoAuthUpstreamSpec; health: UpstreamHealthResult } | null;
  secondaryNoAuth: { spec: NoAuthUpstreamSpec; health: UpstreamHealthResult } | null;
  personalKeyHealth?: UpstreamHealthResult;
  promptsResourcesHealth?: UpstreamHealthResult;
};

export async function probeAllUpstreams(target: ResolvedTarget): Promise<UpstreamPoolStatus> {
  const noAuthResults = await Promise.all(
    target.upstreams.noAuthPool.map((u) => probeRemoteMcpServer({ id: u.id, name: u.name, url: u.url })),
  );

  const healthyPairs: Array<{ spec: NoAuthUpstreamSpec; health: UpstreamHealthResult }> = [];
  for (let i = 0; i < target.upstreams.noAuthPool.length; i++) {
    const spec = target.upstreams.noAuthPool[i]!;
    const health = noAuthResults[i]!;
    if (health.healthy && health.toolNames.length > 0) {
      healthyPairs.push({ spec, health });
    }
  }

  let personalKeyHealth: UpstreamHealthResult | undefined;
  if (target.upstreams.personalKey && target.githubPatOwner) {
    const pk = target.upstreams.personalKey;
    personalKeyHealth = await probeRemoteMcpServer({
      id: pk.id,
      name: pk.name,
      url: pk.url,
      headers: { [pk.header]: `${pk.prefix}${target.githubPatOwner}` },
    });
  }

  let promptsResourcesHealth: UpstreamHealthResult | undefined;
  if (target.upstreams.promptsResources) {
    const pr = target.upstreams.promptsResources;
    promptsResourcesHealth = await probeRemoteMcpServer({
      id: pr.id,
      name: pr.name,
      url: pr.url,
    });
  }

  return {
    noAuthResults,
    primaryNoAuth: healthyPairs[0] ?? null,
    secondaryNoAuth: healthyPairs[1] ?? null,
    personalKeyHealth,
    promptsResourcesHealth,
  };
}
