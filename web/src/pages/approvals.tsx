import { useEffect, useState } from "preact/hooks";
import { api, seg } from "../api";
import type { PageProps } from "../app";
import { invalidate, useData } from "../data";
import type { Approval } from "../types";
import { Empty, ErrorLine, Loading, useAction, when } from "../ui";

const PATH = "/v1/approvals?status=pending";
const HISTORY = "/v1/approvals?status=history";

type Page = { approvals: Approval[]; next?: string };

/** pretty shows arguments as indented JSON text. */
export function pretty(v: unknown): string {
  if (v === undefined) return "";
  try {
    return JSON.stringify(v, null, 2);
  } catch {
    return String(v);
  }
}

// How long approving can also allow the tool for the requester (F10, ADR 0005).
const grantChoices: [string, string][] = [
  ["", "only this call"],
  ["15m", "this tool for 15 minutes"],
  ["1h", "this tool for 1 hour"],
  ["8h", "this tool for 8 hours"],
];

/** usePages is the first page of path and any further ones the person loaded. */
function usePages(path: string) {
  const first = useData<Page>(path);
  // Pages loaded after the first one, valid only for that first page: when
  // it reloads (a decision, a refresh), they are dropped.
  const [extra, setExtra] = useState<{
    from?: Page;
    more: Approval[];
    next?: string;
  }>({ more: [] });
  const [loadingMore, setLoadingMore] = useState(false);
  const [error, setError] = useState("");
  const fresh = first.data !== undefined && extra.from === first.data;
  const more = fresh ? extra.more : [];
  const next = fresh ? extra.next : first.data?.next;
  const loadMore = async () => {
    if (!next || !first.data) return;
    const from = first.data;
    setLoadingMore(true);
    try {
      const page = await api<Page>(
        "GET",
        path + "&before=" + encodeURIComponent(next),
      );
      setExtra({ from, more: [...more, ...page.approvals], next: page.next });
      setError("");
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setLoadingMore(false);
    }
  };
  const list = first.data ? [...first.data.approvals, ...more] : undefined;
  return { first, list, next, loadMore, loadingMore, error };
}

function MoreButton({ pages }: { pages: ReturnType<typeof usePages> }) {
  if (!pages.next) return null;
  return (
    <div class="actions">
      <button
        type="button"
        class="link"
        disabled={pages.loadingMore}
        onClick={() => void pages.loadMore()}
      >
        {pages.loadingMore ? "Loading…" : "Load more"}
      </button>
    </div>
  );
}

// Every read of the approvals is audited (an approver sees the arguments), so
// the page does not poll: it reads again when the window comes back to the
// front, and on Refresh.
export function Approvals(_: PageProps) {
  const [tab, setTab] = useState<"waiting" | "history">("waiting");
  return (
    <>
      <h1>Approvals</h1>
      <div class="row tabs" role="tablist" aria-label="Approvals">
        <button
          type="button"
          role="tab"
          aria-selected={tab === "waiting"}
          class={tab === "waiting" ? "primary small" : "small"}
          onClick={() => setTab("waiting")}
        >
          Waiting
        </button>
        <button
          type="button"
          role="tab"
          aria-selected={tab === "history"}
          class={tab === "history" ? "primary small" : "small"}
          onClick={() => setTab("history")}
        >
          History
        </button>
      </div>
      {tab === "waiting" ? <Waiting /> : <History />}
    </>
  );
}

