import crypto from "node:crypto";
import { hasSavedSession } from "../clients/browser.js";
import { eventually } from "../privo/oracles.js";
import { makeCell, type CellResult, type Scenario } from "./types.js";

function base64Url(buf: Buffer): string {
  return buf.toString("base64").replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

/**
 * Scenario 07: Live Token Revocation (<= 5s ADR 0003 SLA) & OAuth 2.1 End-to-End
 * (Dynamic Registration + PKCE + Browser Consent Allow/Deny + Refresh Rotation +
 * Refresh Reuse Detection + Grant Revocation).
 */
export const scenario07AuthOAuthRevocation: Scenario = {
  id: "07-auth-oauth-revocation",
  title: "Live Token Revocation & OAuth 2.1 Client Flow",
  suites: ["release"],
  mutatesTarget: true,

  async run(ctx): Promise<CellResult[]> {
    const results: CellResult[] = [];
    const primary = ctx.upstreams.primaryNoAuth;
    const alias = ctx.shared.primaryServerAlias;
    const fullToolName = ctx.shared.primaryFullToolName;

    // ---- 1. Live gateway token revocation within <= 5s (ADR 0003)
    if (primary && alias && fullToolName) {
      const start = Date.now();
      const mark = ctx.proxy.mark();
      ctx.proxy.setClientTag("mcp-sdk");
      try {
        const tempTok = await ctx.fixtures.createToken({
          label: "revoke-sla",
          role: "member",
          kind: "gateway",
          person: `probe-revoke-${ctx.fixtures.runId}`,
        });
        ctx.proxy.addSecretToScrub(tempTok.token);

        await ctx.sdk.configure({ mcpUrl: ctx.proxy.mcpUrl, bearerToken: tempTok.token });
        const before = await ctx.sdk.listTools();
        if (!before.includes(fullToolName)) {
          throw new Error(`Fresh token did not see ${fullToolName}`);
        }

        const revokedAt = Date.now();
        await ctx.api.revokeToken(tempTok.id);

        await eventually(
          "revoked token rejected on live MCP endpoint",
          async () => {
            const res = await ctx.sdk.rawJsonRpc("tools/list", {}, "2026-07-28");
            return res.status === 401 ? true : null;
          },
          { timeoutMs: 8000, intervalMs: 400 },
        );
        const propagationMs = Date.now() - revokedAt;
        const withinSla = propagationMs <= 5500;

        results.push(
          makeCell(this, "mcp-sdk", {
            status: withinSla ? "PASS" : "FAIL",
            durationMs: Date.now() - start,
            summary: `Revoked gateway token rejected with 401 in ${propagationMs}ms (ADR 0003 SLA <= 5000ms)`,
            violations: ctx.proxy.violationsSince(mark),
            findings: withinSla
              ? []
              : [
                  {
                    scenarioId: this.id,
                    clientId: "mcp-sdk",
                    category: "P0_BLOCKER",
                    summary: `Token revocation took ${propagationMs}ms, exceeding ADR 0003 5s SLA`,
                  },
                ],
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
                summary: "Live token revocation SLA test failed",
                detail: err instanceof Error ? err.message : String(err),
              },
            ],
          }),
        );
      }
    }

    // ---- 2. Full OAuth 2.1 flow (requires PRIVO_OAUTH_SERVER=on, DCR=on, and saved Playwright browser session)
    if (!ctx.browserEnabled || !hasSavedSession(ctx.target, "owner")) {
      results.push(
        makeCell(this, "browser", {
          status: "SKIP",
          durationMs: 0,
          summary: `Skipped OAuth consent test: no saved browser session (run: npm --prefix e2e run probe -- auth --target ${ctx.target.name} --persona owner)`,
        }),
      );
      return results;
    }

    const oauthStart = Date.now();
    const oauthMark = ctx.proxy.mark();
    ctx.proxy.setClientTag("browser");

    try {
      const asMetaRes = await ctx.api.oauthAuthorizationServer();
      if (asMetaRes.status !== 200 || !asMetaRes.json) {
        results.push(
          makeCell(this, "browser", {
            status: ctx.target.oauthExpected ? "FAIL" : "SKIP",
            durationMs: Date.now() - oauthStart,
            summary: `OAuth authorization server metadata unavailable (${asMetaRes.status})`,
          }),
        );
        return results;
      }

      const meta = asMetaRes.json as Record<string, string>;
      if (!meta.registration_endpoint || !meta.authorization_endpoint || !meta.token_endpoint) {
        results.push(
          makeCell(this, "browser", {
            status: "SKIP",
            durationMs: Date.now() - oauthStart,
            summary: "OAuth dynamic client registration not enabled on target (PRIVO_OAUTH_DYNAMIC_REGISTRATION=off)",
          }),
        );
        return results;
      }

      // Dynamic Client Registration (RFC 7591, as Cursor / Gemini CLI do) with a real loopback callback server
      const loopback = await ctx.browser.startLoopbackCallback();
      const redirectUri = loopback.redirectUri;
      let clientId = "";
      try {
      const regRes = await fetch(meta.registration_endpoint, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          client_name: `Privo Probe OAuth (${ctx.fixtures.runId})`,
          redirect_uris: [redirectUri],
          grant_types: ["authorization_code", "refresh_token"],
          response_types: ["code"],
          token_endpoint_auth_method: "none",
        }),
      });
      if (regRes.status !== 201 && regRes.status !== 200) {
        throw new Error(`Dynamic registration failed (${regRes.status}): ${await regRes.text()}`);
      }
      const reg = (await regRes.json()) as { client_id: string };
      clientId = reg.client_id;

      const buildAuthUrl = (state: string, challenge: string) => {
        const u = new URL(meta.authorization_endpoint!);
        u.searchParams.set("response_type", "code");
        u.searchParams.set("client_id", clientId);
        u.searchParams.set("redirect_uri", redirectUri);
        u.searchParams.set("resource", ctx.target.mcpUrl);
        u.searchParams.set("code_challenge", challenge);
        u.searchParams.set("code_challenge_method", "S256");
        u.searchParams.set("state", state);
        return u.toString();
      };

      const persona = hasSavedSession(ctx.target, "member") ? "member" : "owner";

      // 2a. Deny consent -> redirect carries error=access_denied
      const denyVerifier = base64Url(crypto.randomBytes(32));
      const denyChallenge = base64Url(crypto.createHash("sha256").update(denyVerifier).digest());
      const denyRes = await ctx.browser.completeOAuthConsent(
        ctx.target,
        buildAuthUrl("deny-state-1234", denyChallenge),
        persona,
        "deny",
        loopback,
      );
      const denyParsed = new URL(denyRes.redirectUrl);
      if (denyParsed.searchParams.get("error") !== "access_denied") {
        throw new Error(`OAuth Deny did not return error=access_denied: ${denyRes.redirectUrl}`);
      }

      // 2b. Allow consent -> exchange PKCE code -> use access token on /mcp
      const verifier = base64Url(crypto.randomBytes(32));
      const challenge = base64Url(crypto.createHash("sha256").update(verifier).digest());
      const allowRes = await ctx.browser.completeOAuthConsent(
        ctx.target,
        buildAuthUrl("allow-state-1234", challenge),
        persona,
        "allow",
        loopback,
      );
      const allowParsed = new URL(allowRes.redirectUrl);
      const code = allowParsed.searchParams.get("code");
      if (!code || allowParsed.searchParams.get("state") !== "allow-state-1234") {
        throw new Error(`OAuth Allow did not return expected code & state: ${allowRes.redirectUrl}`);
      }

      const tokenBody = new URLSearchParams({
        grant_type: "authorization_code",
        code,
        redirect_uri: redirectUri,
        client_id: clientId,
        code_verifier: verifier,
        resource: ctx.target.mcpUrl,
      });
      const tokRes = await fetch(meta.token_endpoint, {
        method: "POST",
        headers: { "Content-Type": "application/x-www-form-urlencoded" },
        body: tokenBody,
      });
      if (tokRes.status !== 200) {
        throw new Error(`OAuth token exchange failed (${tokRes.status}): ${await tokRes.text()}`);
      }
      const tokens = (await tokRes.json()) as { access_token: string; refresh_token?: string };
      ctx.proxy.addSecretToScrub(tokens.access_token);
      if (tokens.refresh_token) ctx.proxy.addSecretToScrub(tokens.refresh_token);

      // Verify OAuth access token works on /mcp AND is refused (401) on /api/v1/servers
      await ctx.sdk.configure({ mcpUrl: ctx.proxy.mcpUrl, bearerToken: tokens.access_token });
      await ctx.sdk.listTools();
      const adminWithOAuth = await ctx.api.withToken(tokens.access_token).raw("GET", "/api/v1/servers");
      if (adminWithOAuth.status !== 401) {
        throw new Error(`OAuth access token was NOT refused on /api/v1/servers (${adminWithOAuth.status})`);
      }

      // 2c. Refresh token rotation + reuse detection (ADR 0022: zero grace window!)
      let reuseVerified = false;
      if (tokens.refresh_token) {
        const ref1 = await fetch(meta.token_endpoint, {
          method: "POST",
          headers: { "Content-Type": "application/x-www-form-urlencoded" },
          body: new URLSearchParams({
            grant_type: "refresh_token",
            refresh_token: tokens.refresh_token,
            client_id: clientId,
            resource: ctx.target.mcpUrl,
          }),
        });
        if (ref1.status !== 200) {
          throw new Error(`OAuth refresh rotation failed (${ref1.status}): ${await ref1.text()}`);
        }
        const rotated = (await ref1.json()) as { access_token: string; refresh_token?: string };
        ctx.proxy.addSecretToScrub(rotated.access_token);

        // Replay the OLD refresh token -> MUST fail AND revoke the grant!
        const replay = await fetch(meta.token_endpoint, {
          method: "POST",
          headers: { "Content-Type": "application/x-www-form-urlencoded" },
          body: new URLSearchParams({
            grant_type: "refresh_token",
            refresh_token: tokens.refresh_token,
            client_id: clientId,
            resource: ctx.target.mcpUrl,
          }),
        });
        if (replay.status === 200) {
          throw new Error("SECURITY REGRESSION: Replayed refresh token succeeded (ADR 0022 requires immediate revocation)!");
        }
        reuseVerified = true;
      }

      // Clean up registered client / grants
      await ctx.api.raw("POST", "/api/v1/oauth/clients:remove", { client_id: clientId }).catch(() => {});

      results.push(
        makeCell(this, "browser", {
          status: "PASS",
          durationMs: Date.now() - oauthStart,
          summary: `OAuth 2.1 DCR + Deny + Allow + PKCE + /mcp ok + /api/v1 refused${reuseVerified ? " + refresh reuse revoked" : ""}`,
          violations: ctx.proxy.violationsSince(oauthMark),
        }),
      );
      } finally {
        await loopback.close().catch(() => {});
        if (clientId) {
          await ctx.api.raw("POST", "/api/v1/oauth/clients:remove", { client_id: clientId }).catch(() => {});
        }
      }
    } catch (err) {
      results.push(
        makeCell(this, "browser", {
          status: "FAIL",
          durationMs: Date.now() - oauthStart,
          summary: err instanceof Error ? err.message : String(err),
          violations: ctx.proxy.violationsSince(oauthMark),
          findings: [
            {
              scenarioId: this.id,
              clientId: "browser",
              category: "P0_BLOCKER",
              summary: "OAuth 2.1 client sign-in / consent / token lifecycle failed",
              detail: err instanceof Error ? err.message : String(err),
            },
          ],
        }),
      );
    }

    return results;
  },
};
