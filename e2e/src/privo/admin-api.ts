/**
 * Typed HTTP client for Privo's Admin API (/api/v1/*) and public discovery/health endpoints.
 * Shapes mirror internal/control and web/src/types.ts.
 */

export type ServerView = {
  id: string;
  alias: string;
  transport: string;
  enabled: boolean;
  status: string;
  error?: string;
  target: string;
  items: Record<string, number>;
  pending_review: number;
  auth?: string;
  registration?: string;
  connect_url?: string;
  key_header?: string;
  teams?: string[];
  team_tools?: Record<string, string[]>;
  header_names?: string[];
};

export type PinView = {
  kind: string;
  name: string;
  hash: string;
  status: string;
  definition: {
    description?: string;
    title?: string;
    inputSchema?: Record<string, unknown>;
    annotations?: Record<string, unknown>;
    [key: string]: unknown;
  } | null;
  findings?: Array<{ path: string; rule: string; severity: string; message: string; excerpt?: string }>;
  serving?: boolean;
};

export type PolicyCondition = {
  arg: string;
  op: string;
  value?: unknown;
};

export type PolicyView = {
  id: string;
  server: string;
  tool: string;
  subject: string;
  action: string;
  priority: number;
  note?: string;
  created_by: string;
  expires_at?: string | null;
  subject_person?: string;
  conditions?: PolicyCondition[];
  grant_of?: string;
};

export type ApprovalView = {
  id: string;
  status: string;
  requester: string;
  requester_person: string;
  server: string;
  tool: string;
  reason: string;
  created_at: string;
  expires_at: string;
  arguments?: unknown;
  decided_by?: string;
  decided_at?: string | null;
  own?: boolean;
  decide_blocked?: string;
};

export type TokenView = {
  id: string;
  token?: string;
  name: string;
  role: string;
  kind: string;
  person: string;
  bound?: boolean;
  created_at?: string;
  expires_at: string;
};

export type TeamView = {
  slug: string;
  name: string;
  description: string;
  profile: string;
  built_in?: boolean;
  members: Array<{ person: string; name: string }>;
  servers: string[];
};

export type MemberView = {
  person: string;
  name: string;
  roles: string[];
  github_login: string;
  created_at: string;
  removed: boolean;
};

export type AuditEventView = {
  id: string;
  time: string;
  actor: string;
  actor_id: string;
  server?: string;
  item?: string;
  tool?: string;
  action: string;
  decision: string;
  outcome: string;
  latency_ms?: number;
  approved_by?: string;
  user?: string;
  client?: string;
  arguments?: unknown;
};

export type AuditIntegrityReport = {
  report: {
    ok: boolean;
    blocks?: number;
    events?: number;
    last_sealed?: string;
    problem?: string;
    [key: string]: unknown;
  };
  retention_days: number;
};

export type TelemetrySummary = {
  window: string;
  by: string;
  total: {
    calls: number;
    ok: number;
    tool_errors: number;
    errors: number;
    refused: number;
  };
  groups: Array<{ key: string; calls: number; ok: number; refused: number }>;
  recording: boolean;
};

export class PrivoApiError extends Error {
  constructor(
    public readonly status: number,
    public readonly method: string,
    public readonly path: string,
    public readonly body: string,
  ) {
    super(`Privo API ${method} ${path} failed (${status}): ${body}`);
  }
}

export class PrivoAdminApi {
  constructor(
    public readonly baseUrl: string,
    private readonly token: string,
  ) {}

  withToken(token: string): PrivoAdminApi {
    return new PrivoAdminApi(this.baseUrl, token);
  }

