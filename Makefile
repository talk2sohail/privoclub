# Privo build targets. Requires Go 1.25+.
# The version is the semantic version in internal/buildinfo/VERSION (docs/versioning.md).
VERSION := $(shell tr -d '[:space:]' < internal/buildinfo/VERSION)
LDFLAGS := -s -w
IMAGE   ?= privo:$(VERSION)

.PHONY: all build test vet lint vuln acceptance image clean

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

# Black-box tests against the built binary (test/acceptance). Expected to fail
# until feature F02 lands; see docs/todo.md.
acceptance:
	go test -tags acceptance -count=1 -timeout 5m ./test/acceptance/...

image:
	docker build -t $(IMAGE) .

clean:
	rm -rf bin
