import { eventually, waitForAuditCall } from "../privo/oracles.js";
import { makeCell, type CellResult, type Scenario } from "./types.js";

/**
 * Scenario 02: Connect real upstream MCP server, verify tool pinning lifecycle
 * (pending hidden -> accept -> visible -> block -> hidden -> accept again),
 * dual MCP protocol version validation (2025-11-25 & 2026-07-28), and live
 * tool execution across EVERY configured AI client wrapper.
 */
export const scenario02ConnectPinning: Scenario = {
  id: "02-connect-pinning",
  title: "Real Upstream Connect, Pinning & Live Tool Call",
  suites: ["smoke", "release"],
  mutatesTarget: true,

  async run(ctx): Promise<CellResult[]> {
    const results: CellResult[] = [];
    const primary = ctx.upstreams.primaryNoAuth;

    if (!primary) {
      return [
        makeCell(this, "control-plane", {
          status: "UPSTREAM_UNAVAILABLE",
          durationMs: 0,
          summary: "All configured real no-auth upstreams failed direct pre-flight health check",
          findings: [
            {
              scenarioId: this.id,
              clientId: "control-plane",
              category: "UPSTREAM_UNAVAILABLE",
              summary: "No healthy real upstream available in noAuthPool",
              detail: JSON.stringify(ctx.upstreams.noAuthResults, null, 2),
            },
          ],
        }),
      ];
    }

    // On read-only prod smoke (allowMutations === false), reuse an existing healthy server if present
    let alias: string;
    let gatewayToken: string;
    let toolName = primary.spec.sampleCall.tool;
    let callArgs = primary.spec.sampleCall.args;

    if (!ctx.target.allowMutations) {
      const existingServers = await ctx.api.listServers();
      const ready = existingServers.find((s) => s.enabled && s.status === "up" && (s.items.tool ?? 0) > 0);
      if (!ready || !ctx.target.memberGatewayToken) {
        return [
          makeCell(this, "control-plane", {
            status: "SKIP",
            durationMs: 0,
            summary:
              "Read-only prod smoke requires an existing connected server and PRIVO_PROD_MEMBER_TOKEN",
          }),
        ];
      }
      alias = ready.alias;
      gatewayToken = ctx.target.memberGatewayToken;
    } else {
      // Provision fresh real upstream + temporary gateway token on staging
      const srv = await ctx.fixtures.createServer({
        shortCode: primary.spec.shortCode,
        transport: primary.spec.transport,
        url: primary.spec.url,
      });
      alias = srv.alias;

      const tok = await ctx.fixtures.createToken({
        label: "gw-member",
        role: "member",
        kind: "gateway",
        person: ctx.target.memberPerson || "probe-member",
      });
      gatewayToken = tok.token;
      ctx.proxy.addSecretToScrub(gatewayToken);
      ctx.shared.memberGatewayToken = gatewayToken;
      ctx.shared.memberTokenId = tok.id;

      // Wait for real upstream discovery
      const pins = await ctx.fixtures.waitForServerUpAndPins(alias);
      const toolPins = pins.filter((p) => p.kind === "tool");
      if (toolPins.length === 0) {
        return [
          makeCell(this, "control-plane", {
            status: "FAIL",
            durationMs: 0,
            summary: `Real upstream ${primary.spec.name} connected but yielded 0 tool pins`,
            findings: [
              {
                scenarioId: this.id,
                clientId: "control-plane",
                category: "P0_BLOCKER",
                summary: `Privo reported ${alias} up with 0 tools while direct probe saw ${primary.health.toolNames.length}`,
              },
            ],
          }),
        ];
      }

      // Pinning verification (release suite): unaccepted tools MUST be hidden before acceptance
      ctx.proxy.setClientTag("mcp-sdk");
      await ctx.sdk.configure({ mcpUrl: ctx.proxy.mcpUrl, bearerToken: gatewayToken });
      const beforeAccept = await ctx.sdk.listTools();
      const leakedPending = beforeAccept.filter((n) => n.startsWith(`${alias}__`));
      if (leakedPending.length > 0) {
        return [
          makeCell(this, "mcp-sdk", {
            status: "FAIL",
            durationMs: 0,
            summary: `Unaccepted pending tools leaked to client: ${leakedPending.join(", ")}`,
            findings: [
              {
                scenarioId: this.id,
                clientId: "mcp-sdk",
                category: "P0_BLOCKER",
                summary: "Pending unreviewed tools were exposed on tools/list",
              },
            ],
          }),
        ];
      }

      // Pick the sample tool if present, otherwise first discovered tool
      const samplePin = toolPins.find((p) => p.name === toolName) ?? toolPins[0]!;
      toolName = samplePin.name;
      if (samplePin.name !== primary.spec.sampleCall.tool) {
        callArgs = {};
      }

      // Accept sample tool; if a second tool exists, block it then re-accept it during release suite
      await ctx.api.acceptPins(alias, [{ kind: samplePin.kind, name: samplePin.name, hash: samplePin.hash }]);

      if (ctx.suite === "release" && toolPins.length >= 2) {
        const secondPin = toolPins.find((p) => p.name !== samplePin.name)!;
        await ctx.api.blockPins(alias, [{ kind: secondPin.kind, name: secondPin.name, hash: secondPin.hash }]);
        const afterBlock = await ctx.sdk.listTools();
        if (afterBlock.includes(`${alias}__${secondPin.name}`)) {
          return [
            makeCell(this, "mcp-sdk", {
              status: "FAIL",
              durationMs: 0,
              summary: `Blocked tool ${alias}__${secondPin.name} appeared in tools/list`,
              findings: [
                {
                  scenarioId: this.id,
                  clientId: "mcp-sdk",
                  category: "P0_BLOCKER",
                  summary: "Blocked pin leaked into tools/list",
                },
              ],
            }),
          ];
        }
        // Accept remaining tools so later scenarios (toolsets, approvals) have multiple tools
        await ctx.fixtures.acceptAllPins(alias);
      }
    }

    const fullToolName = `${alias}__${toolName}`;
    ctx.shared.primaryServerAlias = alias;
    ctx.shared.primaryToolName = toolName;
    ctx.shared.primaryFullToolName = fullToolName;

    // ---- 1. Deterministic MCP SDK client + dual protocol version wire check (2025-11-25 & 2026-07-28)
    {
      const start = Date.now();
      const mark = ctx.proxy.mark();
      ctx.proxy.setClientTag("mcp-sdk");
      try {
        await ctx.sdk.configure({ mcpUrl: ctx.proxy.mcpUrl, bearerToken: gatewayToken });
        await eventually(
          `${fullToolName} visible in tools/list`,
          async () => {
            const tools = await ctx.sdk.listTools();
            return tools.includes(fullToolName) ? tools : null;
          },
          { timeoutMs: 12_000 },
        );

        // Verify raw responses on BOTH MCP 2025-11-25 and 2026-07-28 through Wire-Inspector Proxy
        for (const pv of ["2025-11-25", "2026-07-28"] as const) {
          await ctx.sdk.rawJsonRpc("tools/list", {}, pv);
        }

        const sinceIso = new Date(Date.now() - 2000).toISOString();
        const callRes = await ctx.sdk.callTool(fullToolName, callArgs);
        if (callRes.isError) {
          throw new Error(`Expected successful call to ${fullToolName}, got error: ${callRes.text}`);
        }

        const auditRow = await waitForAuditCall(ctx.api, {
          server: alias,
          item: toolName,
          action: "tools/call",
          decisionPrefix: "allow",
          outcomePrefix: "ok",
          sinceIso,
        });

        const violations = ctx.proxy.violationsSince(mark);
        results.push(
          makeCell(this, "mcp-sdk", {
            status: "PASS",
            durationMs: Date.now() - start,
            summary: `Listed & called ${fullToolName} (${primary.spec.name}); audited (${auditRow.latency_ms ?? 0}ms); 2025-11-25 & 2026-07-28 valid`,
            violations,
            wireSample: ctx.proxy.recordsSince(mark).slice(-6),
          }),
        );
      } catch (err) {
        results.push(
          makeCell(this, "mcp-sdk", {
            status: "FAIL",
            durationMs: Date.now() - start,
            summary: err instanceof Error ? err.message : String(err),
            violations: ctx.proxy.violationsSince(mark),
            wireSample: ctx.proxy.recordsSince(mark).slice(-6),
            findings: [
              {
                scenarioId: this.id,
                clientId: "mcp-sdk",
                category: "P0_BLOCKER",
                summary: `MCP SDK connect/call failed on ${fullToolName}`,
                detail: err instanceof Error ? err.message : String(err),
              },
            ],
          }),
        );
      }
    }

    // ---- 2. Every real CLI AI client wrapper (Claude Code, Codex, Gemini CLI)
    for (const client of ctx.cliClients) {
      const start = Date.now();
      const mark = ctx.proxy.mark();
      ctx.proxy.setClientTag(client.id);
      try {
        await client.configure({ mcpUrl: ctx.proxy.mcpUrl, bearerToken: gatewayToken });
        const prompt =
          `Use the Privo MCP tool named "${fullToolName}" with arguments ${JSON.stringify(callArgs)}. ` +
          `Summarize the result in 2 short sentences. Do not invent data if the tool fails.`;

        const res = await client.runPrompt(prompt, { timeoutMs: 90_000 });
        const wireRecords = ctx.proxy.recordsSince(mark);
        const violations = ctx.proxy.violationsSince(mark);
        const toolWireCalls = wireRecords.filter((r) => r.rpcMethod === "tools/call" && r.toolName === fullToolName);

        if (!res.ok) {
          results.push(
            makeCell(this, client.id, {
              status: "FAIL",
              durationMs: Date.now() - start,
              summary: `${client.name} exited with error: ${res.error ?? res.exitCode}`,
              violations,
              wireSample: wireRecords.slice(-6),
              findings: [
                {
                  scenarioId: this.id,
                  clientId: client.id,
                  category: "P0_BLOCKER",
                  summary: `${client.name} failed during live MCP call`,
                  detail: res.error || res.rawStderr.slice(0, 500),
                },
              ],
            }),
          );
          continue;
        }

        if (toolWireCalls.length === 0) {
          results.push(
            makeCell(this, client.id, {
              status: "UX_GAP",
              durationMs: Date.now() - start,
              summary: `${client.name} completed without invoking ${fullToolName} over MCP`,
              violations,
              wireSample: wireRecords.slice(-6),
              findings: [
                {
                  scenarioId: this.id,
                  clientId: client.id,
                  category: "UX_CLIENT_GAP",
                  summary: `${client.name} did not issue tools/call for ${fullToolName}`,
                  detail: `Output: ${res.outputText.slice(0, 400)}`,
                },
              ],
            }),
          );
          continue;
        }

        const succeeded = toolWireCalls.some((c) => c.httpStatus === 200 && c.isToolError !== true);
        results.push(
          makeCell(this, client.id, {
            status: succeeded ? "PASS" : "FAIL",
            durationMs: Date.now() - start,
            summary: succeeded
              ? `${client.name} connected & executed ${fullToolName} (${toolWireCalls.length} wire call(s), proto=${toolWireCalls[0]?.protocolVersion})`
              : `${client.name} called ${fullToolName} but Privo returned an error`,
            violations,
            wireSample: wireRecords.slice(-6),
            findings: succeeded
              ? []
              : [
                  {
                    scenarioId: this.id,
                    clientId: client.id,
                    category: "P0_BLOCKER",
                    summary: `Live call through ${client.name} returned tool error`,
                    detail: toolWireCalls[0]?.toolResultPreview,
                  },
                ],
          }),
        );
      } catch (err) {
        results.push(
          makeCell(this, client.id, {
            status: "FAIL",
            durationMs: Date.now() - start,
            summary: err instanceof Error ? err.message : String(err),
            violations: ctx.proxy.violationsSince(mark),
            findings: [
              {
                scenarioId: this.id,
                clientId: client.id,
                category: "P0_BLOCKER",
                summary: `${client.name} adapter threw exception`,
                detail: err instanceof Error ? err.message : String(err),
              },
            ],
          }),
        );
      } finally {
        await client.teardown();
      }
    }

    return results;
  },
};
