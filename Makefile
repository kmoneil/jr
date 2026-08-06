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
#
# GOLDEN_PKGS write the same bytes whatever the build tags are, so they are
# recorded once. GOLDEN_PKGS_PROFILE hold output that legitimately differs
# between profiles — the command list, the tag list, the kinds a build emits —
# and are recorded under every shipped tag set. Recording only one is how half
# the output contract went unenforced: everything behind the write tag had no
# golden at all, and the test that would have compared it skipped instead.
GOLDEN_PKGS         := ./internal/render/ ./internal/adf/
GOLDEN_PKGS_PROFILE := ./internal/cli/

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

## build: full human build, every capability for macOS
.PHONY: build-mac
build-mac:
	@mkdir -p $(BIN)
	GOOS=darwin GOARCH=amd64 go build -tags "$(TAGS_FULL)" -ldflags "$(LDFLAGS)" -o $(BIN)/$(BINARY)-mac $(PKG)

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
build-all: build build-mac build-agent build-reader build-ci

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

## test-race: run the suite under the race detector
.PHONY: test-race
test-race:
	go test -race ./...

## test-profiles: run the test suite under every shipped tag set
.PHONY: test-profiles
test-profiles:
	@set -e; for tags in "$(TAGS_CI)" "$(TAGS_READER)" "$(TAGS_AGENT)" "$(TAGS_FULL)"; do \
		echo "== tags=$${tags:-none}"; \
		go test -tags "$$tags" ./...; \
	done

## cost: measure what each output format costs, in tokens
#
# Deliberately not part of `make ci`. It fetches a tokenizer, and nothing in
# the test suite is allowed to touch the network. The relationship the default
# rests on is asserted by TestFormatCostFavoursTSVForCollections, which needs
# neither; this prints the number behind it.
.PHONY: cost
cost:
	@command -v uv >/dev/null || { \
		echo "uv is not installed: https://docs.astral.sh/uv/getting-started/"; \
		exit 1; }
	@uv run scripts/format-cost.py

## cover: run tests with a coverage profile
.PHONY: cover
cover:
	go test -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out | tail -1

# Time budget per fuzz target. Override for a longer hunt:
#   make fuzz FUZZTIME=5m
FUZZTIME ?= 60s

# Fuzz sweep. Each target runs for FUZZTIME.
#
# -run=^$$ so the regular test suite does NOT run before each target: without
# it a regular test failure masks the fuzz result, and the serial setup adds
# seconds per target across the sweep.
#
# -fuzz is anchored ^...$$ to avoid prefix-match ambiguity, e.g.
# FuzzValuesRoundTrip vs a later FuzzValuesRoundTripAdversarial.
#
# Targets are discovered rather than listed, so a new fuzz target is picked up
# the moment it is written. A target this sweep never ran is worse than useless:
# it reads as coverage that does not exist.
#
# The sweep builds with TAGS_FULL for exactly that reason. It ran untagged until
# internal/workflow grew fuzz targets behind the write tag, and `go test -list`
# reported none — five minutes of green sweep over code it could not compile.
# The full tag set is the only one that can reach every target there is.
#
# Output is captured rather than piped, because a pipe would discard the exit
# status and the sweep would report success while a target was failing.
## fuzz: run every fuzz target for FUZZTIME each (default 60s)
.PHONY: fuzz
fuzz:
	@echo "==> Fuzz sweep (FUZZTIME=$(FUZZTIME) per target, tags=$(TAGS_FULL))"
	@failed=0; start=$$(date +%s); ran=0; \
	for pkg in $$(go list -tags "$(TAGS_FULL)" ./...); do \
		for target in $$(go test -tags "$(TAGS_FULL)" $$pkg -list 'Fuzz.*' 2>/dev/null | grep '^Fuzz' || true); do \
			ran=$$((ran + 1)); \
			printf "    %-52s " "$${pkg#$(MODULE)/} $$target"; \
			if out=$$(go test -tags "$(TAGS_FULL)" $$pkg -run=^$$ -fuzz="^$$target$$" -fuzztime=$(FUZZTIME) 2>&1); then \
				echo "$$out" | tail -1; \
			else \
				failed=1; echo "FAIL"; echo "$$out" | sed 's/^/        /'; \
			fi; \
		done; \
	done; \
	echo "==> $$ran target(s) in $$(($$(date +%s) - start))s"; \
	exit $$failed

## fuzz-jql-values: hammer the JQL value round-trip fuzzer
.PHONY: fuzz-jql-values
fuzz-jql-values:
	go test ./internal/jql/ -run=^$$ -fuzz='^FuzzValuesRoundTrip$$' -fuzztime=$(FUZZTIME)

## fuzz-jql-tokenize: hammer the JQL tokenizer fuzzer
.PHONY: fuzz-jql-tokenize
fuzz-jql-tokenize:
	go test ./internal/jql/ -run=^$$ -fuzz='^FuzzTokenizeDoesNotPanic$$' -fuzztime=$(FUZZTIME)

## fuzz-jql-date: hammer the JQL date parser fuzzer
.PHONY: fuzz-jql-date
fuzz-jql-date:
	go test ./internal/jql/ -run=^$$ -fuzz='^FuzzParseDateDoesNotPanic$$' -fuzztime=$(FUZZTIME)

## golden: rewrite the output-contract golden files
.PHONY: golden
golden:
	go test $(GOLDEN_PKGS) -update
	@set -e; for tags in "$(TAGS_CI)" "$(TAGS_READER)" "$(TAGS_AGENT)" "$(TAGS_FULL)"; do \
		echo "== tags=$${tags:-none}"; \
		go test -tags "$$tags" $(GOLDEN_PKGS_PROFILE) -update; \
	done
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
ci: fmt-check vet lint test-profiles test-race build-all size

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
