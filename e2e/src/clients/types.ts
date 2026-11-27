import { execFile } from "node:child_process";
import { promisify } from "node:util";

export const execFileAsync = promisify(execFile);

export type ClientId = "mcp-sdk" | "claude-code" | "agy" | "codex" | "gemini" | "browser";

export type ClientAvailability = {
  id: ClientId;
  name: string;
  installed: boolean;
  version?: string;
  authReady: boolean;
  note?: string;
};

export type ClientConfigOpts = {
  mcpUrl: string;
  bearerToken?: string;
  protocolVersion?: string;
};

export type ClientPromptResult = {
  ok: boolean;
  durationMs: number;
  exitCode: number;
  outputText: string;
  toolCalls: Array<{ name: string; input?: unknown; output?: string; isError?: boolean }>;
  rawStdout: string;
  rawStderr: string;
  error?: string;
};

export interface McpClientAdapter {
  readonly id: Exclude<ClientId, "browser">;
  readonly name: string;
  readonly kind: "cli" | "sdk";

  checkAvailability(): Promise<ClientAvailability>;
  configure(opts: ClientConfigOpts): Promise<void>;
  listTools(): Promise<string[]>;
  runPrompt(prompt: string, opts?: { timeoutMs?: number }): Promise<ClientPromptResult>;
  teardown(): Promise<void>;
}

export async function detectBinary(
  cmd: string,
  versionArgs: string[] = ["--version"],
): Promise<{ installed: boolean; version?: string }> {
  try {
    const { stdout, stderr } = await execFileAsync(cmd, versionArgs, { timeout: 5000 });
    const line = (stdout || stderr).trim().split(/\r?\n/)[0]?.trim();
    return { installed: true, version: line || "installed" };
  } catch {
    return { installed: false };
  }
}
