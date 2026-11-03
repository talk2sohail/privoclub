// The app's only way to the server (ADR 0019): JSON over the session, with
// the session's CSRF token in a header on every call.

export type Can = {
  decide: boolean;
  servers: boolean;
  rules: boolean;
  people: boolean;
  manage_tokens: boolean;
  use_gateway: boolean;
  activity: boolean;
  connections: boolean;
  oauth_servers: boolean;
};

export type Session = {
  user: { name: string; person: string; role: string };
  can: Can;
  csrf: string;
  mcp_url: string;
  invite_roles: string[];
  change_roles: string[];
  token_roles: string[];
  landing: string;
};

export class ApiError extends Error {
  readonly status: number;
  constructor(status: number, message: string) {
    super(message);
    this.status = status;
  }
}

let csrf = "";

/** Where the browser goes when the session is gone. Replaced in tests. */
export const nav = {
  signIn(): void {
    window.location.assign("/admin/signin");
  },
};

async function decode(res: Response): Promise<Record<string, unknown>> {
  const text = await res.text();
  if (!text) return {};
  try {
    const v: unknown = JSON.parse(text);
    return v && typeof v === "object" ? (v as Record<string, unknown>) : {};
  } catch {
    return {};
  }
}

function failure(res: Response, data: Record<string, unknown>): ApiError {
  if (res.status === 401) {
    nav.signIn();
    return new ApiError(401, "Your session ended. Sign in again.");
  }
  const msg = typeof data.error === "string" && data.error ? data.error : `The server answered ${res.status}. Try again.`;
  return new ApiError(res.status, msg);
}

/** loadSession learns who is signed in, and the CSRF token for later calls. */
export async function loadSession(): Promise<Session> {
  const res = await fetch("/admin/api/session", { credentials: "same-origin", headers: { Accept: "application/json" } });
  const data = await decode(res);
  if (!res.ok) throw failure(res, data);
  const s = data as unknown as Session;
  csrf = s.csrf;
  return s;
}

/**
 * api calls /admin/api{path}: for the admin API, path starts with /v1/.
 * It resolves to the decoded JSON or rejects with an ApiError carrying the
 * server's own message.
 */
export async function api<T = Record<string, unknown>>(method: string, path: string, body?: unknown): Promise<T> {
  const headers: Record<string, string> = { Accept: "application/json", "X-Privo-CSRF": csrf };
  let payload: string | undefined;
  if (body !== undefined) {
    headers["Content-Type"] = "application/json";
    payload = JSON.stringify(body);
  }
  let res: Response;
  try {
    res = await fetch("/admin/api" + path, { method, headers, body: payload, credentials: "same-origin" });
  } catch {
    throw new ApiError(0, "Privo could not be reached. Check your connection and try again.");
  }
  const data = await decode(res);
  if (!res.ok) throw failure(res, data);
  return data as T;
}

/** seg escapes one path segment. */
export function seg(s: string): string {
  return encodeURIComponent(s);
}
