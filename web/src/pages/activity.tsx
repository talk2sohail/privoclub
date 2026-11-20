import type { PageProps } from "../app";
import { useData } from "../data";
import { Link, query } from "../router";
import type { AuditEvent } from "../types";
import { Empty, ErrorLine, Loading, when } from "../ui";

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

export function Activity({ loc }: PageProps) {
  const admin = query(loc, "view") === "admin";
  const res = useData<{ events: AuditEvent[] }>("/v1/audit/events?limit=100" + (admin ? "&kind=admin" : ""), POLL_MS);
  const events = res.data?.events;
  return (
    <>
      <h1>Activity</h1>
      <p class="tabs">
        {admin ? <Link href="/admin/activity">Tool calls</Link> : <strong>Tool calls</strong>} ·{" "}
        {admin ? <strong>Admin changes</strong> : <Link href="/admin/activity?view=admin">Admin changes</Link>}
      </p>
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
                        {e.account && <small class="meta"> as {shortApprover(e.account)}</small>}
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
                    <th>Token</th>
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
                        {e.account && <small class="meta"> as {shortApprover(e.account)}</small>}
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
    </>
  );
}
