import { useEffect } from "preact/hooks";
import { api, seg } from "../api";
import type { PageProps } from "../app";
import { invalidate, useData } from "../data";
import type { Approval } from "../types";
import { Empty, ErrorLine, Loading, useAction, when } from "../ui";

const PATH = "/v1/approvals?status=pending";

/** pretty shows arguments as indented JSON text. */
export function pretty(v: unknown): string {
  if (v === undefined) return "";
  try {
    return JSON.stringify(v, null, 2);
  } catch {
    return String(v);
  }
}

// Every read of the approvals is audited (an approver sees the arguments), so
// the page does not poll: it reads again when the window comes back to the
// front, and on Refresh.
export function Approvals(_: PageProps) {
  const res = useData<{ approvals: Approval[] }>(PATH);
  const [busy, run] = useAction();
  const { reload } = res;

  useEffect(() => {
    const onVisible = () => {
      if (document.visibilityState === "visible") reload();
    };
    document.addEventListener("visibilitychange", onVisible);
    return () => document.removeEventListener("visibilitychange", onVisible);
  }, [reload]);

  const decide = (a: Approval, approve: boolean) =>
    void run(
      async () => {
        await api("POST", "/v1/approvals/" + seg(a.id) + (approve ? "/approve" : "/deny"));
        invalidate("/v1/approvals");
      },
      approve ? "Approved. The call can go ahead." : "Denied.",
    ).then((ok) => {
      if (!ok) invalidate("/v1/approvals");
    });

  const list = res.data?.approvals;
  return (
    <>
      <h1>
        Approvals{" "}
        <button type="button" class="link small" onClick={reload} disabled={res.loading}>
          {res.loading ? "refreshing…" : "refresh"}
        </button>
      </h1>
      <ErrorLine text={res.error} />
      {!list && res.loading && <Loading what="approvals" />}
      {list && list.length === 0 && <Empty>Nothing is waiting for a decision.</Empty>}
      {(list ?? []).map((a) => (
        <article class="approval" key={a.id}>
          <h2>
            <code>{a.tool}</code> <small>on {a.server}</small>
          </h2>
          <p class="meta">
            Requested by <strong>{a.requester_person}</strong>
            {a.own && " (you)"} using the token “{a.requester}”. Expires {when(a.expires_at)}.
          </p>
          {a.reason && <p>{a.reason}</p>}
          {a.decide_blocked && <p class="warn-text">{a.decide_blocked}</p>}
          {a.arguments_error ? (
            <p class="warn-text">The arguments cannot be shown, so this call can only be denied.</p>
          ) : (
            <pre class="args">{pretty(a.arguments)}</pre>
          )}
          {!a.decide_blocked && (
            <div class="actions">
              {!a.arguments_error && (
                <button type="button" class="approve" disabled={busy} onClick={() => decide(a, true)}>
                  Approve this call
                </button>
              )}
              <button type="button" class="deny" disabled={busy} onClick={() => decide(a, false)}>
                Deny
              </button>
            </div>
          )}
        </article>
      ))}
    </>
  );
}
