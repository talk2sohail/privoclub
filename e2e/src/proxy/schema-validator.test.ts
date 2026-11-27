import { describe, expect, it } from "vitest";
import { scrubHeaders, scrubString, scrubUnknown } from "./scrubber.js";
import { validateMcpResponse } from "./schema-validator.js";

describe("validateMcpResponse", () => {
  it("catches missing resultType on MCP 2026-07-28 (F21 Claude Code regression)", () => {
    const violations = validateMcpResponse("tools/call", "2026-07-28", {
      jsonrpc: "2.0",
      id: 1,
      result: { content: [{ type: "text", text: "ok" }] },
    });
    expect(violations.some((v) => v.rule === "mcp.2026.resultType")).toBe(true);
  });

  it("allows missing resultType on MCP 2025-11-25", () => {
    const violations = validateMcpResponse("tools/call", "2025-11-25", {
      jsonrpc: "2.0",
      id: 1,
      result: { content: [{ type: "text", text: "ok" }] },
    });
    expect(violations).toHaveLength(0);
  });

  it("catches empty cacheScope (F02 Claude Code regression)", () => {
    const violations = validateMcpResponse("tools/list", "2026-07-28", {
      jsonrpc: "2.0",
      id: 2,
      result: {
        resultType: "tools/list",
        cacheHints: { cacheScope: "", ttlMs: 5000 },
        tools: [],
      },
    });
    expect(violations.some((v) => v.rule === "mcp.cacheHints.cacheScope")).toBe(true);
  });

  it("accepts a valid 2026-07-28 tools/list response with private cacheHints", () => {
    const violations = validateMcpResponse("tools/list", "2026-07-28", {
      jsonrpc: "2.0",
      id: 3,
      result: {
        resultType: "tools/list",
        cacheHints: { cacheScope: "private", ttlMs: 5000 },
        tools: [
          {
            name: "dw__read_wiki_structure",
            inputSchema: { type: "object", properties: { repoName: { type: "string" } } },
          },
        ],
      },
    });
    expect(violations).toHaveLength(0);
  });

  it("catches null tool entries", () => {
    const violations = validateMcpResponse("tools/list", "2025-11-25", {
      jsonrpc: "2.0",
      id: 4,
      result: { tools: [null] },
    });
    expect(violations.some((v) => v.rule === "mcp.tools.entry-object")).toBe(true);
  });
});

describe("scrubber", () => {
  it("redacts Privo tokens, GitHub PATs, and sensitive headers", () => {
    expect(scrubString("Bearer privo_pat_abc1234567890")).not.toContain("abc1234567890");
    expect(scrubString("token=ghp_1234567890abcdefghij")).not.toContain("abcdefghij");
    const h = scrubHeaders({ Authorization: "Bearer supersecret", "X-Custom": "safe" });
    expect(h.Authorization).toBe("[REDACTED]");
    expect(h["X-Custom"]).toBe("safe");
    const obj = scrubUnknown({ access_token: "secret123", ok: true }) as Record<string, unknown>;
    expect(obj.access_token).toBe("[REDACTED]");
    expect(obj.ok).toBe(true);
  });
});
