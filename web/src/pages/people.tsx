import { useState } from "preact/hooks";
import { api, seg } from "../api";
import type { PageProps } from "../app";
import { invalidate, useData } from "../data";
import type { Member } from "../types";
import { RolePicker, roleText } from "../roles";
import { Copy, ErrorLine, Loading, formValues, useAction, when } from "../ui";

type Invite = { person: string; roles: string[]; url: string; expires_at: string };

// Only owners change owners and admins (ADR 0025); the API enforces it.
const elevated = (roles: string[]) => roles.includes("owner") || roles.includes("admin");

export function People({ session }: PageProps) {
  const res = useData<{ members: Member[] }>("/v1/members");
  const [invite, setInvite] = useState<Invite | null>(null);
  const [removing, setRemoving] = useState("");
  const [inviteRoles, setInviteRoles] = useState<string[]>(["member"]);
  const [editing, setEditing] = useState<{ person: string; roles: string[] } | null>(null);
  const ownerEditor = session.change_roles.includes("owner");
  const [busy, run] = useAction();
  const members = res.data?.members;

  const create = (e: Event) => {
    const form = e.currentTarget as HTMLFormElement;
    const f = formValues(e);
    const person = (f.person ?? "").trim();
    void run(async () => {
      const out = await api<{ invite: Invite }>("POST", "/v1/invites", { person, roles: inviteRoles });
      setInvite(out.invite);
      form.reset();
      setInviteRoles(["member"]);
    }, "Invite created. Send the link below to " + person + ".");
  };

  const saveRoles = (person: string, roles: string[]) =>
    void run(async () => {
      await api("PATCH", "/v1/members/" + seg(person), { roles });
      setEditing(null);
      invalidate("/v1/members");
    }, "Roles changed. They apply to " + person + "'s next request.");

  const restore = (person: string) =>
    void run(async () => {
      await api("POST", "/v1/members/" + seg(person) + "/restore");
      invalidate("/v1/members");
    }, person + " is back, with the roles they had. They sign in again and make new tokens.");

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
            <strong>{roleText(invite.roles)}</strong>. It is shown only now.
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
                  <th>Roles</th>
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
                            {roleText(m.roles)} <small class="meta">(you)</small>
                          </>
                        ) : editing?.person === m.person ? (
                          <div class="stack">
                            <RolePicker
                              legend={"Roles of " + m.person}
                              offered={session.change_roles}
                              value={editing.roles}
                              onChange={(roles) => setEditing({ person: m.person, roles })}
                            />
                            <span class="row">
                              <button type="button" class="primary small" disabled={busy || editing.roles.length === 0} onClick={() => saveRoles(m.person, editing.roles)}>
                                Save roles
                              </button>
                              <button type="button" class="link" onClick={() => setEditing(null)}>
                                Cancel
                              </button>
                            </span>
                          </div>
                        ) : (
                          <>
                            {roleText(m.roles)}{" "}
                            {(ownerEditor || !elevated(m.roles)) && (
                              <button type="button" class="link" aria-label={"Change roles of " + m.person} onClick={() => setEditing({ person: m.person, roles: m.roles })}>
                                change
                              </button>
                            )}
                          </>
                        )}
                      </td>
                      <td>
                        {m.removed && (ownerEditor || !elevated(m.roles)) && (
                          <button type="button" class="link" disabled={busy} onClick={() => restore(m.person)}>
                            Restore {m.person}
                          </button>
                        )}
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
        <p class="meta">
          Removing someone signs them out, deletes their connected accounts and revokes every token with their name. Restoring them brings back their account
          and roles, nothing else.
        </p>
      </section>
      <section class="card">
        <h2>Invite someone</h2>
        <form class="stack" onSubmit={create}>
          <label>
            Person <small class="meta">the name their tokens and approvals will show, for example ada</small>
            <input name="person" required maxLength={128} autocomplete="off" />
          </label>
          <RolePicker legend="Roles" offered={session.invite_roles} value={inviteRoles} onChange={setInviteRoles} />
          <p class="meta">A person can hold several of member, approver and auditor: a member who also approves decides other people's calls, never their own. Admin stands alone.</p>
          <div class="actions">
            <button type="submit" class="primary" disabled={busy || inviteRoles.length === 0}>
              Create invite link
            </button>
          </div>
        </form>
      </section>
    </>
  );
}
