import { useState } from "preact/hooks";
import { api, seg } from "../api";
import type { PageProps } from "../app";
import { invalidate, useData } from "../data";
import type { Condition, Policy, ServerView } from "../types";
import { Copy, Empty, ErrorLine, Loading, formValues, useAction, when } from "../ui";

const opLabel: Record<string, string> = {
  equals: "is",
  not_equals: "is not",
  in: "is one of",
  matches: "matches",
  exists: "is given",
  missing: "is not given",
};

/** conditionText reads a condition as a person would say it. */
export function conditionText(c: Condition): string {
  const v = c.op === "exists" || c.op === "missing" ? "" : " " + (Array.isArray(c.value) ? c.value.map((x) => JSON.stringify(x)).join(", ") : JSON.stringify(c.value));
  return c.arg + " " + (opLabel[c.op] ?? c.op) + v;
}

/** parseValue reads what a person typed: JSON when it is JSON (numbers,
 * true, "quoted"), otherwise the text itself; for "is one of", a comma list. */
export function parseValue(op: string, raw: string): unknown {
  const one = (s: string): unknown => {
    const t = s.trim();
    try {
      return JSON.parse(t);
    } catch {
      return t;
    }
  };
  if (op === "exists" || op === "missing") return undefined;
  if (op === "in") return raw.split(",").map(one);
  if (op === "matches") return raw.trim();
  return one(raw);
}

export function subjectLabel(sub: string, person?: string): string {
  if (sub === "*") return "everyone";
  if (sub.startsWith("user:")) return person || "a removed member";
  if (sub.startsWith("role:")) return sub.slice(5) + "s";
  if (sub.startsWith("token:")) return "token " + sub.slice(6);
  return sub;
}

const actionLabel: Record<string, string> = { allow: "allow", ask: "ask for approval", deny: "deny" };

type Draft = { arg: string; op: string; value: string };

export function Rules(_: PageProps) {
  const [conds, setConds] = useState<Draft[]>([]);
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
        conditions: conds.map((c) => ({ arg: c.arg.trim(), op: c.op, value: parseValue(c.op, c.value) })),
      });
      form.reset();
      setConds([]);
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
                      {(r.conditions ?? []).map((c, i) => (
                        <div key={i}>
                          <small class="meta">when {conditionText(c)}</small>
                        </div>
                      ))}
                    </td>
                    <td>{subjectLabel(r.subject, r.subject_person)}</td>
                    <td>
                      <span class={"action " + r.action}>{actionLabel[r.action] ?? r.action}</span>
                    </td>
                    <td>
                      {r.grant_of && <small class="meta">grant from an approval: replaces an ask, never a deny. </small>}
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
              <option value="role:approver">approvers</option>
              <option value="role:auditor">auditors</option>
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
          <fieldset>
            <legend>Only when the call's arguments… (optional)</legend>
            {conds.map((c, i) => (
              <div class="row" key={i}>
                <input aria-label={"Argument " + (i + 1)} placeholder="argument, e.g. repo" value={c.arg} onInput={(e) => setConds(conds.map((x, j) => (j === i ? { ...x, arg: (e.currentTarget as HTMLInputElement).value } : x)))} />
                <select aria-label={"Test " + (i + 1)} value={c.op} onChange={(e) => setConds(conds.map((x, j) => (j === i ? { ...x, op: (e.currentTarget as HTMLSelectElement).value } : x)))}>
                  {Object.entries(opLabel).map(([op, label]) => (
                    <option key={op} value={op}>
                      {label}
                    </option>
                  ))}
                </select>
                {c.op !== "exists" && c.op !== "missing" && (
                  <input
                    aria-label={"Value " + (i + 1)}
                    placeholder={c.op === "in" ? "a, b, c" : c.op === "matches" ? "release/*" : "value"}
                    value={c.value}
                    onInput={(e) => setConds(conds.map((x, j) => (j === i ? { ...x, value: (e.currentTarget as HTMLInputElement).value } : x)))}
                  />
                )}
                <button type="button" class="link" onClick={() => setConds(conds.filter((_, j) => j !== i))}>
                  Remove
                </button>
              </div>
            ))}
            {conds.length < 8 && (
              <button type="button" class="link" onClick={() => setConds([...conds, { arg: "", op: "equals", value: "" }])}>
                Add a condition
              </button>
            )}
            <p class="meta">All conditions must hold. They read the arguments exactly as sent to the server: "1" and 1 differ. The tool's page lists its inputs.</p>
          </fieldset>
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
      <RulesFile />
    </>
  );
}

// RulesFile exports the rules as YAML and imports a file, after a dry run
// that says what would change (ADR 0024).
function RulesFile() {
  const [exported, setExported] = useState("");
  const [text, setText] = useState("");
  const [mode, setMode] = useState("merge");
  const [plan, setPlan] = useState<{ add: number; delete: number; default: string } | null>(null);
  const [busy, run] = useAction();
  const doExport = () =>
    void run(async () => {
      const out = await api<{ yaml: string }>("GET", "/v1/policies/export");
      setExported(out.yaml);
    }, "Exported. Keep the file where your team keeps configuration.");
  const check = () =>
    void run(async () => {
      setPlan(await api<{ add: number; delete: number; default: string }>("POST", "/v1/policies/import", { yaml: text, mode, dry_run: true }));
    }, "Checked. Nothing has changed yet.");
  const apply = () =>
    void run(async () => {
      await api("POST", "/v1/policies/import", { yaml: text, mode });
      setPlan(null);
      setText("");
      invalidate("/v1/policies");
    }, "Imported.");
  return (
    <section class="card">
      <h2>Rules as a file</h2>
      <p class="meta">Export the rules as YAML to keep them in Git, and import a file here. Grants from approvals are not part of the file.</p>
      <div class="actions">
        <button type="button" disabled={busy} onClick={doExport}>
          Export
        </button>
      </div>
      {exported && <Copy text={exported} label="rules file" />}
      <label class="stack">
        Import a rules file
        <textarea
          aria-label="Rules file"
          rows={8}
          value={text}
          onInput={(e) => {
            setText((e.currentTarget as HTMLTextAreaElement).value);
            setPlan(null);
          }}
        />
      </label>
      <label class="inline">
        <input type="radio" name="import-mode" checked={mode === "merge"} onChange={() => (setMode("merge"), setPlan(null))} /> Add these rules
      </label>
      <label class="inline">
        <input type="radio" name="import-mode" checked={mode === "replace"} onChange={() => (setMode("replace"), setPlan(null))} /> Replace all rules
        with these
      </label>
      {plan && (
        <p class="flash">
          This adds {plan.add} {plan.add === 1 ? "rule" : "rules"}
          {mode === "replace" && <> and deletes {plan.delete}</>}
          {plan.default && <>, and sets the default to {plan.default}</>}.
        </p>
      )}
      <div class="actions">
        <button type="button" disabled={busy || !text.trim()} onClick={check}>
          Check
        </button>
        <button type="button" class="primary" disabled={busy || !plan} onClick={apply}>
          Import
        </button>
      </div>
    </section>
  );
}
