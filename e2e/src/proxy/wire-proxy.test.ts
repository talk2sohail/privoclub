import http from "node:http";
import type { AddressInfo } from "node:net";
import { afterEach, describe, expect, it } from "vitest";
import { WireInspectorProxy } from "./wire-proxy.js";

describe("WireInspectorProxy", () => {
  let upstream: http.Server | null = null;
  let proxy: WireInspectorProxy | null = null;

  afterEach(async () => {
    if (proxy) {
      await proxy.stop();
      proxy = null;
    }
    if (upstream) {
      await new Promise<void>((resolve) => upstream!.close(() => resolve()));
      upstream = null;
    }
  });

  it("forwards /mcp traffic, scrubs Authorization, and catches MCP 2026-07-28 schema violations", async () => {
    upstream = http.createServer((req, res) => {
      if (req.url === "/mcp") {
        res.writeHead(200, { "Content-Type": "application/json" });
        // Deliberately omit resultType to verify the proxy flags the F21 regression
        res.end(
          JSON.stringify({
            jsonrpc: "2.0",
            id: 7,
            result: {
              cacheHints: { cacheScope: "private", ttlMs: 5000 },
              content: [{ type: "text", text: "ok" }],
            },
          }),
        );
        return;
      }
      res.writeHead(404);
      res.end();
    });

    await new Promise<void>((resolve) => upstream!.listen(0, "127.0.0.1", () => resolve()));
    const upstreamPort = (upstream.address() as AddressInfo).port;

    proxy = new WireInspectorProxy(`http://127.0.0.1:${upstreamPort}`);
    proxy.setClientTag("claude-code");
    proxy.addSecretToScrub("privo_pat_topsecret12345");
    await proxy.start();

    const res = await fetch(proxy.mcpUrl, {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
        Authorization: "Bearer privo_pat_topsecret12345",
        "MCP-Protocol-Version": "2026-07-28",
      },
      body: JSON.stringify({
        jsonrpc: "2.0",
        id: 7,
        method: "tools/call",
        params: { name: "dw__read_wiki_structure", arguments: { repoName: "modelcontextprotocol/go-sdk" } },
      }),
    });

    expect(res.status).toBe(200);
    const records = proxy.allRecords();
    expect(records).toHaveLength(1);
    const rec = records[0]!;
    expect(rec.clientTag).toBe("claude-code");
    expect(rec.rpcMethod).toBe("tools/call");
    expect(rec.toolName).toBe("dw__read_wiki_structure");
    expect(rec.requestHeaders.authorization).toBe("[REDACTED]");
    expect(rec.violations.some((v) => v.rule === "mcp.2026.resultType")).toBe(true);
  });
});
