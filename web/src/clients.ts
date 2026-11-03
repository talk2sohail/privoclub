// How each AI client is pointed at Privo (ADR 0020): one MCP server, Privo's
// /mcp, with the person's token as a bearer header. Every snippet is plain
// text the person copies; the app never runs or opens any of it.

export type Client = {
  id: string;
  name: string;
  /** where the snippet goes, shown above it */
  where: string;
  snippet: (mcpURL: string, token: string) => string;
  /** a second step, when there is one */
  then?: string;
};

const json = (v: unknown) => JSON.stringify(v, null, 2);

export const clients: Client[] = [
  {
    id: "claude-code",
    name: "Claude Code",
    where: "Run in a terminal:",
    snippet: (url, token) => `claude mcp add --transport http privo ${url} --header "Authorization: Bearer ${token}"`,
    then: "Then run /mcp in Claude Code to see Privo's tools.",
  },
  {
    id: "cursor",
    name: "Cursor",
    where: "Add to ~/.cursor/mcp.json (or .cursor/mcp.json in a project):",
    snippet: (url, token) => json({ mcpServers: { privo: { url, headers: { Authorization: `Bearer ${token}` } } } }),
  },
  {
    id: "vscode",
    name: "VS Code",
    where: "Add to .vscode/mcp.json, or to your user mcp.json (MCP: Open User Configuration):",
    snippet: (url, token) => json({ servers: { privo: { type: "http", url, headers: { Authorization: `Bearer ${token}` } } } }),
  },
  {
    id: "codex",
    name: "Codex",
    where: "Add to ~/.codex/config.toml:",
    snippet: (url) => `[mcp_servers.privo]\nurl = "${url}"\nbearer_token_env_var = "PRIVO_TOKEN"`,
    then: "Then keep the token in your shell's environment, for example in ~/.zshrc: export PRIVO_TOKEN=<your token>",
  },
  {
    id: "windsurf",
    name: "Windsurf",
    where: "Add to ~/.codeium/windsurf/mcp_config.json:",
    snippet: (url, token) => json({ mcpServers: { privo: { serverUrl: url, headers: { Authorization: `Bearer ${token}` } } } }),
  },
  {
    id: "other",
    name: "Other",
    where: "Any client that speaks MCP over Streamable HTTP:",
    snippet: (url, token) => `Server URL: ${url}\nHeader:     Authorization: Bearer ${token}`,
  },
];

/** claudeCommand is the Claude Code line (kept for the Tokens page). */
export function claudeCommand(mcpURL: string, token: string): string {
  return clients[0]!.snippet(mcpURL, token);
}

/** A placeholder shown before a token exists. */
export const tokenPlaceholder = "<your token>";