  async raw(
    method: string,
    path: string,
    body?: unknown,
    opts: { auth?: boolean; timeoutMs?: number } = {},
  ): Promise<{ status: number; headers: Headers; text: string; json: unknown }> {
    const headers: Record<string, string> = { Accept: "application/json" };
    if (opts.auth !== false && this.token) {
      headers.Authorization = `Bearer ${this.token}`;
    }
    let payload: string | undefined;
    if (body !== undefined) {
      headers["Content-Type"] = "application/json";
      payload = JSON.stringify(body);
    }

    let res: Response | undefined;
    let lastErr: unknown;
    const maxAttempts = method === "GET" ? 3 : 2;
    for (let attempt = 1; attempt <= maxAttempts; attempt++) {
      try {
        res = await fetch(`${this.baseUrl}${path}`, {
          method,
          headers,
          body: payload,
          signal: AbortSignal.timeout(opts.timeoutMs ?? 20_000),
        });
        break;
      } catch (err) {
        lastErr = err;
        const msg = err instanceof Error ? err.message : String(err);
        const isTransient = msg.includes("fetch failed") || msg.includes("ECONNRESET") || msg.includes("UND_ERR");
        if (!isTransient || attempt === maxAttempts) {
          const cause =
            err instanceof Error && "cause" in err && err.cause
              ? ` (${err.cause instanceof Error ? err.cause.message : String(err.cause)})`
              : "";
          throw new Error(`${method} ${path} network error: ${msg}${cause}`);
        }
        await new Promise((r) => setTimeout(r, 400 * attempt));
      }
    }
    if (!res) {
      throw lastErr instanceof Error ? lastErr : new Error(String(lastErr));
    }
    const text = await res.text();
    let json: unknown = undefined;
    if (text) {
      try {
        json = JSON.parse(text);
      } catch {
        json = undefined;
      }
    }
    return { status: res.status, headers: res.headers, text, json };
  }

  private async request<T>(method: string, path: string, body?: unknown, timeoutMs = 25_000): Promise<T> {
    const res = await this.raw(method, path, body, { auth: true, timeoutMs });
    if (res.status < 200 || res.status >= 300) {
      throw new PrivoApiError(res.status, method, path, res.text.slice(0, 500));
    }
    return res.json as T;
  }

  // ---- Public endpoints
  livez() {
    return this.raw("GET", "/livez", undefined, { auth: false });
  }
  readyz() {
    return this.raw("GET", "/readyz", undefined, { auth: false });
  }
  version() {
    return this.raw("GET", "/version", undefined, { auth: false });
  }
  oauthProtectedResource() {
    return this.raw("GET", "/.well-known/oauth-protected-resource/mcp", undefined, { auth: false });
  }
  oauthAuthorizationServer() {
    return this.raw("GET", "/.well-known/oauth-authorization-server", undefined, { auth: false });
  }
  rootPage() {
    return this.raw("GET", "/", undefined, { auth: false });
  }

  // ---- Servers & pins
  async listServers(): Promise<ServerView[]> {
    const res = await this.request<{ servers: ServerView[] }>("GET", "/api/v1/servers");
    return res.servers ?? [];
  }

  async createServer(spec: {
    alias: string;
    transport: "http" | "sse";
    url: string;
    headers?: Record<string, string>;
    personal_key?: { header?: string; prefix?: string; help?: string; help_url?: string };
    teams?: string[];
  }): Promise<ServerView> {
    const res = await this.request<{ server: ServerView }>("POST", "/api/v1/servers", spec, 30_000);
    return res.server;
  }

  async patchServer(
    alias: string,
    patch: { enabled?: boolean; teams?: string[]; headers?: Record<string, string>; url?: string },
  ): Promise<ServerView> {
    const res = await this.request<{ server: ServerView }>(
      "PATCH",
      `/api/v1/servers/${encodeURIComponent(alias)}`,
      patch,
      30_000,
    );
    return res.server;
  }

  async deleteServer(alias: string): Promise<void> {
    await this.request("DELETE", `/api/v1/servers/${encodeURIComponent(alias)}`);
  }

  async listPins(alias: string): Promise<PinView[]> {
    const res = await this.request<{ pins: PinView[] }>("GET", `/api/v1/servers/${encodeURIComponent(alias)}/pins`);
    return res.pins ?? [];
  }

  async acceptPins(alias: string, pins: Array<{ kind: string; name: string; hash: string }>): Promise<{ accepted: number }> {
    return this.request("POST", `/api/v1/servers/${encodeURIComponent(alias)}/pins:accept`, { pins });
  }

