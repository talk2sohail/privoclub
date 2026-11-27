import { AgyAdapter } from "./agy.js";
import { BrowserAdapter } from "./browser.js";
import { ClaudeCodeAdapter } from "./claude-code.js";
import { CodexAdapter } from "./codex.js";
import { GeminiCliAdapter } from "./gemini.js";
import { McpSdkAdapter } from "./mcp-sdk.js";
import type { ClientId, McpClientAdapter } from "./types.js";

export const ALL_MCP_CLIENT_IDS: Array<Exclude<ClientId, "browser">> = [
  "mcp-sdk",
  "claude-code",
  "agy",
  "codex",
  "gemini",
];

export const ALL_CLIENT_IDS: ClientId[] = [...ALL_MCP_CLIENT_IDS, "browser"];

export function createMcpAdapter(id: Exclude<ClientId, "browser">): McpClientAdapter {
  switch (id) {
    case "mcp-sdk":
      return new McpSdkAdapter();
    case "claude-code":
      return new ClaudeCodeAdapter();
    case "agy":
      return new AgyAdapter();
    case "codex":
      return new CodexAdapter();
    case "gemini":
      return new GeminiCliAdapter();
  }
}

export function createBrowserAdapter(): BrowserAdapter {
  return new BrowserAdapter();
}

export { AgyAdapter, BrowserAdapter, ClaudeCodeAdapter, CodexAdapter, GeminiCliAdapter, McpSdkAdapter };
export * from "./types.js";
