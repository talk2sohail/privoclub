# Arguments checked against the schema

0.20.0, [ADR 0001](https://github.com/talk2sohail/privoclub/blob/main/docs/adr/0001-request-pipeline.md)
(stage 5). Every tool call's arguments are checked against the `inputSchema` of
the tool definition you accepted, before [rules and approvals](rules.md).

## What happens on a mismatch

A call whose arguments do not match the schema — a wrong type, a missing
required field, or an extra field where `additionalProperties: false` forbids
it — is refused as a tool error with a short reason the AI client can act on.

- Nothing is sent to the upstream server.
- No approval request is created, even when a rule would ask for approval.
- [Activity](activity.md) and [the audit trail](audit.md) record the call as
  `refused:invalid_arguments`, without the arguments or the error reason.

## Supported schemas and limits

Draft 2020-12 and draft-07 JSON Schemas work, including local `$ref`
references (`$defs` and `definitions`). A tool with no `inputSchema` accepts
any JSON object.

Privo treats every upstream schema as untrusted input and fails closed:

- **No remote fetches.** Privo never fetches a `$ref` from another URL. A tool
  whose schema refers to an external document, uses an unsupported draft, or
  cannot be parsed is flagged with a warning in the server's tool review and
  its calls are refused (`refused:schema_unreadable`), with a warning on
  **Activity → Log**.
- **Bounded work.** Schemas are capped at 64 KiB and 64 nesting levels before
  decoding, and validation runs within a fixed step budget (1,000,000 units per
  call, 1,000 nesting frames). A schema with a `$ref` loop or exponential
  expansion is flagged at review time (`refused:schema_unreadable`); arguments
  too costly to check within the budget are refused
  (`refused:arguments_too_costly`).
- **Numbers.** Schema validation checks numbers as 64-bit floats; exact
  numeric comparison at any precision stays with [rules on arguments](rules.md#rules-on-arguments).

!!! note
    Tool definitions accepted before 0.20.0 are checked on their first call
    after upgrading and fail closed the same way if their schema cannot be
    read.
