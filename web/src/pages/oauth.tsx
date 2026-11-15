import { api, seg } from "../api";
import type { PageProps } from "../app";
import { invalidate, useData } from "../data";
import type { ConnectedClient, OAuthClient } from "../types";
import { Empty, ErrorLine, formValues, useAction, when } from "../ui";

type Connected = { clients: ConnectedClient[]; enabled: boolean };
type Rules = { enabled: boolean; policy: string; registration: boolean; clients: OAuthClient[] };

const kinds: Record<string, string> = {
  known: "on Privo’s list",
  approved: "approved here",
  document: "not listed",
  registered: "registered itself, not verified",
};

// SignedInClients is the member's own list: AI clients that signed in with
// OAuth and act as them. It is part of the Tokens page.
export function SignedInClients({ mcpURL }: { mcpURL: string }) {
  const mine = useData<Connected>("/v1/me/clients");
  const [busy, run] = useAction();
  if (mine.data && !mine.data.enabled) return null;
  const end = (c: ConnectedClient) =>
    void run(async () => {
      await api("DELETE", "/v1/me/clients/" + seg(c.id));
      invalidate("/v1/me/clients");
      invalidate("/v1/oauth/grants");
    }, `${c.client} can no longer act as you. It stops working within seconds.`);
  return (
    <section class="card">
      <h2>AI clients that signed in</h2>
      <p class="meta">
        A client that supports OAuth needs no token: give it <code>{mcpURL}</code> and it asks you here. It acts as you until you take the access away, for at
        most 90 days.
      </p>
      <ErrorLine text={mine.error} />
      {mine.data && mine.data.clients.length === 0 && <Empty>None yet.</Empty>}
      {mine.data && mine.data.clients.length > 0 && (
        <div class="list-wrap">
          <table class="list">
            <thead>
              <tr>
                <th>Client</th>
                <th>Allowed</th>
                <th>Last used</th>
                <th>Ends</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {mine.data.clients.map((c) => (
                <tr key={c.id}>
                  <td>
                    {c.client}
                    {c.kind !== "known" && <small class="meta"> ({kinds[c.kind] ?? c.kind})</small>}
                  </td>
                  <td>{when(c.allowed_at)}</td>
                  <td>{when(c.last_used_at)}</td>
                  <td>{when(c.expires_at)}</td>
                  <td>
                    <button type="button" class="link" disabled={busy} onClick={() => end(c)}>
                      Take access away
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

// OAuthClients is the administrators' page: which AI clients may ask a member
// for access, and which ones members have let in.
export function OAuthClients(_: PageProps) {
  const rules = useData<Rules>("/v1/oauth/clients");
  const grants = useData<Connected>("/v1/oauth/grants");
  const [busy, run] = useAction();

  const approve = (e: Event) => {
    const form = e.currentTarget as HTMLFormElement;
    const f = formValues(e);
    void run(async () => {
      const c = await api<OAuthClient>("POST", "/v1/oauth/clients", { client_id: (f.client_id ?? "").trim() });
      form.reset();
      invalidate("/v1/oauth/clients");
      return c;
    }, "Client approved. Members can now let it in.");
  };
  const remove = (c: OAuthClient) =>
    void run(async () => {
      await api("POST", "/v1/oauth/clients:remove", { client_id: c.client_id });
      invalidate("/v1/oauth/clients");
      invalidate("/v1/oauth/grants");
    }, c.kind === "known" ? `Every connection of ${c.name} was ended.` : `${c.name} was removed and its connections ended.`);
  const end = (c: ConnectedClient) =>
    void run(async () => {
      await api("DELETE", "/v1/oauth/grants/" + seg(c.id));
      invalidate("/v1/oauth/grants");
    }, `${c.client} can no longer act as ${c.person ?? "that member"}.`);

  const r = rules.data;
  return (
    <>
      <h1>Clients</h1>
      <ErrorLine text={rules.error} />
      {r && !r.enabled && (
        <section class="card">
          <h2>Signing in with OAuth is off</h2>
          <p>
            AI clients reach this Privo with a personal token from the Tokens page. To let them sign in with OAuth instead, set <code>PRIVO_OAUTH_SERVER=on</code>{" "}
            and restart Privo.
          </p>
        </section>
      )}
      {r && r.enabled && (
        <>
          <section class="card">
            <h2>Clients that may ask</h2>
            <p class="meta">
              {r.policy === "any"
                ? "Every client that publishes a description of itself may ask a member for access."
                : r.policy === "approved"
                  ? "Only the clients approved here may ask a member for access."
                  : "The clients on Privo’s list and the ones approved here may ask a member for access."}{" "}
              {r.registration ? "Clients may also register themselves; those are shown as not verified." : "Clients cannot register themselves."} A member still
              has to allow each client on a page that names it.
            </p>
            {r.clients.length === 0 && <Empty>None.</Empty>}
            {r.clients.length > 0 && (
              <div class="list-wrap">
                <table class="list">
                  <thead>
                    <tr>
                      <th>Client</th>
                      <th>How it is accepted</th>
                      <th>Connections</th>
                      <th />
                    </tr>
                  </thead>
                  <tbody>
                    {r.clients.map((c) => (
                      <tr key={c.client_id}>
                        <td>
                          {c.name || c.client_id}
                          <br />
                          <small class="meta">{c.client_id}</small>
                        </td>
                        <td>{kinds[c.kind] ?? c.kind}</td>
                        <td>{c.kind === "known" ? "" : c.grants}</td>
                        <td>
                          <button type="button" class="link" disabled={busy} onClick={() => remove(c)}>
                            {c.kind === "known" ? "End its connections" : "Remove"}
                          </button>
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
            <form class="row" onSubmit={approve}>
              <input
                name="client_id"
                type="url"
                required
                maxLength={2048}
                placeholder="https://client.example/oauth/client-metadata.json"
                aria-label="Address of the client's metadata document"
                autocomplete="off"
              />
              <button type="submit" class="primary" disabled={busy}>
                Approve client
              </button>
            </form>
            <p class="meta">Approve a client by the address of its metadata document. Privo reads the document now and refuses one it cannot use.</p>
          </section>
          <section class="card">
            <h2>Connected clients</h2>
            <ErrorLine text={grants.error} />
            {grants.data && grants.data.clients.length === 0 && <Empty>No member has let a client in.</Empty>}
            {grants.data && grants.data.clients.length > 0 && (
              <div class="list-wrap">
                <table class="list">
                  <thead>
                    <tr>
                      <th>Person</th>
                      <th>Client</th>
                      <th>Allowed</th>
                      <th>Last used</th>
                      <th />
                    </tr>
                  </thead>
                  <tbody>
                    {grants.data.clients.map((c) => (
                      <tr key={c.id}>
                        <td>{c.person}</td>
                        <td>
                          {c.client}
                          {c.kind !== "known" && <small class="meta"> ({kinds[c.kind] ?? c.kind})</small>}
                        </td>
                        <td>{when(c.allowed_at)}</td>
                        <td>{when(c.last_used_at)}</td>
                        <td>
                          <button type="button" class="link" disabled={busy} onClick={() => end(c)}>
                            Take access away
                          </button>
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </section>
        </>
      )}
    </>
  );
}
