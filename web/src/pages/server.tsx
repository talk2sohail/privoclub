import { useEffect, useState } from "preact/hooks";
import { api, seg } from "../api";
import type { PageProps } from "../app";
import { invalidate, useData } from "../data";
import { Link, navigate } from "../router";
import type { Pin, ServerConnection, ServerView } from "../types";
import { Empty, ErrorLine, Loading, Status, firstLine, when, useAction } from "../ui";
import { authLabel } from "./servers";

// A new server takes a moment to connect and list its tools: the page asks
// again every few seconds for a while after it opens, and while connecting.
const WATCH_MS = 60_000;
const POLL_MS = 3_000;

export function Server({ alias }: PageProps & { alias: string }) {
  const [opened] = useState(() => Date.now());
  const [watching, setWatching] = useState(true);
  useEffect(() => {
    const t = setTimeout(() => setWatching(false), WATCH_MS - (Date.now() - opened));
    return () => clearTimeout(t);
  }, [opened]);

  const [connecting, setConnecting] = useState(false);
  const poll = watching || connecting ? POLL_MS : 0;
  const list = useData<{ servers: ServerView[] }>("/v1/servers", poll);
  const pins = useData<{ pins: Pin[] }>("/v1/servers/" + seg(alias) + "/pins", poll);
  const current = list.data?.servers.find((s) => s.alias === alias);
  const status = current?.status;
  useEffect(() => setConnecting(status === "connecting"), [status]);

  if (!current) {
    if (list.loading) return <Loading what={alias} />;
    return (
      <>
        <p>
          <Link href="/admin/servers">← Servers</Link>
        </p>
        <h1>No such server</h1>
        <ErrorLine text={list.error} />
        <p>There is no server named {alias}.</p>
      </>
    );
  }

  const all = pins.data?.pins ?? [];
  const pending = all.filter((p) => p.status === "pending");
  const accepted = all.filter((p) => p.status === "accepted");
  const items = current.items ?? {};

  return (
    <>
      <p>
        <Link href="/admin/servers">← Servers</Link>
      </p>
      <h1>
        {current.alias} <Status value={current.status} />
      </h1>
      <p class="meta">
        {current.target} · {current.transport} · {authLabel(current.auth)} sign-in · {items.tool ?? 0} tools, {items.prompt ?? 0} prompts,{" "}
        {items.resource ?? 0} resources
      </p>
      {current.error && <p class="flash warn">{current.error}</p>}
      {current.auth === "oauth" && current.connect_url && (
        <p class="card">
          People connect their own account at <a href={current.connect_url}>{current.connect_url}</a>. The tool list is read with the account of the
          first owner or admin who connects.
        </p>
      )}
      <Review alias={alias} pending={pending} loading={pins.loading && !pins.data} />
      {accepted.length > 0 && (
        <section class="card">
          <h2>Available to AI clients</h2>
          <div class="list-wrap">
            <table class="list">
              <thead>
                <tr>
                  <th>Kind</th>
                  <th>Name</th>
                  <th>Description</th>
                </tr>
              </thead>
              <tbody>
                {accepted.map((p) => (
                  <tr key={p.kind + p.name}>
                    <td>{p.kind}</td>
                    <td>
                      <code>{p.name}</code>
                    </td>
                    <td>{firstLine(p.definition?.description)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </section>
      )}
      {current.auth === "oauth" && <Connected alias={alias} />}
      <Settings srv={current} />
    </>
  );
}

function Review({ alias, pending, loading }: { alias: string; pending: Pin[]; loading: boolean }) {
  const [unticked, setUnticked] = useState<Set<string>>(new Set());
  const [busy, run] = useAction();
  const key = (p: Pin) => p.kind + "|" + p.hash + "|" + p.name;
  const chosen = pending.filter((p) => !unticked.has(key(p)));

  const accept = () =>
    void run(async () => {
      await api("POST", "/v1/servers/" + seg(alias) + "/pins:accept", { pins: chosen.map((p) => ({ kind: p.kind, hash: p.hash, name: p.name })) });
      invalidate("/v1/servers");
    }, "Accepted. Those tools are now available to AI clients.");

  return (
    <section class="card">
      <h2>Waiting for review</h2>
      {loading && <Loading what="tools" />}
      {!loading && pending.length === 0 && <Empty>Nothing waiting.</Empty>}
      {pending.length > 0 && (
        <>
          <p class="meta">New or changed tools are hidden from AI clients until you accept them. Read what each one says it does.</p>
          <div class="list-wrap">
            <table class="list">
              <thead>
                <tr>
                  <th>Accept</th>
                  <th>Kind</th>
                  <th>Name</th>
                  <th>Description</th>
                </tr>
              </thead>
              <tbody>
                {pending.map((p) => (
                  <tr key={key(p)}>
                    <td>
                      <input
                        type="checkbox"
                        checked={!unticked.has(key(p))}
                        aria-label={"Accept " + p.name}
                        onChange={(e) => {
                          const next = new Set(unticked);
                          if ((e.currentTarget as HTMLInputElement).checked) next.delete(key(p));
                          else next.add(key(p));
                          setUnticked(next);
                        }}
                      />
                    </td>
                    <td>{p.kind}</td>
                    <td>
                      <code>{p.name}</code>
                    </td>
                    <td>{firstLine(p.definition?.description)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          <div class="actions">
            <button type="button" class="approve" disabled={busy || chosen.length === 0} onClick={accept}>
              Accept {chosen.length} {chosen.length === 1 ? "tool" : "tools"}
            </button>
          </div>
        </>
      )}
    </section>
  );
}

function Connected({ alias }: { alias: string }) {
  const path = "/v1/servers/" + seg(alias) + "/connections";
  const conns = useData<{ connections: ServerConnection[] }>(path);
  const [busy, run] = useAction();
  const list = conns.data?.connections ?? [];
  return (
    <section class="card">
      <h2>Connected people</h2>
      <ErrorLine text={conns.error} />
      {conns.data && list.length === 0 && <Empty>Nobody has connected yet.</Empty>}
      {list.length > 0 && (
        <div class="list-wrap">
          <table class="list">
            <thead>
              <tr>
                <th>Person</th>
                <th>Connected</th>
                <th>Last used</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {list.map((c) => (
                <tr key={c.person}>
                  <td>
                    {c.person}
                    {c.catalog && <small class="meta"> (reads the tool list)</small>}
                  </td>
                  <td>{when(c.connected_at)}</td>
                  <td>{when(c.last_used_at)}</td>
                  <td>
                    <button
                      type="button"
                      class="deny"
                      disabled={busy}
                      onClick={() =>
                        void run(async () => {
                          await api("DELETE", path + "/" + seg(c.person));
                          invalidate(path);
                        }, "Disconnected " + c.person + ".")
                      }
                    >
                      Disconnect
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </section>
  );
}

function Settings({ srv }: { srv: ServerView }) {
  const [confirm, setConfirm] = useState("");
  const [busy, run] = useAction();
  const toggle = () =>
    void run(
      async () => {
        await api("PATCH", "/v1/servers/" + seg(srv.alias), { enabled: !srv.enabled });
        invalidate("/v1/servers");
      },
      srv.enabled ? "Server turned off." : "Server turned on.",
    );
  const remove = (e: Event) => {
    e.preventDefault();
    void run(async () => {
      await api("DELETE", "/v1/servers/" + seg(srv.alias));
      invalidate("/v1/servers");
      navigate("/admin/servers");
    }, "Server deleted.");
  };
  return (
    <section class="card">
      <h2>Settings</h2>
      <div class="actions">
        <button type="button" class={srv.enabled ? undefined : "primary"} disabled={busy} onClick={toggle}>
          {srv.enabled ? "Turn off" : "Turn on"}
        </button>
      </div>
      <form class="stack danger-zone" onSubmit={remove}>
        <label>
          <span>
            Delete this server, its rules and everyone's connections. Type <code>{srv.alias}</code> to confirm:
          </span>
          <input name="confirm" autocomplete="off" value={confirm} onInput={(e) => setConfirm((e.currentTarget as HTMLInputElement).value)} />
        </label>
        <div class="actions">
          <button type="submit" class="deny" disabled={busy || confirm !== srv.alias}>
            Delete server
          </button>
        </div>
      </form>
    </section>
  );
}
