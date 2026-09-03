# driverforge/cli Makefile

BINARY      := driverforge
MODULE      := github.com/driverforge/cli
BUILDINFO   := $(MODULE)/internal/buildinfo
DIST        := dist

# Version metadata stamped into the binary via -ldflags.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

LDFLAGS := -s -w \
	-X $(BUILDINFO).Version=$(VERSION) \
	-X $(BUILDINFO).Commit=$(COMMIT) \
	-X $(BUILDINFO).Date=$(DATE)

# Dev-stack endpoint overrides for `make build-dev` — point a locally built CLI
# at the dev stack (*.driverforge.test) so you don't have to set env vars each
# run. Override on the command line if your local URLs differ.
AUTH_PKG     := $(MODULE)/internal/auth
DRIVERFORGEAPI_PKG := $(MODULE)/internal/driverforgeapi
DEV_AUTH_URL  ?= https://auth.driverforge.test
DEV_GRAPH_URL ?= https://graph.driverforge.test/graphql

DEV_LDFLAGS := $(LDFLAGS) \
	-X $(AUTH_PKG).AuthURL=$(DEV_AUTH_URL) \
	-X $(DRIVERFORGEAPI_PKG).GraphURL=$(DEV_GRAPH_URL)

# Platforms for `make release` (GOOS/GOARCH pairs).
PLATFORMS := darwin/arm64 darwin/amd64 linux/arm64 linux/amd64

# Install prefix for `make install-bin` (the end-user layout, like the curl installer).
PREFIX ?= /usr/local

# Where `make install-dev` places the dev binary — Go's bin dir, so it sits next
# to a `make install`-ed prod `driverforge` and is already on PATH.
GOBINDIR ?= $(shell go env GOBIN)
ifeq ($(strip $(GOBINDIR)),)
GOBINDIR := $(shell go env GOPATH)/bin
endif

.DEFAULT_GOAL := build

.PHONY: build
build: ## Build the driverforge binary for the host platform
	go build -trimpath -ldflags '$(LDFLAGS)' -o $(BINARY) ./cmd/driverforge

.PHONY: build-dev
build-dev: ## Build driverforge-dev pointed at the local dev stack (*.driverforge.test)
	go build -trimpath -ldflags '$(DEV_LDFLAGS)' -o $(BINARY)-dev ./cmd/driverforge
	@echo "built $(BINARY)-dev → auth=$(DEV_AUTH_URL) graph=$(DEV_GRAPH_URL)"

.PHONY: install
install: ## Install driverforge into $GOBIN (or ~/go/bin) via go install
	go install -trimpath -ldflags '$(LDFLAGS)' ./cmd/driverforge

.PHONY: install-bin
install-bin: build ## Install the built binary to $(PREFIX)/bin (end-user layout; may need sudo)
	install -d "$(PREFIX)/bin"
	install -m 0755 "$(BINARY)" "$(PREFIX)/bin/$(BINARY)"

.PHONY: install-dev
install-dev: build-dev ## Install driverforge-dev (dev-stack build) into Go's bin dir, alongside the prod driverforge
	install -d "$(GOBINDIR)"
	install -m 0755 "$(BINARY)-dev" "$(GOBINDIR)/$(BINARY)-dev"
	@echo "installed $(GOBINDIR)/$(BINARY)-dev → auth=$(DEV_AUTH_URL) graph=$(DEV_GRAPH_URL)"
	@echo "run it as: $(BINARY)-dev   (your prod '$(BINARY)' is untouched)"

.PHONY: run
run: ## Build and run (use ARGS="build -e dev")
	go run -ldflags '$(LDFLAGS)' ./cmd/driverforge $(ARGS)

.PHONY: test
test: ## Run all tests
	go test ./...

.PHONY: cover
cover: ## Run tests with a coverage summary
	go test -cover ./...

.PHONY: vet
vet: ## Run go vet
	go vet ./...

.PHONY: vuln
vuln: ## Report reachable known vulnerabilities (govulncheck)
	go run golang.org/x/vuln/cmd/govulncheck@latest ./...

.PHONY: fmt
fmt: ## Format all Go sources
	gofmt -w .

.PHONY: fmt-check
fmt-check: ## Fail if any Go source is not gofmt-clean
	@out=$$(gofmt -l .); if [ -n "$$out" ]; then echo "not gofmt-clean:"; echo "$$out"; exit 1; fi

.PHONY: tidy
tidy: ## Tidy go.mod/go.sum
	go mod tidy

.PHONY: qlty
qlty: ## Run the maintainability gate over files changed since main
	node scripts/qlty-gate.js

.PHONY: check
check: fmt-check vet test ## Run fmt-check, vet, and tests

.PHONY: release
release: ## Cross-compile binaries into $(DIST)/ for all PLATFORMS
	@mkdir -p $(DIST)
	@for p in $(PLATFORMS); do \
		os=$${p%/*}; arch=$${p#*/}; \
		out=$(DIST)/$(BINARY)-$$os-$$arch; \
		echo "building $$out"; \
		GOOS=$$os GOARCH=$$arch CGO_ENABLED=0 \
			go build -trimpath -ldflags '$(LDFLAGS)' -o $$out ./cmd/driverforge || exit 1; \
	done

.PHONY: clean
clean: ## Remove build artifacts
	rm -rf $(BINARY) $(BINARY)-dev $(DIST)

.PHONY: help
help: ## Show this help
	@grep -hE '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) \
		| awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2}'
