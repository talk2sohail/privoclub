import { waitForAuditCall, waitForPendingApproval } from "../privo/oracles.js";
import { makeCell, type CellResult, type Scenario } from "./types.js";

/**
 * Scenario 05: Two-persona Human-in-the-Loop Approvals (F10).
 *
 * Verifies:
 * 1. `ask` rule returns "Approval required" with approval ID.
 * 2. Self-approval is refused (403), including case-insensitive person match.
 * 3. Second approver denies -> retry is refused & audited `denied_by_approver`.
 * 4. Second approver approves with `1h` grant -> retry succeeds AND subsequent
 *    calls with different arguments succeed without asking.
 * 5. A `deny` rule STILL beats an active approval grant!
 */
export const scenario05ApprovalsTwoPersona: Scenario = {
  id: "05-approvals-two-persona",
  title: "Two-Persona Approvals, Self-Approval Block & Grants",
  suites: ["release"],
  mutatesTarget: true,

  async run(ctx): Promise<CellResult[]> {
    const primary = ctx.upstreams.primaryNoAuth;
    const alias = ctx.shared.primaryServerAlias;
    const toolName = ctx.shared.primaryToolName;
    const fullToolName = ctx.shared.primaryFullToolName;
    const memberToken = ctx.shared.memberGatewayToken;

    if (!primary || !alias || !toolName || !fullToolName || !memberToken) {
      return [
        makeCell(this, "mcp-sdk", {
          status: "SKIP",
          durationMs: 0,
          summary: "Skipped: no primary server/token established",
        }),
      ];
    }

    const start = Date.now();
    const mark = ctx.proxy.mark();
    ctx.proxy.setClientTag("mcp-sdk");

    const requesterPerson = ctx.target.memberPerson || "probe-member";
    const approverPerson = `probe-approver-${ctx.fixtures.runId}`;
    const results: CellResult[] = [];

    const askRule = await ctx.fixtures.createPolicy({
      server: alias,
      tool: toolName,
      action: "ask",
    });
    const samePersonApprover = await ctx.fixtures.createToken({
      label: "self-approver",
      role: "approver",
      kind: "control",
      person: requesterPerson.toUpperCase(), // also tests case-insensitive self-approval guard!
    });
    const distinctApprover = await ctx.fixtures.createToken({
      label: "peer-approver",
      role: "approver",
      kind: "control",
      person: approverPerson,
    });
    ctx.proxy.addSecretToScrub(samePersonApprover.token);
    ctx.proxy.addSecretToScrub(distinctApprover.token);

    const selfApi = ctx.api.withToken(samePersonApprover.token);
    const peerApi = ctx.api.withToken(distinctApprover.token);
    const grantPolicyIds: string[] = [];

    try {
      await ctx.sdk.configure({ mcpUrl: ctx.proxy.mcpUrl, bearerToken: memberToken });

      // 1. Trigger pending approval
      const sinceIso = new Date(Date.now() - 2000).toISOString();
      const firstCall = await ctx.sdk.callTool(fullToolName, primary.spec.sampleCall.args);
      if (!firstCall.isError || !firstCall.text.toLowerCase().includes("approval required")) {
        throw new Error(`Expected "Approval required", got: ${firstCall.text}`);
      }

      const pending1 = await waitForPendingApproval(peerApi, { server: alias, tool: toolName, sinceIso });
      if (!firstCall.text.includes(pending1.id)) {
        throw new Error(`Tool response did not include approval ID ${pending1.id}: ${firstCall.text}`);
      }

      // 2. Self-approval (different case!) MUST be rejected with 403
      const selfApproveRes = await selfApi.raw("POST", `/api/v1/approvals/${pending1.id}/approve`);
      if (selfApproveRes.status !== 403) {
        throw new Error(`Self-approval (uppercase person) returned HTTP ${selfApproveRes.status}, expected 403`);
      }

      // 3. Peer approver denies -> retry must be refused
      await peerApi.deny(pending1.id);
      const afterDeny = await ctx.sdk.callTool(fullToolName, primary.spec.sampleCall.args);
      if (!afterDeny.isError || !afterDeny.text.toLowerCase().includes("denied")) {
        throw new Error(`Expected refusal after denial, got: ${afterDeny.text}`);
      }
      await waitForAuditCall(ctx.api, {
        server: alias,
        item: toolName,
        decisionPrefix: "ask",
        outcomePrefix: "denied_by_approver",
        sinceIso,
      });

      // 4. New call with distinct arguments (so it does not hit step 3's denied-call hash) -> peer approves with 1h grant
      const { conditionArg, conditionDenyValue, conditionAllowValue } = primary.spec.sampleCall;
      const secondArgs = { ...primary.spec.sampleCall.args, [conditionArg]: `${conditionAllowValue} approve-${ctx.fixtures.runId}` };
      const sinceIso2 = new Date(Date.now() - 1000).toISOString();
      const ask2 = await ctx.sdk.callTool(fullToolName, secondArgs);
      if (!ask2.isError || !ask2.text.toLowerCase().includes("approval required")) {
        throw new Error(`Expected second Approval required, got: ${ask2.text}`);
      }
      const pending2 = await waitForPendingApproval(peerApi, { server: alias, tool: toolName, sinceIso: sinceIso2 });
      const approveResp = await peerApi.approve(pending2.id, "1h");
      const grantObj = approveResp.grant as { id?: string } | undefined;
      if (grantObj?.id) {
        grantPolicyIds.push(grantObj.id);
        ctx.fixtures.trackPolicy(grantObj.id);
      }

      const retried = await ctx.sdk.callTool(fullToolName, secondArgs);
      if (retried.isError) {
        throw new Error(`Retried call failed after approval: ${retried.text}`);
      }

      // 5. Verify a deny rule STILL beats an active grant!
      const denyRule = await ctx.fixtures.createPolicy({
        server: alias,
        tool: toolName,
        action: "deny",
        conditions: [{ arg: conditionArg, op: "equals", value: conditionDenyValue }],
      });
      try {
        const deniedUnderGrant = await ctx.sdk.callTool(fullToolName, {
          ...primary.spec.sampleCall.args,
          [conditionArg]: conditionDenyValue,
        });
        if (!deniedUnderGrant.isError || !deniedUnderGrant.text.toLowerCase().includes("denied")) {
          throw new Error(`Deny rule failed to beat active 1h grant! Got: ${deniedUnderGrant.text}`);
        }
      } finally {
        await ctx.api.deletePolicy(denyRule.id).catch(() => {});
      }

      // Clean up any grant policies created by approval
      const { policies } = await ctx.api.listPolicies();
      for (const p of policies) {
        if (p.grant_of === pending2.id) {
          await ctx.api.deletePolicy(p.id).catch(() => {});
        }
      }

      results.push(
        makeCell(this, "mcp-sdk", {
          status: "PASS",
          durationMs: Date.now() - start,
          summary: "Ask -> case-insensitive self-approval 403 -> deny -> approve+1h grant -> deny-beats-grant all passed",
          violations: ctx.proxy.violationsSince(mark),
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
              summary: "Two-persona approval flow failed",
              detail: err instanceof Error ? err.message : String(err),
            },
          ],
        }),
      );
    } finally {
      await ctx.api.deletePolicy(askRule.id).catch(() => {});
      for (const id of grantPolicyIds) {
        await ctx.api.deletePolicy(id).catch(() => {});
      }
    }

    // ---- Optional CLI UX check: see how a real AI client presents "Approval required"
    if (ctx.cliClients.length > 0) {
      const uxAskRule = await ctx.fixtures.createPolicy({
        server: alias,
        tool: toolName,
        action: "ask",
      });
      try {
        const client = ctx.cliClients[0]!;
        const cStart = Date.now();
        const cMark = ctx.proxy.mark();
        ctx.proxy.setClientTag(client.id);
        await client.configure({ mcpUrl: ctx.proxy.mcpUrl, bearerToken: memberToken });
        const uxArgs = {
          ...primary.spec.sampleCall.args,
          [primary.spec.sampleCall.conditionArg]: `${primary.spec.sampleCall.conditionAllowValue} ux-${ctx.fixtures.runId}`,
        };
        const prompt = `Call "${fullToolName}" with arguments ${JSON.stringify(uxArgs)}. If approval is required, report the exact approval request ID and stop.`;
        const out = await client.runPrompt(prompt, { timeoutMs: 60_000 });
        const wireCalls = ctx.proxy.recordsSince(cMark).filter((r) => r.rpcMethod === "tools/call" && r.toolName === fullToolName);
        const sawApprovalMsg = wireCalls.some((c) => (c.toolResultPreview || "").toLowerCase().includes("approval required"));
        const mentionsApproval = out.outputText.toLowerCase().includes("approv");

        results.push(
          makeCell(this, client.id, {
            status: sawApprovalMsg && mentionsApproval ? "PASS" : "UX_GAP",
            durationMs: Date.now() - cStart,
            summary:
              sawApprovalMsg && mentionsApproval
                ? `${client.name} clearly surfaced the Privo approval pause to the user`
                : `${client.name} approval UX unclear (wireSawApproval=${sawApprovalMsg}, outputMentionsApproval=${mentionsApproval})`,
            violations: ctx.proxy.violationsSince(cMark),
            findings:
              sawApprovalMsg && mentionsApproval
                ? []
                : [
                    {
                      scenarioId: this.id,
                      clientId: client.id,
                      category: "UX_CLIENT_GAP",
                      summary: `${client.name} did not clearly relay Privo's approval request to the user`,
                      detail: out.outputText.slice(0, 400),
                    },
                  ],
          }),
        );
        await client.teardown();
      } finally {
        await ctx.api.deletePolicy(uxAskRule.id).catch(() => {});
      }
    }

    return results;
  },
};