  async blockPins(alias: string, pins: Array<{ kind: string; name: string; hash: string }>): Promise<{ blocked: number }> {
    return this.request("POST", `/api/v1/servers/${encodeURIComponent(alias)}/pins:block`, { pins });
  }

  async setTeamTools(alias: string, teamSlug: string, tools: string[] | null): Promise<void> {
    await this.request(
      "PATCH",
      `/api/v1/servers/${encodeURIComponent(alias)}/teams/${encodeURIComponent(teamSlug)}`,
      { tools },
    );
  }

  async applyPolicyPreset(alias: string, preset: "read-only" | "ask-before-writes"): Promise<PolicyView[]> {
    const res = await this.request<{ policies: PolicyView[] }>(
      "POST",
      `/api/v1/servers/${encodeURIComponent(alias)}/policy:preset`,
      { preset },
    );
    return res.policies ?? [];
  }

  // ---- Teams
  async listTeams(): Promise<TeamView[]> {
    const res = await this.request<{ teams: TeamView[] }>("GET", "/api/v1/teams");
    return res.teams ?? [];
  }

  async createTeam(spec: { name: string; slug: string; description?: string; profile?: string }): Promise<TeamView> {
    const res = await this.request<{ team: TeamView }>("POST", "/api/v1/teams", spec);
    return res.team;
  }

  async deleteTeam(slug: string): Promise<void> {
    await this.request("DELETE", `/api/v1/teams/${encodeURIComponent(slug)}`);
  }

  async addTeamMember(slug: string, person: string): Promise<void> {
    await this.request("POST", `/api/v1/teams/${encodeURIComponent(slug)}/members`, { person });
  }

  async removeTeamMember(slug: string, person: string): Promise<void> {
    await this.request(
      "DELETE",
      `/api/v1/teams/${encodeURIComponent(slug)}/members/${encodeURIComponent(person)}`,
    );
  }

  // ---- Members
  async listMembers(): Promise<MemberView[]> {
    const res = await this.request<{ members: MemberView[] }>("GET", "/api/v1/members");
    return res.members ?? [];
  }

  async setMemberRoles(person: string, roles: string[]): Promise<MemberView> {
    const res = await this.request<{ member: MemberView }>(
      "PATCH",
      `/api/v1/members/${encodeURIComponent(person)}`,
      { roles },
    );
    return res.member;
  }

  // ---- Tokens
  async listTokens(): Promise<TokenView[]> {
    const res = await this.request<{ tokens: TokenView[] }>("GET", "/api/v1/tokens");
    return res.tokens ?? [];
  }

  async createToken(spec: {
    name: string;
    role: string;
    kind: "gateway" | "control";
    person?: string;
    confirm?: boolean;
  }): Promise<TokenView & { token: string }> {
    return this.request("POST", "/api/v1/tokens", spec);
  }

  async revokeToken(id: string): Promise<void> {
    await this.request("DELETE", `/api/v1/tokens/${encodeURIComponent(id)}`);
  }

  // ---- Policies
  async listPolicies(): Promise<{ policies: PolicyView[]; default: string }> {
    return this.request("GET", "/api/v1/policies");
  }

  async createPolicy(spec: {
    server: string;
    tool: string;
    action: "allow" | "deny" | "ask";
    subject?: string;
    priority?: number;
    note?: string;
    expires_in?: string;
    conditions?: PolicyCondition[];
  }): Promise<PolicyView> {
    const res = await this.request<{ policy: PolicyView }>("POST", "/api/v1/policies", spec);
    return res.policy;
  }

  async deletePolicy(id: string): Promise<void> {
    await this.request("DELETE", `/api/v1/policies/${encodeURIComponent(id)}`);
  }

  async exportPoliciesYaml(): Promise<string> {
    const res = await this.raw("GET", "/api/v1/policies/export");
    if (res.status !== 200) {
      throw new PrivoApiError(res.status, "GET", "/api/v1/policies/export", res.text);
    }
    if (res.json && typeof res.json === "object" && "yaml" in (res.json as Record<string, unknown>)) {
      return String((res.json as Record<string, unknown>).yaml);
    }
    return res.text;
  }

