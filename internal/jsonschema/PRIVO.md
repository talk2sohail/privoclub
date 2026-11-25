# Vendored jsonschema-go (Privo)

A copy of `github.com/google/jsonschema-go/jsonschema` at **v0.4.3** (MIT,
`LICENSE`), used only by `internal/argcheck` to check tool-call arguments
against an upstream server's input schema (F02). The Go MCP SDK keeps using
the module itself.

Why a copy: schemas come from upstream servers, which are untrusted, and the
library validates by plain recursion with no limit and no way to stop it.
The security review of 2026-11-22 (`docs/todo.md`, F02) found that a `$ref`
loop overflowed the stack (a fatal error that no `recover()` catches, ending
the process for every tenant), that references sharing their targets cost
2^n steps, and that single steps could be costly (a large `enum`, a long
`pattern` on a long string, hashing for `uniqueItems`, error messages that
printed whole values). A static reading of the schema in front of the
library was tried first and missed keys that Go's JSON decoding matches in
any case (`"ALLOF"`, `"$REF"`); a budget inside the validator cannot miss
them.

Changes from v0.4.3 (search for "Privo"):

- `budget.go` (new): `ErrBudget`, `DefaultBudget`, `MaxFrames`, the spending
  helpers, `brief` and `fixedCost`.
- `validate.go` (all messages): no message repeats a whole value, list or
  long name; what the budget does not pay for must stay small.
- `validate.go`: `Validate` calls the new `ValidateWithin`; `validate` spends
  each schema's fixed cost and the instance's length, refuses past
  `MaxFrames`, and returns `ErrBudget` once the budget is gone (a stopped
  result is never read as valid: `ValidateWithin` returns `ErrBudget`
  whatever the branches said). `pattern`, `patternProperties` and
  `uniqueItems` spend for their work. Messages show values briefly and
  `anyOf` reports its first reason only. `validateDefaults` gets the default
  budget.
- `resolve.go`: `resolvedInfo.cost`, set from `fixedCost`; a schema's
  location is kept to 120 bytes and stops growing past that (`grow`), since
  whole JSON pointers over many subschemas with long keys held hundreds of
  megabytes.
- Messages name lists by count and first entry (`names`), clip locations,
  and `anyOf` carries its first reason as clipped text.
- `Resolved.Nodes` (new) lets a caller bound the memory of what it caches.
- `infer.go` and the tests are left out.

To update: copy the new release's files over these, re-apply the changes
above, and run `go test ./internal/jsonschema/ ./internal/argcheck/`.

golangci-lint skips this directory (`.golangci.yml`): the copy keeps
upstream's style so it stays easy to compare. `go vet` and the tests still
run on it.
