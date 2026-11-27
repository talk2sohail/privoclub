import { mkdtemp, rm, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { spawn } from "node:child_process";
import { detectBinary, type ClientAvailability, type ClientConfigOpts, type ClientPromptResult, type McpClientAdapter } from "./types.js";

/**
 * Wrapper around the real Claude Code CLI (`claude`).
 * Uses `--mcp-config <tmp>/mcp.json --strict-mcp-config` so it NEVER modifies
 * or reads the developer's personal MCP servers.
 */
export class ClaudeCodeAdapter implements McpClientAdapter {
  readonly id = "claude-code" as const;
  readonly name = "Claude Code";
  readonly kind = "cli" as const;

  private tempDir: string | null = null;
  private mcpConfigPath: string | null = null;
  private readonly model = process.env.PRIVO_PROBE_CLAUDE_MODEL || "haiku";

  async checkAvailability(): Promise<ClientAvailability> {
    const bin = await detectBinary("claude", ["--version"]);
    const hasKey = Boolean(process.env.ANTHROPIC_API_KEY);
    return {
      id: this.id,
      name: this.name,
      installed: bin.installed,
      version: bin.version,
      authReady: bin.installed && (hasKey || Boolean(process.env.HOME)),
      note: bin.installed
        ? hasKey
          ? `model=${this.model} (ANTHROPIC_API_KEY)`
          : `model=${this.model} (using local Claude login if signed in)`
        : "Install with: npm i -g @anthropic-ai/claude-code",
    };
  }

  async configure(opts: ClientConfigOpts): Promise<void> {
    await this.teardown();
    this.tempDir = await mkdtemp(path.join(os.tmpdir(), "privo-probe-claude-"));
    this.mcpConfigPath = path.join(this.tempDir, "mcp.json");

    const serverEntry: Record<string, unknown> = {
      type: "http",
      url: opts.mcpUrl,
    };
    if (opts.bearerToken) {
      serverEntry.headers = { Authorization: `Bearer ${opts.bearerToken}` };
    }

    await writeFile(
      this.mcpConfigPath,
      JSON.stringify({ mcpServers: { privo: serverEntry } }, null, 2),
      "utf8",
    );
  }

  async listTools(): Promise<string[]> {
    const res = await this.runPrompt(
      "List every MCP tool currently available from the `privo` MCP server as a plain JSON array of exact tool names, with no extra text.",
      { timeoutMs: 60_000 },
    );
    // Extract `privo` tool names from stream-json init event if present
    const fromInit = parseClaudeInitTools(res.rawStdout);
    if (fromInit.length > 0) return fromInit;
    return extractJsonArray(res.outputText);
  }

  async runPrompt(prompt: string, opts: { timeoutMs?: number } = {}): Promise<ClientPromptResult> {
    if (!this.mcpConfigPath || !this.tempDir) {
      throw new Error("ClaudeCodeAdapter not configured");
    }
    const start = Date.now();
    const timeoutMs = opts.timeoutMs ?? 90_000;

    const args = [
      "-p",
      prompt,
      "--mcp-config",
      this.mcpConfigPath,
      "--strict-mcp-config",
      "--output-format",
      "stream-json",
      "--verbose",
      "--permission-mode",
      "bypassPermissions",
      "--model",
      this.model,
      "--max-turns",
      "6",
    ];

    const { exitCode, stdout, stderr, timedOut } = await runProcess("claude", args, {
      cwd: this.tempDir,
      timeoutMs,
      env: process.env,
    });

    const parsed = parseClaudeStreamJson(stdout);
    const ok = !timedOut && exitCode === 0;
    return {
      ok,
      durationMs: Date.now() - start,
      exitCode,
      outputText: parsed.finalText || stdout.slice(0, 1500),
      toolCalls: parsed.toolCalls,
      rawStdout: stdout,
      rawStderr: stderr,
      error: timedOut ? `Timed out after ${timeoutMs}ms` : exitCode !== 0 ? stderr || `Exit code ${exitCode}` : undefined,
    };
  }

  async teardown(): Promise<void> {
    if (this.tempDir) {
      await rm(this.tempDir, { recursive: true, force: true }).catch(() => {});
      this.tempDir = null;
      this.mcpConfigPath = null;
    }
  }
}

function parseClaudeInitTools(ndjson: string): string[] {
  for (const line of ndjson.split(/\r?\n/)) {
    if (!line.trim()) continue;
    try {
      const ev = JSON.parse(line) as Record<string, unknown>;
      if (ev.type === "system" && ev.subtype === "init" && Array.isArray(ev.tools)) {
        return ev.tools
          .map((t) => String(t))
          .filter((t) => t.startsWith("mcp__privo__"))
          .map((t) => t.replace(/^mcp__privo__/, ""));
      }
    } catch {
      // skip non-JSON line
    }
  }
  return [];
}

function parseClaudeStreamJson(ndjson: string): {
  finalText: string;
  toolCalls: ClientPromptResult["toolCalls"];
} {
  let finalText = "";
  const toolCalls: ClientPromptResult["toolCalls"] = [];

  for (const line of ndjson.split(/\r?\n/)) {
    if (!line.trim()) continue;
    try {
      const ev = JSON.parse(line) as Record<string, unknown>;
      if (ev.type === "result" && typeof ev.result === "string") {
        finalText = ev.result;
      }
      if (ev.type === "assistant" && ev.message && typeof ev.message === "object") {
        const content = (ev.message as Record<string, unknown>).content;
        if (Array.isArray(content)) {
          for (const block of content) {
            if (block && typeof block === "object") {
              const b = block as Record<string, unknown>;
              if (b.type === "tool_use" && typeof b.name === "string") {
                toolCalls.push({ name: b.name.replace(/^mcp__privo__/, ""), input: b.input });
              } else if (b.type === "text" && typeof b.text === "string" && !finalText) {
                finalText = b.text;
              }
            }
          }
        }
      }
    } catch {
      // ignore non-JSON output
    }
  }
  return { finalText, toolCalls };
}

function extractJsonArray(text: string): string[] {
  const m = text.match(/\[[\s\S]*\]/);
  if (!m) return [];
  try {
    const parsed = JSON.parse(m[0]);
    return Array.isArray(parsed) ? parsed.map(String) : [];
  } catch {
    return [];
  }
}

export async function runProcess(
  cmd: string,
  args: string[],
  opts: { cwd: string; timeoutMs: number; env: NodeJS.ProcessEnv },
): Promise<{ exitCode: number; stdout: string; stderr: string; timedOut: boolean }> {
  return new Promise((resolve) => {
    const child = spawn(cmd, args, {
      cwd: opts.cwd,
      env: opts.env,
      stdio: ["ignore", "pipe", "pipe"],
    });
    let stdout = "";
    let stderr = "";
    let timedOut = false;

    child.stdout.on("data", (d: Buffer) => {
      stdout += d.toString("utf8");
    });
    child.stderr.on("data", (d: Buffer) => {
      stderr += d.toString("utf8");
    });

    const timer = setTimeout(() => {
      timedOut = true;
      child.kill("SIGKILL");
    }, opts.timeoutMs);

    child.on("close", (code) => {
      clearTimeout(timer);
      resolve({ exitCode: code ?? 1, stdout, stderr, timedOut });
    });
    child.on("error", (err) => {
      clearTimeout(timer);
      resolve({ exitCode: 1, stdout, stderr: `${stderr}\n${err.message}`, timedOut });
    });
  });
}
