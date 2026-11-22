# Privo build targets. Requires Go 1.26+ (go.mod pins the toolchain).
# The version is the semantic version in internal/buildinfo/VERSION (docs/site/development/versioning.md).
VERSION := $(shell tr -d '[:space:]' < internal/buildinfo/VERSION)
LDFLAGS := -s -w
IMAGE   ?= privo:$(VERSION)

.PHONY: all build test vet lint vuln acceptance image clean ui ui-test ui-check ui-audit docs docs-serve docs-requirements

all: vet lint test build

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/privo ./cmd/privo

test:
	go test -race -count=1 ./...

vet:
	go vet ./...

lint:
	golangci-lint run

vuln:
	go run golang.org/x/vuln/cmd/govulncheck@latest ./...

# Black-box tests against the built binary (test/acceptance).
acceptance:
	go test -tags acceptance -count=1 -timeout 5m ./test/acceptance/...

image:
	docker build -t $(IMAGE) .

# The admin app (web/, ADR 0019). Needs Node 22. Its build in web/dist is
# committed, so `build` and `image` never need Node; run `make ui` after
# changing anything in web/ and commit web/dist with it.
ui:
	cd web && npm ci && npm run build

ui-test:
	cd web && npm ci && npm run typecheck && npm test

# CI: the tests pass and the committed build is exactly what the source builds.
ui-check: ui-test
	cd web && npm run build
	git diff --exit-code -- web/dist
	test -z "$$(git status --porcelain -- web/dist)"

# CI (F14 R-05): production dependencies have no known vulnerability and
# every package's registry signature checks. Install scripts never run
# (web/.npmrc).
ui-audit:
	cd web && npm audit --omit=dev && npm audit signatures

# The documentation site (mkdocs.yml, pages in docs/site/; docs/documentation-site.md).
# Needs Python 3.12. Installs only the pinned, hashed requirements, and only
# wheels, so no package runs code at install time. The site goes to build/site.
PYTHON    ?= python3
DOCS_VENV := build/docs-venv
# python:3.12-slim on 2026-11-22, used only to regenerate docs/requirements.txt.
DOCS_PY_IMAGE := python:3.12-slim@sha256:5e2bdb1b0d542d5039cffb8181c65930093949194a65c365dfb1b0fe13a4fa5b

$(DOCS_VENV)/.installed: docs/requirements.txt
	rm -rf $(DOCS_VENV)
	$(PYTHON) -m venv $(DOCS_VENV)
	$(DOCS_VENV)/bin/pip install --quiet --disable-pip-version-check --require-hashes --only-binary=:all: -r docs/requirements.txt
	touch $@

docs: $(DOCS_VENV)/.installed
	$(DOCS_VENV)/bin/mkdocs build --strict

docs-serve: $(DOCS_VENV)/.installed
	$(DOCS_VENV)/bin/mkdocs serve --strict --dev-addr 127.0.0.1:8000

# After editing docs/requirements.in: pin every dependency with its hashes.
# Commit the two files on their own.
docs-requirements:
	docker run --rm -v "$(CURDIR)/docs:/docs" -w /docs $(DOCS_PY_IMAGE) sh -c \
	  'pip install --quiet --root-user-action=ignore pip-tools && pip-compile --quiet --generate-hashes --allow-unsafe --strip-extras --no-emit-index-url --output-file requirements.txt requirements.in'

clean:
	rm -rf bin build
