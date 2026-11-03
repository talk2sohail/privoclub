import { useState } from "preact/hooks";
import { api, seg } from "../api";
import type { PageProps } from "../app";
import { invalidate, useData } from "../data";
import type { Member } from "../types";
import { Copy, ErrorLine, Loading, formValues, useAction, when } from "../ui";

type Invite = { person: string; role: string; url: string; expires_at: string };

export function People({ session }: PageProps) {
  const res = useData<{ members: Member[] }>("/v1/members");
  const [invite, setInvite] = useState<Invite | null>(null);
  const [removing, setRemoving] = useState("");
  const [busy, run] = useAction();
  const members = res.data?.members;

  const create = (e: Event) => {
    const form = e.currentTarget as HTMLFormElement;
    const f = formValues(e);
    const person = (f.person ?? "").trim();
    void run(async () => {
      const out = await api<{ invite: Invite }>("POST", "/v1/invites", { person, role: f.role });
      setInvite(out.invite);
      form.reset();
    }, "Invite created. Send the link below to " + person + ".");
  };

  const setRole = (person: string, role: string) =>
    void run(async () => {
      await api("PATCH", "/v1/members/" + seg(person), { role });
      invalidate("/v1/members");
    }, "Role changed.");

  const remove = (person: string) =>
    void run(async () => {
      await api("POST", "/v1/members/" + seg(person) + "/remove");
      setRemoving("");
      invalidate("/v1/members");
      invalidate("/v1/tokens");
    }, "Removed " + person + ". Their tokens stop working within seconds.");

  return (
    <>
      <h1>People</h1>
      {invite && (
        <section class="card highlight">
          <h2>Invite link for {invite.person}</h2>
          <p>
            Send this link to {invite.person}. It works once, until {when(invite.expires_at)}, and makes the GitHub account they sign in with a{" "}
            <strong>{invite.role}</strong>. It is shown only now.
          </p>
          <Copy text={invite.url} label="invite link" />
        </section>
      )}
      <section class="card">
        <h2>Members</h2>
        <ErrorLine text={res.error} />
        {!members && res.loading && <Loading what="members" />}
        {members && (
          <div class="list-wrap">
            <table class="list">
              <thead>
                <tr>
                  <th>Person</th>
                  <th>GitHub</th>
                  <th>Role</th>
                  <th />
                </tr>
              </thead>
              <tbody>
                {members.map((m) => {
                  const me = m.person === session.user.person;
                  return (
                    <tr key={m.person} class={m.removed ? "muted" : undefined}>
                      <td>
                        <strong>{m.person}</strong>
                        {m.name && m.name !== m.person && (
                          <>
                            <br />
                            <small class="meta">{m.name}</small>
                          </>
                        )}
                      </td>
                      <td>{m.github_login}</td>
                      <td>
                        {m.removed ? (
                          "removed"
                        ) : me ? (
                          <>
                            {m.role} <small class="meta">(you)</small>
                          </>
                        ) : (
                          <select
                            aria-label={"Role of " + m.person}
                            value={m.role}
                            disabled={busy}
                            onChange={(e) => setRole(m.person, (e.currentTarget as HTMLSelectElement).value)}
                          >
                            {(session.change_roles.includes(m.role) ? session.change_roles : [m.role, ...session.change_roles]).map((r) => (
                              <option key={r} value={r}>
                                {r}
                              </option>
                            ))}
                          </select>
                        )}
                      </td>
                      <td>
                        {!m.removed &&
                          !me &&
                          (removing === m.person ? (
                            <span class="row">
                              <button type="button" class="deny" disabled={busy} onClick={() => remove(m.person)}>
                                Remove {m.person}
                              </button>
                              <button type="button" class="link" onClick={() => setRemoving("")}>
                                Cancel
                              </button>
                            </span>
                          ) : (
                            <button type="button" class="link" onClick={() => setRemoving(m.person)}>
                              Remove…
                            </button>
                          ))}
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        )}
        <p class="meta">Removing someone signs them out, deletes their connected accounts and revokes every token with their name.</p>
      </section>
      <section class="card">
        <h2>Invite someone</h2>
        <form class="stack" onSubmit={create}>
          <label>
            Person <small class="meta">the name their tokens and approvals will show, for example ada</small>
            <input name="person" required maxLength={128} autocomplete="off" />
          </label>
          <label>
            Role
            <select name="role">
              {session.invite_roles.map((r) => (
                <option key={r} value={r}>
                  {r}
                </option>
              ))}
            </select>
          </label>
          <p class="meta">
            Members use tools through their AI clients. Approvers decide approvals. Auditors read the activity. Admins manage everything except owners.
          </p>
          <div class="actions">
            <button type="submit" class="primary" disabled={busy}>
              Create invite link
            </button>
          </div>
        </form>
      </section>
    </>
  );
}
