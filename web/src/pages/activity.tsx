import { Fragment } from "preact";
import { useState } from "preact/hooks";
import { api } from "../api";
import type { PageProps } from "../app";
import { useData } from "../data";
import { Link, query } from "../router";
import type { AuditEvent } from "../types";
import { Empty, ErrorLine, Loading, formValues, useAction, when } from "../ui";
import { Log, Overview } from "./overview";

type Filters = { actor?: string; server?: string; action?: string; decision?: string; from?: string; to?: string };

/** filterQuery turns the filters into query parameters ("" when none). */
export function filterQuery(f: Filters): string {
  const q = new URLSearchParams();
  for (const [k, v] of Object.entries(f)) {
    const t = (v ?? "").trim();
    if (!t) continue;
    // Dates from a date input mean the start of that day, UTC.
    q.set(k, k === "from" || k === "to" ? (t.length === 10 ? t + "T00:00:00Z" : t) : t);
  }
  const s = q.toString();
  return s ? "&" + s : "";
}

type Page = { events: AuditEvent[]; next?: string };

/** shortDecision drops the approval id: "approved:<id>" reads as "approved". */
export function shortDecision(d: string): string {
  return d.startsWith("approved:") ? "approved" : d;
}

/** shortApprover drops the id from "name (user <id>)" or "name (token <id>)". */
export function shortApprover(a: string | undefined): string {
  if (!a) return "";
  const i = a.indexOf(" (");
  return i > 0 ? a.slice(0, i) : a;
}

// Reading the activity writes nothing, so the page keeps itself current.
const POLL_MS = 10_000;

const VIEWS: [string, string][] = [
  ["overview", "Overview"],
  ["calls", "Tool calls"],
  ["admin", "Admin changes"],
  ["log", "Log"],
];

export function Activity({ loc, session }: PageProps) {
  const v = query(loc, "view");
  const view = VIEWS.some(([k]) => k === v) ? v : "overview";
  return (
    <>
      <h1>Activity</h1>
      <p class="tabs">
        {VIEWS.map(([k, label], i) => (
          <Fragment key={k}>
            {i > 0 && " · "}
            {k === view ? <strong>{label}</strong> : <Link href={k === "overview" ? "/admin/activity" : `/admin/activity?view=${k}`}>{label}</Link>}
          </Fragment>
        ))}
      </p>
      {view === "overview" && <Overview loc={loc} />}
      {view === "log" && <Log loc={loc} />}
      {(view === "calls" || view === "admin") && (
        <>
          <AuditList key={view} admin={view === "admin"} />
          <Integrity manage={session.can.manage_audit} />
        </>
      )}
    </>
  );
}

/** AuditList is the audit (ADR 0006): who did what, newest first, filtered and paged (F12). */
function AuditList({ admin }: { admin: boolean }) {
  const [filters, setFilters] = useState<Filters>({});
  const base = "/v1/audit/events?limit=100" + (admin ? "&kind=admin" : "") + filterQuery(filters);
  const res = useData<Page>(base, POLL_MS);
  // Pages after the first, valid for that first page only.
  const [extra, setExtra] = useState<{ from?: Page; more: AuditEvent[]; next?: string }>({ more: [] });
  const fresh = res.data !== undefined && extra.from === res.data;
  const next = fresh ? extra.next : res.data?.next;
  const events = res.data ? [...res.data.events, ...(fresh ? extra.more : [])] : undefined;
  const [busy, run] = useAction();
  const loadMore = () =>
    void run(async () => {
      if (!next || !res.data) return;
      const from = res.data;
      const page = await api<Page>("GET", base + "&before=" + encodeURIComponent(next));
      setExtra({ from, more: [...(fresh ? extra.more : []), ...page.events], next: page.next });
    });
  const exportCSV = () =>
    void run(async () => {
      const out = await api<{ csv: string; rows: number; truncated: boolean }>("GET", "/v1/audit/export?" + (admin ? "kind=admin" : "kind=calls") + filterQuery(filters));
      const url = URL.createObjectURL(new Blob([out.csv], { type: "text/csv" }));
      const a = document.createElement("a");
      a.href = url;
      a.download = "privo-" + (admin ? "admin-changes" : "tool-calls") + ".csv";
      a.click();
      URL.revokeObjectURL(url);
    }, "Exported. Recorded arguments are not part of the file.");
  const applyFilters = (e: Event) => {
    const f = formValues(e);
    setFilters({ actor: f.actor, server: f.server, action: f.action, decision: f.decision, from: f.from, to: f.to });
  };
  return (
    <>
      <details>
        <summary>Filter and export</summary>
        <form class="stack" onSubmit={applyFilters}>
          <div class="row">
            <label>
              Who <input name="actor" placeholder="a token or person" autocomplete="off" />
            </label>
            <label>
              Server <input name="server" autocomplete="off" />
            </label>
            <label>
              {admin ? "Change starts with" : "Action starts with"} <input name="action" placeholder={admin ? "policy." : "tools/call"} autocomplete="off" />
            </label>
            <label>
              Decision starts with <input name="decision" placeholder="denied" autocomplete="off" />
            </label>
            <label>
              From <input type="date" name="from" />
            </label>
            <label>
              Until <input type="date" name="to" />
            </label>
          </div>
          <div class="actions">
            <button type="submit">Filter</button>
            <button type="button" class="link" onClick={() => setFilters({})}>
              Clear
            </button>
            <button type="button" disabled={busy} onClick={exportCSV}>
              Export CSV
            </button>
          </div>
        </form>
      </details>
      <ErrorLine text={res.error} />
      {!events && res.loading && <Loading what="activity" />}
      {events && events.length === 0 && <Empty>Nothing yet.</Empty>}
      {events && events.length > 0 && (
        <div class="list-wrap">
          <table class="list">
            {admin ? (
              <>
                <thead>
                  <tr>
                    <th>Time</th>
                    <th>Who</th>
                    <th>What</th>
                    <th>Server</th>
                    <th>Outcome</th>
                  </tr>
                </thead>
                <tbody>
                  {events.map((e, i) => (
                    <tr key={i}>
                      <td>{when(e.time)}</td>
                      <td>
                        {e.actor}
                        {e.user && e.user !== e.actor && <small class="meta"> for {e.user}</small>}
                      </td>
                      <td>
                        <code>{e.action}</code>
                      </td>
                      <td>{e.server}</td>
                      <td>{(e.outcome ?? "").replaceAll("_", " ")}</td>
                    </tr>
                  ))}
                </tbody>
              </>
            ) : (
              <>
                <thead>
                  <tr>
                    <th>Time</th>
                    <th>Token or client</th>
                    <th>Server</th>
                    <th>Tool</th>
                    <th>Decision</th>
                    <th>Outcome</th>
                  </tr>
                </thead>
                <tbody>
                  {events.map((e, i) => (
                    <tr key={i}>
                      <td>{when(e.time)}</td>
                      <td>
                        {e.actor}
                        {e.user && e.user !== e.actor && <small class="meta"> as {e.user}</small>}
                      </td>
                      <td>{e.server}</td>
                      <td>
                        <code>{e.item}</code>
                      </td>
                      <td>
                        {shortDecision(e.decision ?? "")}
                        {e.approved_by && <small class="meta"> by {shortApprover(e.approved_by)}</small>}
                      </td>
                      <td>{(e.outcome ?? "").replaceAll("_", " ")}</td>
                    </tr>
                  ))}
                </tbody>
              </>
            )}
          </table>
        </div>
      )}
      {next && (
        <div class="actions">
          <button type="button" class="link" disabled={busy} onClick={loadMore}>
            Load more
          </button>
        </div>
      )}
    </>
  );
}

