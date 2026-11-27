import { Client } from "@modelcontextprotocol/sdk/client/index.js";
import { StreamableHTTPClientTransport } from "@modelcontextprotocol/sdk/client/streamableHttp.js";
import type { ClientAvailability, ClientConfigOpts, ClientPromptResult, McpClientAdapter } from "./types.js";

export type DirectToolCallResult = {
  isError: boolean;
  text: string;
  raw: unknown;
};

/**
 * Official @modelcontextprotocol/sdk Streamable HTTP client wrapper.
 * Provides deterministic protocol coverage and Cursor / VS Code / Windsurf
 * TypeScript MCP SDK parity, plus raw JSON-RPC probes for strict edge cases.
 */
export class McpSdkAdapter implements McpClientAdapter {
  readonly id = "mcp-sdk" as const;
  readonly name = "MCP TypeScript SDK (Cursor/VS Code parity)";
  readonly kind = "sdk" as const;

  private opts: ClientConfigOpts | null = null;
  private client: Client | null = null;

  async checkAvailability(): Promise<ClientAvailability> {
    return {
      id: this.id,
      name: this.name,
      installed: true,
      version: "@modelcontextprotocol/sdk",
      authReady: true,
    };
  }

  async configure(opts: ClientConfigOpts): Promise<void> {
    await this.teardown();
    this.opts = opts;
    await this.connect();
  }

  private async connect(): Promise<Client> {
    if (!this.opts) throw new Error("McpSdkAdapter not configured");
    const headers: Record<string, string> = {};
    if (this.opts.bearerToken) {
      headers.Authorization = `Bearer ${this.opts.bearerToken}`;
    }
    const client = new Client({ name: "privo-probe-ts-sdk", version: "0.1.0" });
    const transport = new StreamableHTTPClientTransport(new URL(this.opts.mcpUrl), {
      requestInit: { headers },
    });
    await client.connect(transport);
    this.client = client;
    return client;
  }

  private async ensureClient(): Promise<Client> {
    return this.client ?? this.connect();
  }

  async listTools(): Promise<string[]> {
    const c = await this.ensureClient();
    const res = await c.listTools();
    return res.tools.map((t) => t.name);
  }

  async listPrompts(): Promise<string[]> {
    const c = await this.ensureClient();
    const res = await c.listPrompts();
    return res.prompts.map((p) => p.name);
  }

  async listResources(): Promise<string[]> {
    const c = await this.ensureClient();
    const res = await c.listResources();
    return res.resources.map((r) => r.uri);
  }

  async callTool(name: string, args: Record<string, unknown> = {}): Promise<DirectToolCallResult> {
    const c = await this.ensureClient();
    const res = await c.callTool({ name, arguments: args });
    const content = Array.isArray(res.content) ? res.content : [];
    const text = content
      .map((item) => (item && typeof item === "object" && "text" in item ? String((item as { text: unknown }).text) : ""))
      .filter(Boolean)
      .join("\n");
    return {
      isError: Boolean(res.isError),
      text,
      raw: res,
    };
  }

  async getPrompt(name: string, args?: Record<string, string>): Promise<unknown> {
    const c = await this.ensureClient();
    return c.getPrompt({ name, arguments: args });
  }

  async readResource(uri: string): Promise<unknown> {
    const c = await this.ensureClient();
    return c.readResource({ uri });
  }

  /**
   * Sends a raw JSON-RPC POST to `/mcp` with an explicit MCP-Protocol-Version header.
   * Used to verify both MCP 2025-11-25 and 2026-07-28 directly through the Wire-Inspector Proxy.
   */
  async rawJsonRpc(
    method: string,
    params: Record<string, unknown>,
    protocolVersion: "2025-11-25" | "2026-07-28",
  ): Promise<{ status: number; body: unknown }> {
    if (!this.opts) throw new Error("McpSdkAdapter not configured");
    const headers: Record<string, string> = {
      "Content-Type": "application/json",
      Accept: "application/json, text/event-stream",
      "MCP-Protocol-Version": protocolVersion,
    };
    if (this.opts.bearerToken) {
      headers.Authorization = `Bearer ${this.opts.bearerToken}`;
    }
    const res = await fetch(this.opts.mcpUrl, {
      method: "POST",
      headers,
      body: JSON.stringify({
        jsonrpc: "2.0",
        id: Math.floor(Math.random() * 1_000_000),
        method,
        params,
      }),
      signal: AbortSignal.timeout(20_000),
    });
    const text = await res.text();
    let body: unknown = text;
    try {
      body = JSON.parse(text);
    } catch {
      // SSE or plain text
      const dataLine = text
        .split(/\r?\n/)
        .filter((l) => l.startsWith("data:"))
        .map((l) => l.slice(5).trim())
        .join("\n");
      if (dataLine) {
        try {
          body = JSON.parse(dataLine);
        } catch {
          body = text;
        }
      }
    }
    return { status: res.status, body };
  }

  async runPrompt(prompt: string): Promise<ClientPromptResult> {
    const start = Date.now();
    // SDK adapter doesn't run an LLM; listTools serves as its basic prompt smoke
    const tools = await this.listTools();
    return {
      ok: true,
      durationMs: Date.now() - start,
      exitCode: 0,
      outputText: `Prompt "${prompt}" acknowledged by deterministic MCP SDK client (${tools.length} tools visible).`,
      toolCalls: [],
      rawStdout: JSON.stringify({ tools }),
      rawStderr: "",
    };
  }

  async teardown(): Promise<void> {
    if (this.client) {
      await this.client.close().catch(() => {});
      this.client = null;
    }
  }
}
