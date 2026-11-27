import { mkdir, mkdtemp, rm, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { runProcess } from "./claude-code.js";
import {
  detectBinary,
  type ClientAvailability,
  type ClientConfigOpts,
  type ClientPromptResult,
  type McpClientAdapter,
} from "./types.js";

/**
 * Wrapper around the real Google Antigravity CLI (`agy`).
 * Writes an isolated workspace `<tmp>/.agents/mcp_config.json` pointing `privo`
 * at the Wire-Inspector Proxy so the developer's global MCP config stays untouched.
 */
export class AgyAdapter implements McpClientAdapter {
  readonly id = "agy" as const;
  readonly name = "Antigravity CLI (agy)";
  readonly kind = "cli" as const;

  private tempDir: string | null = null;
  private readonly model = process.env.PRIVO_PROBE_AGY_MODEL || "";

  async checkAvailability(): Promise<ClientAvailability> {
    const bin = await detectBinary("agy", ["--version"]);
    return {
      id: this.id,
      name: this.name,
      installed: bin.installed,
      version: bin.version,
      authReady: bin.installed,
      note: bin.installed
        ? this.model
          ? `model=${this.model}`
          : "Using signed-in Antigravity CLI account"
        : "Install Google Antigravity CLI (`agy`)",
    };
  }

  async configure(opts: ClientConfigOpts): Promise<void> {
    await this.teardown();
    this.tempDir = await mkdtemp(path.join(os.tmpdir(), "privo-probe-agy-"));
    const agentsDir = path.join(this.tempDir, ".agents");
    await mkdir(agentsDir, { recursive: true });

    const serverCfg: Record<string, unknown> = {
      serverUrl: opts.mcpUrl,
      timeoutSeconds: 120,
    };
    if (opts.bearerToken) {
      serverCfg.headers = { Authorization: `Bearer ${opts.bearerToken}` };
    }

    await writeFile(
      path.join(agentsDir, "mcp_config.json"),
      JSON.stringify({ mcpServers: { privo: serverCfg } }, null, 2),
      "utf8",
    );
  }

  async listTools(): Promise<string[]> {
    const res = await this.runPrompt(
      "List every MCP tool currently available from the `privo` MCP server as a plain JSON array of exact tool names, with no extra text.",
      { timeoutMs: 60_000 },
    );
    const m = res.outputText.match(/\[[\s\S]*\]/);
    if (!m) return [];
    try {
      const parsed = JSON.parse(m[0]);
      return Array.isArray(parsed) ? parsed.map(String) : [];
    } catch {
      return [];
    }
  }

  async runPrompt(prompt: string, opts: { timeoutMs?: number } = {}): Promise<ClientPromptResult> {
    if (!this.tempDir) {
      throw new Error("AgyAdapter not configured");
    }
    const start = Date.now();
    const timeoutMs = opts.timeoutMs ?? 90_000;
    const timeoutSec = Math.max(15, Math.floor(timeoutMs / 1000));

    const args = [
      "-p",
      prompt,
      "--output-format",
      "json",
      "--dangerously-skip-permissions",
      "--disable-slash-commands",
      "--print-timeout",
      `${timeoutSec}s`,
    ];
    if (this.model) {
      args.push("--model", this.model);
    }

    const { exitCode, stdout, stderr, timedOut } = await runProcess("agy", args, {
      cwd: this.tempDir,
      timeoutMs: timeoutMs + 5000,
      env: process.env,
    });

    let outputText = stdout.trim();
    const toolCalls: ClientPromptResult["toolCalls"] = [];

    try {
      const parsed = JSON.parse(stdout) as Record<string, unknown>;
      if (typeof parsed.result === "string") outputText = parsed.result;
      else if (typeof parsed.response === "string") outputText = parsed.response;
      else if (typeof parsed.text === "string") outputText = parsed.text;
    } catch {
      // Fall back to last JSON object or raw stdout
      const lines = stdout
        .split(/\r?\n/)
        .map((l) => l.trim())
        .filter(Boolean);
      for (const line of lines) {
        try {
          const ev = JSON.parse(line) as Record<string, unknown>;
          if (typeof ev.result === "string") outputText = ev.result;
          else if (typeof ev.response === "string") outputText = ev.response;
        } catch {
          // ignore non-JSON lines
        }
      }
    }

    return {
      ok: !timedOut && exitCode === 0,
      durationMs: Date.now() - start,
      exitCode,
      outputText,
      toolCalls,
      rawStdout: stdout,
      rawStderr: stderr,
      error: timedOut
        ? `Timed out after ${timeoutMs}ms`
        : exitCode !== 0
          ? stderr || stdout.slice(0, 500) || `Exit code ${exitCode}`
          : undefined,
    };
  }

  async teardown(): Promise<void> {
    if (this.tempDir) {
      await rm(this.tempDir, { recursive: true, force: true }).catch(() => {});
      this.tempDir = null;
    }
  }
}
