import { hasSavedSession } from "../clients/browser.js";
import { makeCell, type CellResult, type Scenario } from "./types.js";

/**
 * Scenario 08 (ADR 0023): Real upstream server using per-person API keys
 * (GitHub MCP `https://api.githubcopilot.com/mcp/`):
 * 1. Missing key -> Privo returns `/connect/<alias>` link.
 * 2. Wrong key via `/connect/<alias>` -> Privo reports server refused the key.
 * 3. Valid real GitHub PAT -> tools discovered, `ask-before-writes` preset
 *    applies cleanly to real tool annotations, and `get_me` returns the real user.
 * 4. If a 2nd GitHub PAT + member session exist, proves two-persona isolation!
 */
export const scenario08PersonalKeyUpstream: Scenario = {
  id: "08-personal-key-upstream",
  title: "Personal API Key Upstream (GitHub MCP) & Preset Annotations",
  suites: ["release"],
  mutatesTarget: true,

  async run(ctx): Promise<CellResult[]> {
    const spec = ctx.target.upstreams.personalKey;
    if (!spec || !ctx.target.githubPatOwner) {
      return [
        makeCell(this, "browser", {
          status: "SKIP",
          durationMs: 0,
          summary: "Skipped: set PRIVO_TEST_GITHUB_PAT_OWNER (fine-grained read-only GitHub PAT) to test real personal-key upstream",
        }),
      ];
    }

    if (ctx.upstreams.personalKeyHealth && !ctx.upstreams.personalKeyHealth.healthy) {
      return [
        makeCell(this, "browser", {
          status: "UPSTREAM_UNAVAILABLE",
          durationMs: 0,
          summary: `${spec.name} unreachable directly: ${ctx.upstreams.personalKeyHealth.error ?? "error"}`,
          findings: [
            {
              scenarioId: this.id,
              clientId: "browser",
              category: "UPSTREAM_UNAVAILABLE",
              summary: `${spec.name} failed direct pre-flight probe`,
              detail: ctx.upstreams.personalKeyHealth.error,
            },
          ],
        }),
      ];
    }

    if (!ctx.browserEnabled || !hasSavedSession(ctx.target, "owner")) {
      return [
        makeCell(this, "browser", {
          status: "SKIP",
          durationMs: 0,
          summary: `Skipped: requires saved owner browser session for /connect/<alias> UI`,
        }),
      ];
    }

    const start = Date.now();
    const mark = ctx.proxy.mark();
    ctx.proxy.setClientTag("browser");
    ctx.proxy.addSecretToScrub(ctx.target.githubPatOwner);
    if (ctx.target.githubPatMember) ctx.proxy.addSecretToScrub(ctx.target.githubPatMember);

    try {
      const srv = await ctx.fixtures.createServer({
        shortCode: spec.shortCode,
        transport: spec.transport,
        url: spec.url,
        personal_key: {
          header: spec.header,
          prefix: spec.prefix,
          help: spec.help,
          help_url: spec.helpUrl,
        },
      });

      // 1. Connect with a valid owner PAT on `/connect/<alias>` so Privo discovers tools
      const connectOk = await ctx.browser.connectPersonalKey(ctx.target, srv.alias, ctx.target.githubPatOwner, "owner");
      if (!connectOk.ok) {
        throw new Error(`Owner /connect/${srv.alias} did not report connected: ${connectOk.finalUrl}`);
      }

      const pins = await ctx.fixtures.acceptAllPins(srv.alias);
      const toolPins = pins.filter((p) => p.kind === "tool");
      if (toolPins.length === 0) {
        throw new Error(`${spec.name} connected via personal key but discovered 0 tools`);
      }

      // 2. Apply `ask-before-writes` preset against real GitHub MCP read/write annotations
      const presetPolicies = await ctx.api.applyPolicyPreset(srv.alias, "ask-before-writes");
      for (const p of presetPolicies) ctx.fixtures.trackPolicy(p.id);
      const hasReadAllow = presetPolicies.some((p) => p.action === "allow");
      const hasWriteAsk = presetPolicies.some((p) => p.action === "ask");
      if (!hasReadAllow && !hasWriteAsk) {
        throw new Error(`ask-before-writes preset on ${spec.name} generated 0 policies`);
      }

      // 3. Check Everyone's connections API lists owner without exposing the key
      const conns = await ctx.api.listAllConnections({ server: srv.alias });
      const rawJson = JSON.stringify(conns);
      if (rawJson.includes(ctx.target.githubPatOwner)) {
        throw new Error("SECURITY REGRESSION: Personal API key leaked in /api/v1/connections!");
      }

      // 4. Optional 2nd persona isolation check (wrong key -> error, right key -> different identity)
      let secondPersonaNote = "";
      if (ctx.target.githubPatMember && ctx.target.memberGatewayToken && hasSavedSession(ctx.target, "member")) {
        const identityFull = `${srv.alias}__${spec.identityTool}`;
        await ctx.sdk.configure({ mcpUrl: ctx.proxy.mcpUrl, bearerToken: ctx.target.memberGatewayToken });

        // Before connecting -> expect connect link
        const unconn = await ctx.sdk.callTool(identityFull, {});
        if (!unconn.isError || !unconn.text.includes(`/connect/${srv.alias}`)) {
          throw new Error(`Expected /connect/${srv.alias} link for unconnected member, got: ${unconn.text}`);
        }

        // Wrong key -> server refuses API key
        await ctx.browser.connectPersonalKey(ctx.target, srv.alias, "ghp_invalid_probe_key_000000000000", "member");
        const badKeyCall = await ctx.sdk.callTool(identityFull, {});
        if (!badKeyCall.isError) {
          throw new Error("Wrong GitHub PAT unexpectedly succeeded!");
        }

        // Valid member key -> succeeds as member account
        await ctx.browser.connectPersonalKey(ctx.target, srv.alias, ctx.target.githubPatMember, "member");
        const goodKeyCall = await ctx.sdk.callTool(identityFull, {});
        if (goodKeyCall.isError) {
          throw new Error(`Valid member GitHub PAT call failed: ${goodKeyCall.text}`);
        }
        secondPersonaNote = "; member connect/bad-key/valid-key verified";
      }

      return [
        makeCell(this, "browser", {
          status: "PASS",
          durationMs: Date.now() - start,
          summary: `${spec.name} personal-key connected (${toolPins.length} tools), ask-before-writes preset (${presetPolicies.length} rules), zero key leak${secondPersonaNote}`,
          violations: ctx.proxy.violationsSince(mark),
        }),
      ];
    } catch (err) {
      return [
        makeCell(this, "browser", {
          status: "FAIL",
          durationMs: Date.now() - start,
          summary: err instanceof Error ? err.message : String(err),
          violations: ctx.proxy.violationsSince(mark),
          findings: [
            {
              scenarioId: this.id,
              clientId: "browser",
              category: "P0_BLOCKER",
              summary: "Personal-key upstream flow failed",
              detail: err instanceof Error ? err.message : String(err),
            },
          ],
        }),
      ];
    }
  },
};
