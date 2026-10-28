# Privo build targets. Requires Go 1.25+.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
PKG     := privo.club/privo/internal/buildinfo
LDFLAGS := -s -w -X $(PKG).Version=$(VERSION) -X $(PKG).Commit=$(COMMIT) -X $(PKG).Date=$(DATE)
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
	docker build --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) --build-arg DATE=$(DATE) -t $(IMAGE) .

clean:
	rm -rf bin
