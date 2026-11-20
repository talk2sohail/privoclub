import { useEffect, useState } from "preact/hooks";
import { api, seg } from "../api";
import type { PageProps } from "../app";
import { invalidate, useData } from "../data";
import { Link, navigate } from "../router";
import { TeamPicker } from "../catalog";
import type { Pin, ServerConnection, ServerView, Team } from "../types";
import { Definition, Findings, previousOf, warnings } from "../review";
import { Empty, ErrorLine, Loading, Status, firstLine, flash, when, useAction } from "../ui";
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
  // What AI clients get: accepted and presented by the server now (an older
  // accepted definition the server no longer offers is not available).
  const accepted = all.filter((p) => p.status === "accepted" && p.serving !== false);
  const earlier = all.filter((p) => p.status === "accepted" && p.serving === false);
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
      {earlier.length > 0 && (
        <details class="card">
          <summary>Accepted earlier, not offered now ({earlier.length})</summary>
          <p class="meta">
            The server no longer offers these versions, so AI clients do not get them. If it offers one again, AI clients get it without another review.
          </p>
          <ul>
            {earlier.map((p) => (
              <li key={p.kind + p.name + p.hash}>
                <code>{p.name}</code> <small class="meta">{p.kind}</small> {firstLine(p.definition?.description)}
              </li>
            ))}
          </ul>
        </details>
      )}
      {session.can.teams && <Access srv={current} />}
      {session.can.servers && (current.teams ?? []).length > 0 && <Toolsets srv={current} tools={toolNames(accepted)} />}
      {(current.auth === "oauth" || current.auth === "key") && <Connected alias={alias} />}
      {current.transport !== "stdio" && <Edit key={current.alias + current.target} srv={current} />}
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

/** toolNames are the distinct names of the accepted tools, sorted. */
function toolNames(accepted: Pin[]): string[] {
  return [...new Set(accepted.filter((p) => p.kind === "tool").map((p) => p.name))].sort();
}

