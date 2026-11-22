# Tests

Every change keeps these green, locally and in CI (`.github/workflows/ci.yml`):

| Check | Command | What it covers |
| --- | --- | --- |
| Unit tests | `go test -race -count=1 ./...` | Every package, with the race detector |
| Vet and lint | `go vet ./...`, `golangci-lint run` | |
| Vulnerabilities | `govulncheck ./...` | Known vulnerabilities in what the code calls |
| Acceptance | `make acceptance` | The black-box suite against the built binary |
| Admin app | `make ui-check`, `make ui-audit` | Types, tests (Vitest), the committed build, dependencies |
| This site | `make docs` | Every page builds; no broken link, anchor or page left out |
| Image | CI's `image` job | The container builds, with an SBOM and provenance |

## The acceptance suite

`test/acceptance` builds the real binary and drives it from outside, as an
operator and an MCP client would: start it, call `/mcp` with the official MCP
Go SDK, use the admin API, back up and restore, restart with the wrong key.
It has the build tag `acceptance`, so `go test ./...` skips it; run it with
`make acceptance`.

## Tests that keep the documents honest

Some tests read documents, so the documentation cannot fall behind the code:

- `internal/config`: every setting the code reads is in the registry
  (`internal/config/settings.go`), every registered setting is read, every
  one is in the [configuration reference](../installation/configuration.md)
  and that page names no other, and the example file
  [`docs/privo.example.yaml`](https://github.com/talk2sohail/privoclub/blob/main/docs/privo.example.yaml) has a commented
  line for each one and parses with all of them switched on.
- `internal/buildinfo`: `TestVersionIsSemver` fails when
  `internal/buildinfo/VERSION` is not a valid semantic version
  ([Versioning](versioning.md)).

## Naming tests

A test's name says the behaviour it checks, as a sentence
(`TestRestoreWithTheWrongKeyRefusesToStart`). Each feature names its tests in
the project's tracker before the code is written
([How a feature is made](workflow.md)).
