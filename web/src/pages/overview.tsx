// The Activity overview (F28, ADR 0024): how calls went over a window.

import { Fragment } from "preact";
import { useData } from "../data";
import { Link, query } from "../router";
import type { LogEntry, StatBucket, StatGroup, StatSeries, StatSummary, TelemetryStorage } from "../types";
import { Empty, ErrorLine, Loading, when } from "../ui";

export const WINDOWS = ["1h", "24h", "7d", "30d"] as const;
export const BREAKDOWNS: Record<string, string> = { server: "Server", tool: "Tool", member: "Member", client: "Client" };

/** ms reads a duration: 0.4 ms, 12 ms, 1.2 s. */
export function ms(v: number): string {
  if (!Number.isFinite(v) || v <= 0) return "–";
  if (v < 10) return `${v.toFixed(1)} ms`;
  if (v < 1000) return `${Math.round(v)} ms`;
  return `${(v / 1000).toFixed(v < 10_000 ? 2 : 1)} s`;
}

/** bytes reads a size: 512 B, 3.4 KB, 1.2 MB. */
export function bytes(v: number): string {
  if (!Number.isFinite(v) || v <= 0) return "–";
  if (v < 1024) return `${Math.round(v)} B`;
  if (v < 1024 * 1024) return `${(v / 1024).toFixed(1)} KB`;
  return `${(v / 1024 / 1024).toFixed(1)} MB`;
}

/** pct reads a rate as a percentage. */
export function pct(v: number): string {
  if (!v) return "0%";
  return v < 0.001 ? "<0.1%" : `${(v * 100).toFixed(v < 0.1 ? 1 : 0)}%`;
}

function href(view: string, params: Record<string, string>): string {
  const q = new URLSearchParams({ view, ...params });
  return `/admin/activity?${q.toString()}`;
}

/** Chart draws calls per bucket as bars, the failed part darker. */
export function Chart({ buckets }: { buckets: StatBucket[] }) {
  const max = Math.max(1, ...buckets.map((b) => b.calls));
  const w = 100 / Math.max(1, buckets.length);
  const total = buckets.reduce((n, b) => n + b.calls, 0);
  const failed = buckets.reduce((n, b) => n + b.errors, 0);
  return (
    <svg class="chart" viewBox="0 0 100 40" preserveAspectRatio="none" role="img" aria-label={`${total} calls, ${failed} failed, over ${buckets.length} intervals`}>
      {buckets.map((b, i) => {
        const h = (b.calls / max) * 38;
        const e = (b.errors / max) * 38;
        return (
          <g key={b.start}>
            <title>{`${when(b.start)}: ${b.calls} calls, ${b.errors} failed, ${b.refused} refused, p95 ${ms(b.p95_ms)}`}</title>
            <rect class="calls" x={i * w + w * 0.1} y={40 - h} width={w * 0.8} height={h} />
            {e > 0 && <rect class="errors" x={i * w + w * 0.1} y={40 - e} width={w * 0.8} height={e} />}
          </g>
        );
      })}
    </svg>
  );
}

function Totals({ t }: { t: StatGroup }) {
  const ran = t.ok + t.tool_errors + t.errors;
  return (
    <dl class="cards">
      <div class="stat">
        <dt>Calls</dt>
        <dd>{t.calls.toLocaleString()}</dd>
      </div>
      <div class="stat">
        <dt>Failed</dt>
        <dd>
          {t.errors.toLocaleString()} <small class="meta">{ran > 0 ? pct(t.error_rate) : ""}</small>
        </dd>
      </div>
      <div class="stat">
        <dt>Tool errors</dt>
        <dd>{t.tool_errors.toLocaleString()}</dd>
      </div>
      <div class="stat">
        <dt>Refused</dt>
        <dd>{t.refused.toLocaleString()}</dd>
      </div>
      <div class="stat">
        <dt>Median</dt>
        <dd>{ms(t.total_ms.p50)}</dd>
      </div>
      <div class="stat">
        <dt>p95</dt>
        <dd>{ms(t.total_ms.p95)}</dd>
      </div>
    </dl>
  );
}

function StorageLine({ s }: { s: TelemetryStorage }) {
  if (!s.recording) {
    return (
      <p class="meta">
        Call statistics are off on this Privo (<code>PRIVO_TELEMETRY_RETENTION_DAYS=0</code>). The audit still records every call.
      </p>
    );
  }
  return (
    <p class="meta">
      Kept for {s.retention_days} days · {s.calls.toLocaleString()} calls{s.oldest_call ? ` since ${when(s.oldest_call)}` : ""}
      {s.dropped > 0 && ` · ${s.dropped.toLocaleString()} not recorded since the last start (busy or disk errors)`} · Nothing here leaves this server, and no
      arguments or results are kept.
    </p>
  );
}

