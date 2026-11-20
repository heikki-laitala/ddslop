BINARY            := ddslop
BIN_DIR           := bin
GO                ?= go
GOLANGCI_LINT     ?= golangci-lint
MARKDOWNLINT      ?= npx --yes markdownlint-cli2@0.18.1
VERSION           ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS           := -X github.com/heikki-laitala/ddslop/internal/cli.version=$(VERSION)
COVER_MIN         ?= 80
COVER_PROFILE     := coverage.out
ORACLE_CASES      ?= 300
ORACLE_SEED       ?= 0

.DEFAULT_GOAL := help

.PHONY: help build run test cover oracle oracle-rust oracle-go oracle-ts notices lint lint-go lint-md fmt tidy clean

help: ## Show available targets
	@awk 'BEGIN {FS = ":.*##"} /^[a-zA-Z_-]+:.*##/ {printf "  %-10s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

build: ## Build the binary into bin/
	$(GO) build -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/$(BINARY) .

run: ## Run the tool (pass args with ARGS="...")
	$(GO) run . $(ARGS)

test: ## Run tests with the race detector
	$(GO) test -race ./...

cover: ## Run tests with coverage; fail below COVER_MIN percent (default 80)
	$(GO) test -count=1 -race -covermode=atomic -coverpkg=./... -coverprofile=$(COVER_PROFILE) ./...
	@total=$$($(GO) tool cover -func=$(COVER_PROFILE) | awk '/^total:/ {sub("%", "", $$3); print $$3}'); \
	echo "total coverage: $$total% (minimum $(COVER_MIN)%)"; \
	awk -v t="$$total" -v m="$(COVER_MIN)" 'BEGIN { exit !(t + 0 >= m + 0) }' || \
		{ echo "coverage $$total% is below $(COVER_MIN)%"; exit 1; }

oracle: oracle-rust oracle-go oracle-ts ## Check too-public and dead-code against each language's compiler

oracle-rust: ## Compare Rust too-public with rustc on generated crates (ORACLE_CASES, ORACLE_SEED; needs cargo)
	@command -v cargo >/dev/null || { echo "make oracle-rust needs cargo"; exit 1; }
	DDSLOP_RUSTC_CASES=$(ORACLE_CASES) DDSLOP_RUSTC_SEED=$(ORACLE_SEED) $(GO) test -count=1 -run TestTooPublicAgreesWithRustcOnReachability -v ./internal/cli

oracle-go: ## Apply Go too-public and dead-code suggestions to generated modules and go vet them (ORACLE_CASES, ORACLE_SEED)
	DDSLOP_GO_CASES=$(ORACLE_CASES) DDSLOP_GO_SEED=$(ORACLE_SEED) $(GO) test -count=1 -timeout 1h -run TestGoSuggestionsKeepTheModuleBuilding -v ./internal/cli

oracle-ts: ## Apply TypeScript too-public and dead-code suggestions to generated packages and run tsc (ORACLE_CASES, ORACLE_SEED; needs npx or DDSLOP_TSC)
	@[ -n "$$DDSLOP_TSC" ] || command -v npx >/dev/null || { echo "make oracle-ts needs npx, or DDSLOP_TSC set to a tsc command"; exit 1; }
	DDSLOP_TS_CASES=$(ORACLE_CASES) DDSLOP_TS_SEED=$(ORACLE_SEED) $(GO) test -count=1 -timeout 1h -run TestTypeScriptSuggestionsKeepThePackageCompiling -v ./internal/cli

notices: ## Regenerate THIRD_PARTY_NOTICES.md from the compiled-in modules
	scripts/notices.sh > THIRD_PARTY_NOTICES.md

lint: lint-go lint-md ## Run all linters

lint-go: ## Lint Go code with golangci-lint
	$(GOLANGCI_LINT) run ./...

lint-md: ## Lint Markdown files with markdownlint-cli2
	$(MARKDOWNLINT) "**/*.md"

fmt: ## Format Go code and fix Markdown lint issues
	$(GOLANGCI_LINT) fmt ./...
	$(MARKDOWNLINT) --fix "**/*.md"

tidy: ## Tidy go.mod
	$(GO) mod tidy

clean: ## Remove build output
	rm -rf $(BIN_DIR) $(COVER_PROFILE)
