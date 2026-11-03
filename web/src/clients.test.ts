import { describe, expect, it } from "vitest";
import { claudeCommand, clients } from "./clients";

const url = "https://privo.example/mcp";
const tok = "privo_pat_abc_def";

describe("AI client setup", () => {
  it("has one snippet per client, each pointing at Privo with the token", () => {
    expect(clients.map((c) => c.id)).toEqual(["claude-code", "cursor", "vscode", "codex", "windsurf", "other"]);
    for (const c of clients) {
      const text = c.snippet(url, tok);
      expect(text).toContain(url);
      if (c.id !== "codex") expect(text).toContain("Bearer " + tok);
    }
  });

  it("writes each client's own format", () => {
    const by = (id: string) => clients.find((c) => c.id === id)!.snippet(url, tok);
    expect(by("claude-code")).toBe(`claude mcp add --transport http privo ${url} --header "Authorization: Bearer ${tok}"`);
    expect(claudeCommand(url, tok)).toBe(by("claude-code"));
    expect(JSON.parse(by("cursor"))).toEqual({ mcpServers: { privo: { url, headers: { Authorization: `Bearer ${tok}` } } } });
    expect(JSON.parse(by("vscode"))).toEqual({ servers: { privo: { type: "http", url, headers: { Authorization: `Bearer ${tok}` } } } });
    expect(JSON.parse(by("windsurf"))).toEqual({ mcpServers: { privo: { serverUrl: url, headers: { Authorization: `Bearer ${tok}` } } } });
    // Codex reads the token from the environment, so it never sits in config.toml.
    expect(by("codex")).toBe(`[mcp_servers.privo]\nurl = "${url}"\nbearer_token_env_var = "PRIVO_TOKEN"`);
    expect(by("codex")).not.toContain(tok);
  });

  it("keeps a token with quotes or braces inside the JSON string", () => {
    const odd = 'a"b}c';
    const parsed = JSON.parse(clients.find((c) => c.id === "cursor")!.snippet(url, odd));
    expect(parsed.mcpServers.privo.headers.Authorization).toBe("Bearer " + odd);
  });
});
