// A person's roles (ADR 0025): owner or admin alone, or any of member,
// approver and auditor. The API decides; this only offers valid sets.

const alone = ["owner", "admin"];

const explain: Record<string, string> = {
  member: "uses tools through their AI client",
  approver: "decides other people's approvals",
  auditor: "reads the activity",
  admin: "manages everything except owners",
  owner: "everything",
};

/** pickRole applies one tick to a set: owner and admin stand alone. */
export function pickRole(current: string[], role: string, on: boolean): string[] {
  if (!on) return current.filter((r) => r !== role);
  if (alone.includes(role)) return [role];
  return [...current.filter((r) => !alone.includes(r) && r !== role), role].sort();
}

export function roleText(roles: string[]): string {
  return roles.length ? roles.join(", ") : "no role";
}

export function RolePicker({
  offered,
  value,
  onChange,
  legend,
}: {
  offered: string[];
  value: string[];
  onChange: (roles: string[]) => void;
  legend: string;
}) {
  return (
    <fieldset>
      <legend>{legend}</legend>
      {offered.map((r) => (
        <label class="inline" key={r}>
          <input type="checkbox" checked={value.includes(r)} onChange={(e) => onChange(pickRole(value, r, (e.currentTarget as HTMLInputElement).checked))} />{" "}
          {r} <small class="meta">{explain[r] ?? ""}</small>
        </label>
      ))}
      {value.length === 0 && <p class="flash warn">Pick at least one role.</p>}
    </fieldset>
  );
}