type Report = { ok: boolean; blocks: number; events: number; pruned_blocks: number; unsealed: number; last_sealed_at?: string; problem?: string };

// Integrity checks the tamper-evident chain (ADR 0006, 0028); those who
// manage the audit also set retention and erase a person's arguments.
function Integrity({ manage }: { manage: boolean }) {
  const [result, setResult] = useState<{ report: Report; retention_days: number } | null>(null);
  const [erasing, setErasing] = useState("");
  const [busy, run] = useAction();
  const check = () =>
    void run(async () => {
      setResult(await api<{ report: Report; retention_days: number }>("GET", "/v1/audit/integrity"));
    });
  const saveRetention = (e: Event) => {
    const f = formValues(e);
    void run(async () => {
      await api("PATCH", "/v1/settings/audit", { retention_days: Number(f.days || 0) });
    }, "Saved. Older events go within the hour; the chain stays checkable.");
  };
  const erase = () =>
    void run(async () => {
      const out = await api<{ erased: number }>("POST", "/v1/audit/erase", { person: erasing.trim() });
      setErasing("");
      return out;
    }, "Erased. The recorded arguments are gone; the chain still checks.");
  const r = result?.report;
  return (
    <section class="card">
      <h2>Integrity</h2>
      <p class="meta">
        Every few seconds Privo seals new events into blocks, each signed and linked to the one before, so a change to the record shows. Keep the
        checkpoints (GET /api/v1/audit/checkpoints) somewhere outside Privo to prove it later.
      </p>
      <div class="actions">
        <button type="button" disabled={busy} onClick={check}>
          Check the record
        </button>
      </div>
      {r &&
        (r.ok ? (
          <p class="flash ok">
            Intact: {r.blocks} blocks and {r.events} events checked{r.pruned_blocks > 0 && <>, {r.pruned_blocks} older blocks past retention</>}
            {r.unsealed > 0 && <>, {r.unsealed} waiting to be sealed</>}
            {r.last_sealed_at && <>; last sealed {when(r.last_sealed_at)}</>}.
          </p>
        ) : (
          <p class="flash warn">Not intact: {r.problem}</p>
        ))}
      {manage && (
        <>
          <h3>Retention</h3>
          <form class="row" onSubmit={saveRetention} key={result?.retention_days ?? "r"}>
            <label class="inline">
              Keep events for <input type="number" name="days" min={0} max={3650} defaultValue={String(result?.retention_days ?? 0)} aria-label="Days to keep" /> days
            </label>
            <button type="submit" disabled={busy}>
              Save
            </button>
            <small class="meta">0 keeps them for ever; otherwise at least 30 days.</small>
          </form>
          <h3>Erase a person's recorded arguments</h3>
          <div class="row">
            <input aria-label="Person to erase" placeholder="person, e.g. ada" value={erasing} onInput={(e) => setErasing((e.currentTarget as HTMLInputElement).value)} />
            <button type="button" class="deny" disabled={busy || !erasing.trim()} onClick={erase}>
              Erase
            </button>
          </div>
          <p class="meta">Clears the arguments recorded for their calls. Who called what, and when, stays: the record must stay checkable.</p>
        </>
      )}
    </section>
  );
}
