// A fake server for the app's tests: answers by "METHOD /path" and records calls.
import { vi } from "vitest";
import type { Session } from "../api";

export type Call = { method: string; url: string; headers: Record<string, string>; body: unknown };

export function fakeServer(routes: Record<string, unknown | ((body: unknown) => [number, unknown])>) {
  const calls: Call[] = [];
  const fetchFn = vi.fn(async (input: RequestInfo | URL, init: RequestInit = {}) => {
    const url = String(input);
    const method = (init.method ?? "GET").toUpperCase();
    const headers = (init.headers ?? {}) as Record<string, string>;
    const body = typeof init.body === "string" ? JSON.parse(init.body) : undefined;
    calls.push({ method, url, headers, body });
    const r = routes[method + " " + url];
    if (r === undefined) return new Response(JSON.stringify({ error: "no route " + method + " " + url }), { status: 404 });
    const [status, data] = typeof r === "function" ? (r as (b: unknown) => [number, unknown])(body) : [200, r];
    return new Response(JSON.stringify(data), { status, headers: { "Content-Type": "application/json" } });
  });
  vi.stubGlobal("fetch", fetchFn);
  return { calls, writes: () => calls.filter((c) => c.method !== "GET") };
}

export function session(role: "owner" | "member" | "approver", over: Partial<Session> = {}): Session {
  const owner = role === "owner";
  return {
    user: { name: "Ada Lovelace", person: "ada", roles: [role] },
    can: {
      decide: role !== "member",
      servers: owner,
      rules: owner,
      people: owner,
      teams: owner,
      manage_tokens: owner,
      use_gateway: role !== "approver",
      activity: owner,
      connections: role !== "approver",
      oauth_servers: true,
    },
    csrf: "csrf-token-123",
    mcp_url: "https://privo.example/mcp",
    invite_roles: ["member", "approver", "auditor", "admin"],
    change_roles: ["member", "approver", "auditor", "admin", "owner"],
    token_roles: ["owner", "admin", "auditor", "approver"],
    landing: owner ? "/admin/servers" : "/admin/start",
    ...over,
  };
}

export async function settle(): Promise<void> {
  for (let i = 0; i < 5; i++) await new Promise((r) => setTimeout(r, 0));
}