export function Overview({ loc }: { loc: string }) {
  const win = (WINDOWS as readonly string[]).includes(query(loc, "window")) ? query(loc, "window") : "24h";
  const by = query(loc, "by") in BREAKDOWNS ? query(loc, "by") : "server";
  const sum = useData<StatSummary>(`/v1/telemetry/summary?window=${win}&by=${by}`, 30_000);
  const series = useData<StatSeries>(`/v1/telemetry/timeseries?window=${win}`, 30_000);
  const storage = useData<TelemetryStorage>("/v1/telemetry/storage", 60_000);
  const s = sum.data;
  const label = (g: StatGroup) => (by === "member" ? (s?.labels?.[g.key] ?? (g.key || "a token of nobody")) : g.key || "–");
  return (
    <>
      <p class="tabs">
        {WINDOWS.map((w, i) => (
          <Fragment key={w}>
            {i > 0 && " · "}
            {w === win ? <strong>Last {w}</strong> : <Link href={href("overview", { window: w, by })}>Last {w}</Link>}
          </Fragment>
        ))}
      </p>
      <ErrorLine text={sum.error || series.error} />
      {!s && sum.loading && <Loading what="statistics" />}
      {s && (
        <>
          <Totals t={s.total} />
          {series.data && series.data.buckets.length > 0 && <Chart buckets={series.data.buckets} />}
          {s.truncated && <p class="meta">More calls fell in this window than one view reads; the figures cover the first 100,000.</p>}
          <h2>
            By{" "}
            {Object.entries(BREAKDOWNS).map(([k, v], i) => (
              <Fragment key={k}>
                {i > 0 && " · "}
                {k === by ? <strong>{v.toLowerCase()}</strong> : <Link href={href("overview", { window: win, by: k })}>{v.toLowerCase()}</Link>}
              </Fragment>
            ))}
          </h2>
          {s.groups.length === 0 ? (
            <Empty>No calls in this window.</Empty>
          ) : (
            <div class="list-wrap">
              <table class="list stats">
                <thead>
                  <tr>
                    <th>{BREAKDOWNS[by]}</th>
                    <th>Calls</th>
                    <th>Failed</th>
                    <th>Tool errors</th>
                    <th>Refused</th>
                    <th>Median</th>
                    <th>p95</th>
                    <th>p99</th>
                    <th title="Time spent getting the server's session, including a sign-in refresh">Connect p95</th>
                    <th title="Time the server itself took">Server p95</th>
                    <th title="Average size of what the server sent back, which the model reads">Result</th>
                  </tr>
                </thead>
                <tbody>
                  {s.groups.map((g) => (
                    <tr key={g.key}>
                      <td>{by === "tool" ? <code>{label(g)}</code> : label(g)}</td>
                      <td data-label="Calls">{g.calls.toLocaleString()}</td>
                      <td data-label="Failed">
                        <span>
                          {g.errors}
                          {g.errors > 0 && <small class="meta"> {pct(g.error_rate)}</small>}
                        </span>
                      </td>
                      <td data-label="Tool errors">{g.tool_errors}</td>
                      <td data-label="Refused">{g.refused}</td>
                      <td data-label="Median">{ms(g.total_ms.p50)}</td>
                      <td data-label="p95">{ms(g.total_ms.p95)}</td>
                      <td data-label="p99">{ms(g.total_ms.p99)}</td>
                      <td data-label="Connect p95">{ms(g.connect_ms.p95)}</td>
                      <td data-label="Server p95">{ms(g.upstream_ms.p95)}</td>
                      <td data-label="Result">{bytes(g.result_bytes.avg)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </>
      )}
      {storage.data && <StorageLine s={storage.data} />}
    </>
  );
}

const CATEGORIES: Record<string, string> = { upstream: "Connections", grant: "Sign-ins and keys", gateway: "Gateway", audit: "Audit", lifecycle: "Start and stop" };

export function Log({ loc }: { loc: string }) {
  const cat = query(loc, "category") in CATEGORIES ? query(loc, "category") : "";
  const res = useData<{ logs: LogEntry[] }>("/v1/telemetry/logs?limit=200" + (cat ? `&category=${cat}` : ""), 15_000);
  const logs = res.data?.logs;
  return (
    <>
      <p class="tabs">
        {cat === "" ? <strong>All</strong> : <Link href={href("log", {})}>All</Link>}
        {Object.entries(CATEGORIES).map(([k, v]) => (
          <Fragment key={k}>
            {" · "}
            {k === cat ? <strong>{v}</strong> : <Link href={href("log", { category: k })}>{v}</Link>}
          </Fragment>
        ))}
      </p>
      <ErrorLine text={res.error} />
      {!logs && res.loading && <Loading what="the log" />}
      {logs && logs.length === 0 && <Empty>Nothing logged.</Empty>}
      {logs && logs.length > 0 && (
        <div class="list-wrap">
          <table class="list">
            <thead>
              <tr>
                <th>Time</th>
                <th>Level</th>
                <th>What</th>
                <th>Server</th>
                <th>Member</th>
                <th>Message</th>
              </tr>
            </thead>
            <tbody>
              {logs.map((l) => (
                <tr key={l.id} class={`level-${l.level}`}>
                  <td>{when(l.time)}</td>
                  <td>{l.level}</td>
                  <td>{CATEGORIES[l.category] ?? l.category}</td>
                  <td>{l.server ?? ""}</td>
                  <td>{l.member ?? ""}</td>
                  <td>
                    {l.message}
                    {l.repeats > 1 && <small class="meta"> ×{l.repeats}</small>}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </>
  );
}
