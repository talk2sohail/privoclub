import { copyFile, mkdtemp, rm, writeFile } from "node:fs/promises";
import { existsSync } from "node:fs";
import os from "node:os";
import path from "node:path";
import { runProcess } from "./claude-code.js";
import { detectBinary, type ClientAvailability, type ClientConfigOpts, type ClientPromptResult, type McpClientAdapter } from "./types.js";

/**
 * Wrapper around the real OpenAI Codex CLI (`codex`).
 * Writes Privo's exact recommended `config.toml` snippet into an isolated
 * `CODEX_HOME` with `bearer_token_env_var = "PRIVO_TOKEN"`, never touching `~/.codex`.
 */
export class CodexAdapter implements McpClientAdapter {
  readonly id = "codex" as const;
  readonly name = "Codex CLI";
  readonly kind = "cli" as const;

  private tempDir: string | null = null;
  private codexHome: string | null = null;
  private bearerToken = "";
  private readonly model = process.env.PRIVO_PROBE_CODEX_MODEL || "";

  async checkAvailability(): Promise<ClientAvailability> {
    const bin = await detectBinary("codex", ["--version"]);
    const hasKey = Boolean(process.env.OPENAI_API_KEY);
    const hasAuthFile = existsSync(path.join(os.homedir(), ".codex", "auth.json"));
    return {
      id: this.id,
      name: this.name,
      installed: bin.installed,
      version: bin.version,
      authReady: bin.installed && (hasKey || hasAuthFile),
      note: bin.installed
        ? hasKey
          ? "OPENAI_API_KEY configured"
          : hasAuthFile
            ? "Using ~/.codex/auth.json"
            : "Set OPENAI_API_KEY or run `codex login`"
        : "Install with: npm i -g @openai/codex",
    };
  }

  async configure(opts: ClientConfigOpts): Promise<void> {
    await this.teardown();
    this.tempDir = await mkdtemp(path.join(os.tmpdir(), "privo-probe-codex-"));
    this.codexHome = path.join(this.tempDir, "codex-home");
    await mkdtemp(path.join(this.tempDir, "work-"));
    this.bearerToken = opts.bearerToken ?? "";

    // Copy auth.json if present so signed-in Codex users don't need OPENAI_API_KEY
    const userAuth = path.join(os.homedir(), ".codex", "auth.json");
    const { mkdir } = await import("node:fs/promises");
    await mkdir(this.codexHome, { recursive: true });
    if (existsSync(userAuth)) {
      await copyFile(userAuth, path.join(this.codexHome, "auth.json")).catch(() => {});
    }

    const tomlLines = [
      `[mcp_servers.privo]`,
      `url = ${JSON.stringify(opts.mcpUrl)}`,
    ];
    if (this.bearerToken) {
      tomlLines.push(`bearer_token_env_var = "PRIVO_TOKEN"`);
    }
    await writeFile(path.join(this.codexHome, "config.toml"), tomlLines.join("\n") + "\n", "utf8");
  }

  async listTools(): Promise<string[]> {
    const res = await this.runPrompt(
      "List every MCP tool available from the privo MCP server as a plain JSON array of exact tool names, with no commentary.",
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
    if (!this.tempDir || !this.codexHome) {
      throw new Error("CodexAdapter not configured");
    }
    const start = Date.now();
    const timeoutMs = opts.timeoutMs ?? 90_000;

    const args = ["exec", "--json", "--skip-git-repo-check"];
    if (this.model) {
      args.push("-m", this.model);
    }
    args.push(prompt);

    const env: NodeJS.ProcessEnv = {
      ...process.env,
      CODEX_HOME: this.codexHome,
      PRIVO_TOKEN: this.bearerToken,
    };

    const { exitCode, stdout, stderr, timedOut } = await runProcess("codex", args, {
      cwd: this.tempDir,
      timeoutMs,
      env,
    });

    const parsed = parseCodexJsonEvents(stdout);
    return {
      ok: !timedOut && exitCode === 0,
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
      this.codexHome = null;
      this.bearerToken = "";
    }
  }
}

function parseCodexJsonEvents(ndjson: string): {
  finalText: string;
  toolCalls: ClientPromptResult["toolCalls"];
} {
  let finalText = "";
  const toolCalls: ClientPromptResult["toolCalls"] = [];

  for (const line of ndjson.split(/\r?\n/)) {
    if (!line.trim()) continue;
    try {
      const ev = JSON.parse(line) as Record<string, unknown>;
      const item = ev.item as Record<string, unknown> | undefined;
      if (item && item.type === "agent_message" && typeof item.text === "string") {
        finalText = item.text;
      }
      if (item && item.type === "mcp_tool_call" && typeof item.tool === "string") {
        toolCalls.push({
          name: item.tool,
          input: item.arguments,
          output: typeof item.result === "string" ? item.result : undefined,
          isError: Boolean(item.error),
        });
      }
    } catch {
      // ignore non-JSON lines
    }
  }
  return { finalText, toolCalls };
}
