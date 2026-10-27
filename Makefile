VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short=12 HEAD 2>/dev/null)
PKG     := github.com/ACaccel/lcm/internal/buildinfo
LDFLAGS := -s -w -X $(PKG).Version=$(VERSION) -X $(PKG).Commit=$(COMMIT)

# The web console needs Node.js and npm; without them the Go targets still
# work and the binary serves a page that says how to build the console.
HAVE_NODE := $(shell command -v node >/dev/null 2>&1 && command -v npm >/dev/null 2>&1 && echo yes)

.PHONY: build web web-check test lint fmt fmt-check vet check

# Builds the console first when Node is available, so the binary embeds it.
build:
ifeq ($(HAVE_NODE),yes)
	$(MAKE) web
else
	@echo "notice: node/npm not found; building without the web console"
endif
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o bin/lcm ./cmd/lcm

web/node_modules: web/package-lock.json
	cd web && npm ci
	@touch $@

# Installs dependencies and writes the console into internal/webui/dist.
web: web/node_modules
	cd web && npm run build

# Typecheck, lint, unit tests and the generated-API-types staleness check.
web-check: web/node_modules
	cd web && npm run check

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
ifeq ($(HAVE_NODE),yes)
	$(MAKE) web-check
else
	@echo "notice: node/npm not found; skipped web-check"
endif
