# Guideline: a documentation website for Privo, built with MkDocs

Status: proposal, 2026-11-22. This document says how to build
`docs.privo.club` in the way osquery builds `osquery.readthedocs.io`, adapted
to this repository. Nothing here is built yet; the tasks are at the end.

## 1. What osquery does (the model)

osquery keeps its whole documentation site in the main repository and builds
it with [MkDocs](https://www.mkdocs.org/). Read from `osquery/osquery` on
2026-11-22:

| Piece | osquery |
|---|---|
| Configuration | `mkdocs.yml` at the repository root, about 75 lines |
| Pages | Markdown under `docs/wiki/`: `index.md` and the folders `introduction/`, `installation/`, `deployment/`, `development/`, `theme/` |
| Output | `site_dir: build/wiki`, never committed |
| Theme | `theme: readthedocs`, built into MkDocs, no options; one extra stylesheet (`extra_css: theme/osquery.css`) |
| Markdown | one extension, `admonition` (note and warning boxes); no plugins |
| Navigation | an explicit `nav:` of 4 sections and 33 pages: Introduction (4), Installation (4), Deployment (15), Development (10) |
| "Edit on GitHub" | `repo_url` plus `edit_uri: .../edit/master/docs/wiki/` |
| Hosting | Read the Docs, from `.readthedocs.yml`: config version 2, Ubuntu 22.04, Python 3.11, `submodules: exclude: all`, requirements from `docs/requirements.txt` |
| Dependencies | `docs/requirements.txt` pins exactly two: `mkdocs==1.4.2`, `jinja2==3.1.6` |

What makes it work, and what we copy:

1. **The docs live next to the code** and change in the same pull request.
2. **A separate folder holds only what is published** (`docs/wiki`), so
   internal files in `docs/` never reach the site by accident.
3. **The built-in theme and no plugins.** Almost nothing to install, update
   or trust.
4. **An explicit `nav:`** orders the pages the way a reader learns: what it
   is, how to install it, how to run it, how to develop it.
5. **Pinned versions** and a one-file hosting config.

## 2. How Privo adapts it

### 2.1 Layout

```
mkdocs.yml                     the site's configuration (repository root, as osquery)
docs/site/                     every published page, and nothing else
  index.md                     Welcome: what Privo is, in one page
  introduction/                how it works and the words it uses
  installation/                build, run, Coolify, configuration reference
  using/                       the admin site, servers, teams, rules, approvals, audit
  security/                    the threat model in plain words, secrets, egress
  development/                 building, testing, the workflow, decisions
  theme/privo.css              small overrides (colours, the logo), as osquery's theme/osquery.css
docs/requirements.txt          pinned, hashed Python requirements for the build
build/site/                    the built site (in .gitignore, never committed)
```

Why `docs/site/` and not `docs/`: `docs/` holds records that are not user
documentation. These stay internal and unpublished:
- `todo.md`
- `reviews/`
- `checkpoint-*.md`
- `manual-test*.md`
- `upstream/`

MkDocs publishes every Markdown file under `docs_dir`, so the published
pages get a folder of their own, as osquery's `docs/wiki/`. The name `site`
is not free: `site/` at the root is the landing page (a separate Coolify
app). The docs folder is `docs/site/` and the output is `build/site/`, so
neither touches it.

### 2.2 `mkdocs.yml`

Start from osquery's file and change only what Privo needs:

```yaml
site_name: Privo
site_url: https://docs.privo.club/
site_description: The team gateway to AI work and the MCP world
theme:
  name: readthedocs          # osquery's theme, built into MkDocs
  navigation_depth: 3
  collapse_navigation: false

repo_url: https://github.com/talk2sohail/privoclub
edit_uri: edit/main/docs/site/
docs_dir: docs/site
site_dir: build/site
extra_css:
  - theme/privo.css

strict: true                  # a broken link or a page missing from nav fails the build
validation:
  links:
    absolute_links: warn
    unrecognized_links: warn
  omitted_files: warn         # a page left out of nav fails under strict

markdown_extensions:
  - admonition               # !!! note / !!! warning boxes, as osquery
  - toc:
      permalink: true        # a link anchor on every heading
  - tables
  - fenced_code

nav:
  - Introduction:
      - Welcome to Privo: index.md
      - How Privo works: introduction/how-it-works.md
      - Words Privo uses: introduction/concepts.md
  - Installation:
      - Build and run: installation/build-and-run.md
      - Deploy on Coolify: installation/coolify.md
      - Configuration reference: installation/configuration.md
      - The configuration file: installation/configuration-file.md
      - Backup, restore and upgrades: installation/operations.md
  - Using Privo:
      - The admin site and signing in: using/admin-site.md
      - Inviting your team: using/people.md
      - Adding servers: using/servers.md
      - Each person's own account: using/connections.md
      - Teams and the catalog: using/teams.md
      - Rules, approvals and grants: using/rules.md
      - Arguments checked against the schema: using/arguments.md
      - Tokens and AI clients: using/tokens.md
      - AI clients that sign in with OAuth: using/oauth-clients.md
      - Activity and Health: using/activity.md
      - The audit trail: using/audit.md
      - Roles: using/roles.md
  - Security:
      - Threat model: security/threat-model.md
      - Secrets and keys: security/secrets.md
      - Outbound requests (egress): security/egress.md
      - Reporting a vulnerability: security/disclosure.md
  - Development:
      - Building Privo: development/building.md
      - Tests: development/tests.md
      - How a feature is made: development/workflow.md
      - Architecture decisions: development/decisions.md
      - Versioning: development/versioning.md
```

`theme: readthedocs` is the decision that keeps us closest to osquery: no
plugin, no theme package, nothing to update but MkDocs itself. The usual
alternative, Material for MkDocs, has search suggestions, dark mode and tabs.
It adds a large dependency and its own release cadence. Choose it only on
purpose, and record that choice in the tracker.

### 2.3 Pinned, hashed requirements

osquery pins exact versions. Privo also pins hashes, as the admin app's npm
build does (F14 R-05: `npm audit signatures`):

```
# docs/requirements.in (what we choose)
mkdocs==1.6.1

# docs/requirements.txt (generated: pip-compile --generate-hashes docs/requirements.in)
mkdocs==1.6.1 \
    --hash=sha256:...
# ...and every dependency, each with its hash
```

The build installs only with `pip install --require-hashes -r
docs/requirements.txt`. A change to the requirements is its own commit, so
the dependency change can be read on its own.

### 2.4 Where each page comes from

Most of the content already exists. It moves; it is not rewritten from
nothing.

| Published page | Source today |
|---|---|
| Welcome, How Privo works | README introduction; ADR 0001 (pipeline) and ADR 0009 (threat model), in plain words |
| Build and run | README "Build" and "Run" |
| Deploy on Coolify | `docs/deploy-coolify.md` (moved; the old path keeps a one-line pointer) |
| Configuration reference | README settings table (see 2.5) |
| The configuration file | README "Configuration file" and `docs/privo.example.yaml` |
| Admin site, people, servers, connections, teams, rules, tokens, OAuth clients, activity, audit, roles | README sections of the same names |
| Arguments checked | README "Arguments checked" and the threat note of F02 in `docs/todo.md` |
| Threat model, secrets, egress | ADRs 0003, 0008, 0009 and 0013, summarised; each page links to the ADR |
| Building, tests | README "Build", `Makefile`, `test/acceptance` |
| How a feature is made | `CLAUDE.md` "Workflow for every feature" |
| Architecture decisions | `docs/adr/README.md`; the ADRs stay where they are and the page links to them on GitHub |
| Versioning | `docs/versioning.md` (moved) |

The README then shrinks to what a GitHub visitor needs:
- what Privo is;
- a five-line quick start;
- a link to `docs.privo.club`.

### 2.5 One source for the settings

The settings registry (`internal/config/settings.go`) is the truth.
`TestRegistryIsInTheREADME` already fails when a setting is missing from the
README table. When the table moves to `installation/configuration.md`:

- the test reads that page instead, and also fails for a setting on the page
  that the registry does not know;
- `TestExampleFileListsEverySetting` stays as it is (`docs/privo.example.yaml`).

So a new setting cannot ship without its documentation, which is the rule
the README table follows today.

### 2.6 Hosting: two options

**A. Read the Docs, exactly as osquery.** Add `.readthedocs.yaml`:

```yaml
version: 2
build:
  os: ubuntu-24.04
  tools:
    python: "3.12"
mkdocs:
  configuration: mkdocs.yml
  fail_on_warning: true
python:
  install:
    - requirements: docs/requirements.txt
```

- Builds on every push and keeps a version per branch or tag.
- Pull request previews are free for public repositories.
- A private repository needs a paid plan.
- The site is served from Read the Docs, with `docs.privo.club` as a custom
  domain.

**B. Coolify, beside the landing page (recommended while the repository is
private).** A third Coolify app, `privo-docs`, built from
`docs/Dockerfile`:

- **Stage 1:** `python:3.12-slim`; `pip install --require-hashes -r
  docs/requirements.txt`; `mkdocs build --strict`.
- **Stage 2:** a static file server (`caddy` or `nginx`, pinned by digest)
  serving `build/site/`. Headers:
  - CSP: `default-src 'self'; script-src 'self'; style-src 'self'
    'unsafe-inline'`. The readthedocs theme writes some inline styles; check
    the built pages before tightening it.
  - `X-Content-Type-Options: nosniff`.
  - `Referrer-Policy: same-origin`.
  - Long caching only for hashed assets.
- **Domain:** `docs.privo.club`. It deploys from `main`, as the gateway does.

Either way the site holds nothing secret and needs no server-side code.

### 2.7 Continuous integration

Add a `docs` job to `.github/workflows/ci.yml`, next to `ui`:

```yaml
docs:
  runs-on: ubuntu-latest
  steps:
    - uses: actions/checkout@<pinned sha>
    - uses: actions/setup-python@<pinned sha>
      with: { python-version: "3.12" }
    - run: pip install --require-hashes -r docs/requirements.txt
    - run: mkdocs build --strict
```

Add `make docs` (build) and `make docs-serve` (`mkdocs serve` on
`127.0.0.1:8000`) to the `Makefile`. `--strict` turns these into failures:
- a broken link between pages;
- a page missing from `nav`;
- a missing anchor.

Links to GitHub files (ADRs, code) are absolute and are not checked by
MkDocs. A weekly link check (for example `lychee` on `build/site`) can follow
later.

### 2.8 How pages are written

The house style of the README and ADRs carries over:

- Plain words, short sentences, the reader's task first. One page per task;
  a page that needs a second scroll of headings is two pages.
- **Every feature page says the version it arrived in**, as the README does
  ("0.21.0"), so a self-hosted operator knows whether it applies to them.
- Settings are written as `PRIVO_*` variables and link to the configuration
  reference; they are never explained twice.
- Use `!!! warning` for anything that loses data or weakens security (for
  example, switching GitHub sign-in off). Use `!!! note` for what is useful
  but optional. Nothing else is boxed.
- Examples never contain a real secret, token or private address. Use
  `privo_pat_...`, `example.com` and documentation IP ranges.
- Screenshots only where a page is hard to describe in words. Store them
  next to the page and give each alt text.
- Each design page links to its ADR, and each ADR's "As built" section links
  back to the page, so the two never contradict each other quietly.

### 2.9 Versions of the docs

osquery publishes one version, the latest. Privo does the same at first: the
site follows `main`, and pages carry "since 0.x.y". A second version is
needed only when a release line is supported in parallel. Then use Read the
Docs versions (option A), or `mike` for option B, and record that choice in
the tracker.

### 2.10 What changes in the project's rules

When the site exists:

- `CLAUDE.md`, step 6 of the workflow becomes "Update the page in
  `docs/site/` and tick the feature's to-do items", and the layout lists
  `docs/site/` and `mkdocs.yml`.
- "No feature merges without its section in `docs/todo.md` updated" gains
  "and its page in `docs/site/`".
- `docs/manual-test.md` stays internal; it links to the published pages for
  each feature.

## 3. Tasks, in the order of the pull requests

1. **Skeleton:**
   - `mkdocs.yml`, `docs/requirements.in` and `docs/requirements.txt`
     (hashed);
   - `docs/site/index.md` and empty section pages;
   - `make docs` and `make docs-serve`, and `build/` in `.gitignore`;
   - the CI `docs` job.

   Acceptance: `mkdocs build --strict` passes in CI.
2. **Move the content:**
   - Move the README sections and `docs/deploy-coolify.md` into the pages of
     2.4, with no change in meaning, and shrink the README.
   - Leave a pointer at every old path.
   - Move the settings test to the configuration page (2.5).

   Acceptance: every README section has a page, and the settings test passes
   against the page.
3. **Write what is missing:**
   - How Privo works;
   - Words Privo uses;
   - the security pages;
   - Tests;
   - How a feature is made.
4. **Hosting:** choose A or B (2.6) and record the choice in the tracker.
   Then add either `.readthedocs.yaml` or `docs/Dockerfile` with its Coolify
   app, and point `docs.privo.club` at it. Link the docs from the landing
   page and the admin app (the help link in the header).
5. **Rules:** update `CLAUDE.md` (2.10), and add the docs site as a feature
   section in `docs/todo.md`.

Each pull request targets `main` and keeps every check green.
