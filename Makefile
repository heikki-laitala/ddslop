BINARY            := ddslop
BIN_DIR           := bin
GO                ?= go
GOLANGCI_LINT     ?= golangci-lint
MARKDOWNLINT      ?= npx --yes markdownlint-cli2@0.18.1
COVER_MIN         ?= 80
COVER_PROFILE     := coverage.out

.DEFAULT_GOAL := help

.PHONY: help build run test cover lint lint-go lint-md fmt tidy clean

help: ## Show available targets
	@awk 'BEGIN {FS = ":.*##"} /^[a-zA-Z_-]+:.*##/ {printf "  %-10s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

build: ## Build the binary into bin/
	$(GO) build -o $(BIN_DIR)/$(BINARY) .

run: ## Run the tool (pass args with ARGS="...")
	$(GO) run . $(ARGS)

test: ## Run tests with the race detector
	$(GO) test -race ./...

cover: ## Run tests with coverage; fail below COVER_MIN percent (default 80)
	$(GO) test -race -covermode=atomic -coverpkg=./... -coverprofile=$(COVER_PROFILE) ./...
	@total=$$($(GO) tool cover -func=$(COVER_PROFILE) | awk '/^total:/ {sub("%", "", $$3); print $$3}'); \
	echo "total coverage: $$total% (minimum $(COVER_MIN)%)"; \
	awk -v t="$$total" -v m="$(COVER_MIN)" 'BEGIN { exit !(t + 0 >= m + 0) }' || \
		{ echo "coverage $$total% is below $(COVER_MIN)%"; exit 1; }

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
