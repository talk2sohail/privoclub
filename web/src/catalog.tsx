// Adding a server from the built-in catalog (ADR 0020). An entry only fills in
// the add-server request: the admin API still checks the address, finds the
// sign-in and lists every tool for review.

import { useState } from "preact/hooks";
import { api } from "./api";
import { invalidate } from "./data";
import { navigate } from "./router";
import type { CatalogEntry, Team } from "./types";
import { Copy, formValues, useAction } from "./ui";

export function signInLabel(e: CatalogEntry): string {
  if (e.auth === "none") return "no sign-in";
  return e.registration === "own_app" ? "own account · needs an OAuth app" : "own account";
}

/** TeamPicker: checkboxes for "everyone" and each team. */
export function TeamPicker({
  teams,
  chosen,
  onChange,
  legend = "Who can use it",
}: {
  teams: Team[];
  chosen: string[];
  onChange: (t: string[]) => void;
  legend?: string;
}) {
  const toggle = (slug: string, on: boolean) => onChange(on ? [...chosen, slug].sort() : chosen.filter((t) => t !== slug));
  return (
    <fieldset>
      <legend>{legend}</legend>
      {teams.map((t) => (
        <label class="inline" key={t.slug}>
          <input type="checkbox" checked={chosen.includes(t.slug)} onChange={(e) => toggle(t.slug, (e.currentTarget as HTMLInputElement).checked)} />{" "}
          {t.name}
          {t.built_in && <small class="meta"> (every member)</small>}
        </label>
      ))}
      {chosen.length === 0 && <p class="flash warn">Nobody will be able to use this server until you pick a team.</p>}
    </fieldset>
  );
}

export function CatalogAdd({
  entry,
  teams,
  initialTeams,
  callbackURL,
  oauthAvailable,
  onCancel,
  ready = true,
}: {
  entry: CatalogEntry;
  teams: Team[];
  initialTeams: string[];
  callbackURL: string;
  oauthAvailable: boolean;
  ready?: boolean;
  onCancel: () => void;
}) {
  const [chosen, setChosen] = useState<string[]>(initialTeams);
  const [busy, run] = useAction();
  const ownApp = entry.auth === "oauth" && entry.registration === "own_app";
  const blocked = entry.auth === "oauth" && !oauthAvailable;

  const submit = (e: Event) => {
    const f = formValues(e);
    const alias = (f.alias ?? "").trim();
    const body: Record<string, unknown> = { alias, transport: entry.transport, url: entry.url, teams: chosen };
    if (entry.auth === "oauth") {
      const oauth: Record<string, unknown> = {};
      const id = (f.client_id ?? "").trim();
      if (id) {
        oauth.client_id = id;
        oauth.client_secret = (f.client_secret ?? "").trim();
      }
      if (entry.scopes?.length) oauth.scopes = entry.scopes;
      body.oauth = oauth;
    }
    void run(async () => {
      await api("POST", "/v1/servers", body);
      invalidate("/v1/servers");
      invalidate("/v1/catalog");
      invalidate("/v1/teams");
      navigate("/admin/servers/" + alias);
    }, `${entry.name} added. Review its tools below; nothing is available to AI clients until you accept it.`);
  };

  return (
    <form class="stack catalog-add" onSubmit={submit} aria-label={"Add " + entry.name}>
      <label>
        Name in Privo <small class="meta">AI clients see its tools as name__tool</small>
        <input name="alias" required pattern="[a-z][a-z0-9\-]{1,15}" defaultValue={entry.alias} autocomplete="off" />
      </label>
      <p class="meta">
        <code>{entry.url}</code>
      </p>
      <TeamPicker teams={teams} chosen={chosen} onChange={setChosen} />
      {blocked && <p class="flash warn">Servers where each person uses their own account need the admin site's sign-in and PRIVO_PUBLIC_URL.</p>}
      {ownApp && (
        <fieldset>
          <legend>{entry.name} OAuth app</legend>
          <p>
            {entry.app_help}{" "}
            {entry.app_url && (
              <a href={entry.app_url} target="_blank" rel="noopener noreferrer">
                Open {entry.name}
              </a>
            )}
          </p>
          {callbackURL && <Copy text={callbackURL} label="callback URL" />}
          <label>
            Client ID <input name="client_id" required autocomplete="off" />
          </label>
          <label>
            Client secret <small class="meta">stored encrypted, never shown again</small>
            <input name="client_secret" type="password" autocomplete="off" />
          </label>
        </fieldset>
      )}
      {entry.auth === "oauth" && !ownApp && <p class="meta">Privo registers itself with {entry.name}. Each person connects their own account once.</p>}
      {entry.auth === "none" && <p class="meta">No sign-in: everyone who can use it shares it.</p>}
      <div class="actions">
        {!ready && <p class="meta">Loading teams…</p>}
        <button type="submit" class="primary" disabled={busy || blocked || !ready}>
          {busy ? "Adding…" : "Add " + entry.name}
        </button>
        <button type="button" class="link" onClick={onCancel}>
          Cancel
        </button>
      </div>
    </form>
  );
}
