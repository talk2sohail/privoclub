import { api, seg } from "../api";
import type { PageProps } from "../app";
import { invalidate, useData } from "../data";
import type { Policy, ServerView } from "../types";
import { Empty, ErrorLine, Loading, formValues, useAction, when } from "../ui";

export function subjectLabel(sub: string): string {
  if (sub === "*") return "everyone";
  if (sub.startsWith("role:")) return sub.slice(5) + "s";
  if (sub.startsWith("token:")) return "token " + sub.slice(6);
  return sub;
}

const actionLabel: Record<string, string> = { allow: "allow", ask: "ask for approval", deny: "deny" };

export function Rules(_: PageProps) {
  const res = useData<{ policies: Policy[]; default: string }>("/v1/policies");
  const servers = useData<{ servers: ServerView[] }>("/v1/servers");
  const [busy, run] = useAction();
  const rules = res.data?.policies;

  const setDefault = (e: Event) => {
    const f = formValues(e);
    void run(async () => {
      await api("PATCH", "/v1/settings/policy", { default: f.default });
      invalidate("/v1/policies");
    }, "Default saved.");
  };

  const add = (e: Event) => {
    const form = e.currentTarget as HTMLFormElement;
    const f = formValues(e);
    void run(async () => {
      await api("POST", "/v1/policies", {
        server: f.server,
        tool: (f.tool ?? "").trim() || "*",
        subject: f.subject,
        action: f.action,
        note: (f.note ?? "").trim(),
      });
      form.reset();
      invalidate("/v1/policies");
    }, "Rule added.");
  };

  const remove = (id: string) =>
    void run(async () => {
      await api("DELETE", "/v1/policies/" + seg(id));
      invalidate("/v1/policies");
    }, "Rule deleted.");

  return (
    <>
      <h1>Rules</h1>
      <p class="meta">
        Rules decide what happens when an AI client calls a tool: <strong>allow</strong> it, <strong>ask</strong> a person to approve it first, or{" "}
        <strong>deny</strong> it. The most specific matching rule wins.
      </p>
      <ErrorLine text={res.error} />
      <section class="card">
        <h2>When no rule matches</h2>
        {res.data && (
          <form class="row" onSubmit={setDefault} key={res.data.default}>
            <select name="default" aria-label="Default action" defaultValue={res.data.default}>
              <option value="allow">allow</option>
              <option value="ask">ask for approval</option>
              <option value="deny">deny</option>
            </select>
            <button type="submit" disabled={busy}>
              Save
            </button>
          </form>
        )}
      </section>
      <section class="card">
        <h2>Rules</h2>
        {!rules && res.loading && <Loading what="rules" />}
        {rules && rules.length === 0 && <Empty>No rules yet: every call gets the default above.</Empty>}
        {rules && rules.length > 0 && (
          <div class="list-wrap">
            <table class="list">
              <thead>
                <tr>
                  <th>Server</th>
                  <th>Tool</th>
                  <th>Who</th>
                  <th>Action</th>
                  <th>Note</th>
                  <th />
                </tr>
              </thead>
              <tbody>
                {rules.map((r) => (
                  <tr key={r.id}>
                    <td>{r.server === "*" ? "every server" : r.server}</td>
                    <td>
                      <code>{r.tool}</code>
                    </td>
                    <td>{subjectLabel(r.subject)}</td>
                    <td>
                      <span class={"action " + r.action}>{actionLabel[r.action] ?? r.action}</span>
                    </td>
                    <td>
                      {r.note}
                      {r.expires_at && <small class="meta"> until {when(r.expires_at)}</small>}
                    </td>
                    <td>
                      <button type="button" class="link" disabled={busy} onClick={() => remove(r.id)}>
                        Delete
                      </button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </section>
      <section class="card">
        <h2>Add a rule</h2>
        <form class="stack" onSubmit={add}>
          <label>
            Server
            <select name="server">
              <option value="*">every server</option>
              {(servers.data?.servers ?? []).map((s) => (
                <option key={s.alias} value={s.alias}>
                  {s.alias}
                </option>
              ))}
            </select>
          </label>
          <label>
            Tool <small class="meta">a name as the server calls it, or * for all, or a pattern like delete_*</small>
            <input name="tool" defaultValue="*" autocomplete="off" />
          </label>
          <label>
            Who
            <select name="subject">
              <option value="*">everyone</option>
              <option value="role:member">members</option>
              <option value="role:admin">admins</option>
              <option value="role:owner">owners</option>
            </select>
          </label>
          <label>
            Action
            <select name="action">
              <option value="ask">ask for approval</option>
              <option value="deny">deny</option>
              <option value="allow">allow</option>
            </select>
          </label>
          <label>
            Note <small class="meta">optional, why this rule exists</small>
            <input name="note" maxLength={500} />
          </label>
          <div class="actions">
            <button type="submit" class="primary" disabled={busy}>
              Add rule
            </button>
          </div>
        </form>
      </section>
    </>
  );
}
