VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short=12 HEAD 2>/dev/null)
PKG     := github.com/ACaccel/lcm/internal/buildinfo
LDFLAGS := -s -w -X $(PKG).Version=$(VERSION) -X $(PKG).Commit=$(COMMIT)

.PHONY: build test lint fmt fmt-check vet check

build:
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o bin/lcm ./cmd/lcm

test:
	go test -race ./...

vet:
	go vet ./...

lint:
	golangci-lint run

fmt:
	golangci-lint fmt

fmt-check:
	@out="$$(golangci-lint fmt --diff 2>&1)"; \
	if [ -n "$$out" ]; then echo "$$out"; echo "Run 'make fmt' to fix formatting."; exit 1; fi

check: fmt-check vet lint test
