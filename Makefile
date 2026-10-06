# wegwitness build and development tasks.
#
# Run `make` or `make help` for the available targets.

SHELL := /usr/bin/env bash
.SHELLFLAGS := -eu -o pipefail -c
.DEFAULT_GOAL := help

BINARY  := wegwitness
BIN_DIR := bin
PKG     := github.com/wegweiserzone/wegwitness
INFO    := $(PKG)/internal/buildinfo

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --verify HEAD 2>/dev/null)
DATE    ?= $(shell date -u -d "@$${SOURCE_DATE_EPOCH:-$$(date +%s)}" +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || date -u +%Y-%m-%dT%H:%M:%SZ)

LDFLAGS := -s -w \
	-X '$(INFO).version=$(VERSION)' \
	-X '$(INFO).commit=$(COMMIT)' \
	-X '$(INFO).date=$(DATE)'

GOBIN         := $(shell go env GOPATH)/bin
GOLANGCI_LINT := $(GOBIN)/golangci-lint
GOVULNCHECK   := $(GOBIN)/govulncheck

# The weg binary the interop test runs against. A sibling checkout of
# wegweiser, built with its own `make build`, is where it usually is.
WEG_BIN ?= $(abspath ../wegweiser/bin/weg)

.PHONY: help
help: ## Show this help
	@grep -hE '^[a-zA-Z0-9_-]+:.*?## ' $(MAKEFILE_LIST) \
		| awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'

.PHONY: build
build: ## Build the wegwitness binary into bin/
	@mkdir -p $(BIN_DIR)
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/$(BINARY) ./cmd/wegwitness
	@echo "built $(BIN_DIR)/$(BINARY) $(VERSION)"

.PHONY: install
install: ## Install wegwitness into GOPATH/bin
	CGO_ENABLED=0 go install -trimpath -ldflags "$(LDFLAGS)" ./cmd/wegwitness

IMAGE ?= wegwitness

.PHONY: image
image: ## Build the container image
	podman build --format docker \
		--build-arg "VERSION=$(VERSION)" --build-arg "COMMIT=$(COMMIT)" --build-arg "DATE=$(DATE)" \
		-f packaging/Containerfile -t $(IMAGE) .

.PHONY: clean
clean: ## Remove build and coverage artefacts
	rm -rf $(BIN_DIR) coverage.out coverage.html

.PHONY: check
check: tidy-check fmt-check vet lint test ## Run every check CI runs

.PHONY: test
test: ## Run tests with the race detector
	go test -race -shuffle=on ./...

# Skipped by `make test`, which has no weg to run. This one does.
.PHONY: interop
interop: ## Run the witness against real weg servers (WEG_BIN, default ../wegweiser/bin/weg)
	@test -x "$(WEG_BIN)" || { echo "no weg binary at $(WEG_BIN); build one with 'make build' in wegweiser, or set WEG_BIN"; exit 1; }
	WEG_BIN="$(WEG_BIN)" go test -race -count=1 -v ./internal/interop

.PHONY: vet
vet: ## Run go vet
	go vet ./...

.PHONY: lint
lint: $(GOLANGCI_LINT) ## Run golangci-lint
	$(GOLANGCI_LINT) run ./...

.PHONY: fmt
fmt: $(GOLANGCI_LINT) ## Format the code
	$(GOLANGCI_LINT) fmt ./...

.PHONY: fmt-check
fmt-check: $(GOLANGCI_LINT) ## Fail if the code is not formatted
	$(GOLANGCI_LINT) fmt --diff ./...

.PHONY: tidy
tidy: ## Tidy go.mod and go.sum
	go mod tidy

.PHONY: tidy-check
tidy-check: ## Fail if go.mod or go.sum would change
	@cp go.mod go.mod.bak && cp go.sum go.sum.bak 2>/dev/null || true
	@go mod tidy
	@if ! diff -q go.mod go.mod.bak >/dev/null 2>&1 || ! diff -q go.sum go.sum.bak >/dev/null 2>&1; then \
		mv go.mod.bak go.mod; mv go.sum.bak go.sum 2>/dev/null || true; \
		echo "go.mod or go.sum is not tidy; run 'make tidy'"; exit 1; \
	fi
	@rm -f go.mod.bak go.sum.bak

.PHONY: vuln
vuln: $(GOVULNCHECK) ## Check dependencies for known vulnerabilities
	$(GOVULNCHECK) ./...

.PHONY: tools
tools: $(GOLANGCI_LINT) $(GOVULNCHECK) ## Install the development tools

$(GOLANGCI_LINT):
	go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest

$(GOVULNCHECK):
	go install golang.org/x/vuln/cmd/govulncheck@latest
