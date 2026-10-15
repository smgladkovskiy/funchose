# funchose: library, command line and their checks.
#
# The functional tests are a module of their own (internal/functional): they
# start PostgreSQL with testcontainers, so they need Docker, and they keep
# testcontainers out of the library's go.mod.

# golangci-lint: the one on PATH, or this version installed into bin/ on first use.
GOLANGCI_LINT_VERSION ?= v2.14.0
BIN_DIR               := $(CURDIR)/bin
GOLANGCI_LINT         ?= $(or $(shell command -v golangci-lint 2>/dev/null),$(BIN_DIR)/golangci-lint)
# PostgreSQL image of the functional tests: postgres:14-alpine ... postgres:18-alpine.
PG_IMAGE      ?= postgres:18-alpine
COVER_DIR     ?= $(CURDIR)/.cover
FUNCTIONAL    := internal/functional
MODULE        := github.com/smgladkovskiy/funchose

.DEFAULT_GOAL := help

.PHONY: help
help: ## list the targets
	@awk 'BEGIN {FS = ":.*## "} /^[a-z-]+:.*## / {printf "  %-16s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

$(BIN_DIR)/golangci-lint:
	GOBIN=$(BIN_DIR) go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)

.PHONY: lint
lint: $(GOLANGCI_LINT) ## run golangci-lint on both modules
	$(GOLANGCI_LINT) run ./...
	cd $(FUNCTIONAL) && $(GOLANGCI_LINT) run --config $(CURDIR)/.golangci.yml ./...

.PHONY: lint-fix
lint-fix: $(GOLANGCI_LINT) ## let golangci-lint fix what it can
	$(GOLANGCI_LINT) run --fix ./...
	cd $(FUNCTIONAL) && $(GOLANGCI_LINT) run --config $(CURDIR)/.golangci.yml --fix ./...

.PHONY: test
test: ## run the unit tests: no database, no Docker
	go test -count=1 -race ./...

.PHONY: test-functional
test-functional: ## run the functional tests against $(PG_IMAGE) in Docker
	cd $(FUNCTIONAL) && FUNCHOSE_PG_IMAGE=$(PG_IMAGE) go test -count=1 -race ./...

.PHONY: test-all
test-all: test test-functional ## run unit and functional tests

.PHONY: cover
cover: ## measure the coverage of unit and functional tests together into cover.out
	rm -rf $(COVER_DIR) && mkdir -p $(COVER_DIR)/unit $(COVER_DIR)/functional
	go test -count=1 -covermode=atomic -coverpkg=./... ./... -args -test.gocoverdir=$(COVER_DIR)/unit
	cd $(FUNCTIONAL) && FUNCHOSE_PG_IMAGE=$(PG_IMAGE) go test -count=1 -covermode=atomic \
		-coverpkg=$(MODULE)/... ./... -args -test.gocoverdir=$(COVER_DIR)/functional
	go tool covdata textfmt -i=$(COVER_DIR)/unit,$(COVER_DIR)/functional -o cover.out
	go tool cover -func=cover.out | tail -1

.PHONY: tidy
tidy: ## go mod tidy in both modules
	go mod tidy
	cd $(FUNCTIONAL) && go mod tidy

.PHONY: ci
ci: lint test test-functional ## what CI runs