  async importPoliciesYaml(yaml: string, mode: "merge" | "replace", dryRun: boolean): Promise<Record<string, unknown>> {
    return this.request("POST", "/api/v1/policies/import", { yaml, mode, dry_run: dryRun });
  }

  // ---- Approvals
  async listApprovals(status = "pending"): Promise<ApprovalView[]> {
    const res = await this.request<{ approvals: ApprovalView[] }>(
      "GET",
      `/api/v1/approvals?status=${encodeURIComponent(status)}`,
    );
    return res.approvals ?? [];
  }

  async getApproval(id: string): Promise<ApprovalView> {
    const res = await this.request<{ approval: ApprovalView }>("GET", `/api/v1/approvals/${encodeURIComponent(id)}`);
    return res.approval;
  }

  async approve(id: string, grant?: "15m" | "1h" | "8h"): Promise<Record<string, unknown>> {
    return this.request(
      "POST",
      `/api/v1/approvals/${encodeURIComponent(id)}/approve`,
      grant ? { grant } : undefined,
    );
  }

  async deny(id: string): Promise<Record<string, unknown>> {
    return this.request("POST", `/api/v1/approvals/${encodeURIComponent(id)}/deny`);
  }

  // ---- Audit & telemetry
  async listAuditEvents(params: {
    kind?: "calls" | "admin";
    server?: string;
    actor?: string;
    decision?: string;
    limit?: number;
  } = {}): Promise<AuditEventView[]> {
    const q = new URLSearchParams();
    if (params.kind) q.set("kind", params.kind);
    if (params.server) q.set("server", params.server);
    if (params.actor) q.set("actor", params.actor);
    if (params.decision) q.set("decision", params.decision);
    if (params.limit) q.set("limit", String(params.limit));
    const qs = q.toString() ? `?${q.toString()}` : "";
    const res = await this.request<{ events: AuditEventView[] }>("GET", `/api/v1/audit/events${qs}`);
    return res.events ?? [];
  }

  async exportAuditCsv(params: { server?: string } = {}): Promise<{ csv: string; rows: number; truncated: boolean }> {
    const qs = params.server ? `?server=${encodeURIComponent(params.server)}` : "";
    return this.request("GET", `/api/v1/audit/export${qs}`);
  }

  async checkAuditIntegrity(): Promise<AuditIntegrityReport> {
    return this.request("GET", "/api/v1/audit/integrity");
  }

  async telemetrySummary(window = "24h", by = "server"): Promise<TelemetrySummary> {
    return this.request("GET", `/api/v1/telemetry/summary?window=${encodeURIComponent(window)}&by=${encodeURIComponent(by)}`);
  }

  // ---- OAuth & connections
  async listOAuthClients(): Promise<{ enabled: boolean; policy: string; registration: boolean; clients: unknown[] }> {
    return this.request("GET", "/api/v1/oauth/clients");
  }

  async listOAuthGrants(): Promise<{ enabled: boolean; clients: Array<{ id: string; client: string; person?: string }> }> {
    return this.request("GET", "/api/v1/oauth/grants");
  }

  async revokeOAuthGrant(id: string): Promise<void> {
    await this.request("DELETE", `/api/v1/oauth/grants/${encodeURIComponent(id)}`);
  }

  async listAllConnections(params: { person?: string; server?: string } = {}): Promise<unknown[]> {
    const q = new URLSearchParams();
    if (params.person) q.set("person", params.person);
    if (params.server) q.set("server", params.server);
    const qs = q.toString() ? `?${q.toString()}` : "";
    const res = await this.request<{ connections: unknown[] }>("GET", `/api/v1/connections${qs}`);
    return res.connections ?? [];
  }

  async disconnectUser(alias: string, person: string): Promise<void> {
    await this.request(
      "DELETE",
      `/api/v1/servers/${encodeURIComponent(alias)}/connections/${encodeURIComponent(person)}`,
    );
  }
}
