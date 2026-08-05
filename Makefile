BINARY  := jr
MODULE  := github.com/kmoneil/jira-cli
PKG     := ./cmd/jr
BIN     := bin

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo 0.1.0-dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

INFO    := $(MODULE)/internal/buildinfo
LDFLAGS := -s -w \
	-X '$(INFO).Release=$(VERSION)' \
	-X '$(INFO).Commit=$(COMMIT)' \
	-X '$(INFO).Built=$(DATE)'

# Shipped profiles. A feature excluded here contributes zero bytes: an agent
# introspecting the binary sees the truth, not a list of commands that refuse.
TAGS_FULL   := tui,prompt,render,browser,clipboard,mcp,write,admin
TAGS_AGENT  := mcp,write
TAGS_READER := mcp
TAGS_CI     :=

# The reader build must stay small enough to ship in a container and must not
# depend on anything that touches a terminal, a display server, or os/exec.
READER_MAX_BYTES := 12582912

# Packages that own golden files. `make golden` rewrites them; every other
# package would reject the -update flag.
GOLDEN_PKGS := ./internal/render/ ./internal/cli/

.DEFAULT_GOAL := help

## help: list the available targets
.PHONY: help
help:
	@grep -hE '^## ' $(MAKEFILE_LIST) | sed 's/^## /  /' | sort

## build: full human build, every capability
.PHONY: build
build:
	@mkdir -p $(BIN)
	go build -tags "$(TAGS_FULL)" -ldflags "$(LDFLAGS)" -o $(BIN)/$(BINARY) $(PKG)

## build-agent: no TTY assumptions, no interactivity, no browser, no clipboard
.PHONY: build-agent
build-agent:
	@mkdir -p $(BIN)
	go build -tags "$(TAGS_AGENT)" -ldflags "$(LDFLAGS)" -o $(BIN)/$(BINARY)-agent $(PKG)

## build-reader: physically cannot mutate Jira
.PHONY: build-reader
build-reader:
	@mkdir -p $(BIN)
	go build -tags "$(TAGS_READER)" -ldflags "$(LDFLAGS)" -o $(BIN)/$(BINARY)-reader $(PKG)

## build-ci: query only, smallest possible
.PHONY: build-ci
build-ci:
	@mkdir -p $(BIN)
	go build -tags "$(TAGS_CI)" -ldflags "$(LDFLAGS)" -o $(BIN)/$(BINARY)-ci $(PKG)

## build-all: every shipped profile
.PHONY: build-all
build-all: build build-agent build-reader build-ci

## size: assert the reader build stays under its budget
.PHONY: size
size: build-reader
	@bytes=$$(wc -c < $(BIN)/$(BINARY)-reader); \
	printf '%-24s %10d bytes (limit %d)\n' "$(BINARY)-reader" "$$bytes" "$(READER_MAX_BYTES)"; \
	if [ "$$bytes" -gt "$(READER_MAX_BYTES)" ]; then \
		echo "reader build exceeds its size budget"; exit 1; \
	fi

## test: run every test in the default (query-only) build
.PHONY: test
test:
	go test ./...

## test-profiles: run the test suite under every shipped tag set
.PHONY: test-profiles
test-profiles:
	@set -e; for tags in "$(TAGS_CI)" "$(TAGS_READER)" "$(TAGS_AGENT)" "$(TAGS_FULL)"; do \
		echo "== tags=$${tags:-none}"; \
		go test -tags "$$tags" ./...; \
	done

## cover: run tests with a coverage profile
.PHONY: cover
cover:
	go test -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out | tail -1

# Seconds per fuzz target. CI uses the default; raise it locally when changing
# anything that quotes, escapes, or parses.
FUZZTIME ?= 30s

## fuzz: run every fuzz target for FUZZTIME each (default 30s)
.PHONY: fuzz
fuzz:
	@set -e; \
	for pkg in $$(go list ./... ); do \
		targets=$$(go test -list '^Fuzz' $$pkg 2>/dev/null | grep '^Fuzz' || true); \
		for t in $$targets; do \
			echo "== $$pkg $$t"; \
			go test $$pkg -run '^$$' -fuzz "^$$t$$" -fuzztime $(FUZZTIME); \
		done; \
	done

## golden: rewrite the output-contract golden files
.PHONY: golden
golden:
	go test $(GOLDEN_PKGS) -update
	@echo
	@echo "Golden files rewritten. Any diff is a change to the public output"
	@echo "contract: bump the schema version of the affected kind in the same commit."

## lint: run golangci-lint
.PHONY: lint
lint:
	golangci-lint run

## fmt: format the tree
.PHONY: fmt
fmt:
	gofumpt -w .
	go mod tidy

## vet: run go vet across every shipped tag set
.PHONY: vet
vet:
	@set -e; for tags in "$(TAGS_CI)" "$(TAGS_READER)" "$(TAGS_AGENT)" "$(TAGS_FULL)"; do \
		go vet -tags "$$tags" ./...; \
	done

## ci: everything CI enforces, runnable locally
.PHONY: ci
ci: fmt-check vet lint test-profiles build-all size

## fmt-check: fail if the tree is not formatted
.PHONY: fmt-check
fmt-check:
	@out=$$(gofumpt -l .); \
	if [ -n "$$out" ]; then echo "not formatted:"; echo "$$out"; exit 1; fi

## hooks: install the repo's git hooks
.PHONY: hooks
hooks:
	git config core.hooksPath .githooks
	@echo "core.hooksPath set to .githooks"

## contract: print the output contract this build exposes
.PHONY: contract
contract:
	@go run $(PKG) contract

## clean: remove build output
.PHONY: clean
clean:
	rm -rf $(BIN) coverage.out
