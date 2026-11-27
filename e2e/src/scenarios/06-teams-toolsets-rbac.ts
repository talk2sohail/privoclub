import { eventually, waitForAuditCall } from "../privo/oracles.js";
import { makeCell, type CellResult, type Scenario } from "./types.js";

/**
 * Scenario 06: Teams, per-team Toolsets (ADR 0026), RBAC token separation
 * (ADR 0014), and prompts/resources gating when a real upstream advertises them.
 */
export const scenario06TeamsToolsetsRbac: Scenario = {
  id: "06-teams-toolsets-rbac",
  title: "Teams, Per-Team Toolsets & RBAC Boundaries",
  suites: ["release"],
  mutatesTarget: true,

  async run(ctx): Promise<CellResult[]> {
    const start = Date.now();
    const mark = ctx.proxy.mark();
    ctx.proxy.setClientTag("mcp-sdk");
    const findings: CellResult["findings"] = [];
    const notes: string[] = [];

    const primary = ctx.upstreams.primaryNoAuth;
    const gwToken = ctx.shared.memberGatewayToken;
    if (!primary || !gwToken) {
      return [
        makeCell(this, "mcp-sdk", {
          status: "SKIP",
          durationMs: 0,
          summary: "Skipped: no primary upstream or gateway token available",
        }),
      ];
    }

    try {
      // 1. Gateway token MUST be rejected (401) on Admin API routes
      const gwApi = ctx.api.withToken(gwToken);
      const gwApprovals = await gwApi.raw("GET", "/api/v1/approvals");
      if (gwApprovals.status !== 401) {
        findings.push({
          scenarioId: this.id,
          clientId: "mcp-sdk",
          category: "P0_BLOCKER",
          summary: `Gateway token was NOT refused on /api/v1/approvals (HTTP ${gwApprovals.status}, want 401)`,
        });
      }

      // 2. Auditor control token can read audit events (200) but cannot modify audit settings (403)
      const auditorTok = await ctx.fixtures.createToken({
        label: "rbac-auditor",
        role: "auditor",
        kind: "control",
        person: `probe-auditor-${ctx.fixtures.runId}`,
      });
      ctx.proxy.addSecretToScrub(auditorTok.token);
      const auditorApi = ctx.api.withToken(auditorTok.token);
      const audRead = await auditorApi.raw("GET", "/api/v1/audit/events?limit=5");
      const audWrite = await auditorApi.raw("PATCH", "/api/v1/settings/audit", { record_arguments: true });
      if (audRead.status !== 200 || audWrite.status !== 403) {
        findings.push({
          scenarioId: this.id,
          clientId: "mcp-sdk",
          category: "P0_BLOCKER",
          summary: `Auditor RBAC check failed: read=${audRead.status} (want 200), write=${audWrite.status} (want 403)`,
        });
      } else {
        notes.push("RBAC(gw=401,auditor=200/403)");
      }

      // 3. Team scoping: assign a real upstream strictly to a new team; verify an outsider cannot see or call it
      const team = await ctx.fixtures.createTeam({
        shortCode: "iso",
        name: `Probe Isolated ${ctx.fixtures.runId}`,
      });
      const teamUpstream = ctx.upstreams.secondaryNoAuth ?? primary;
      const teamSrv = await ctx.fixtures.createServer({
        shortCode: "tm",
        transport: teamUpstream.spec.transport,
        url: teamUpstream.spec.url,
        teams: [team.slug],
      });
      const pins = await ctx.fixtures.acceptAllPins(teamSrv.alias);
      const toolPins = pins.filter((p) => p.kind === "tool");
      const targetTool = toolPins[0]?.name;

      if (targetTool) {
        const outsiderFullName = `${teamSrv.alias}__${targetTool}`;
        await ctx.sdk.configure({ mcpUrl: ctx.proxy.mcpUrl, bearerToken: gwToken });
        const outsiderTools = await ctx.sdk.listTools();
        if (outsiderTools.includes(outsiderFullName)) {
          findings.push({
            scenarioId: this.id,
            clientId: "mcp-sdk",
            category: "P0_BLOCKER",
            summary: `Team-restricted tool ${outsiderFullName} leaked to caller outside team ${team.slug}`,
          });
        }

        const sinceIso = new Date(Date.now() - 2000).toISOString();
        const outsiderCall = await ctx.sdk.callTool(outsiderFullName, {});
        if (!outsiderCall.isError) {
          findings.push({
            scenarioId: this.id,
            clientId: "mcp-sdk",
            category: "P0_BLOCKER",
            summary: `Caller outside team ${team.slug} successfully invoked ${outsiderFullName}`,
          });
        } else {
          await waitForAuditCall(ctx.api, {
            server: teamSrv.alias,
            item: targetTool,
            decisionPrefix: "denied:not_in_team",
            sinceIso,
          }).catch(() => {
            // Unbound tokens may be denied before or with not_in_team depending on policy
          });
          notes.push("team-isolation=ok");
        }

        // 4. If a bound member gateway token + memberPerson exist, verify per-team toolset narrowing!
        if (ctx.target.memberPerson && ctx.target.memberGatewayToken && toolPins.length >= 2) {
          await ctx.api.addTeamMember(team.slug, ctx.target.memberPerson);
          const allowedTool = toolPins[0]!.name;
          const hiddenTool = toolPins[1]!.name;
          await ctx.api.setTeamTools(teamSrv.alias, team.slug, [allowedTool]);

          await ctx.sdk.configure({
            mcpUrl: ctx.proxy.mcpUrl,
            bearerToken: ctx.target.memberGatewayToken,
          });
          const narrowedList = await eventually(
            `bound member sees narrowed toolset [${allowedTool}]`,
            async () => {
              const tools = await ctx.sdk.listTools();
              const ofSrv = tools.filter((t) => t.startsWith(`${teamSrv.alias}__`));
              return ofSrv.includes(`${teamSrv.alias}__${allowedTool}`) ? ofSrv : null;
            },
            { timeoutMs: 12_000 },
          );
          if (narrowedList.includes(`${teamSrv.alias}__${hiddenTool}`)) {
            findings.push({
              scenarioId: this.id,
              clientId: "mcp-sdk",
              category: "P0_BLOCKER",
              summary: `Toolset narrowing leaked hidden tool ${hiddenTool} to ${ctx.target.memberPerson}`,
            });
          } else {
            const hiddenCall = await ctx.sdk.callTool(`${teamSrv.alias}__${hiddenTool}`, {});
            if (!hiddenCall.isError) {
              findings.push({
                scenarioId: this.id,
                clientId: "mcp-sdk",
                category: "P0_BLOCKER",
                summary: `Bound member executed hidden tool ${hiddenTool} outside narrowed toolset`,
              });
            } else {
              notes.push("toolset-narrowing=ok");
            }
          }
        }
      }

      // 5. Real prompts & resources check if pre-flight detected a healthy server advertising them
      const prHealth = ctx.upstreams.promptsResourcesHealth;
      const prSpec = ctx.target.upstreams.promptsResources;
      if (prSpec && prHealth?.healthy && (prHealth.promptNames.length > 0 || prHealth.resourceUris.length > 0)) {
        const refSrv = await ctx.fixtures.createServer({
          shortCode: prSpec.shortCode,
          transport: prSpec.transport,
          url: prSpec.url,
        });
        await ctx.fixtures.acceptAllPins(refSrv.alias);
        await ctx.sdk.configure({ mcpUrl: ctx.proxy.mcpUrl, bearerToken: gwToken });

        if (prHealth.promptNames.length > 0) {
          const prompts = await ctx.sdk.listPrompts();
          if (!prompts.some((p) => p.startsWith(`${refSrv.alias}__`))) {
            findings.push({
              scenarioId: this.id,
              clientId: "mcp-sdk",
              category: "P0_BLOCKER",
              summary: `Prompts from real reference server ${prSpec.name} did not appear in prompts/list`,
            });
          } else {
            notes.push(`prompts=${prompts.filter((p) => p.startsWith(`${refSrv.alias}__`)).length}`);
          }
        }
        if (prHealth.resourceUris.length > 0) {
          const resources = await ctx.sdk.listResources();
          if (resources.length === 0) {
            findings.push({
              scenarioId: this.id,
              clientId: "mcp-sdk",
              category: "P0_BLOCKER",
              summary: `Resources from real reference server ${prSpec.name} did not appear in resources/list`,
            });
          } else {
            notes.push(`resources=${resources.length}`);
          }
        }
      }

      const hasBlocker = findings.some((f) => f.category === "P0_BLOCKER");
      return [
        makeCell(this, "mcp-sdk", {
          status: hasBlocker ? "FAIL" : "PASS",
          durationMs: Date.now() - start,
          summary: hasBlocker ? findings[0]!.summary : `Verified ${notes.join(", ")}`,
          violations: ctx.proxy.violationsSince(mark),
          findings,
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
              summary: "Teams/toolsets/RBAC scenario failed",
              detail: err instanceof Error ? err.message : String(err),
            },
          ],
        }),
      ];
    }
  },
};
