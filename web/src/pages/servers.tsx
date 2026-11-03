import { useState } from "preact/hooks";
import { api } from "../api";
import type { PageProps } from "../app";
import { invalidate, useData } from "../data";
import { Link, navigate } from "../router";
import { CatalogAdd, TeamPicker, signInLabel } from "../catalog";
import type { Catalog, CatalogEntry, ServerView, Team } from "../types";
import { Empty, ErrorLine, Loading, Status, formValues, useAction } from "../ui";

export function authLabel(auth: string | undefined): string {
  return auth === "oauth" ? "each person's own account" : "shared";
}

export function teamNames(slugs: string[] | undefined, teams: Team[]): string {
  if (!slugs || slugs.length === 0) return "nobody";
  return slugs.map((s) => teams.find((t) => t.slug === s)?.name ?? s).join(", ");
}

export function Servers({ session }: PageProps) {
  const list = useData<{ servers: ServerView[] }>("/v1/servers");
  const teamsData = useData<{ teams: Team[] }>(session.can.teams ? "/v1/teams" : null);
  const teams = teamsData.data?.teams ?? [];
  // Adding waits for the teams, so a server is never opened to everyone just
  // because the picker had not loaded (F26 review R-05).
  const teamsReady = !session.can.teams || teamsData.data !== undefined;
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
                <th>Who</th>
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
                  <td class={s.teams && s.teams.length === 0 ? "warn-text" : undefined}>{teamNames(s.teams, teams)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      <ErrorLine text={teamsData.error} />
      <FromCatalog teams={teams} ready={teamsReady} />
      <AddServer canOAuth={session.can.oauth_servers} teams={teams} ready={teamsReady} />
    </>
  );
}

/** FromCatalog lists the catalog by profile; one entry at a time opens its form. */
export function FromCatalog({
  teams,
  profile,
  initialTeams,
  ready = true,
}: {
  teams: Team[];
  profile?: string;
  initialTeams?: string[];
  ready?: boolean;
}) {
  const cat = useData<Catalog>("/v1/catalog");
  const [filter, setFilter] = useState(profile ?? "");
  const [open, setOpen] = useState("");
  const data = cat.data;
  const shown = (data?.entries ?? []).filter((e) => !filter || e.profiles.includes(filter));
  return (
    <section class="card">
      <h2>{profile ? "Suggested servers" : "Add from the catalog"}</h2>
      <ErrorLine text={cat.error} />
      {!data && cat.loading && <Loading what="the catalog" />}
      {data && !profile && (
        <div class="row tabs" role="tablist" aria-label="Profile">
          <button type="button" role="tab" class={filter === "" ? "primary small" : "small"} aria-selected={filter === ""} onClick={() => setFilter("")}>
            All
          </button>
          {data.profiles.map((p) => (
            <button key={p.id} type="button" role="tab" class={filter === p.id ? "primary small" : "small"} aria-selected={filter === p.id} onClick={() => setFilter(p.id)}>
              {p.name}
            </button>
          ))}
        </div>
      )}
      {data && (
        <ul class="catalog">
          {shown.map((e: CatalogEntry) => (
            <li key={e.id}>
              <div class="catalog-head">
                <span>
                  <strong>{e.name}</strong> <small class="meta">{signInLabel(e)}</small>
                  <br />
                  <small class="meta">{e.description}</small>
                </span>
                {e.installed ? (
                  <Link href={"/admin/servers/" + e.installed}>added as {e.installed}</Link>
                ) : (
                  open !== e.id && (
                    <button type="button" class="small" onClick={() => setOpen(e.id)}>
                      Add
                    </button>
                  )
                )}
              </div>
              {open === e.id && !e.installed && (
                <CatalogAdd
                  entry={e}
                  teams={teams}
                  initialTeams={initialTeams ?? ["everyone"]}
                  callbackURL={data.callback_url}
                  oauthAvailable={data.oauth_available}
                  ready={ready}
                  onCancel={() => setOpen("")}
                />
              )}
            </li>
          ))}
        </ul>
      )}
      {data && <p class="meta">Checked {data.entries[0]?.checked_on}. An entry only fills in the form: Privo still checks the server and you review every tool.</p>}
    </section>
  );
}

function AddServer({ canOAuth, teams, ready }: { canOAuth: boolean; teams: Team[]; ready: boolean }) {
  const [authMode, setAuthMode] = useState("none");
  const [chosen, setChosen] = useState<string[]>(["everyone"]);
  const [busy, run] = useAction();

  const submit = (e: Event) => {
    const f = formValues(e);
    const alias = (f.alias ?? "").trim();
    const body: Record<string, unknown> = { alias, transport: f.transport, url: (f.url ?? "").trim() };
    if (teams.length > 0) body.teams = chosen;
    if (!ready) return;
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
      <h2>Add another server</h2>
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
        {teams.length > 0 && <TeamPicker teams={teams} chosen={chosen} onChange={setChosen} />}
        {!ready && <p class="meta">Loading teams…</p>}
        <div class="actions">
          <button type="submit" class="primary" disabled={busy || !ready}>
            {busy ? "Adding…" : "Add server"}
          </button>
        </div>
      </form>
    </section>
  );
}
