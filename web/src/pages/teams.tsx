// Teams (ADR 0020): who is in which team, and which servers each team uses.
// A team made from a profile is shown the catalog's servers for it.

import { useState } from "preact/hooks";
import { api, seg } from "../api";
import type { PageProps } from "../app";
import { invalidate, useData } from "../data";
import { Link } from "../router";
import type { Member, Profile, ServerView, Team } from "../types";
import { Empty, ErrorLine, Loading, formValues, useAction } from "../ui";
import { FromCatalog } from "./servers";

type TeamsAnswer = { teams: Team[]; profiles: Profile[] };

function refresh() {
  invalidate("/v1/teams");
  invalidate("/v1/servers");
  invalidate("/v1/catalog");
}

export function Teams({ session }: PageProps) {
  const data = useData<TeamsAnswer>("/v1/teams");
  const members = useData<{ members: Member[] }>("/v1/members");
  const servers = useData<{ servers: ServerView[] }>(session.can.servers ? "/v1/servers" : null);
  const [open, setOpen] = useState("");
  const teams = data.data?.teams;
  const profiles = (data.data?.profiles ?? []).filter((p) => p.id !== "everyone");

  return (
    <>
      <h1>Teams</h1>
      <p class="meta">
        Each team sees the servers given to it, and nothing else. Everyone is built in: every member is in it, so its servers are for the whole
        organisation.
      </p>
      <ErrorLine text={data.error} />
      {!teams && data.loading && <Loading what="teams" />}
      {teams && (
        <div class="list-wrap">
          <table class="list">
            <thead>
              <tr>
                <th>Team</th>
                <th>People</th>
                <th>Servers</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {teams.map((t) => (
                <tr key={t.slug} class={open === t.slug ? "current" : undefined}>
                  <td>
                    <strong>{t.name}</strong> <small class="meta">{t.slug}</small>
                    {t.description && (
                      <>
                        <br />
                        <small class="meta">{t.description}</small>
                      </>
                    )}
                  </td>
                  <td>{t.built_in ? "every member" : t.members.length}</td>
                  <td>{t.servers.length === 0 ? <span class="meta">none</span> : t.servers.join(", ")}</td>
                  <td>
                    <button type="button" class="small" onClick={() => setOpen(open === t.slug ? "" : t.slug)}>
                      {open === t.slug ? "Close" : "Open"}
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      {teams?.map(
        (t) =>
          open === t.slug && (
            <TeamDetail
              key={t.slug}
              team={t}
              teams={teams}
              members={(members.data?.members ?? []).filter((m) => !m.removed)}
              servers={servers.data?.servers ?? []}
              canServers={session.can.servers}
              onDeleted={() => setOpen("")}
            />
          ),
      )}
      <NewTeam profiles={profiles} onCreated={(slug) => setOpen(slug)} />
    </>
  );
}

function NewTeam({ profiles, onCreated }: { profiles: Profile[]; onCreated: (slug: string) => void }) {
  const [busy, run] = useAction();
  const [profile, setProfile] = useState("");
  const [name, setName] = useState("");
  const pick = (id: string) => {
    setProfile(id);
    const p = profiles.find((x) => x.id === id);
    if (p && (name === "" || profiles.some((x) => x.name === name))) setName(p.name);
  };
  const submit = (e: Event) => {
    const f = formValues(e);
    void run(async () => {
      const out = await api<{ team: Team }>("POST", "/v1/teams", { name: name.trim(), description: (f.description ?? "").trim(), profile });
      setName("");
      setProfile("");
      refresh();
      onCreated(out.team.slug);
    }, "Team created. Add its people and servers below.");
  };
  return (
    <section class="card">
      <h2>New team</h2>
      <form class="stack" onSubmit={submit}>
        <fieldset>
          <legend>Start from</legend>
          <label class="inline">
            <input type="radio" name="profile" checked={profile === ""} onChange={() => pick("")} /> A blank team
          </label>
          {profiles.map((p) => (
            <label class="inline" key={p.id}>
              <input type="radio" name="profile" checked={profile === p.id} onChange={() => pick(p.id)} /> {p.name}{" "}
              <small class="meta">{p.description}</small>
            </label>
          ))}
        </fieldset>
        <label>
          Name
          <input name="name" required maxLength={64} value={name} onInput={(e) => setName((e.currentTarget as HTMLInputElement).value)} autocomplete="off" />
        </label>
        <label>
          Description <small class="meta">optional</small>
          <input name="description" maxLength={280} autocomplete="off" />
        </label>
        <div class="actions">
          <button type="submit" class="primary" disabled={busy}>
            Create team
          </button>
        </div>
      </form>
    </section>
  );
}

function TeamDetail({
  team,
  teams,
  members,
  servers,
  canServers,
  onDeleted,
}: {
  team: Team;
  teams: Team[];
  members: Member[];
  servers: ServerView[];
  canServers: boolean;
  onDeleted: () => void;
}) {
  const [busy, run] = useAction();
  const [confirm, setConfirm] = useState("");
  const base = "/v1/teams/" + seg(team.slug);
  const notIn = members.filter((m) => !team.members.some((x) => x.person === m.person));
  const otherServers = servers.filter((s) => !team.servers.includes(s.alias));

  const addMember = (e: Event) => {
    const f = formValues(e);
    if (!f.person) return;
    void run(async () => {
      await api("POST", base + "/members", { person: f.person });
      refresh();
    }, `Added ${f.person} to ${team.name}.`);
  };
  const removeMember = (person: string) =>
    void run(async () => {
      await api("DELETE", base + "/members/" + seg(person));
      refresh();
    }, `Took ${person} out of ${team.name}.`);
  const addServer = (e: Event) => {
    const f = formValues(e);
    if (!f.alias) return;
    void run(async () => {
      await api("POST", base + "/servers", { alias: f.alias });
      refresh();
    }, `${team.name} can now use ${f.alias}.`);
  };
  const removeServer = (alias: string) =>
    void run(async () => {
      await api("DELETE", base + "/servers/" + seg(alias));
      refresh();
    }, `${team.name} no longer uses ${alias}.`);
  const remove = (e: Event) => {
    e.preventDefault();
    void run(async () => {
      await api("DELETE", base);
      refresh();
      onDeleted();
    }, `Deleted ${team.name}.`);
  };

  return (
    <section class="card highlight" aria-label={team.name}>
      <h2>{team.name}</h2>
      {!team.built_in && (
        <>
          <h3>People</h3>
          {team.members.length === 0 && <Empty>Nobody yet.</Empty>}
          <ul class="chips">
            {team.members.map((m) => (
              <li key={m.person}>
                {m.name} <small class="meta">{m.person}</small>{" "}
                <button type="button" class="link" disabled={busy} aria-label={"Remove " + m.person} onClick={() => removeMember(m.person)}>
                  ×
                </button>
              </li>
            ))}
          </ul>
          {notIn.length > 0 ? (
            <form class="row" onSubmit={addMember}>
              <select name="person" aria-label="Person to add">
                {notIn.map((m) => (
                  <option key={m.person} value={m.person}>
                    {m.name} ({m.person})
                  </option>
                ))}
              </select>
              <button type="submit" disabled={busy}>
                Add to team
              </button>
            </form>
          ) : (
            <p class="meta">
              Everyone is in this team already. Invite more people on <Link href="/admin/people">People</Link>.
            </p>
          )}
        </>
      )}

      {canServers && (
        <>
          <h3>Servers</h3>
          {team.servers.length === 0 && <Empty>No servers yet.</Empty>}
          <ul class="chips">
            {team.servers.map((alias) => (
              <li key={alias}>
                <Link href={"/admin/servers/" + alias}>{alias}</Link>{" "}
                <button type="button" class="link" disabled={busy} aria-label={"Stop " + team.name + " using " + alias} onClick={() => removeServer(alias)}>
                  ×
                </button>
              </li>
            ))}
          </ul>
          {otherServers.length > 0 && (
            <form class="row" onSubmit={addServer}>
              <select name="alias" aria-label="Server to give this team">
                {otherServers.map((s) => (
                  <option key={s.alias} value={s.alias}>
                    {s.alias}
                  </option>
                ))}
              </select>
              <button type="submit" disabled={busy}>
                Give access
              </button>
            </form>
          )}
          {team.profile && <FromCatalog teams={teams} profile={team.profile} initialTeams={[team.slug]} />}
        </>
      )}

      {!team.built_in && (
        <form class="stack danger-zone" onSubmit={remove}>
          <label>
            <span>
              Delete this team and the rules that name it. Its people keep their other teams. Type <code>{team.slug}</code> to confirm:
            </span>
            <input autocomplete="off" value={confirm} onInput={(e) => setConfirm((e.currentTarget as HTMLInputElement).value)} />
          </label>
          <div class="actions">
            <button type="submit" class="deny" disabled={busy || confirm !== team.slug}>
              Delete team
            </button>
          </div>
        </form>
      )}
    </section>
  );
}
