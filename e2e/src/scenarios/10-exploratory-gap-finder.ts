import { makeCell, type CellResult, type Scenario } from "./types.js";

/**
 * Scenario 10: Exploratory / Gap-Finder Mode.
 *
 * Gives real AI clients open-ended, realistic user goals (without hand-holding)
 * against a Privo gateway configured with real policies (an allowed repo and a
 * denied repo) and inspects wire traces + transcripts to flag UX friction,
 * excessive retry loops, confusing error phrasing, or client quirks.
 */
export const scenario10ExploratoryGapFinder: Scenario = {
  id: "10-exploratory-gap-finder",
  title: "Exploratory Real-Client Gap & UX Friction Hunter",
  suites: ["explore"],
  mutatesTarget: true,

  async run(ctx): Promise<CellResult[]> {
    const primary = ctx.upstreams.primaryNoAuth;
    if (!primary) {
      return [
        makeCell(this, "control-plane", {
          status: "UPSTREAM_UNAVAILABLE",
          durationMs: 0,
          summary: "No healthy real upstream available for exploratory run",
        }),
      ];
    }

    if (ctx.cliClients.length === 0) {
      return [
        makeCell(this, "control-plane", {
          status: "SKIP",
          durationMs: 0,
          summary: "No CLI AI clients (claude-code, codex, gemini) available for exploratory gap hunting",
        }),
      ];
    }

    // Ensure a real server + token + conditional deny rule exist for exploratory probing
    let alias = ctx.shared.primaryServerAlias;
    let toolName = ctx.shared.primaryToolName;
    let gatewayToken = ctx.shared.memberGatewayToken;

    if (!alias || !toolName || !gatewayToken) {
      const srv = await ctx.fixtures.createServer({
        shortCode: primary.spec.shortCode,
        transport: primary.spec.transport,
        url: primary.spec.url,
      });
      alias = srv.alias;
      const pins = await ctx.fixtures.acceptAllPins(alias);
      toolName = pins.find((p) => p.name === primary.spec.sampleCall.tool)?.name ?? pins[0]?.name ?? primary.spec.sampleCall.tool;

      const tok = await ctx.fixtures.createToken({
        label: "explore-member",
        role: "member",
        kind: "gateway",
        person: ctx.target.memberPerson || "probe-explorer",
      });
      gatewayToken = tok.token;
      ctx.proxy.addSecretToScrub(gatewayToken);
    }

    const { conditionArg, conditionDenyValue, conditionAllowValue } = primary.spec.sampleCall;
    const denyRule = await ctx.fixtures.createPolicy({
      server: alias,
      tool: toolName,
      action: "deny",
      conditions: [{ arg: conditionArg, op: "equals", value: conditionDenyValue }],
    });

    const results: CellResult[] = [];
    try {
      for (const client of ctx.cliClients) {
        const start = Date.now();
        const mark = ctx.proxy.mark();
        ctx.proxy.setClientTag(client.id);

        try {
          await client.configure({ mcpUrl: ctx.proxy.mcpUrl, bearerToken: gatewayToken });
          const openEndedPrompt =
            `You are connected to our company's Privo MCP gateway. ` +
            `Please look up "${conditionDenyValue}" AND "${conditionAllowValue}" using the available documentation/wiki tools. ` +
            `If either request is blocked by company policy, do not keep retrying it blindly—tell me clearly what succeeded, what was blocked, and why.`;

          const res = await client.runPrompt(openEndedPrompt, { timeoutMs: 120_000 });
          const wire = ctx.proxy.recordsSince(mark);
          const violations = ctx.proxy.violationsSince(mark);
          const toolCalls = wire.filter((r) => r.rpcMethod === "tools/call");
          const deniedCalls = toolCalls.filter((c) => c.isToolError && (c.toolResultPreview || "").toLowerCase().includes("denied"));
          const okCalls = toolCalls.filter((c) => c.isToolError !== true && c.httpStatus === 200);

          const findings: CellResult["findings"] = [];

          // Detect retry loops on policy denial (classic UX gap when error wording isn't decisive)
          if (deniedCalls.length >= 3) {
            findings.push({
              scenarioId: this.id,
              clientId: client.id,
              category: "UX_CLIENT_GAP",
              summary: `${client.name} looped ${deniedCalls.length} times against a Denied policy response`,
              detail: `Consider tightening Privo's deny message so models stop retrying blocked arguments.`,
            });
          }
          if (okCalls.length === 0) {
            findings.push({
              scenarioId: this.id,
              clientId: client.id,
              category: "UX_CLIENT_GAP",
              summary: `${client.name} never completed the allowed lookup (${conditionAllowValue})`,
              detail: res.outputText.slice(0, 400),
            });
          }
          if (deniedCalls.length === 0) {
            findings.push({
              scenarioId: this.id,
              clientId: client.id,
              category: "UX_CLIENT_GAP",
              summary: `${client.name} did not attempt the denied lookup (${conditionDenyValue})`,
            });
          }

          const hasGap = findings.length > 0;
          results.push(
            makeCell(this, client.id, {
              status: !res.ok ? "FAIL" : hasGap ? "UX_GAP" : "PASS",
              durationMs: Date.now() - start,
              summary: hasGap
                ? findings.map((f) => f.summary).join("; ")
                : `${client.name} handled mixed deny+allow cleanly (${okCalls.length} ok, ${deniedCalls.length} denied, no retry loop)`,
              violations,
              wireSample: wire.slice(-8),
              findings,
            }),
          );
        } catch (err) {
          results.push(
            makeCell(this, client.id, {
              status: "FAIL",
              durationMs: Date.now() - start,
              summary: err instanceof Error ? err.message : String(err),
              violations: ctx.proxy.violationsSince(mark),
            }),
          );
        } finally {
          await client.teardown();
        }
      }
    } finally {
      await ctx.api.deletePolicy(denyRule.id).catch(() => {});
    }

    return results;
  },
};
