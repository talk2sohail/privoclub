# Building Privo

Privo is one static Go binary, `cmd/privo`. Building it needs Go 1.27 or
later; `go.mod` pins the toolchain (1.27.4 or later: GO-2026-6859).

| Target | What it does |
| --- | --- |
| `make` | vet, lint, test, build |
| `make build` | `bin/privo`, static (`CGO_ENABLED=0`) |
| `make test` | Unit tests with the race detector |
| `make vet`, `make lint` | `go vet`; `golangci-lint` |
| `make vuln` | `govulncheck` |
| `make acceptance` | The black-box suite against the built binary |
| `make image` | The container image, tagged with the version |
| `make ui` | Build the admin app (Node 22) |
| `make ui-check` | The admin app's types and tests, and `web/dist` matches the source |
| `make ui-audit` | No known vulnerability in the admin app's production dependencies, and every package's registry signature checks |
| `make docs` | Build this site into `build/site` (Python 3.12) |
| `make docs-serve` | Preview this site at `http://127.0.0.1:8000` |

## The admin app

The admin app is a Preact, Vite and TypeScript app in `web/`. Its build in
`web/dist` is committed and embedded in the binary, so `make build` and
`make image` never need Node. Run `make ui` after changing anything in `web/`
and commit `web/dist` with it; CI fails when the committed build is not
exactly what the source builds. Install scripts never run (`web/.npmrc`).

## This site

The pages are Markdown in `docs/site/`, the navigation is in `mkdocs.yml`, and
[MkDocs](https://www.mkdocs.org/) builds them with its built-in readthedocs
theme. `make docs` installs MkDocs into `build/docs-venv` from
`docs/requirements.txt`, which pins every package with its hashes, and builds
with `--strict`: a broken link or anchor, or a page missing from the
navigation, fails the build. CI runs the same.

To change the MkDocs version, edit `docs/requirements.in`, run
`make docs-requirements` (it regenerates `docs/requirements.txt` with hashes,
in a container) and commit the two files on their own.

How pages are written: [the guideline](https://github.com/talk2sohail/privoclub/blob/main/docs/documentation-site.md#28-how-pages-are-written).
