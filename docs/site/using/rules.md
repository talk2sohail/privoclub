# Rules, approvals and grants

A rule says what happens to a call: **allow**, **ask** (hold it for a
person's approval) or **deny**. Rules are on the admin site under **Rules**.
A rule's subject is everyone (`*`), a role (`role:<role>`), a team
(`team:<slug>`), one member (`user:<id>`) or one token (`token:<id>`).

## Rules on arguments

0.15.0, [ADR 0027](https://github.com/talk2sohail/privoclub/blob/main/docs/adr/0027-rule-conditions-grants-and-yaml.md).
A rule can look at what a call does: **Rules → Add a rule → Only when the
call's arguments…**, for example deny `delete_repo` when `repo` is `prod`, or
ask before `create_payment` when `amount` is one of a list.

- Tests are `equals`, `not_equals`, `in`, `matches` (a pattern like
  `release/*`), `exists` and `missing`, on a dotted path (`options.force`).
- All of a rule's tests must hold.
- They read the arguments exactly as sent to the server, and `"1"` is not
  `1`.

## Approvals

When a rule says ask, the call waits up to `PRIVO_APPROVAL_HOLD` (25 seconds
by default) for a decision
([ADR 0005](https://github.com/talk2sohail/privoclub/blob/main/docs/adr/0005-approvals.md)).

- `/admin/approvals` lists the calls waiting for a decision, with their
  arguments. Owners, admins and approvers can approve or deny, in the browser
  with their GitHub account.
- Nobody approves their own call: self-approval is refused between tokens and
  sessions of the same person.
- **History** lists decided requests.
- A person sees their own waiting requests on **Start** and can withdraw one.

## Grants

Approving can also allow the same tool for that person for 15 minutes, 1 hour
or 8 hours: a grant, which is a rule with that expiry, shown on **Rules**.

A grant from an approval only ever turns an "ask" into "allow", never a deny:
it never outranks a deny that already won.

## Rules as a file

**Rules as a file** exports every rule and the default as YAML, and imports a
file after a check that says what would change. `merge` adds; `replace`
replaces everything but grants. One bad rule refuses the whole file.

API: `GET /api/v1/policies/export`, `POST /api/v1/policies/import` with
`{"yaml": "...", "mode": "merge", "dry_run": true}`.
