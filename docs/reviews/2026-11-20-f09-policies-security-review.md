# Security review: F09, rules on arguments, grants and YAML (2026-11-20)

Scope: commit `5191fc9` (ADR 0024). A separate read-only review pass, run as
CLAUDE.md asks for every change to the request pipeline. The reviewer read
the code and ran one standalone check of the number comparison.

**Result: two Medium findings, both fixed in the next commit.**

## Findings

### 1. Medium (0.9): a long number made a deny or ask condition miss

- **What happened:** numbers over 64 characters, or with an exponent past
  ±400, were compared as text. An AI client could send `42.000…0` (70
  characters): "deny `transfer` when `account_id` is one of 42, 77" missed,
  and the server read 42.
- **Fixed:** numbers compare by exact value at any length, as a sign, the
  significant digits and an exponent, without expanding them. The arguments
  refuse an exponent of more than 9 digits.
- **Tests:** `TestConditions` (the 70-digit case), `TestUnsureFailsClosed`
  and `TestCanonicalRefusesHugeExponents`.

### 2. Medium (0.8): strict types and exact case let servers that coerce be reached around a rule

- **What happened:** a condition that was false for an argument of another
  type let `{"force": "yes"}`, `{"pr": "1"}` or `{"repo": "Prod"}` dodge a
  deny on servers that coerce types or ignore case. FastMCP's pydantic layer
  is one; GitHub names are another.
- **Fixed:** an argument of another type than the rule's value is *unsure*.
  An unsure deny or ask applies; an unsure allow does not. The new tests
  `equals_any_case`, `in_any_case` and `matches_any_case` cover names. The
  ADR and threat note no longer claim that shape cannot matter. The Rules
  page explains both.
- **Tests:** `TestConditions` and `TestUnsureFailsClosed`.

## Notes acted on

- **A grant lifted every ask** on its tool for its person, including a higher
  conditioned ask the approver never saw (0.6). A grant now covers asks up to
  the rank it was made over. Test: `TestGrantCoversItsRankOnly`.
- **Precision of big integers.** Rule values from the JSON API lost precision
  above 2^53. Conditions now read their values with exact numbers. Test:
  `TestConditionNumbersStayExact`.
- **Grants from before the migration** are marked from their note. Test:
  `TestMigrationMarksOldGrants`.
- **The dry run** now checks members as the import does.
- **Export** leaves out rules for removed members, which match nobody and
  would refuse the import.
- **ADR wording:** the ADR no longer says that an unknown server refuses an
  import. Rules may name a server that does not exist yet, as they can on
  create.

## Paths checked that held

- **Same bytes:** the policy and the upstream server see the same bytes for
  tools (and the same marshalled map for prompts).
- **Keys:** duplicate and escaped keys are handled by the canonical form.
- **Crafted arguments** cannot crash the gateway.
- **Grants:**
  - A grant never overrides a deny or the default deny.
  - Grants cannot be created through the API or import.
  - Replace keeps them.
- **Validation:** import shares create's checks; unknown fields are refused
  in JSON and in YAML.
- **`person:` names** resolve within the org and refuse removed members.
- **One transaction:** the import, including the default, is applied in one
  transaction.
- **Export** needs `policy.manage` and holds no secrets.
- **Tenancy** holds on every new query.
