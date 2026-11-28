// The start page (ADR 0020): what a person can use through Privo and how to
// set up their AI client. Members land here.

import { useState } from "preact/hooks";
import { api, seg } from "../api";
import type { PageProps } from "../app";
import { invalidate, useData } from "../data";
import { Link } from "../router";
import { ClientSetup } from "../setup";
import type { MyApproval, MyServer, MyTools, Token } from "../types";
import { Copy, Empty, ErrorLine, Loading, Status, firstLine, formValues, useAction, when } from "../ui";

export function Start({ session }: PageProps) {
  const tools = useData<MyTools>("/v1/me/tools");
  const mine = useData<{ tokens: Token[] }>("/v1/me/tokens");
  const [created, setCreated] = useState<Token | null>(null);
  const [openServer, setOpenServer] = useState<string>("");
  const [busy, run] = useAction();
  const data = tools.data;
  const perPerson = (s: { auth?: string }) => s.auth === "oauth" || s.auth === "key";
  const needConnect = (data?.servers ?? []).filter((s) => perPerson(s) && !s.connected);
  const hasToken = (mine.data?.tokens.length ?? 0) > 0 || !!created;

  const create = (e: Event) => {
    const form = e.currentTarget as HTMLFormElement;
    const f = formValues(e);
    void run(async () => {
      setCreated(await api<Token>("POST", "/v1/me/tokens", { name: (f.name ?? "").trim() }));
      form.reset();
      invalidate("/v1/me/tokens");
      invalidate("/v1/tokens");
    }, "Token created. Copy it now: it is shown only once.");
  };

  const first = session.user.name.split(" ")[0] || session.user.person;
  return (
    <>
      <h1>Welcome, {first}</h1>
      <p class="meta">
        Privo is one MCP server for all your work tools. Add it to your AI client once; the tools your teams use show up there, and your
        organisation's rules and approvals apply to every call.
      </p>
      <ErrorLine text={tools.error} />
      {data && (
        <p>
          Your teams:{" "}
          {data.teams.map((t, i) => (
            <span key={t.slug}>
              {i > 0 && ", "}
              <strong>{t.name}</strong>
            </span>
          ))}
        </p>
      )}

      <MyRequests />

      <section class="card">
        <h2>1. Your tools</h2>
        {!data && tools.loading && <Loading what="your tools" />}
        {data && data.servers.length === 0 && (
          <Empty>No servers are shared with your teams yet. Ask an admin to add the ones you need, or to put you in a team.</Empty>
        )}
        {data && data.servers.length > 0 && (
          <div class="list-wrap">
            <table class="list">
              <thead>
                <tr>
                  <th>Server</th>
                  <th>Tools</th>
                  <th>Status</th>
                  <th>Your account</th>
                </tr>
              </thead>
              <tbody>
                {data.servers.map((s) => {
                  const list = s.tool_list ?? [];
                  const total = s.total_tools ?? s.tools;
                  const muted = Math.max(0, total - s.tools);
                  const expanded = openServer === s.alias;
                  return (
                    <>
                      <tr key={s.alias}>
                        <td>
                          <strong>{s.name}</strong> <small class="meta">{s.alias}__…</small>
                          {s.description && (
                            <>
                              <br />
                              <small class="meta">{s.description}</small>
                            </>
                          )}
                        </td>
                        <td>
                          {muted > 0 ? (
                            <>
                              {s.tools} of {total} <small class="meta">({muted} off)</small>
                            </>
                          ) : (
                            s.tools
                          )}
                          {list.length > 0 && (
                            <>
                              {" "}
                              <button
                                type="button"
                                class="link small"
                                aria-expanded={expanded}
                                onClick={() => setOpenServer(expanded ? "" : s.alias)}
                              >
                                {expanded ? "Hide tools" : "Tools"}
                              </button>
                            </>
                          )}
                        </td>
                        <td>
                          <Status value={s.status} />
                        </td>
                        <td>
                          {!perPerson(s) && <span class="meta">not needed</span>}
                          {perPerson(s) && s.connected && <span>{s.auth === "key" ? "key saved" : "connected"}</span>}
                          {perPerson(s) && !s.connected && s.connect_url && (
                            <a class="badge" href={s.connect_url}>
                              {s.auth === "key" ? "Enter your key" : "Connect"}
                            </a>
                          )}
                        </td>
                      </tr>
                      {expanded && list.length > 0 && (
                        <tr key={s.alias + ":tools"} class="member-tools-row">
                          <td colSpan={4}>
                            <MemberServerTools key={s.alias + ":" + s.tools + ":" + total} server={s} />
                          </td>
                        </tr>
                      )}
                    </>
                  );
                })}
              </tbody>
            </table>
          </div>
        )}
        {needConnect.length > 0 && (
          <p class="meta">
            Servers marked Connect use your own account: connect it once, and Privo calls them as you. You can disconnect any time on{" "}
            <Link href="/admin/connections">Connections</Link>.
          </p>
        )}
      </section>

      <section class="card">
        <h2>2. A token for your AI client</h2>
        {created?.token ? (
          <div class="highlight">
            <p>
              Copy <strong>{created.name}</strong> now. It is shown only once{created.expires_at ? ` and works until ${when(created.expires_at)}` : ""}.
            </p>
            <Copy text={created.token} label="token" />
          </div>
        ) : (
          <>
            <p class="meta">
              Each AI client gets its own token, so you can revoke one without the others.
              {hasToken && (
                <>
                  {" "}
                  You have {mine.data?.tokens.length} already: see <Link href="/admin/tokens">Tokens</Link>.
                </>
              )}
            </p>
            <form class="row" onSubmit={create}>
              <input name="name" required maxLength={64} placeholder="claude-code-laptop" aria-label="Name of the AI client" autocomplete="off" />
              <button type="submit" class="primary" disabled={busy}>
                Create token
              </button>
            </form>
          </>
        )}
      </section>

      <section class="card">
        <h2>3. Add Privo to your AI client</h2>
        <ClientSetup mcpURL={session.mcp_url} token={created?.token} />
        {!created?.token && <p class="meta">Create a token above and it is filled in for you.</p>}
      </section>
    </>
  );
}

