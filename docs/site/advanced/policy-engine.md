# Conditional rules, grants and YAML

The everyday use of rules and approvals is in
[Rules and approvals](../basics/rules-and-approvals.md). This page covers the
rest: which rule wins, rules for a team or one person, conditions on a call's
arguments, grants, presets, and rules kept as a file.

Rules on arguments, grants that only ever replace an ask, and rules as a file
arrived in 0.15.0
([ADR 0027](https://github.com/talk2sohail/privoclub/blob/main/docs/adr/0027-rule-conditions-grants-and-yaml.md)).

## Which rule wins

1. Privo collects every rule that matches the call: its server, its tool,
   who is calling, and its conditions.
2. The rule with the highest **priority** wins. Rules added on the **Rules**
   page have priority 0; the admin API and the rules file can set any
   priority from -1000 to 1000.
3. Among matching rules of the same priority, the strictest wins: deny beats
   ask, and ask beats allow.
4. When no rule matches, the organisation's default applies. Under the
   default **allow**, a tool whose reviewed definition declares itself
   destructive (`destructiveHint`) still asks first: a tool's own
   annotations can tighten a decision, never relax one.

So an allow rule cannot carve an exception out of an ask or a deny that also
matches, unless it has a higher priority.

## Who a rule is for

| Subject | Matches |
| --- | --- |
| `*` | Everyone, including tokens that belong to nobody |
| `role:<role>` | People who hold that role, such as `role:approver` |
| `team:<slug>` | The members of one team |
| `user:<id>` | One member; the rules file writes `person:<name>` |
| `token:<id>` | One token |

The **Rules** page offers everyone and the roles; rules for a team, a person
or a token are written in the rules file or with the admin API. Deleting a
team deletes the rules that name it. A rule for a member who was removed stays
in the list as "a removed member".

## Conditions on a call's arguments

On **Rules → Add a rule**, under **Only when the call's arguments…**, choose
**Add a condition** and give an argument, a test and a value. For example:
deny `delete_repo` when `repo` is `prod`, or ask before `create_payment` when
`amount` is one of a list. Before any rule, the arguments are checked against
the tool's schema ([Arguments checked against the schema](arguments.md)).

| On the page | In the file | Holds when the argument |
| --- | --- | --- |
| is | `equals` | equals the value |
| is not | `not_equals` | differs from it |
| is one of | `in` | equals one of a list, written `a, b, c` |
| matches | `matches` | matches a pattern such as `release/*` |
| is given | `exists` | is present in the call |
| is not given | `missing` | is absent |

`equals`, `in` and `matches` also come in "any case" forms
(`equals_any_case`, `in_any_case`, `matches_any_case`) for names a server
reads without case, such as a repository or a host.

- The argument is a dotted path into the call's arguments, such as
  `options.force`.
- All of a rule's conditions must hold.
- Conditions read the arguments exactly as sent to the server, so `"1"` is
  not `1`. When an argument has another type than your value, a deny or an
  ask still applies, and an allow does not, since many servers read them
  alike.
- The server's page lists each tool's inputs.

## Grants

When an approver allows "this tool for" 15 minutes, 1 hour or 8 hours,
Privo adds a grant: a rule for that person and tool, shown on **Rules** with
its end time and marked as a grant from an approval.

- A grant only ever turns an ask into allow; it never outranks a deny.
- It lifts only an ask of the same priority as the rule it was made over, or
  lower, or the default's: never a higher-priority ask the approver did not
  see.
- Grants are not part of the rules file.

## Approval settings

- A call waits up to `PRIVO_APPROVAL_HOLD` for a decision (see the
  [configuration reference](../installation/configuration.md)). A request
  stays open for 10 minutes; its end time is shown on it.
- By default nobody approves their own calls: self-approval is refused
  between the tokens and sessions of the same person. For an install with a
  single person, `PATCH /api/v1/settings/approvals` with
  `{"allow_self": true}` lets people approve their own calls
  ([ADR 0005](https://github.com/talk2sohail/privoclub/blob/main/docs/adr/0005-approvals.md)).

## Presets

`POST /api/v1/servers/<alias>/policy:preset` writes one rule per reviewed
tool of a server, from the tools' own annotations:

- `{"preset": "read-only"}` allows the tools that declare themselves
  read-only and denies the rest;
- `{"preset": "ask-before-writes"}` allows the read-only tools and asks for
  the rest.

The server must be connected and have reviewed tools. Tools added later fall
to the default. A preset trusts the annotations in definitions you reviewed.

## Annotation overrides

Upstream tool annotations (`readOnlyHint`, `destructiveHint`, `openWorldHint`)
are untrusted claims from the server. On a server's page under
[Servers](../basics/servers.md), an owner or administrator can override any of
those three hints on a tool pin — either while reviewing a pending tool or on an
accepted tool — or reset a hint back to **Upstream default**.

- Overrides are bound to the exact pinned definition hash (`kind = "tool"`,
  `name`, `hash`). If a server changes a tool's definition, the new version
  starts without inherited overrides.
- AI clients receive the effective (overridden) annotations from `tools/list`.
- Policy evaluation (tightening an `allow` default when `destructiveHint` is
  `true`), approval grants, and server presets (`read-only`,
  `ask-before-writes`) read the effective annotations. Explicit rules and
  `ask`/`deny` org defaults still cannot be relaxed by annotations.
- The server's original `annotations` remain visible alongside
  `annotation_overrides`, and every change is recorded in
  [the audit trail](audit.md) (`pin.annotations:<alias>:<tool>`).

API: `PATCH /api/v1/servers/{alias}/pins/annotations` with
`{"name": "...", "hash": "...", "readOnlyHint": true|false|null, "destructiveHint": true|false|null, "openWorldHint": true|false|null}`
(`null` clears that field back to the upstream server's value; omitted fields
keep their current override).

## Rules as a file

At the bottom of **Rules**, under **Rules as a file**:

- **Export** gives every rule and the default as YAML, to keep in Git. Grants
  and expired rules are left out, and a rule for one member names the person
  (`person:<name>`), so the file reads well and moves between installs.
- **Import a rules file**: paste the YAML and choose **Add these rules**
  (merge) or **Replace all rules** (replace everything but grants). Choose
  **Check** to see what would be added and deleted, with nothing changed yet,
  then **Import**. One bad rule, an unknown team or an unknown person refuses
  the whole file.

A file looks like this:

```yaml
version: 1
default: ask
rules:
  - server: github
    tool: merge_pull_request
    subject: team:payments
    action: ask
    note: a second pair of eyes on payments
    when:
      - arg: repo
        op: equals
        value: payments-api
  - server: supabase
    tool: execute_sql
    subject: "*"
    action: deny
    when:
      - arg: project_id
        op: equals
        value: prod-payments
  - server: github
    tool: "*"
    subject: person:ada
    action: allow
    priority: 10
```

Each rule takes `server`, `tool`, `subject`, `action`, and optionally
`priority`, `note`, `expires_at` and `when`. A file holds at most 2,000 rules
and 1 MiB.

With the admin API: `GET /api/v1/policies/export`, and
`POST /api/v1/policies/import` with
`{"yaml": "...", "mode": "merge", "dry_run": true}`.
