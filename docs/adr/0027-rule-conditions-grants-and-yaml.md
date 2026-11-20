# 0027. Rules look at arguments; grants only replace an ask; rules as YAML

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
   - the same three on strings while ignoring case: `equals_any_case`,
     `in_any_case`, `matches_any_case`, for names a server reads without
     case;
   - `exists` or `missing`.

   Numbers compare by exact value at any length: `42.000…0` is `42`.
   Arguments refuse an exponent of more than 9 digits.
3. **Doubt fails closed** (amended after the security review of
   2026-11-20). Many servers read `"1"` as `1`, or `"yes"` as `true`, so an
   argument of another type than the rule's value is *unsure*, not a no. An
   unsure condition lets a deny or an ask apply, and keeps an allow from
   applying. So does a rule whose stored conditions cannot be read. A path
   that is absent is a plain no for `equals`, `in` and `matches`, and a yes
   for `not_equals` and `missing`. The canonical arguments are what the
   condition reads and what is sent upstream (ADR 0001 stage 7).
   Allow-lists (an allow with `in`, under a default of ask or deny) are the
   form that fails closed by construction; the documentation says so.
4. **Prompts use their arguments.** Resources have none, so a condition on
   a resource rule holds only for `missing` and `not_equals`.
5. **Limits:**
   - at most 8 conditions per rule;
   - a path of at most 8 keys and 128 characters;
   - a value of at most 1 KiB of JSON.

### Grants

6. **A grant only replaces an ask, and only up to its rank.** A grant is an
   allow rule made from an approval, marked with the approval's id. Its
   priority is the rank of the ask it was made over (`-1000` when the
   default asked). The rules are evaluated without grants first. A matching
   grant turns the result into allow only when that result is an ask of its
   rank or lower, or the default's. A deny, by any rule at any rank, always
   stands. A higher ask the approver never saw stays an ask (security review
   2026-11-20).

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
   - **Checks:** each rule is checked as the API checks one. A team or
     member the install does not know refuses the whole import, which is
     applied in one transaction. A server alias need not exist yet, as when
     creating a rule: the rule waits for the server. Rules for removed
     members are left out of an export.
   - **Who:** only `policy.manage`.

## Consequences

- Rules can be as precise as the arguments of a call. The person writing
  them has to know the tool's argument names; the server page shows them
  (F11's review lists the inputs).
- Grants become safer: a deny is never outranked, whatever its conditions,
  and a grant lifts only the asks up to the rank it was made over.
- A YAML file in Git is the source of truth for teams that want it. Privo
  keeps no history of rule changes beyond the audit log.
