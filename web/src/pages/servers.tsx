import { useState } from "preact/hooks";
import { api } from "../api";
import type { PageProps } from "../app";
import { invalidate, useData } from "../data";
import { Link, navigate } from "../router";
import type { ServerView } from "../types";
import { Empty, ErrorLine, Loading, Status, formValues, useAction } from "../ui";

export function authLabel(auth: string | undefined): string {
  return auth === "oauth" ? "each person's own account" : "shared";
}

export function Servers({ session }: PageProps) {
  const list = useData<{ servers: ServerView[] }>("/v1/servers");
  const servers = list.data?.servers;
  return (
    <>
      <h1>Servers</h1>
      <p class="meta">MCP servers your team's AI clients reach through Privo.</p>
      <ErrorLine text={list.error} />
      {!servers && list.loading && <Loading what="servers" />}
      {servers && servers.length === 0 && <Empty>No servers yet. Add the first one below.</Empty>}
      {servers && servers.length > 0 && (
        <div class="list-wrap">
          <table class="list">
            <thead>
              <tr>
                <th>Name</th>
                <th>Status</th>
                <th>Tools</th>
                <th>Sign-in</th>
              </tr>
            </thead>
            <tbody>
              {servers.map((s) => (
                <tr key={s.alias}>
                  <td>
                    <Link href={"/admin/servers/" + s.alias}>
                      <strong>{s.alias}</strong>
                    </Link>
                    <br />
                    <small class="meta">{s.target}</small>
                  </td>
                  <td>
                    <Status value={s.status} />
                  </td>
                  <td>
                    {s.items?.tool ?? 0}
                    {s.pending_review > 0 && (
                      <>
                        {" "}
                        <Link class="badge" href={"/admin/servers/" + s.alias}>
                          {s.pending_review} to review
                        </Link>
                      </>
                    )}
                  </td>
                  <td>{authLabel(s.auth)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      <AddServer canOAuth={session.can.oauth_servers} />
    </>
  );
}

function AddServer({ canOAuth }: { canOAuth: boolean }) {
  const [authMode, setAuthMode] = useState("none");
  const [busy, run] = useAction();

  const submit = (e: Event) => {
    const f = formValues(e);
    const alias = (f.alias ?? "").trim();
    const body: Record<string, unknown> = { alias, transport: f.transport, url: (f.url ?? "").trim() };
    if (authMode === "header") {
      body.headers = { [(f.header_name ?? "").trim() || "Authorization"]: f.header_value ?? "" };
    } else if (authMode === "oauth") {
      const id = (f.client_id ?? "").trim();
      body.oauth = id ? { client_id: id, client_secret: (f.client_secret ?? "").trim() } : {};
    }
    void run(async () => {
      await api("POST", "/v1/servers", body);
      invalidate("/v1/servers");
      navigate("/admin/servers/" + alias);
    }, "Server added. Its tools show up below for review in a few seconds.");
  };

  return (
    <section class="card">
      <h2>Add a server</h2>
      <form class="stack" onSubmit={submit}>
        <label>
          Name <small class="meta">lowercase letters, digits and dashes; AI clients see tools as name__tool</small>
          <input name="alias" required pattern="[a-z][a-z0-9\-]{1,15}" placeholder="deepwiki" autocomplete="off" />
        </label>
        <label>
          Server URL
          <input name="url" type="url" required placeholder="https://mcp.example.com/mcp" />
        </label>
        <label>
          Transport
          <select name="transport">
            <option value="http">Streamable HTTP (most servers)</option>
            <option value="sse">SSE (older servers)</option>
          </select>
        </label>
        <fieldset>
          <legend>How Privo signs in to it</legend>
          <label class="inline">
            <input type="radio" name="auth" value="none" checked={authMode === "none"} onChange={() => setAuthMode("none")} /> No sign-in needed
          </label>
          <label class="inline">
            <input type="radio" name="auth" value="header" checked={authMode === "header"} onChange={() => setAuthMode("header")} /> A shared API key
            (sent as a header)
          </label>
          {canOAuth && (
            <label class="inline">
              <input type="radio" name="auth" value="oauth" checked={authMode === "oauth"} onChange={() => setAuthMode("oauth")} /> Each person's own
              account (OAuth: GitHub, Linear, Notion…)
            </label>
          )}
        </fieldset>
        {authMode === "header" && (
          <>
            <label>
              Header name <input name="header_name" placeholder="Authorization" autocomplete="off" />
            </label>
            <label>
              Header value <small class="meta">stored encrypted, never shown again</small>
              <input name="header_value" type="password" required autocomplete="off" placeholder="Bearer sk-…" />
            </label>
          </>
        )}
        {authMode === "oauth" && (
          <details>
            <summary>OAuth app (only if the provider needs one, like GitHub)</summary>
            <label>
              Client ID <input name="client_id" autocomplete="off" />
            </label>
            <label>
              Client secret <input name="client_secret" type="password" autocomplete="off" />
            </label>
          </details>
        )}
        <div class="actions">
          <button type="submit" class="primary" disabled={busy}>
            {busy ? "Adding…" : "Add server"}
          </button>
        </div>
      </form>
    </section>
  );
}
