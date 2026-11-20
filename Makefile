# Privo build targets. Requires Go 1.26+ (go.mod pins the toolchain).
# The version is the semantic version in internal/buildinfo/VERSION (docs/versioning.md).
VERSION := $(shell tr -d '[:space:]' < internal/buildinfo/VERSION)
LDFLAGS := -s -w
IMAGE   ?= privo:$(VERSION)

.PHONY: all build test vet lint vuln acceptance image clean ui ui-test ui-check

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

clean:
	rm -rf bin
