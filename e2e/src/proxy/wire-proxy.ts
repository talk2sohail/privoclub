import http from "node:http";
import type { AddressInfo } from "node:net";
import { scrubHeaders, scrubUnknown } from "./scrubber.js";
import { validateMcpResponse, type ProtocolViolation } from "./schema-validator.js";

export type WireRecord = {
  id: number;
  timestamp: string;
  clientTag: string;
  httpMethod: string;
  path: string;
  httpStatus: number;
  durationMs: number;
  protocolVersion: string;
  rpcMethod?: string;
  rpcId?: string | number | null;
  toolName?: string;
  isToolError?: boolean;
  toolResultPreview?: string;
  requestHeaders: Record<string, string>;
  responseHeaders: Record<string, string>;
  requestBody?: unknown;
  responseBodies: unknown[];
  violations: ProtocolViolation[];
  error?: string;
};

function parseSseJsonMessages(raw: string): unknown[] {
  const out: unknown[] = [];
  let dataLines: string[] = [];
  const flush = () => {
    if (dataLines.length === 0) return;
    const joined = dataLines.join("\n").trim();
    dataLines = [];
    if (!joined) return;
    try {
      out.push(JSON.parse(joined));
    } catch {
      out.push({ _rawSse: joined });
    }
  };
  for (const line of raw.split(/\r?\n/)) {
    if (line === "") {
      flush();
    } else if (line.startsWith("data:")) {
      dataLines.push(line.slice(5).trimStart());
    }
  }
  flush();
  return out;
}

function extractToolPreview(msg: unknown): { isError?: boolean; preview?: string } {
  if (!msg || typeof msg !== "object") return {};
  const result = (msg as Record<string, unknown>).result;
  if (!result || typeof result !== "object") return {};
  const r = result as Record<string, unknown>;
  const isError = typeof r.isError === "boolean" ? r.isError : undefined;
  let preview: string | undefined;
  if (Array.isArray(r.content)) {
    const texts = r.content
      .filter((c): c is { type: string; text: string } => Boolean(c && typeof c === "object" && "text" in c))
      .map((c) => String(c.text));
    if (texts.length > 0) {
      preview = texts.join("\n").slice(0, 400);
    }
  }
  return { isError, preview };
}

/**
 * Local transparent HTTP->HTTPS proxy placed between real AI clients and the
 * deployed Privo gateway. Gives us a deterministic wire-level oracle for every
 * MCP call and catches strict MCP schema regressions immediately.
 */
export class WireInspectorProxy {
  private server: http.Server | null = null;
  private port = 0;
  private seq = 0;
  private negotiatedProtocolVersion = "2025-11-25";
  private readonly records: WireRecord[] = [];
  private readonly extraSecrets = new Set<string>();
  private clientTag = "unknown";

  constructor(private readonly targetOrigin: string) {}

  addSecretToScrub(secret: string): void {
    if (secret && secret.length >= 6) {
      this.extraSecrets.add(secret);
    }
  }

  setClientTag(tag: string): void {
    this.clientTag = tag;
  }

  get baseUrl(): string {
    if (!this.port) throw new Error("WireInspectorProxy has not been started.");
    return `http://127.0.0.1:${this.port}`;
  }

  get mcpUrl(): string {
    return `${this.baseUrl}/mcp`;
  }

  allRecords(): WireRecord[] {
    return [...this.records];
  }

  recordsSince(mark: number): WireRecord[] {
    return this.records.slice(mark);
  }

  mark(): number {
    return this.records.length;
  }

  violationsSince(mark: number): ProtocolViolation[] {
    return this.records.slice(mark).flatMap((r) => r.violations);
  }

  async start(): Promise<void> {
    if (this.server) return;
    this.server = http.createServer((req, res) => {
      void this.handleRequest(req, res);
    });
    await new Promise<void>((resolve, reject) => {
      this.server!.once("error", reject);
      this.server!.listen(0, "127.0.0.1", () => resolve());
    });
    const addr = this.server.address() as AddressInfo;
    this.port = addr.port;
  }

  async stop(): Promise<void> {
    if (!this.server) return;
    const srv = this.server;
    this.server = null;
    this.port = 0;
    await new Promise<void>((resolve) => srv.close(() => resolve()));
  }

