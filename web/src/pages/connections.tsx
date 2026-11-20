import { useEffect, useState } from "preact/hooks";
import { api, seg } from "../api";
import type { PageProps } from "../app";
import { invalidate, useData } from "../data";
import { navigate, query } from "../router";
import type { AnyConnection, MyConnection } from "../types";
import { Empty, ErrorLine, Loading, flash, formValues, useAction, when } from "../ui";

const PATH = "/me/connections";

export function Connections({ loc, session }: PageProps) {
  const res = useData<{ connections: MyConnection[]; can_connect: boolean }>(PATH);
  const [busy, run] = useAction();
  const result = query(loc, "result");

  // The connect flow (a server-rendered page) comes back here with a result.
  useEffect(() => {
    if (result === "connected") {
      navigate("/admin/connections", { replace: true });
      flash("ok", "Connected. Your AI tools can now use this server as you.");
    }
  }, [result]);

  const disconnect = (alias: string) =>
    void run(async () => {
      await api("DELETE", PATH + "/" + seg(alias));
      invalidate(PATH);
    }, "Disconnected. Privo no longer holds a token or key of yours for it.");

  const list = res.data?.connections;
  return (
    <>
      <h1>Your connections</h1>
      <p class="meta">Some servers run each call with the caller's own account. Connect yours once here; your AI tools then use it through Privo.</p>
      <ErrorLine text={res.error} />
      {!list && res.loading && <Loading what="connections" />}
      {list && list.length === 0 && <Empty>No server here needs your own account.</Empty>}
      {(list ?? []).map((c) => (
        <article class="approval" key={c.alias}>
          <h2>
            <code>{c.alias}</code> <small>{c.kind === "key" ? `takes your own API key (${c.issuer})` : `signs in at ${c.issuer}`}</small>
          </h2>
          {c.connected ? (
            <>
              <p class="meta">
                Connected {when(c.connected_at)}. Last used {when(c.last_used_at) || "never"}.
                {c.scopes && (
                  <>
                    {" "}
                    Access: <code>{c.scopes}</code>.
                  </>
                )}
              </p>
              <div class="actions">
                <button type="button" class="deny" disabled={busy} onClick={() => disconnect(c.alias)}>
                  Disconnect
                </button>
              </div>
            </>
          ) : res.data?.can_connect ? (
            <p>
              <a href={"/connect/" + seg(c.alias)}>{c.kind === "key" ? "Enter your API key" : "Connect your account"}</a>
            </p>
          ) : (
            <p class="meta">Your role does not use tools.</p>
          )}
        </article>
      ))}
      {session.can.servers && <Everyone />}
    </>
  );
}

type Page = { connections: AnyConnection[]; next: string };

// Everyone lists every person's connections across servers for admins (F07
// A): who connected what, when, and what went unused; End revokes at the
// provider and deletes.
function Everyone() {
  const [filter, setFilter] = useState({ person: "", server: "" });
  const qs = new URLSearchParams();
  if (filter.person) qs.set("person", filter.person);
  if (filter.server) qs.set("server", filter.server);
  const base = "/v1/connections" + (qs.toString() ? "?" + qs.toString() : "");
  const res = useData<Page>(base);
  const [more, setMore] = useState<{ from?: Page; rows: AnyConnection[]; next: string }>({ rows: [], next: "" });
  const fresh = res.data !== undefined && more.from === res.data;
  const rows = res.data ? [...res.data.connections, ...(fresh ? more.rows : [])] : undefined;
  const next = fresh ? more.next : (res.data?.next ?? "");
  const [ending, setEnding] = useState("");
  const [busy, run] = useAction();
  const loadMore = () =>
    void run(async () => {
      if (!res.data || !next) return;
      const page = await api<Page>("GET", base + (base.includes("?") ? "&" : "?") + "after=" + encodeURIComponent(next));
      setMore({ from: res.data, rows: [...(fresh ? more.rows : []), ...page.connections], next: page.next });
    });
  const end = (c: AnyConnection) =>
    void run(async () => {
      await api("DELETE", "/v1/servers/" + seg(c.server) + "/connections/" + seg(c.person));
      setEnding("");
      setMore({ rows: [], next: "" });
      invalidate("/v1/connections");
    }, "Ended. " + c.person + "'s access to " + c.server + " was revoked at the provider where it allows it, and removed here.");
  return (
    <section class="card">
      <h2>Everyone's connections</h2>
      <form
        class="row"
        onSubmit={(e) => {
          const f = formValues(e);
          setFilter({ person: (f.person ?? "").trim(), server: (f.server ?? "").trim() });
        }}
      >
        <input name="person" placeholder="person" aria-label="Filter by person" autocomplete="off" />
        <input name="server" placeholder="server" aria-label="Filter by server" autocomplete="off" />
        <button type="submit">Filter</button>
      </form>
      <ErrorLine text={res.error} />
      {!rows && res.loading && <Loading what="connections" />}
      {rows && rows.length === 0 && <Empty>No connections.</Empty>}
      {rows && rows.length > 0 && (
        <div class="list-wrap">
          <table class="list">
            <thead>
              <tr>
                <th>Server</th>
                <th>Person</th>
                <th>Kind</th>
                <th>Connected</th>
                <th>Last used</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {rows.map((c) => {
                const key = c.server + "/" + c.person;
                return (
                  <tr key={key} class={c.unused ? "muted" : undefined}>
                    <td>{c.server}</td>
                    <td>{c.person}</td>
                    <td>{c.kind === "key" ? "API key" : "account"}</td>
                    <td>{when(c.connected_at)}</td>
                    <td>
                      {when(c.last_used_at) || "never"}
                      {c.unused && <small class="meta"> (unused for 90 days)</small>}
                    </td>
                    <td>
                      {ending === key ? (
                        <span class="row">
                          <button type="button" class="deny" disabled={busy} onClick={() => end(c)}>
                            End {c.person}'s {c.server}
                          </button>
                          <button type="button" class="link" onClick={() => setEnding("")}>
                            Cancel
                          </button>
                        </span>
                      ) : (
                        <button type="button" class="link" onClick={() => setEnding(key)}>
                          End…
                        </button>
                      )}
                    </td>
                  </tr>
                );
              })}
            </tbody>
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
    </section>
  );
}
