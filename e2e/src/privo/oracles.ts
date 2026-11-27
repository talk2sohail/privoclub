import type { ApprovalView, AuditEventView, PrivoAdminApi } from "./admin-api.js";

export async function sleep(ms: number): Promise<void> {
  await new Promise((resolve) => setTimeout(resolve, ms));
}

export async function eventually<T>(
  description: string,
  fn: () => Promise<T | null | undefined | false>,
  opts: { timeoutMs?: number; intervalMs?: number } = {},
): Promise<T> {
  const timeoutMs = opts.timeoutMs ?? 15_000;
  const intervalMs = opts.intervalMs ?? 500;
  const deadline = Date.now() + timeoutMs;
  let lastError: unknown;

  while (Date.now() < deadline) {
    try {
      const val = await fn();
      if (val !== null && val !== undefined && val !== false) {
        return val;
      }
    } catch (err) {
      lastError = err;
    }
    await sleep(intervalMs);
  }

  const suffix = lastError ? ` (last error: ${lastError instanceof Error ? lastError.message : String(lastError)})` : "";
  throw new Error(`Timed out after ${timeoutMs}ms waiting for: ${description}${suffix}`);
}

export async function waitForAuditCall(
  api: PrivoAdminApi,
  match: {
    server: string;
    item?: string;
    action?: string;
    decisionPrefix?: string;
    outcomePrefix?: string;
    sinceIso?: string;
  },
  timeoutMs = 15_000,
): Promise<AuditEventView> {
  return eventually(
    `audit call event server=${match.server} item=${match.item ?? "*"} decision=${match.decisionPrefix ?? "*"} outcome=${match.outcomePrefix ?? "*"}`,
    async () => {
      const events = await api.listAuditEvents({ kind: "calls", server: match.server, limit: 100 });
      return (
        events.find((e) => {
          if (match.sinceIso && e.time < match.sinceIso) return false;
          if (match.item && e.item !== match.item && e.tool !== match.item) return false;
          if (match.action && e.action !== match.action) return false;
          if (match.decisionPrefix && !e.decision.startsWith(match.decisionPrefix)) return false;
          if (match.outcomePrefix && !e.outcome.startsWith(match.outcomePrefix)) return false;
          return true;
        }) ?? null
      );
    },
    { timeoutMs, intervalMs: 600 },
  );
}

export async function waitForAdminAudit(
  api: PrivoAdminApi,
  actionPrefix: string,
  sinceIso?: string,
  timeoutMs = 12_000,
): Promise<AuditEventView> {
  return eventually(
    `admin audit action prefix "${actionPrefix}"`,
    async () => {
      const events = await api.listAuditEvents({ kind: "admin", limit: 150 });
      return (
        events.find((e) => {
          if (sinceIso && e.time < sinceIso) return false;
          return e.action.startsWith(actionPrefix);
        }) ?? null
      );
    },
    { timeoutMs, intervalMs: 600 },
  );
}

export async function waitForPendingApproval(
  api: PrivoAdminApi,
  match: { server: string; tool?: string; sinceIso?: string },
  timeoutMs = 15_000,
): Promise<ApprovalView> {
  return eventually(
    `pending approval for server=${match.server} tool=${match.tool ?? "*"}`,
    async () => {
      const approvals = await api.listApprovals("pending");
      return (
        approvals.find((a) => {
          if (a.server !== match.server) return false;
          if (match.tool && a.tool !== match.tool) return false;
          if (match.sinceIso && a.created_at < match.sinceIso) return false;
          return true;
        }) ?? null
      );
    },
    { timeoutMs, intervalMs: 500 },
  );
}

export async function assertAuditIntegrity(api: PrivoAdminApi): Promise<{ blocks: number; events: number }> {
  const res = await api.checkAuditIntegrity();
  if (!res.report?.ok) {
    throw new Error(`Audit integrity check FAILED: ${JSON.stringify(res.report)}`);
  }
  return {
    blocks: Number(res.report.blocks ?? 0),
    events: Number(res.report.events ?? 0),
  };
}
