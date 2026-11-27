/**
 * Redacts credentials, Privo tokens, GitHub PATs, cookies, and API keys
 * before any wire trace, log, or release report is persisted.
 */

const SECRET_PATTERNS: Array<[RegExp, string]> = [
  [/privo_pat_[A-Za-z0-9._-]+/g, "privo_pat_[REDACTED]"],
  [/privo_oat_[A-Za-z0-9._-]+/g, "privo_oat_[REDACTED]"],
  [/gh[pousr]_[A-Za-z0-9_]{16,}/g, "gh_[REDACTED]"],
  [/github_pat_[A-Za-z0-9_]{16,}/g, "github_pat_[REDACTED]"],
  [/sk-[A-Za-z0-9_-]{20,}/g, "sk-[REDACTED]"],
  [/AIza[A-Za-z0-9_-]{20,}/g, "AIza[REDACTED]"],
  [/(Bearer\s+)[A-Za-z0-9._~+/=-]{8,}/gi, "$1[REDACTED]"],
];

const SENSITIVE_HEADER_NAMES = new Set([
  "authorization",
  "cookie",
  "set-cookie",
  "x-api-key",
  "x-privo-csrf",
  "context7_api_key",
  "proxy-authorization",
]);

const SENSITIVE_KEY_NAMES = new Set([
  "token",
  "access_token",
  "refresh_token",
  "client_secret",
  "password",
  "secret",
  "api_key",
  "apikey",
  "csrf",
]);

export function scrubString(input: string, extraSecrets: string[] = []): string {
  let out = input;
  for (const s of extraSecrets) {
    if (s && s.length >= 6) {
      out = out.split(s).join("[REDACTED]");
    }
  }
  for (const [pattern, replacement] of SECRET_PATTERNS) {
    out = out.replace(pattern, replacement);
  }
  return out;
}

export function scrubHeaders(
  headers: Record<string, string | string[] | undefined>,
  extraSecrets: string[] = [],
): Record<string, string> {
  const out: Record<string, string> = {};
  for (const [k, v] of Object.entries(headers)) {
    if (v === undefined) continue;
    const joined = Array.isArray(v) ? v.join(", ") : v;
    if (SENSITIVE_HEADER_NAMES.has(k.toLowerCase())) {
      out[k] = "[REDACTED]";
    } else {
      out[k] = scrubString(joined, extraSecrets);
    }
  }
  return out;
}

export function scrubUnknown(value: unknown, extraSecrets: string[] = []): unknown {
  if (typeof value === "string") return scrubString(value, extraSecrets);
  if (Array.isArray(value)) return value.map((item) => scrubUnknown(item, extraSecrets));
  if (value && typeof value === "object") {
    const out: Record<string, unknown> = {};
    for (const [k, v] of Object.entries(value as Record<string, unknown>)) {
      if (SENSITIVE_KEY_NAMES.has(k.toLowerCase())) {
        out[k] = "[REDACTED]";
      } else {
        out[k] = scrubUnknown(v, extraSecrets);
      }
    }
    return out;
  }
  return value;
}
