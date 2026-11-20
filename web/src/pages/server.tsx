import { useEffect, useState } from "preact/hooks";
import { api, seg } from "../api";
import type { PageProps } from "../app";
import { invalidate, useData } from "../data";
import { Link, navigate } from "../router";
import { TeamPicker } from "../catalog";
import type { Pin, ServerConnection, ServerView, Team } from "../types";
import { Definition, Findings, previousOf, warnings } from "../review";
import { Empty, ErrorLine, Loading, Status, firstLine, when, useAction } from "../ui";
import { authLabel } from "./servers";

// A new server takes a moment to connect and list its tools: the page asks
// again every few seconds for a while after it opens, and while connecting.
const WATCH_MS = 60_000;
const POLL_MS = 3_000;

export function Server({ alias, session }: PageProps & { alias: string }) {
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
      {current.auth === "key" && current.connect_url && (
        <p class="card">
          People enter their own API key at <a href={current.connect_url}>{current.connect_url}</a>
          {current.key_header && (
            <>
              {" "}
              (sent as <code>{current.key_header}</code>)
            </>
          )}
          . The tool list is read with the key of the first owner or admin who enters one.
        </p>
      )}
      <Review alias={alias} pending={pending} all={all} loading={pins.loading && !pins.data} />
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
      {session.can.teams && <Access srv={current} />}
      {(current.auth === "oauth" || current.auth === "key") && <Connected alias={alias} />}
      <Settings srv={current} />
    </>
  );
}

function Review({ alias, pending, all, loading }: { alias: string; pending: Pin[]; all: Pin[]; loading: boolean }) {
  // Nothing is chosen until the reviewer chooses it, and items that arrive
  // while the page polls arrive unchosen (the F14 review's R-04).
  const [ticked, setTicked] = useState<Set<string>>(new Set());
  const [busy, run] = useAction();
  const key = (p: Pin) => p.kind + "|" + p.hash + "|" + p.name;
  const chosen = pending.filter((p) => ticked.has(key(p)));
  const blocked = all.filter((p) => p.status === "blocked");
  const decide = (verb: "accept" | "block", list: Pin[], done: string) =>
    void run(async () => {
      await api("POST", "/v1/servers/" + seg(alias) + "/pins:" + verb, { pins: list.map((p) => ({ kind: p.kind, hash: p.hash, name: p.name })) });
      setTicked(new Set());
      invalidate("/v1/servers");
    }, done);

  return (
    <section class="card">
      <h2>Waiting for review</h2>
      {loading && <Loading what="tools" />}
      {!loading && pending.length === 0 && <Empty>Nothing waiting.</Empty>}
      {pending.length > 0 && (
        <>
          <p class="meta">
            New or changed tools are hidden from AI clients until you accept them. Read all of what each one says it does: an AI client reads every word of it.
            Block what you do not trust.
          </p>
          <div class="list-wrap">
            <table class="list">
              <thead>
                <tr>
                  <th>Choose</th>
                  <th>Kind</th>
                  <th>Name</th>
                  <th>Description</th>
                </tr>
              </thead>
              <tbody>
                {pending.map((p) => {
                  const warn = warnings(p);
                  return (
                    <tr key={key(p)}>
                      <td>
                        <input
                          type="checkbox"
                          checked={ticked.has(key(p))}
                          aria-label={"Choose " + p.name}
                          onChange={(e) => {
                            const next = new Set(ticked);
                            if ((e.currentTarget as HTMLInputElement).checked) next.add(key(p));
                            else next.delete(key(p));
                            setTicked(next);
                          }}
                        />
                      </td>
                      <td>{p.kind}</td>
                      <td>
                        <code>{p.name}</code>
                        {warn > 0 && <span class="badge warn">{warn === 1 ? "1 warning" : warn + " warnings"}</span>}
                      </td>
                      <td>
                        <Findings list={p.findings ?? []} />
                        <Definition pin={p} previous={previousOf(p, all)} />
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
          <div class="actions">
            <button type="button" class="link" disabled={busy} onClick={() => setTicked(new Set(pending.map(key)))}>
              Choose all {pending.length} shown
            </button>
            <button type="button" class="approve" disabled={busy || chosen.length === 0} onClick={() => decide("accept", chosen, "Accepted. Those tools are now available to AI clients.")}>
              Accept {chosen.length} {chosen.length === 1 ? "tool" : "tools"}
            </button>
            <button type="button" class="deny" disabled={busy || chosen.length === 0} onClick={() => decide("block", chosen, "Blocked. Those definitions stay hidden from AI clients.")}>
              Block {chosen.length}
            </button>
          </div>
        </>
      )}
      {blocked.length > 0 && (
        <details>
          <summary>Blocked ({blocked.length})</summary>
          <ul class="pins">
            {blocked.map((p) => (
              <li key={key(p)} class="pin">
                <div class="row">
                  <code>{p.name}</code> <small class="meta">{p.kind}</small>
                  <button type="button" class="link" disabled={busy} onClick={() => decide("accept", [p], "Accepted. The tool is now available to AI clients.")}>
                    Accept after all
                  </button>
                </div>
                <Definition pin={p} />
              </li>
            ))}
          </ul>
        </details>
      )}
    </section>
  );
}

function Access({ srv }: { srv: ServerView }) {
  const teams = useData<{ teams: Team[] }>("/v1/teams");
  const saved = [...(srv.teams ?? [])].sort();
  const [chosen, setChosen] = useState<string[] | null>(null);
  const [busy, run] = useAction();
  const current = chosen ?? saved;
  const changed = chosen !== null && chosen.join(",") !== saved.join(",");
  const save = () =>
    void run(async () => {
      await api("PATCH", "/v1/servers/" + seg(srv.alias), { teams: current });
      invalidate("/v1/servers");
      invalidate("/v1/teams");
      setChosen(null);
    }, "Saved. The change applies to the next request of every AI client.");
  return (
    <section class="card">
      <h2>Who can use it</h2>
      <ErrorLine text={teams.error} />
      {teams.data && (
        <>
          <p class="meta">People see this server's tools only when they are in one of its teams. Rules and approvals still apply to them.</p>
          <TeamPicker teams={teams.data.teams} chosen={current} onChange={setChosen} legend="Teams" />
          <div class="actions">
            <button type="button" class="primary" disabled={busy || !changed} onClick={save}>
              Save
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
