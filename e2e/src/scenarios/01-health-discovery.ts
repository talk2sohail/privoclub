import { makeCell, type CellResult, type Scenario } from "./types.js";

/**
 * Scenario 01: Health endpoints, version, root page, and OAuth 2.1 discovery
 * metadata (/.well-known/*) required by Claude Code, Claude Web, ChatGPT, VS Code, etc.
 */
export const scenario01HealthDiscovery: Scenario = {
  id: "01-health-discovery",
  title: "Health, Version & OAuth Discovery",
  suites: ["smoke", "release"],
  mutatesTarget: false,

  async run(ctx): Promise<CellResult[]> {
    const start = Date.now();
    const findings: CellResult["findings"] = [];
    const notes: string[] = [];

    // 1. /livez, /readyz, /version
    const [livez, readyz, version, root] = await Promise.all([
      ctx.api.livez(),
      ctx.api.readyz(),
      ctx.api.version(),
      ctx.api.rootPage(),
    ]);

    if (livez.status !== 200) {
      findings.push({
        scenarioId: this.id,
        clientId: "control-plane",
        category: "P0_BLOCKER",
        summary: `/livez returned HTTP ${livez.status}`,
        detail: livez.text.slice(0, 300),
      });
    }
    if (readyz.status !== 200) {
      findings.push({
        scenarioId: this.id,
        clientId: "control-plane",
        category: "P0_BLOCKER",
        summary: `/readyz returned HTTP ${readyz.status}`,
        detail: readyz.text.slice(0, 300),
      });
    }
    if (version.status !== 200 || !version.json || typeof (version.json as Record<string, unknown>).version !== "string") {
      findings.push({
        scenarioId: this.id,
        clientId: "control-plane",
        category: "P0_BLOCKER",
        summary: `/version returned invalid payload (${version.status})`,
        detail: version.text.slice(0, 300),
      });
    } else {
      notes.push(`v${String((version.json as Record<string, unknown>).version)}`);
    }

    if (root.status !== 200 || !root.text.includes("/mcp")) {
      findings.push({
        scenarioId: this.id,
        clientId: "control-plane",
        category: "P0_BLOCKER",
        summary: `Root page / missing expected /mcp info (${root.status})`,
      });
    }

    // 2. Unauthenticated /mcp must return 401 with WWW-Authenticate pointing to protected resource metadata
    const unauthMcp = await ctx.api.raw("POST", "/mcp", { jsonrpc: "2.0", id: 1, method: "initialize", params: {} }, { auth: false });
    if (unauthMcp.status !== 401) {
      findings.push({
        scenarioId: this.id,
        clientId: "control-plane",
        category: "P0_BLOCKER",
        summary: `Unauthenticated POST /mcp returned ${unauthMcp.status}, expected 401`,
      });
    } else {
      const wwwAuth = unauthMcp.headers.get("www-authenticate") || "";
      if (!wwwAuth.toLowerCase().includes("resource_metadata")) {
        findings.push({
          scenarioId: this.id,
          clientId: "control-plane",
          category: "UX_CLIENT_GAP",
          summary: "401 on /mcp missing resource_metadata in WWW-Authenticate header",
          detail: `WWW-Authenticate: ${wwwAuth}`,
        });
      }
    }

    // 3. OAuth protected-resource & authorization-server metadata (hosted clients rely on this)
    const prm = await ctx.api.oauthProtectedResource();
    if (prm.status !== 200 || !prm.json || typeof prm.json !== "object") {
      findings.push({
        scenarioId: this.id,
        clientId: "control-plane",
        category: "P0_BLOCKER",
        summary: `/.well-known/oauth-protected-resource/mcp returned ${prm.status}`,
      });
    } else if (ctx.target.oauthExpected) {
      const authServers = (prm.json as Record<string, unknown>).authorization_servers;
      if (!Array.isArray(authServers) || authServers.length === 0) {
        findings.push({
          scenarioId: this.id,
          clientId: "control-plane",
          category: "P0_BLOCKER",
          summary: "OAuth expected on target, but authorization_servers is empty in protected resource metadata",
          detail: JSON.stringify(prm.json),
        });
      } else {
        const asMeta = await ctx.api.oauthAuthorizationServer();
        if (asMeta.status !== 200 || !asMeta.json) {
          findings.push({
            scenarioId: this.id,
            clientId: "control-plane",
            category: "P0_BLOCKER",
            summary: `/.well-known/oauth-authorization-server returned ${asMeta.status}`,
          });
        } else {
          const meta = asMeta.json as Record<string, unknown>;
          notes.push(`oauth=on`);
          if (ctx.target.dynamicRegistrationExpected && !meta.registration_endpoint) {
            findings.push({
              scenarioId: this.id,
              clientId: "control-plane",
              category: "UX_CLIENT_GAP",
              summary: "Dynamic client registration expected (Cursor/Gemini CLI), but registration_endpoint absent",
            });
          }
        }
      }
    }

    // 4. Verify metrics are NOT publicly exposed
    const metrics = await ctx.api.raw("GET", "/metrics", undefined, { auth: false });
    if (metrics.status === 200 && metrics.text.includes("privo_mcp_calls_total")) {
      findings.push({
        scenarioId: this.id,
        clientId: "control-plane",
        category: "P0_BLOCKER",
        summary: "Public /metrics endpoint exposed Prometheus metrics on public URL",
      });
    }

    const hasBlocker = findings.some((f) => f.category === "P0_BLOCKER");
    const hasGap = findings.some((f) => f.category === "UX_CLIENT_GAP");
    return [
      makeCell(this, "control-plane", {
        status: hasBlocker ? "FAIL" : hasGap ? "UX_GAP" : "PASS",
        durationMs: Date.now() - start,
        summary: hasBlocker
          ? findings.find((f) => f.category === "P0_BLOCKER")!.summary
          : `Healthy (${notes.join(", ")})`,
        findings,
      }),
    ];
  },
};
