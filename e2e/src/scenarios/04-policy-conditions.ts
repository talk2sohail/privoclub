import { waitForAuditCall } from "../privo/oracles.js";
import { makeCell, type CellResult, type Scenario } from "./types.js";

/**
 * Scenario 04: Conditional argument rules (ADR 0027), deny rule non-leakage,
 * YAML export & dry-run import validation (rejects invalid op).
 */
export const scenario04PolicyConditions: Scenario = {
  id: "04-policy-conditions",
  title: "Conditional Argument Rules & Policy YAML",
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
          summary: "Skipped: no primary server established",
        }),
      ];
    }

    const start = Date.now();
    const mark = ctx.proxy.mark();
    ctx.proxy.setClientTag("mcp-sdk");

    const { conditionArg, conditionDenyValue, conditionAllowValue } = primary.spec.sampleCall;
    const secretNote = `probe-internal-note-${ctx.fixtures.runId}`;
    const condRule = await ctx.fixtures.createPolicy({
      server: alias,
      tool: toolName,
      action: "deny",
      note: secretNote,
      conditions: [{ arg: conditionArg, op: "equals", value: conditionDenyValue }],
    });

    try {
      await ctx.sdk.configure({ mcpUrl: ctx.proxy.mcpUrl, bearerToken: token });
      const sinceIso = new Date(Date.now() - 2000).toISOString();

      // 1. Call with denied condition value -> Denied, note/id must NOT leak to model
      const deniedArgs = { ...primary.spec.sampleCall.args, [conditionArg]: conditionDenyValue };
      const deniedRes = await ctx.sdk.callTool(fullToolName, deniedArgs);
      if (!deniedRes.isError || !deniedRes.text.toLowerCase().includes("denied")) {
        throw new Error(`Expected deny for ${conditionArg}=${conditionDenyValue}, got: ${deniedRes.text}`);
      }
      if (deniedRes.text.includes(secretNote) || deniedRes.text.includes(condRule.id)) {
        throw new Error(`Policy note or ID leaked into model error text: ${deniedRes.text}`);
      }
      await waitForAuditCall(ctx.api, {
        server: alias,
        item: toolName,
        action: "tools/call",
        decisionPrefix: "deny",
        outcomePrefix: "denied",
        sinceIso,
      });

      // 2. Call with allowed condition value -> Succeeds
      const allowedArgs = { ...primary.spec.sampleCall.args, [conditionArg]: conditionAllowValue };
      const allowedRes = await ctx.sdk.callTool(fullToolName, allowedArgs);
      if (allowedRes.isError) {
        throw new Error(`Expected allowed call for ${conditionArg}=${conditionAllowValue}, got error: ${allowedRes.text}`);
      }

      // 3. Policy YAML export contains our conditional rule & dry-run rejects bogus op
      const exportedYaml = await ctx.api.exportPoliciesYaml();
      if (!exportedYaml.includes(alias) || !exportedYaml.includes(conditionDenyValue)) {
        throw new Error("Exported policy YAML did not include the conditional rule");
      }

      const dryRunOk = await ctx.api.importPoliciesYaml(exportedYaml, "merge", true);
      if (!dryRunOk) {
        throw new Error("Policy YAML merge dry-run returned empty response");
      }

      const badImport = await ctx.api.raw("POST", "/api/v1/policies/import", {
        yaml: `version: 1\ndefault: allow\nrules:\n  - server: ${alias}\n    tool: ${toolName}\n    action: deny\n    conditions:\n      - arg: ${conditionArg}\n        op: regex\n        value: ".*"\n`,
        mode: "merge",
        dry_run: true,
      });
      if (badImport.status < 400) {
        throw new Error(`Expected 4xx for invalid op: regex in YAML import, got ${badImport.status}`);
      }

      return [
        makeCell(this, "mcp-sdk", {
          status: "PASS",
          durationMs: Date.now() - start,
          summary: `Conditional deny (${conditionArg}=${conditionDenyValue}) enforced with no leak; ${conditionAllowValue} allowed; YAML export/import validated`,
          violations: ctx.proxy.violationsSince(mark),
        }),
      ];
    } catch (err) {
      return [
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
              summary: "Conditional policy / YAML verification failed",
              detail: err instanceof Error ? err.message : String(err),
            },
          ],
        }),
      ];
    } finally {
      await ctx.api.deletePolicy(condRule.id).catch(() => {});
    }
  },
};
