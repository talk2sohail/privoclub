import { existsSync, readFileSync } from "node:fs";
import { mkdir, mkdtemp, rm, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { runProcess } from "./claude-code.js";
import { detectBinary, type ClientAvailability, type ClientConfigOpts, type ClientPromptResult, type McpClientAdapter } from "./types.js";

function hasGeminiLocalAuth(): boolean {
  const settingsPath = path.join(os.homedir(), ".gemini", "settings.json");
  if (!existsSync(settingsPath)) return false;
  try {
    const data = JSON.parse(readFileSync(settingsPath, "utf8")) as Record<string, unknown>;
    const security = data.security as Record<string, unknown> | undefined;
    const auth = security?.auth as Record<string, unknown> | undefined;
    return Boolean(auth?.selectedType || data.selectedAuthType);
  } catch {
    return false;
  }
}

/**
 * Wrapper around the real Gemini CLI (`gemini`).
 * Writes an isolated workspace `.gemini/settings.json` configuring `mcpServers.privo`
 * over Streamable HTTP so the developer's `~/.gemini/settings.json` stays untouched.
 */
export class GeminiCliAdapter implements McpClientAdapter {
  readonly id = "gemini" as const;
  readonly name = "Gemini CLI";
  readonly kind = "cli" as const;

  private tempDir: string | null = null;
  private readonly model = process.env.PRIVO_PROBE_GEMINI_MODEL || "";

  async checkAvailability(): Promise<ClientAvailability> {
    const bin = await detectBinary("gemini", ["--version"]);
    const hasEnv = Boolean(
      process.env.GEMINI_API_KEY ||
        process.env.GOOGLE_API_KEY ||
        process.env.GOOGLE_GENAI_USE_VERTEXAI ||
        process.env.GOOGLE_GENAI_USE_GCA,
    );
    const hasLocal = hasGeminiLocalAuth();
    return {
      id: this.id,
      name: this.name,
      installed: bin.installed,
      version: bin.version,
      authReady: bin.installed && (hasEnv || hasLocal),
      note: bin.installed
        ? hasEnv
          ? "API key / Vertex auth configured"
          : hasLocal
            ? "Using ~/.gemini/settings.json auth"
            : "Set GEMINI_API_KEY or run `gemini` once to authenticate"
        : "Install with: npm i -g @google/gemini-cli",
    };
  }

  async configure(opts: ClientConfigOpts): Promise<void> {
    await this.teardown();
    this.tempDir = await mkdtemp(path.join(os.tmpdir(), "privo-probe-gemini-"));
    const geminiDir = path.join(this.tempDir, ".gemini");
    await mkdir(geminiDir, { recursive: true });

    const serverCfg: Record<string, unknown> = {
      httpUrl: opts.mcpUrl,
      trust: true,
    };
    if (opts.bearerToken) {
      serverCfg.headers = { Authorization: `Bearer ${opts.bearerToken}` };
    }

    await writeFile(
      path.join(geminiDir, "settings.json"),
      JSON.stringify({ mcpServers: { privo: serverCfg } }, null, 2),
      "utf8",
    );
  }

  async listTools(): Promise<string[]> {
    const res = await this.runPrompt(
      "List every MCP tool currently exposed by the `privo` MCP server as a plain JSON array of exact tool names, with no extra commentary.",
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
      throw new Error("GeminiCliAdapter not configured");
    }
    const start = Date.now();
    const timeoutMs = opts.timeoutMs ?? 90_000;

    const args = ["-p", prompt, "--output-format", "json", "--yolo"];
    if (this.model) {
      args.push("-m", this.model);
    }

    const { exitCode, stdout, stderr, timedOut } = await runProcess("gemini", args, {
      cwd: this.tempDir,
      timeoutMs,
      env: process.env,
    });

    let outputText = stdout;
    const toolCalls: ClientPromptResult["toolCalls"] = [];
    try {
      const parsed = JSON.parse(stdout) as Record<string, unknown>;
      if (typeof parsed.response === "string") outputText = parsed.response;
      if (parsed.stats && typeof parsed.stats === "object") {
        const tools = (parsed.stats as Record<string, unknown>).tools as Record<string, unknown> | undefined;
        if (tools && typeof tools.byName === "object" && tools.byName) {
          for (const name of Object.keys(tools.byName as Record<string, unknown>)) {
            toolCalls.push({ name });
          }
        }
      }
    } catch {
      // Fallback to raw stdout if CLI emitted warning lines before JSON
      const idx = stdout.indexOf("{");
      if (idx >= 0) {
        try {
          const parsed = JSON.parse(stdout.slice(idx)) as Record<string, unknown>;
          if (typeof parsed.response === "string") outputText = parsed.response;
        } catch {
          // keep raw stdout
        }
      }
    }

    return {
      ok: !timedOut && exitCode === 0,
      durationMs: Date.now() - start,
      exitCode,
      outputText: outputText.trim(),
      toolCalls,
      rawStdout: stdout,
      rawStderr: stderr,
      error: timedOut ? `Timed out after ${timeoutMs}ms` : exitCode !== 0 ? stderr || `Exit code ${exitCode}` : undefined,
    };
  }

  async teardown(): Promise<void> {
    if (this.tempDir) {
      await rm(this.tempDir, { recursive: true, force: true }).catch(() => {});
      this.tempDir = null;
    }
  }
}
