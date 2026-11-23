# How a feature is made

Privo aims at enterprise quality from the first release: every feature ships
with tests, documentation, audit coverage and a threat note. Security
defaults fail closed. Secrets are never logged, returned by the API, or passed
to hooks. Every table carries `org_id`, and every store method takes a typed
`OrgID`.

Each feature goes through the same steps:

1. **Spec.** Add or update the feature's section in the tracker,
   [`docs/todo.md`](https://github.com/talk2sohail/privoclub/blob/main/docs/todo.md): its goal, acceptance criteria and
   tasks.
2. **Decide.** Write an [architecture decision](decisions.md) for anything
   hard to reverse.
3. **Test first.** Name the tests for each task before writing code
   ([Tests](tests.md)).
4. **Build in small changes.** Keep `go vet`, `go test -race ./...` and the
   end-to-end MCP client test green.
5. **Review.** A separate security review pass on every change that touches
   authentication, secrets, tenancy or the request pipeline. Review reports
   are kept in [`docs/reviews/`](https://github.com/talk2sohail/privoclub/blob/main/docs/reviews).
6. **Document.** Update the feature's page on this site (`docs/site/`), say
   the version it arrived in, and tick the feature's to-do items.

A change that earns a new version bumps it in the same commit
([Versioning](versioning.md)). Pull requests target `main`.
