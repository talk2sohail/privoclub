import { useEffect, useState } from "preact/hooks";
import { api, seg } from "../api";
import type { PageProps } from "../app";
import { invalidate, useData } from "../data";
import { Link, navigate } from "../router";
import { TeamPicker } from "../catalog";
import type { Pin, ServerConnection, ServerView, Team } from "../types";
import { PinDetail, reveal, warnings } from "../review";
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
  const blocked = all.filter((p) => p.status === "blocked");
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
      <Review alias={alias} pending={pending} blocked={blocked} loading={pins.loading && !pins.data} />
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
                  <tr key={p.kind + p.name + p.hash}>
                    <td>{p.kind}</td>
                    <td>
                      <code>{reveal(p.name)}</code>
                    </td>
                    <td>
                      <details>
                        <summary>{firstLine(reveal(String(p.definition?.description ?? ""))) || "details"}</summary>
                        <PinDetail pin={p} />
                      </details>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </section>
      )}
      {session.can.teams && <Access srv={current} />}
      {session.can.servers && (current.teams ?? []).length > 0 && <Toolsets srv={current} tools={toolNames(accepted)} />}
      {current.auth === "oauth" && <Connected alias={alias} />}
      <Settings srv={current} />
    </>
  );
}

function Review({ alias, pending, blocked, loading }: { alias: string; pending: Pin[]; blocked: Pin[]; loading: boolean }) {
  // Nothing is ticked until the reviewer ticks it, and items that arrive while
  // the page polls arrive unticked (the F14 review's R-04).
  const [ticked, setTicked] = useState<Set<string>>(new Set());
  const [busy, run] = useAction();
  const key = (p: Pin) => p.kind + "|" + p.hash + "|" + p.name;
  const chosen = pending.filter((p) => ticked.has(key(p)));
  const tick = (p: Pin, on: boolean) => {
    const next = new Set(ticked);
    if (on) next.add(key(p));
    else next.delete(key(p));
    setTicked(next);
  };

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
            New or changed tools are hidden from AI clients until you accept them. The AI client reads everything below, not just the first line: read it
            all, and block what you do not trust.
          </p>
          <ul class="pins">
            {pending.map((p) => {
              const warn = warnings(p);
              return (
                <li key={key(p)} class="pin">
                  <label class="inline pin-head">
                    <input type="checkbox" checked={ticked.has(key(p))} aria-label={"Choose " + p.name} onChange={(e) => tick(p, (e.currentTarget as HTMLInputElement).checked)} />
                    <code>{reveal(p.name)}</code> <small class="meta">{p.kind}</small>
                    <span class="badge">{p.previous ? "changed" : "new"}</span>
                    {warn > 0 && <span class="badge warn">{warn === 1 ? "1 warning" : warn + " warnings"}</span>}
                  </label>
                  <PinDetail pin={p} />
                </li>
              );
            })}
          </ul>
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
                <div class="pin-head row">
                  <code>{reveal(p.name)}</code> <small class="meta">{p.kind}</small>
                  <button type="button" class="link" disabled={busy} onClick={() => decide("accept", [p], "Accepted. The tool is now available to AI clients.")}>
                    Accept after all
                  </button>
                </div>
                <PinDetail pin={p} />
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

/** toolNames are the distinct names of the accepted tools, sorted. */
function toolNames(accepted: Pin[]): string[] {
  return [...new Set(accepted.filter((p) => p.kind === "tool").map((p) => p.name))].sort();
}

// Toolsets (ADR 0023): per team, all of this server's tools or only some.
function Toolsets({ srv, tools }: { srv: ServerView; tools: string[] }) {
  const teams = useData<{ teams: Team[] }>("/v1/teams");
  const names = new Map((teams.data?.teams ?? []).map((t) => [t.slug, t.name]));
  return (
    <section class="card">
      <h2>Which tools each team sees</h2>
      <p class="meta">
        A team sees all of this server's tools unless you choose some. A person in several teams sees what any of them sees. New tools stay hidden from a
        team that sees only some, until you choose them.
      </p>
      {(srv.teams ?? []).map((slug) => (
        <TeamToolset key={slug} alias={srv.alias} slug={slug} name={names.get(slug) ?? slug} saved={srv.team_tools?.[slug] ?? null} tools={tools} />
      ))}
    </section>
  );
}

function TeamToolset({ alias, slug, name, saved, tools }: { alias: string; slug: string; name: string; saved: string[] | null; tools: string[] }) {
  // null: all tools; a list: only those.
  const [draft, setDraft] = useState<string[] | null | undefined>(undefined);
  const [busy, run] = useAction();
  const current = draft === undefined ? saved : draft;
  const changed = draft !== undefined && JSON.stringify(draft === null ? null : [...draft].sort()) !== JSON.stringify(saved === null ? null : [...saved].sort());
  const offered = [...new Set([...tools, ...(current ?? [])])].sort();
  const save = () =>
    void run(async () => {
      await api("PATCH", "/v1/servers/" + seg(alias) + "/teams/" + seg(slug), { tools: current === null ? null : [...current].sort() });
      invalidate("/v1/servers");
      setDraft(undefined);
    }, "Saved. " + name + " sees the change at its next request.");
  return (
    <fieldset>
      <legend>{name}</legend>
      <label class="inline">
        <input type="radio" name={"toolset-" + slug} checked={current === null} onChange={() => setDraft(null)} /> All tools
      </label>
      <label class="inline">
        <input type="radio" name={"toolset-" + slug} checked={current !== null} onChange={() => setDraft(current ?? [])} /> Only these
      </label>
      {current !== null && (
        <div class="stack">
          {offered.length === 0 && <p class="meta">This server has no reviewed tools yet.</p>}
          {offered.map((tool) => (
            <label class="inline" key={tool}>
              <input
                type="checkbox"
                checked={current.includes(tool)}
                aria-label={name + ": " + tool}
                onChange={(e) =>
                  setDraft((e.currentTarget as HTMLInputElement).checked ? [...current, tool] : current.filter((t) => t !== tool))
                }
              />{" "}
              <code>{tool}</code>
              {!tools.includes(tool) && <small class="meta"> not offered now</small>}
            </label>
          ))}
          {current.length === 0 && <p class="flash warn">{name} will see none of this server's tools.</p>}
        </div>
      )}
      <div class="actions">
        <button type="button" class="primary small" aria-label={"Save the tools " + name + " sees"} disabled={busy || !changed} onClick={save}>
          Save
        </button>
      </div>
    </fieldset>
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
