import { useState } from "preact/hooks";
import { api, seg } from "../api";
import type { PageProps } from "../app";
import { invalidate, useData } from "../data";
import { ClientSetup } from "../setup";
import type { Token } from "../types";
import { Copy, Empty, ErrorLine, formValues, useAction, when } from "../ui";

export { claudeCommand } from "../clients";

export function Tokens({ session }: PageProps) {
  const can = session.can;
  const mine = useData<{ tokens: Token[] }>(can.use_gateway ? "/v1/me/tokens" : null);
  const all = useData<{ tokens: Token[] }>(can.manage_tokens ? "/v1/tokens" : null);
  const [created, setCreated] = useState<Token | null>(null);
  const [busy, run] = useAction();

  const createMine = (e: Event) => {
    const form = e.currentTarget as HTMLFormElement;
    const f = formValues(e);
    void run(async () => {
      setCreated(await api<Token>("POST", "/v1/me/tokens", { name: (f.name ?? "").trim() }));
      form.reset();
      invalidate("/v1/me/tokens");
      invalidate("/v1/tokens");
    }, "Token created. Copy it now: it is shown only once.");
  };

  const createAPI = (e: Event) => {
    const form = e.currentTarget as HTMLFormElement;
    const f = formValues(e);
    void run(async () => {
      setCreated(
        await api<Token>("POST", "/v1/tokens", { name: (f.name ?? "").trim(), role: f.role, kind: "control", person: (f.person ?? "").trim() }),
      );
      form.reset();
      invalidate("/v1/tokens");
    }, "API token created. Copy it now: it is shown only once.");
  };

  const revoke = (t: Token, own: boolean) =>
    void run(async () => {
      await api("DELETE", (own ? "/v1/me/tokens/" : "/v1/tokens/") + seg(t.id));
      if (created?.id === t.id) setCreated(null);
      invalidate("/v1/me/tokens");
      invalidate("/v1/tokens");
    }, "Token revoked. It stops working within seconds.");

  const allTokens = [...(all.data?.tokens ?? [])].sort((a, b) => a.person.localeCompare(b.person));

  return (
    <>
      <h1>Tokens</h1>
      {created?.token && (
        <section class="card highlight">
          <h2>{created.name}</h2>
          <p>Copy the token now. It is shown only once{created.expires_at ? ` and works until ${when(created.expires_at)}` : ""}.</p>
          <Copy text={created.token} label="token" />
          {created.kind === "gateway" && (
            <>
              <h3>Add Privo to your AI client</h3>
              <ClientSetup mcpURL={session.mcp_url} token={created.token} />
            </>
          )}
        </section>
      )}
      {can.use_gateway && (
        <section class="card">
          <h2>Your AI clients</h2>
          <p class="meta">Each AI client (Claude Code on your laptop, Cursor, …) gets its own token, so you can revoke one without the others. Calls run as you.</p>
          <ErrorLine text={mine.error} />
          {mine.data && mine.data.tokens.length === 0 && <Empty>None yet.</Empty>}
          {mine.data && mine.data.tokens.length > 0 && (
            <div class="list-wrap">
              <table class="list">
                <thead>
                  <tr>
                    <th>Name</th>
                    <th>Created</th>
                    <th>Expires</th>
                    <th />
                  </tr>
                </thead>
                <tbody>
                  {mine.data.tokens.map((t) => (
                    <tr key={t.id}>
                      <td>{t.name}</td>
                      <td>{when(t.created_at)}</td>
                      <td>{when(t.expires_at)}</td>
                      <td>
                        <button type="button" class="link" disabled={busy} onClick={() => revoke(t, true)}>
                          Revoke
                        </button>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
          <form class="row" onSubmit={createMine}>
            <input name="name" required maxLength={64} placeholder="claude-code-laptop" aria-label="Name of the AI client" autocomplete="off" />
            <button type="submit" class="primary" disabled={busy}>
              Create token
            </button>
          </form>
        </section>
      )}
      {can.manage_tokens && (
        <section class="card">
          <h2>All tokens</h2>
          <ErrorLine text={all.error} />
          {all.data && allTokens.length === 0 && <Empty>No tokens.</Empty>}
          {allTokens.length > 0 && (
            <div class="list-wrap">
              <table class="list">
                <thead>
                  <tr>
                    <th>Person</th>
                    <th>Name</th>
                    <th>Kind</th>
                    <th>Role</th>
                    <th>Expires</th>
                    <th />
                  </tr>
                </thead>
                <tbody>
                  {allTokens.map((t) => (
                    <tr key={t.id}>
                      <td>{t.person}</td>
                      <td>{t.name}</td>
                      <td>{t.kind === "gateway" ? "AI client" : "API"}</td>
                      <td>{t.role}</td>
                      <td>{when(t.expires_at)}</td>
                      <td>
                        <button type="button" class="link" disabled={busy} onClick={() => revoke(t, false)}>
                          Revoke
                        </button>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
          <details>
            <summary>Create an API token (for scripts and automation)</summary>
            <form class="stack" onSubmit={createAPI}>
              <label>
                Name <input name="name" required maxLength={64} autocomplete="off" />
              </label>
              <label>
                Person <small class="meta">who it acts for</small> <input name="person" maxLength={128} autocomplete="off" />
              </label>
              <label>
                Role
                <select name="role" defaultValue="auditor">
                  {session.token_roles.map((r) => (
                    <option key={r} value={r}>
                      {r}
                    </option>
                  ))}
                </select>
              </label>
              <div class="actions">
                <button type="submit" disabled={busy}>
                  Create API token
                </button>
              </div>
            </form>
          </details>
        </section>
      )}
    </>
  );
}