  private async handleRequest(req: http.IncomingMessage, res: http.ServerResponse): Promise<void> {
    const start = Date.now();
    const id = ++this.seq;
    const reqPath = req.url || "/";
    const httpMethod = req.method || "GET";
    const secrets = [...this.extraSecrets];

    const chunks: Buffer[] = [];
    for await (const chunk of req) {
      chunks.push(typeof chunk === "string" ? Buffer.from(chunk) : chunk);
    }
    const rawReqBody = Buffer.concat(chunks);

    let parsedReq: unknown;
    let rpcMethod: string | undefined;
    let rpcId: string | number | null | undefined;
    let toolName: string | undefined;
    let reqProtocolVersion =
      (typeof req.headers["mcp-protocol-version"] === "string"
        ? req.headers["mcp-protocol-version"]
        : "") || this.negotiatedProtocolVersion;

    if (rawReqBody.length > 0) {
      try {
        parsedReq = JSON.parse(rawReqBody.toString("utf8"));
        if (parsedReq && typeof parsedReq === "object" && !Array.isArray(parsedReq)) {
          const obj = parsedReq as Record<string, unknown>;
          if (typeof obj.method === "string") rpcMethod = obj.method;
          if ("id" in obj) rpcId = obj.id as string | number | null;
          if (rpcMethod === "initialize" && obj.params && typeof obj.params === "object") {
            const pv = (obj.params as Record<string, unknown>).protocolVersion;
            if (typeof pv === "string" && pv) reqProtocolVersion = pv;
          }
          if (rpcMethod === "tools/call" && obj.params && typeof obj.params === "object") {
            const name = (obj.params as Record<string, unknown>).name;
            if (typeof name === "string") toolName = name;
          }
        }
      } catch {
        parsedReq = rawReqBody.toString("utf8").slice(0, 500);
      }
    }

    const upstreamUrl = `${this.targetOrigin.replace(/\/+$/, "")}${reqPath}`;
    const forwardHeaders = new Headers();
    for (const [k, v] of Object.entries(req.headers)) {
      if (v === undefined) continue;
      const lower = k.toLowerCase();
      if (lower === "host" || lower === "connection" || lower === "content-length") continue;
      if (Array.isArray(v)) {
        for (const item of v) forwardHeaders.append(k, item);
      } else {
        forwardHeaders.set(k, v);
      }
    }

    const record: WireRecord = {
      id,
      timestamp: new Date().toISOString(),
      clientTag: this.clientTag,
      httpMethod,
      path: reqPath,
      httpStatus: 0,
      durationMs: 0,
      protocolVersion: reqProtocolVersion,
      rpcMethod,
      rpcId,
      toolName,
      requestHeaders: scrubHeaders(req.headers, secrets),
      responseHeaders: {},
      requestBody: scrubUnknown(parsedReq, secrets),
      responseBodies: [],
      violations: [],
    };

    try {
      const upstreamRes = await fetch(upstreamUrl, {
        method: httpMethod,
        headers: forwardHeaders,
        body: httpMethod === "GET" || httpMethod === "HEAD" ? undefined : rawReqBody,
        redirect: "manual",
      });

      const resBytes = Buffer.from(await upstreamRes.arrayBuffer());
      record.httpStatus = upstreamRes.status;
      record.durationMs = Date.now() - start;

      const respHeadersObj: Record<string, string> = {};
      upstreamRes.headers.forEach((value, key) => {
        respHeadersObj[key] = value;
      });
      record.responseHeaders = scrubHeaders(respHeadersObj, secrets);

      // Forward response faithfully back to the AI client
      for (const [k, v] of Object.entries(respHeadersObj)) {
        const lower = k.toLowerCase();
        if (lower === "transfer-encoding" || lower === "content-encoding" || lower === "content-length") {
          continue;
        }
        res.setHeader(k, v);
      }
      res.setHeader("content-length", String(resBytes.length));
      res.statusCode = upstreamRes.status;
      res.end(resBytes);

      // Parse & validate MCP payloads on /mcp POST responses
      const contentType = upstreamRes.headers.get("content-type") || "";
      if (reqPath.startsWith("/mcp") && upstreamRes.status === 200 && resBytes.length > 0) {
        const text = resBytes.toString("utf8");
        let messages: unknown[] = [];
        if (contentType.includes("text/event-stream")) {
          messages = parseSseJsonMessages(text);
        } else if (contentType.includes("application/json")) {
          try {
            const parsed = JSON.parse(text);
            messages = Array.isArray(parsed) ? parsed : [parsed];
          } catch {
            messages = [{ _raw: text.slice(0, 500) }];
          }
        }

        for (const msg of messages) {
          if (
            rpcMethod === "initialize" &&
            msg &&
            typeof msg === "object" &&
            "result" in msg &&
            (msg as Record<string, unknown>).result &&
            typeof (msg as Record<string, unknown>).result === "object"
          ) {
            const pv = ((msg as Record<string, unknown>).result as Record<string, unknown>).protocolVersion;
            if (typeof pv === "string" && pv) {
              this.negotiatedProtocolVersion = pv;
              record.protocolVersion = pv;
            }
          }

          if (rpcMethod && rpcId !== undefined) {
            record.violations.push(...validateMcpResponse(rpcMethod, record.protocolVersion, msg));
          }

          if (rpcMethod === "tools/call") {
            const { isError, preview } = extractToolPreview(msg);
            if (isError !== undefined) record.isToolError = isError;
            if (preview !== undefined) {
              record.toolResultPreview = String(scrubUnknown(preview, secrets));
            }
          }
          record.responseBodies.push(scrubUnknown(msg, secrets));
        }
      }
    } catch (err) {
      record.durationMs = Date.now() - start;
      record.httpStatus = 502;
      record.error = err instanceof Error ? err.message : String(err);
      res.statusCode = 502;
      res.setHeader("content-type", "application/json");
      res.end(JSON.stringify({ error: "wire-inspector proxy error", detail: record.error }));
    }

    this.records.push(record);
  }
}