// MemberServerTools lets a member inspect the tools their teams grant on a
// server and turn off ones they do not want in their AI client's context
// (ADR 0026 amendment). Turning tools off affects only this member.
function MemberServerTools({ server }: { server: MyServer }) {
  const list = server.tool_list ?? [];
  const initialDisabled = list
    .filter((t) => !t.enabled)
    .map((t) => t.name)
    .sort();
  const [disabled, setDisabled] = useState<string[]>(initialDisabled);
  const [filter, setFilter] = useState("");
  const [busy, run] = useAction();

  const disabledSet = new Set(disabled);
  const changed = disabled.join("\0") !== initialDisabled.join("\0");
  const q = filter.trim().toLowerCase();
  const shown = q
    ? list.filter((t) => t.name.toLowerCase().includes(q) || (t.description ?? "").toLowerCase().includes(q))
    : list;

  const toggle = (name: string, enabled: boolean) => {
    const next = new Set(disabledSet);
    if (enabled) next.delete(name);
    else next.add(name);
    setDisabled([...next].sort());
  };

  const save = () =>
    void run(async () => {
      await api("PATCH", "/v1/me/servers/" + seg(server.alias) + "/tools", { disabled });
      invalidate("/v1/me/tools");
    }, "Saved. The change applies to the next request of your AI clients.");

  return (
    <div class="member-tools">
      <p class="meta">
        Turn off tools you do not need right now to save context in your AI client. Turning a tool off affects only your own calls; if an AI client tries to call one you turned off, Privo reminds you to turn it back on here.
      </p>
      {list.length > 10 && (
        <p class="row">
          <input
            type="search"
            value={filter}
            placeholder="Filter tools…"
            aria-label={"Filter tools of " + server.alias}
            onInput={(e) => setFilter((e.currentTarget as HTMLInputElement).value)}
          />
        </p>
      )}
      <ul class="member-tools-list">
        {shown.map((t) => {
          const on = !disabledSet.has(t.name);
          const desc = firstLine(t.description);
          return (
            <li key={t.name}>
              <label>
                <input
                  type="checkbox"
                  checked={on}
                  onChange={(e) => toggle(t.name, (e.currentTarget as HTMLInputElement).checked)}
                />{" "}
                <code>{t.name}</code>
                {desc && <small class="meta"> — {desc}</small>}
              </label>
            </li>
          );
        })}
      </ul>
      {shown.length === 0 && <p class="meta">No tools match “{filter}”.</p>}
      <div class="row">
        <button type="button" class="link small" disabled={busy || disabled.length === 0} onClick={() => setDisabled([])}>
          Enable all
        </button>
        <button
          type="button"
          class="link small"
          disabled={busy || disabled.length === list.length}
          onClick={() => setDisabled(list.map((t) => t.name).sort())}
        >
          Disable all
        </button>
        <button type="button" class="primary small" disabled={busy || !changed} onClick={save}>
          Save
        </button>
      </div>
    </div>
  );
}

// MyRequests lists the person's own calls that wait for a person's decision,
// and lets them take one back (F10). Hidden when there are none.
function MyRequests() {
  const res = useData<{ approvals: MyApproval[] }>("/v1/me/approvals");
  const [busy, run] = useAction();
  const list = res.data?.approvals ?? [];
  if (list.length === 0) return null;
  const withdraw = (a: MyApproval) =>
    void run(async () => {
      await api("POST", "/v1/me/approvals/" + encodeURIComponent(a.id) + "/withdraw");
      invalidate("/v1/me/approvals");
    }, "Withdrawn. If your AI client calls again, it asks again.");
  return (
    <section class="card">
      <h2>Waiting for approval</h2>
      <p class="meta">Calls from your AI client that a person has to approve. Withdraw one you no longer want.</p>
      <ul class="catalog">
        {list.map((a) => (
          <li key={a.id}>
            <div class="catalog-head">
              <span>
                <code>{a.tool}</code> <small class="meta">on {a.server}, from “{a.requester}”, expires {when(a.expires_at)}</small>
              </span>
              <button type="button" class="link" disabled={busy} onClick={() => withdraw(a)}>
                Withdraw
              </button>
            </div>
          </li>
        ))}
      </ul>
    </section>
  );
}
