import crypto from "node:crypto";
import type { PinView, PolicyCondition, PolicyView, PrivoAdminApi, ServerView, TeamView, TokenView } from "./admin-api.js";
import { eventually } from "./oracles.js";

/**
 * Manages prefix-scoped test fixtures on a deployed Privo instance.
 *
 * Privo server aliases must match `^[a-z][a-z0-9-]{1,15}$` (max 16 chars),
 * so every server created by privo-probe uses `p-<4hex>-<shortCode>` (<= 11 chars).
 * Every resource created during a run is tracked and torn down automatically.
 */
export class FixtureManager {
  public readonly runId: string;
  private readonly servers: string[] = [];
  private readonly policies: string[] = [];
  private readonly teams: string[] = [];
  private readonly tokenIds: string[] = [];

  constructor(private readonly api: PrivoAdminApi, runId?: string) {
    this.runId = runId || crypto.randomBytes(2).toString("hex");
  }

  /** Valid Privo server alias <= 16 chars: e.g. `p-a3f1-dw` */
  serverAlias(shortCode: string): string {
    const clean = shortCode.toLowerCase().replace(/[^a-z0-9]/g, "").slice(0, 6) || "srv";
    return `p-${this.runId}-${clean}`;
  }

  /** Valid team slug: e.g. `p-a3f1-eng` */
  teamSlug(shortCode: string): string {
    const clean = shortCode.toLowerCase().replace(/[^a-z0-9]/g, "").slice(0, 12) || "team";
    return `p-${this.runId}-${clean}`;
  }

  /** Token name prefixed for cleanup */
  tokenName(label: string): string {
    const clean = label.toLowerCase().replace(/[^a-z0-9-]/g, "-").slice(0, 24);
    return `probe-${this.runId}-${clean}`;
  }

  async createServer(spec: {
    shortCode: string;
    transport: "http" | "sse";
    url: string;
    headers?: Record<string, string>;
    personal_key?: { header?: string; prefix?: string; help?: string; help_url?: string };
    teams?: string[];
  }): Promise<ServerView> {
    const alias = this.serverAlias(spec.shortCode);
    const server = await this.api.createServer({
      alias,
      transport: spec.transport,
      url: spec.url,
      headers: spec.headers,
      personal_key: spec.personal_key,
      teams: spec.teams,
    });
    this.servers.push(alias);
    return server;
  }

  async waitForServerUpAndPins(alias: string, timeoutMs = 25_000): Promise<PinView[]> {
    return eventually(
      `server ${alias} status=up with discoverable pins`,
      async () => {
        const servers = await this.api.listServers();
        const found = servers.find((s) => s.alias === alias);
        if (!found || found.status !== "up") return null;
        const pins = await this.api.listPins(alias);
        return pins.length > 0 ? pins : null;
      },
      { timeoutMs, intervalMs: 1000 },
    );
  }

  async acceptAllPins(alias: string, kinds: string[] = ["tool", "prompt", "resource"]): Promise<PinView[]> {
    const pins = await this.waitForServerUpAndPins(alias);
    const toAccept = pins.filter((p) => p.status !== "accepted" && kinds.includes(p.kind));
    if (toAccept.length > 0) {
      await this.api.acceptPins(
        alias,
        toAccept.map((p) => ({ kind: p.kind, name: p.name, hash: p.hash })),
      );
    }
    return this.api.listPins(alias);
  }

  async createTeam(spec: { shortCode: string; name: string; description?: string; profile?: string }): Promise<TeamView> {
    const slug = this.teamSlug(spec.shortCode);
    const team = await this.api.createTeam({
      slug,
      name: spec.name,
      description: spec.description ?? `Temporary privo-probe team (${this.runId})`,
      profile: spec.profile ?? "developers",
    });
    this.teams.push(slug);
    return team;
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
    const pol = await this.api.createPolicy({
      ...spec,
      note: spec.note ?? `probe:${this.runId}`,
    });
    this.policies.push(pol.id);
    return pol;
  }

  trackPolicy(id: string): void {
    if (!this.policies.includes(id)) this.policies.push(id);
  }

  async createToken(spec: {
    label: string;
    role: string;
    kind: "gateway" | "control";
    person?: string;
    confirm?: boolean;
  }): Promise<TokenView & { token: string }> {
    const tok = await this.api.createToken({
      name: this.tokenName(spec.label),
      role: spec.role,
      kind: spec.kind,
      person: spec.person,
      confirm: spec.confirm,
    });
    this.tokenIds.push(tok.id);
    return tok;
  }

  /** Tears down every resource created during this run, ignoring already-deleted items. */
  async cleanup(): Promise<string[]> {
    const warnings: string[] = [];

    for (const id of [...this.policies].reverse()) {
      try {
        await this.api.deletePolicy(id);
      } catch (err) {
        if (!String(err).includes("404")) warnings.push(`deletePolicy(${id}): ${String(err)}`);
      }
    }
    this.policies.length = 0;

    for (const alias of [...this.servers].reverse()) {
      try {
        await this.api.deleteServer(alias);
      } catch (err) {
        if (!String(err).includes("404")) warnings.push(`deleteServer(${alias}): ${String(err)}`);
      }
    }
    this.servers.length = 0;

    for (const slug of [...this.teams].reverse()) {
      try {
        await this.api.deleteTeam(slug);
      } catch (err) {
        if (!String(err).includes("404")) warnings.push(`deleteTeam(${slug}): ${String(err)}`);
      }
    }
    this.teams.length = 0;

    for (const id of [...this.tokenIds].reverse()) {
      try {
        await this.api.revokeToken(id);
      } catch (err) {
        if (!String(err).includes("404")) warnings.push(`revokeToken(${id}): ${String(err)}`);
      }
    }
    this.tokenIds.length = 0;

    return warnings;
  }

  /**
   * Sweeps any orphaned `p-<4hex>-*` / `probe-*` fixtures left behind by crashed runs.
   * Never touches non-probe org resources.
   */
  static async reapOrphans(api: PrivoAdminApi): Promise<{
    serversDeleted: string[];
    policiesDeleted: string[];
    teamsDeleted: string[];
    tokensRevoked: string[];
  }> {
    const probeAliasRe = /^p-[0-9a-f]{4}-[a-z0-9-]+$/;
    const probeTokenRe = /^probe-[0-9a-f]{4}-/;
    const probeNoteRe = /^probe:[0-9a-f]{4}/;

    const serversDeleted: string[] = [];
    const policiesDeleted: string[] = [];
    const teamsDeleted: string[] = [];
    const tokensRevoked: string[] = [];

    const { policies } = await api.listPolicies();
    for (const p of policies) {
      if (probeAliasRe.test(p.server) || (p.note && probeNoteRe.test(p.note))) {
        await api.deletePolicy(p.id).catch(() => {});
        policiesDeleted.push(p.id);
      }
    }

    const servers = await api.listServers();
    for (const s of servers) {
      if (probeAliasRe.test(s.alias)) {
        await api.deleteServer(s.alias).catch(() => {});
        serversDeleted.push(s.alias);
      }
    }

    const teams = await api.listTeams();
    for (const t of teams) {
      if (!t.built_in && probeAliasRe.test(t.slug)) {
        await api.deleteTeam(t.slug).catch(() => {});
        teamsDeleted.push(t.slug);
      }
    }

    const tokens = await api.listTokens();
    for (const t of tokens) {
      if (probeTokenRe.test(t.name)) {
        await api.revokeToken(t.id).catch(() => {});
        tokensRevoked.push(t.name);
      }
    }

    return { serversDeleted, policiesDeleted, teamsDeleted, tokensRevoked };
  }
}
