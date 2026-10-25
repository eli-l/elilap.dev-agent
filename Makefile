# Makefile - developer entry points for elilap.dev-agent.
#
# Every target works offline: dependencies are vendored, so nothing here
# downloads modules. Run "make help" for the list.

GO ?= go
GOFLAGS ?= -mod=vendor
BIN := agent
PKGS := ./...

.DEFAULT_GOAL := help

.PHONY: help setup doctor build test vet fmt fmt-check e2e check verify clean

help: ## Show the available targets
	grep -hE '^[a-zA-Z0-9_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  %-10s %s\n", $$1, $$2}'

setup: ## Prepare a checkout: check the toolchain, create .env, build the binary
	set -e; \
	echo "==> toolchain"; \
	$(GO) version; \
	case "$$($(GO) version | awk '{print $$3}')" in \
	  go1.2[7-9]*|go1.[3-9][0-9]*) ;; \
	  *) echo "go >= 1.27 is required (PLAN.md Decision 1)" >&2; exit 1 ;; \
	esac; \
	echo "==> .env"; \
	if [ -f .env ]; then \
	  echo ".env already exists - left untouched"; \
	elif [ -f .env.example ]; then \
	  cp .env.example .env; echo "created .env from .env.example - fill in the credentials"; \
	else \
	  echo "warning: .env.example not found" >&2; \
	fi; \
	echo "==> build"; \
	$(GO) build $(GOFLAGS) -o $(BIN) ./cmd/agent; \
	echo "==> done - next: make doctor"

doctor: ## Check this machine against the runtime requirements
	$(GO) run $(GOFLAGS) ./cmd/agent doctor

build: ## Build ./agent
	$(GO) build $(GOFLAGS) -o $(BIN) ./cmd/agent

test: ## Run the unit and end-to-end tests
	$(GO) test $(GOFLAGS) $(PKGS)

vet: ## Run go vet
	$(GO) vet $(GOFLAGS) $(PKGS)

fmt: ## Format the Go sources
	gofmt -w cmd internal

fmt-check: ## Fail if any Go source is unformatted
	out="$$(gofmt -l cmd internal)"; \
	if [ -n "$$out" ]; then echo "unformatted files:"; echo "$$out"; exit 1; fi; \
	echo "gofmt: clean"

e2e: ## Run only the fixture-based end-to-end test
	$(GO) test $(GOFLAGS) -run TestE2E -v ./cmd/agent/

check: fmt-check vet test build ## The whole verification suite: format, vet, test, build

verify: check ## Alias for check

clean: ## Remove build artifacts
	rm -f $(BIN)
	rm -rf bin