function Waiting() {
  const pages = usePages(PATH);
  const { reload } = pages.first;
  const [busy, run] = useAction();
  const [grant, setGrant] = useState<Record<string, string>>({});

  useEffect(() => {
    const onVisible = () => {
      if (document.visibilityState === "visible") reload();
    };
    document.addEventListener("visibilitychange", onVisible);
    return () => document.removeEventListener("visibilitychange", onVisible);
  }, [reload]);

  const decide = (a: Approval, approve: boolean) => {
    const g = approve ? (grant[a.id] ?? "") : "";
    const label = grantChoices.find(([v]) => v === g)?.[1] ?? "";
    return void run(
      async () => {
        await api(
          "POST",
          "/v1/approvals/" + seg(a.id) + (approve ? "/approve" : "/deny"),
          g ? { grant: g } : undefined,
        );
        invalidate("/v1/approvals");
        if (g) invalidate("/v1/policies");
      },
      approve
        ? g
          ? "Approved, and " +
            a.requester_person +
            " may use " +
            label +
            " without asking."
          : "Approved. The call can go ahead."
        : "Denied.",
    ).then((ok) => {
      if (!ok) invalidate("/v1/approvals");
    });
  };

  const list = pages.list;
  return (
    <>
      <p>
        <button
          type="button"
          class="link small"
          onClick={reload}
          disabled={pages.first.loading}
        >
          {pages.first.loading ? "refreshing…" : "refresh"}
        </button>
      </p>
      <ErrorLine text={pages.first.error || pages.error} />
      {!list && pages.first.loading && <Loading what="approvals" />}
      {list && list.length === 0 && (
        <Empty>Nothing is waiting for a decision.</Empty>
      )}
      {(list ?? []).map((a) => (
        <article class="approval" key={a.id}>
          <h2>
            <code>{a.tool}</code> <small>on {a.server}</small>
          </h2>
          <p class="meta">
            Requested by <strong>{a.requester_person}</strong>
            {a.own && " (you)"} using the token “{a.requester}”. Expires{" "}
            {when(a.expires_at)}.
          </p>
          {a.reason && <p>{a.reason}</p>}
          {a.decide_blocked && <p class="warn-text">{a.decide_blocked}</p>}
          {a.arguments_error ? (
            <p class="warn-text">
              The arguments cannot be shown, so this call can only be denied.
            </p>
          ) : (
            <pre class="args">{pretty(a.arguments)}</pre>
          )}
          {!a.decide_blocked && (
            <div class="actions">
              {!a.arguments_error && (
                <>
                  {!a.own && (
                    <label class="inline">
                      Allow
                      <select
                        aria-label={"Allow for " + a.requester_person}
                        value={grant[a.id] ?? ""}
                        onChange={(e) =>
                          setGrant({
                            ...grant,
                            [a.id]: (e.currentTarget as HTMLSelectElement)
                              .value,
                          })
                        }
                      >
                        {grantChoices.map(([v, l]) => (
                          <option key={v} value={v}>
                            {l}
                          </option>
                        ))}
                      </select>
                    </label>
                  )}
                  <button
                    type="button"
                    class="approve"
                    disabled={busy}
                    onClick={() => decide(a, true)}
                  >
                    Approve
                  </button>
                </>
              )}
              <button
                type="button"
                class="deny"
                disabled={busy}
                onClick={() => decide(a, false)}
              >
                Deny
              </button>
            </div>
          )}
        </article>
      ))}
      <MoreButton pages={pages} />
    </>
  );
}

const statusLabel: Record<string, string> = {
  approved: "approved, not used yet",
  consumed: "approved and used",
  denied: "denied",
  withdrawn: "withdrawn",
  expired: "expired",
};

function History() {
  const pages = usePages(HISTORY);
  const list = pages.list;
  return (
    <>
      <ErrorLine text={pages.first.error || pages.error} />
      {!list && pages.first.loading && <Loading what="history" />}
      {list && list.length === 0 && <Empty>No decided requests yet.</Empty>}
      {list && list.length > 0 && (
        <div class="list-wrap">
          <table class="list">
            <thead>
              <tr>
                <th>Asked</th>
                <th>Tool</th>
                <th>Requested by</th>
                <th>Outcome</th>
              </tr>
            </thead>
            <tbody>
              {list.map((a) => (
                <tr key={a.id}>
                  <td>{when(a.created_at)}</td>
                  <td>
                    <code>{a.tool}</code>{" "}
                    <small class="meta">on {a.server}</small>
                  </td>
                  <td>{a.requester_person}</td>
                  <td>
                    {statusLabel[a.status] ?? a.status}
                    {a.decided_by && (
                      <small class="meta">
                        {" "}
                        by {a.decided_by.split(" (")[0]}
                      </small>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      <MoreButton pages={pages} />
    </>
  );
}
