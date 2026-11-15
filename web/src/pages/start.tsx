// The start page (ADR 0020): what a person can use through Privo and how to
// set up their AI client. Members land here.

import { useState } from "preact/hooks";
import { api } from "../api";
import type { PageProps } from "../app";
import { invalidate, useData } from "../data";
import { Link } from "../router";
import { ClientSetup } from "../setup";
import type { MyTools, Token } from "../types";
import { Copy, Empty, ErrorLine, Loading, Status, formValues, useAction, when } from "../ui";

export function Start({ session }: PageProps) {
  const tools = useData<MyTools>("/v1/me/tools");
  const mine = useData<{ tokens: Token[] }>("/v1/me/tokens");
  const [created, setCreated] = useState<Token | null>(null);
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
                {data.servers.map((s) => (
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
                    <td>{s.tools}</td>
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
                ))}
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
