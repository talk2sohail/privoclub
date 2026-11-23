# Contributing to Privo

Thank you for helping. Privo aims at enterprise quality from the first
release, so every change, however small, follows the same path.

## Before you start

- For anything larger than a fix, open an issue first, so we can agree on the
  approach before you write code.
- Security problems go through [SECURITY.md](SECURITY.md), never an issue.

## How a change is made

The full workflow is on the documentation site,
[How a feature is made](docs/site/development/workflow.md). In short:

1. **Spec**: the feature's section in [`docs/todo.md`](docs/todo.md) (goal,
   acceptance criteria, tasks).
2. **Decide**: an [architecture decision](docs/adr/) for anything hard to
   reverse.
3. **Test first**: name the tests before writing the code.
4. **Build** in small commits; keep these green:
   ```sh
   make            # vet, lint, race tests, build
   make acceptance # the black-box suite against the binary
   make ui-check   # when you change web/ (Node 22)
   make docs       # when you change docs/site/ (Python 3.12)
   ```
5. **Document**: update the feature's page in `docs/site/`, with the version
   it arrived in.

Rules that are never relaxed: security defaults fail closed; secrets are never
logged, returned by the API or passed to hooks; every table carries `org_id`
and every store method takes a typed `OrgID`.

## Pull requests

- Target `main`, one topic per pull request, and describe what you checked.
- A change to authentication, secrets, tenancy or the request pipeline gets a
  separate security review before it merges.
- By contributing you agree that your contribution is licensed under the
  [MIT License](LICENSE).
