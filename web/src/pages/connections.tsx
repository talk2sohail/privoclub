import { useEffect } from "preact/hooks";
import { api, seg } from "../api";
import type { PageProps } from "../app";
import { invalidate, useData } from "../data";
import { navigate, query } from "../router";
import type { MyConnection } from "../types";
import { Empty, ErrorLine, Loading, flash, useAction, when } from "../ui";

const PATH = "/me/connections";

export function Connections({ loc }: PageProps) {
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
    </>
  );
}
