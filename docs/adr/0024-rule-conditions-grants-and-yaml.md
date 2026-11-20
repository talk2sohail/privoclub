# 0024. Rules look at arguments; grants only replace an ask; rules as YAML

- Status: Proposed, 2026-11-20 (accept after the F09 review)
- Date: 2026-11-20
- Amends: 0001 (decision stage), 0005 (scoped grants)
- Related: 0002 (`policies.conditions` was planned), F10 (grants), F20

## Context

Rules choose by server, tool pattern and subject. Teams want rules about
*what* a call does, for example:

- "deny `delete_repo` when `repo` is `prod`";
- "ask before `create_payment` when `amount` is over a threshold";
- "allow `query` only on the `analytics` database".

Today this needs one tool per case, which servers do not offer.

F10 added grants: time-limited allow rules ranked just above the rule that
asked. With conditions, ranking alone stops being safe. Take a deny for
`repo = prod` at the same rank as an ask for everything. A grant made for a
`sandbox` call would outrank that deny for every later call, `prod`
included.

Admins also want to keep rules in Git and move them between installs.

## Decision

### Conditions

1. **A rule may carry conditions, all of which must hold** (AND) for it to
   match. A rule without conditions matches as before.
2. **What a condition is.** A condition names an argument by a dotted path
   of object keys (`repo`, `options.force`) and is one of:
   - `equals` or `not_equals` a JSON value;
   - `in` a list of up to 50 values;
   - `matches` a glob, on a string (the rules' own glob: no regular
     expressions, so nothing can take unbounded time);
   - `exists` or `missing`.

   Values compare as JSON, so `1` is not `"1"`.
3. **Fail-safe evaluation.** A value of another type, or a path that is
   absent, makes `equals`, `in` and `matches` false. `not_equals` is true
   when the value is absent or different. So a deny meant for `prod` cannot
   be dodged by sending `"prod"` as a different type: the canonical
   arguments are what the condition reads and what is sent upstream (ADR
   0001 stage 7).
4. **Prompts use their arguments.** Resources have none, so a condition on
   a resource rule holds only for `missing` and `not_equals`.
5. **Limits:**
   - at most 8 conditions per rule;
   - a path of at most 8 keys and 128 characters;
   - a value of at most 1 KiB of JSON.

### Grants

6. **A grant only replaces an ask.** A grant is an allow rule made from an
   approval; it is marked with the approval's id. The rules are evaluated
   without grants first, and a matching grant turns the result into allow
   only when that result is ask. A deny, by any rule at any rank, always
   stands. Grants no longer depend on rank.

### YAML

7. **Export and import.**
   - **Export:** the rules and the default action go out as YAML, through
     `GET /api/v1/policies/export`. Grants and expired rules are left out.
     A subject that names a team keeps its slug. A rule for one member
     carries their person name, and a rule for one token carries the token
     id (it moves only within an install).
   - **Import:** `POST /api/v1/policies/import` has two modes. `merge` adds
     the rules. `replace` deletes every rule that is not a grant and adds
     the file's rules. With `dry_run`, it reports what it would do and
     changes nothing.
   - **Checks:** each rule is checked as the API checks one. A team, member
     or server the install does not know refuses the whole import, which is
     applied in one transaction.
   - **Who:** only `policy.manage`.

## Consequences

- Rules can be as precise as the arguments of a call. The person writing
  them has to know the tool's argument names; the server page shows them
  (F11's review lists the inputs).
- Grants become safer and simpler: no ranking, and a deny is never
  outranked, whatever its conditions.
- A YAML file in Git is the source of truth for teams that want it. Privo
  keeps no history of rule changes beyond the audit log.
