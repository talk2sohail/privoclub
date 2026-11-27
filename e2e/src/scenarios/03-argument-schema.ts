import { waitForAuditCall } from "../privo/oracles.js";
import { makeCell, type CellResult, type Scenario } from "./types.js";

/**
 * Scenario 03 (v0.20.0): Arguments checked against the accepted JSON Schema.
 *
 * Verifies:
 * 1. Wrong-type argument is refused BEFORE policy/approval (`refused:invalid_arguments`).
 * 2. Even under an `ask` rule, NO approval request is created for invalid arguments.
 * 3. Real AI clients receive a clear error message and self-correct on retry!
 */
export const scenario03ArgumentSchema: Scenario = {
  id: "03-argument-schema",
  title: "Argument Schema Validation & AI Self-Correction (v0.20.0)",
  suites: ["release"],
  mutatesTarget: true,

  async run(ctx): Promise<CellResult[]> {
    const primary = ctx.upstreams.primaryNoAuth;
    const alias = ctx.shared.primaryServerAlias;
    const toolName = ctx.shared.primaryToolName;
    const fullToolName = ctx.shared.primaryFullToolName;
    const token = ctx.shared.memberGatewayToken;

    if (!primary || !alias || !toolName || !fullToolName || !token) {
      return [
        makeCell(this, "mcp-sdk", {
          status: "SKIP",
          durationMs: 0,
          summary: "Skipped: Scenario 02 did not establish a primary server & token",
        }),
      ];
    }

    const results: CellResult[] = [];

    // Add a temporary `ask` rule so we prove invalid args are refused BEFORE approvals
    const askRule = await ctx.fixtures.createPolicy({
      server: alias,
      tool: toolName,
      action: "ask",
    });

    // ---- 1. Deterministic SDK check
    {
      const start = Date.now();
      const mark = ctx.proxy.mark();
      ctx.proxy.setClientTag("mcp-sdk");
      try {
        await ctx.sdk.configure({ mcpUrl: ctx.proxy.mcpUrl, bearerToken: token });
        const beforeApprovals = (await ctx.api.listApprovals("pending")).filter((a) => a.server === alias).length;
        const sinceIso = new Date(Date.now() - 2000).toISOString();

        const res = await ctx.sdk.callTool(fullToolName, primary.spec.sampleCall.invalidArgs);
        if (!res.isError || !res.text.toLowerCase().includes("input schema")) {
          throw new Error(`Expected input-schema refusal, got isError=${res.isError} text=${res.text}`);
        }

        await waitForAuditCall(ctx.api, {
          server: alias,
          item: toolName,
          action: "tools/call",
          decisionPrefix: "refused:invalid_arguments",
          outcomePrefix: "refused",
          sinceIso,
        });

        const afterApprovals = (await ctx.api.listApprovals("pending")).filter((a) => a.server === alias).length;
        if (afterApprovals !== beforeApprovals) {
          throw new Error(`Invalid arguments created an approval request (${beforeApprovals} -> ${afterApprovals})`);
        }

        results.push(
          makeCell(this, "mcp-sdk", {
            status: "PASS",
            durationMs: Date.now() - start,
            summary: `Invalid args refused with schema error, audited as refused:invalid_arguments, 0 approvals created`,
            violations: ctx.proxy.violationsSince(mark),
            wireSample: ctx.proxy.recordsSince(mark).slice(-4),
          }),
        );
      } catch (err) {
        results.push(
          makeCell(this, "mcp-sdk", {
            status: "FAIL",
            durationMs: Date.now() - start,
            summary: err instanceof Error ? err.message : String(err),
            violations: ctx.proxy.violationsSince(mark),
            findings: [
              {
                scenarioId: this.id,
                clientId: "mcp-sdk",
                category: "P0_BLOCKER",
                summary: "Argument schema validation check failed",
                detail: err instanceof Error ? err.message : String(err),
              },
            ],
          }),
        );
      } finally {
        await ctx.api.deletePolicy(askRule.id).catch(() => {});
      }
    }

    // ---- 2. Real CLI AI client self-correction check (without ask rule)
    for (const client of ctx.cliClients) {
      const start = Date.now();
      const mark = ctx.proxy.mark();
      ctx.proxy.setClientTag(client.id);
      try {
        await client.configure({ mcpUrl: ctx.proxy.mcpUrl, bearerToken: token });
        const prompt =
          `First call "${fullToolName}" with exact arguments ${JSON.stringify(primary.spec.sampleCall.invalidArgs)}. ` +
          `If the gateway refuses because of the input schema, correct the arguments to ${JSON.stringify(primary.spec.sampleCall.args)} and retry.`;
        const res = await client.runPrompt(prompt, { timeoutMs: 90_000 });

        const calls = ctx.proxy
          .recordsSince(mark)
          .filter((r) => r.rpcMethod === "tools/call" && r.toolName === fullToolName);

        if (!res.ok && calls.length === 0) {
          results.push(
            makeCell(this, client.id, {
              status: "FAIL",
              durationMs: Date.now() - start,
              summary: `${client.name} exited before calling MCP: ${res.error ?? res.exitCode}`,
              violations: ctx.proxy.violationsSince(mark),
              findings: [
                {
                  scenarioId: this.id,
                  clientId: client.id,
                  category: "P0_BLOCKER",
                  summary: `${client.name} failed before MCP tool invocation`,
                  detail: res.error || res.rawStderr.slice(0, 500),
                },
              ],
            }),
          );
          continue;
        }

        const sawRefusal = calls.some((c) => c.isToolError && (c.toolResultPreview || "").toLowerCase().includes("schema"));
        const sawSuccess = calls.some((c) => c.isToolError !== true && c.httpStatus === 200);

        if (sawRefusal && sawSuccess) {
          results.push(
            makeCell(this, client.id, {
              status: "PASS",
              durationMs: Date.now() - start,
              summary: `${client.name} received schema refusal and self-corrected on retry (${calls.length} calls)`,
              violations: ctx.proxy.violationsSince(mark),
            }),
          );
        } else if (sawSuccess) {
          results.push(
            makeCell(this, client.id, {
              status: "PASS",
              durationMs: Date.now() - start,
              summary: `${client.name} pre-validated the schema client-side and sent valid args directly`,
              violations: ctx.proxy.violationsSince(mark),
            }),
          );
        } else {
          results.push(
            makeCell(this, client.id, {
              status: "UX_GAP",
              durationMs: Date.now() - start,
              summary: `${client.name} saw refusal=${sawRefusal} but did not recover (${calls.length} calls)`,
              violations: ctx.proxy.violationsSince(mark),
              findings: [
                {
                  scenarioId: this.id,
                  clientId: client.id,
                  category: "UX_CLIENT_GAP",
                  summary: `${client.name} did not self-correct after schema error`,
                },
              ],
            }),
          );
        }
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

    return results;
  },
};