// Toolsets (ADR 0026): per team, all of this server's tools or only some.
function Toolsets({ srv, tools }: { srv: ServerView; tools: string[] }) {
  const teams = useData<{ teams: Team[] }>("/v1/teams");
  const names = new Map((teams.data?.teams ?? []).map((t) => [t.slug, t.name]));
  return (
    <section class="card">
      <h2>Which tools each team sees</h2>
      <p class="meta">
        A team sees all of this server's tools unless you choose some. A person in several teams sees what any of them sees. New tools stay hidden from a
        team that sees only some, until you choose them. Prompts and resources are not narrowed: every team on the server sees them.
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

/**
 * editBody is what an edit sends (ADR 0029): only what changed. headers maps
 * a name to its new value, or "" to remove it; secret undefined keeps the
 * client secret, "" removes it.
 */
export function editBody(
  srv: ServerView,
  f: { url: string; headers: Record<string, string>; clientID?: string; secret?: string },
): Record<string, unknown> {
  const body: Record<string, unknown> = {};
  const url = f.url.trim();
  if (url && url !== srv.target) body.url = url;
  const headers: Record<string, string> = {};
  for (const [k, v] of Object.entries(f.headers)) {
    const name = k.trim();
    if (name && (v !== "" || (srv.header_names ?? []).includes(name))) headers[name] = v;
  }
  if (Object.keys(headers).length > 0) body.headers = headers;
  if (srv.client_id !== undefined) {
    const id = (f.clientID ?? "").trim();
    if ((id && id !== srv.client_id) || f.secret !== undefined) {
      const c: Record<string, string> = { client_id: id || srv.client_id };
      if (f.secret !== undefined) c.client_secret = f.secret;
      body.oauth_client = c;
    }
  }
  return body;
}

// Edit changes a server's address, static headers or hand-entered OAuth
// client (ADR 0029). Values are never shown: a header is set or not.
function Edit({ srv }: { srv: ServerView }) {
  const [busy, run, error] = useAction();
  const [url, setURL] = useState(srv.target);
  const [values, setValues] = useState<Record<string, string>>({});
  const [removed, setRemoved] = useState<Set<string>>(new Set());
  const [newName, setNewName] = useState("");
  const [newValue, setNewValue] = useState("");
  const [clientID, setClientID] = useState(srv.client_id ?? "");
  const [secret, setSecret] = useState<string | undefined>(undefined);
  const [sure, setSure] = useState(false);
  const oauth = srv.auth === "oauth";
  const staticHeaders = !srv.auth;
  const names = srv.header_names ?? [];
  const headers: Record<string, string> = {};
  for (const n of names) {
    if (removed.has(n)) headers[n] = "";
    else if (values[n]) headers[n] = values[n];
  }
  if (newName.trim() && newValue) headers[newName.trim()] = newValue;
  const body = editBody(srv, { url, headers, clientID: srv.client_id !== undefined ? clientID : undefined, secret });
  const moving = body.url !== undefined;
  const lost = moving ? names.filter((n) => !(n in headers) || headers[n] === "") : [];

  const save = (e: Event) => {
    e.preventDefault();
    void run(async () => {
      const out = await api<{ headers_dropped?: string[] }>("PATCH", "/v1/servers/" + seg(srv.alias), body);
      setValues({});
      setRemoved(new Set());
      setNewName("");
      setNewValue("");
      setSecret(undefined);
      setSure(false);
      invalidate("/v1/servers");
      invalidate("/v1/servers/" + seg(srv.alias) + "/pins");
      const done = moving ? "Saved. Everyone connects again, and every tool waits for your review." : "Saved.";
      if (out.headers_dropped?.length) flash("warn", done + " Enter these headers again for the new address: " + out.headers_dropped.join(", ") + ".");
      else flash("ok", done);
    });
  };

  return (
    <section class="card">
      <h2>Edit</h2>
      <form class="stack" onSubmit={save}>
        <label>
          Address
          <input name="url" type="url" required value={url} disabled={oauth} onInput={(e) => setURL((e.currentTarget as HTMLInputElement).value)} />
        </label>
        {oauth && <p class="meta">This server uses each person's own account, set up for this address. To use another address, delete it and add it again.</p>}
        {moving && (
          <div class="flash warn" role="alert">
            <p>A new address is a new server to trust:</p>
            <ul>
              <li>everyone's connection to it ends, and they connect again;</li>
              <li>every tool goes back to review, and AI clients see none until you accept them;</li>
              {lost.length > 0 && <li>header values are not sent to the new address unless you enter them again: {lost.join(", ")}.</li>}
            </ul>
            <label class="inline">
              <input type="checkbox" checked={sure} onChange={(e) => setSure((e.currentTarget as HTMLInputElement).checked)} /> Change the address anyway
            </label>
          </div>
        )}
        {staticHeaders && (
          <fieldset>
            <legend>Headers sent with every request</legend>
            {names.length === 0 && <p class="meta">None.</p>}
            {names.map((n) => (
              <div class="row" key={n}>
                <code>{n}</code> <small class="meta">set</small>
                <input
                  type="password"
                  autocomplete="off"
                  aria-label={"New value of " + n}
                  placeholder="new value (blank keeps it)"
                  disabled={removed.has(n)}
                  value={values[n] ?? ""}
                  onInput={(e) => setValues({ ...values, [n]: (e.currentTarget as HTMLInputElement).value })}
                />
                <label class="inline">
                  <input
                    type="checkbox"
                    checked={removed.has(n)}
                    onChange={(e) => {
                      const next = new Set(removed);
                      if ((e.currentTarget as HTMLInputElement).checked) next.add(n);
                      else next.delete(n);
                      setRemoved(next);
                    }}
                  />{" "}
                  remove
                </label>
              </div>
            ))}
            <div class="row">
              <input aria-label="New header name" placeholder="X-Api-Key" autocomplete="off" value={newName} onInput={(e) => setNewName((e.currentTarget as HTMLInputElement).value)} />
              <input
                type="password"
                aria-label="New header value"
                autocomplete="off"
                placeholder="value"
                value={newValue}
                onInput={(e) => setNewValue((e.currentTarget as HTMLInputElement).value)}
              />
            </div>
          </fieldset>
        )}
        {srv.client_id !== undefined && (
          <fieldset>
            <legend>OAuth client</legend>
            <label>
              Client id <input autocomplete="off" value={clientID} onInput={(e) => setClientID((e.currentTarget as HTMLInputElement).value)} />
            </label>
            {clientID.trim() && clientID.trim() !== srv.client_id && <p class="meta">A new client id ends everyone's connection: they connect again.</p>}
            <label>
              Client secret <small class="meta">{srv.has_client_secret ? "a secret is stored" : "none stored"}</small>
              <input
                type="password"
                autocomplete="off"
                aria-label="New client secret"
                placeholder="new secret (blank keeps it)"
                value={secret ?? ""}
                onInput={(e) => {
                  const v = (e.currentTarget as HTMLInputElement).value;
                  setSecret(v === "" ? undefined : v);
                }}
              />
            </label>
            {srv.has_client_secret && (
              <button type="button" class="link" onClick={() => setSecret("")}>
                Remove the stored secret
              </button>
            )}
            {secret === "" && <p class="meta">The secret will be removed.</p>}
          </fieldset>
        )}
        <ErrorLine text={error} />
        <div class="actions">
          <button type="submit" class="primary" disabled={busy || Object.keys(body).length === 0 || (moving && !sure)}>
            Save changes
          </button>
        </div>
      </form>
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
