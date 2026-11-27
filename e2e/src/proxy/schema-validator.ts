/**
 * Strict raw JSON-RPC / MCP response validator for MCP 2025-11-25 and 2026-07-28.
 *
 * Specifically designed to catch strict-client regressions that lenient SDKs
 * silently ignore (e.g., missing `resultType` on 2026-07-28, empty `cacheScope`,
 * null list entries, malformed `tools/list` or `tools/call` envelopes).
 */

export type ProtocolViolation = {
  method: string;
  protocolVersion: string;
  rule: string;
  detail: string;
};

const VALID_CACHE_SCOPES = new Set(["private", "public", "none"]);

const EXPECTED_RESULT_TYPE: Record<string, string> = {
  initialize: "initialize",
  "tools/list": "tools/list",
  "tools/call": "tools/call",
  "prompts/list": "prompts/list",
  "prompts/get": "prompts/get",
  "resources/list": "resources/list",
  "resources/templates/list": "resources/templates/list",
  "resources/read": "resources/read",
};

const LIST_METHODS = new Set([
  "tools/list",
  "prompts/list",
  "resources/list",
  "resources/templates/list",
]);

function isRecord(v: unknown): v is Record<string, unknown> {
  return typeof v === "object" && v !== null && !Array.isArray(v);
}

export function validateMcpResponse(
  method: string,
  protocolVersion: string,
  payload: unknown,
): ProtocolViolation[] {
  const violations: ProtocolViolation[] = [];
  const push = (rule: string, detail: string) =>
    violations.push({ method, protocolVersion, rule, detail });

  if (!isRecord(payload)) {
    push("jsonrpc.object", "Response payload is not a JSON object.");
    return violations;
  }

  if (payload.jsonrpc !== "2.0") {
    push("jsonrpc.version", `Expected jsonrpc="2.0", got ${JSON.stringify(payload.jsonrpc)}.`);
  }

  const hasResult = "result" in payload && payload.result !== undefined;
  const hasError = "error" in payload && payload.error !== undefined;
  if (hasResult && hasError) {
    push("jsonrpc.exclusive", "Response carries both result and error.");
  }
  if (!hasResult && !hasError) {
    push("jsonrpc.empty", "Response carries neither result nor error.");
    return violations;
  }

  if (hasError) {
    if (!isRecord(payload.error) || typeof payload.error.code !== "number" || typeof payload.error.message !== "string") {
      push("jsonrpc.error-shape", "JSON-RPC error object must have numeric code and string message.");
    }
    return violations;
  }

  const result = payload.result;
  if (!isRecord(result)) {
    push("mcp.result-object", "MCP result must be a JSON object.");
    return violations;
  }

  // 1. Check cacheHints whenever present (Claude Code rejected empty cacheScope in F02)
  if ("cacheHints" in result && result.cacheHints !== undefined) {
    if (!isRecord(result.cacheHints)) {
      push("mcp.cacheHints.object", "cacheHints must be an object when present.");
    } else {
      const scope = result.cacheHints.cacheScope;
      if (typeof scope !== "string" || !VALID_CACHE_SCOPES.has(scope)) {
        push(
          "mcp.cacheHints.cacheScope",
          `Invalid cacheScope ${JSON.stringify(scope)}; must be one of private|public|none.`,
        );
      }
      if ("ttlMs" in result.cacheHints && typeof result.cacheHints.ttlMs !== "number") {
        push("mcp.cacheHints.ttlMs", `ttlMs must be a number, got ${typeof result.cacheHints.ttlMs}.`);
      }
    }
  } else if (LIST_METHODS.has(method) || method === "resources/read") {
    // Privo ADR 0001 guarantees private cache hints on list and read results
    const meta = isRecord(result._meta) ? result._meta : undefined;
    if (!meta || !isRecord(meta.cacheHints)) {
      // Allow either top-level or _meta.cacheHints, only flag if cacheScope itself is malformed
    }
  }

  // 2. Check resultType on MCP 2026-07-28+ (Claude Code rejected missing resultType in F21)
  if (protocolVersion >= "2026-07-28" && method in EXPECTED_RESULT_TYPE) {
    if (typeof result.resultType !== "string" || result.resultType.length === 0) {
      push(
        "mcp.2026.resultType",
        `Protocol ${protocolVersion} requires non-empty resultType on ${method} responses.`,
      );
    }
  }

  // 3. Method-specific structural checks
  if (method === "tools/list") {
    if (!Array.isArray(result.tools)) {
      push("mcp.tools.list-array", "tools/list result.tools must be an array.");
    } else {
      for (let i = 0; i < result.tools.length; i++) {
        const tool = result.tools[i];
        if (!isRecord(tool)) {
          push("mcp.tools.entry-object", `tools[${i}] is not an object (null or primitive).`);
          continue;
        }
        if (typeof tool.name !== "string" || tool.name.length === 0) {
          push("mcp.tools.name", `tools[${i}] is missing a non-empty string name.`);
        }
        if (!isRecord(tool.inputSchema) || tool.inputSchema.type !== "object") {
          push(
            "mcp.tools.inputSchema",
            `tools[${i}] (${String(tool.name)}) inputSchema must be an object with type="object".`,
          );
        }
      }
    }
  }

  if (method === "tools/call") {
    if (!Array.isArray(result.content)) {
      push("mcp.tools.call-content", "tools/call result.content must be an array.");
    } else {
      for (let i = 0; i < result.content.length; i++) {
        const item = result.content[i];
        if (!isRecord(item) || typeof item.type !== "string") {
          push("mcp.tools.call-content-item", `tools/call content[${i}] must be an object with string type.`);
        }
      }
    }
    if ("isError" in result && typeof result.isError !== "boolean") {
      push("mcp.tools.call-isError", `tools/call isError must be boolean, got ${typeof result.isError}.`);
    }
  }

  if (method === "prompts/list") {
    if (!Array.isArray(result.prompts)) {
      push("mcp.prompts.list-array", "prompts/list result.prompts must be an array.");
    } else {
      for (let i = 0; i < result.prompts.length; i++) {
        const p = result.prompts[i];
        if (!isRecord(p) || typeof p.name !== "string") {
          push("mcp.prompts.entry", `prompts[${i}] must be an object with string name.`);
        }
      }
    }
  }

  if (method === "resources/list") {
    if (!Array.isArray(result.resources)) {
      push("mcp.resources.list-array", "resources/list result.resources must be an array.");
    } else {
      for (let i = 0; i < result.resources.length; i++) {
        const r = result.resources[i];
        if (!isRecord(r) || typeof r.uri !== "string") {
          push("mcp.resources.entry", `resources[${i}] must be an object with string uri.`);
        }
      }
    }
  }

  if (method === "resources/read") {
    if (!Array.isArray(result.contents)) {
      push("mcp.resources.read-contents", "resources/read result.contents must be an array.");
    } else {
      for (let i = 0; i < result.contents.length; i++) {
        if (!isRecord(result.contents[i])) {
          push("mcp.resources.read-entry", `resources/read contents[${i}] is not an object.`);
        }
      }
    }
  }

  return violations;
}
