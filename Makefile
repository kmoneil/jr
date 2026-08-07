BINARY  := jr
MODULE  := github.com/kmoneil/jira-cli
PKG     := ./cmd/jr
BIN     := bin

# Always semver, in every case — tagged, untagged, dirty, and no git at all.
# `git describe --always` degraded to a bare commit hash, which is what a Jira
# administrator saw in their access logs. See scripts/version.sh.
#
# Expanded here and checked, rather than left to `?=`. `$(shell)` takes the
# script's output and discards its exit status, so a refusal would otherwise
# stamp an empty release and build anyway — the script would refuse and the
# build would not notice. `?=` also defers expansion to first use, which is too
# late to check. This keeps the override: `make build VERSION=1.2.3` still
# skips the script, because a command-line variable is already defined.
ifeq ($(origin VERSION),undefined)
VERSION := $(shell sh scripts/version.sh)
ifeq ($(strip $(VERSION)),)
$(error scripts/version.sh refused to produce a version — see its message above)
endif
endif
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

## cost: measure what each output format costs, in tokens and in latency
#
# Deliberately not part of `make ci`. It calls the Anthropic API — count_tokens
# for the real Claude token counts, and a streamed request per format for
# time-to-first-token — and nothing in the test suite is allowed to touch the
# network. The relationship the default rests on is asserted by
# TestFormatCostFavoursTSVForCollections, which needs neither a key nor a
# network; this prints the numbers behind it.
#
# Needs ANTHROPIC_API_KEY, or a profile from `ant auth login`. Note an *empty*
# ANTHROPIC_API_KEY still wins the credential race and shadows a profile — the
# check below treats empty as unset for that reason.
#
# COST_ARGS passes flags through:
#   make cost COST_ARGS=--skip-latency   # token counts only; nothing billed
#   make cost COST_ARGS="--reps 9"       # more latency samples
COST_ARGS ?=
.PHONY: cost
cost:
	@command -v uv >/dev/null || { \
		echo "uv is not installed: https://docs.astral.sh/uv/getting-started/"; \
		exit 1; }
	@if [ -z "$$ANTHROPIC_API_KEY" ]; then \
		echo "ANTHROPIC_API_KEY is unset or empty."; \
		echo "Export a key, or run \`ant auth login\` and re-run with"; \
		echo "  env -u ANTHROPIC_API_KEY make cost"; \
		echo "so the empty variable does not shadow the profile."; \
		exit 1; \
	fi
	@uv run scripts/format-cost.py $(COST_ARGS)

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

## lint: run golangci-lint, with every tag on and with none
.PHONY: lint
lint: lint-untagged
	golangci-lint run

# The config lints with all eight tags on, deliberately: a capability that only
# compiles under a tag is still shipped code. The cost is that `unused` then
# sees every file at once, so a symbol that is live under `write` and dead
# without it looks used — which is how `echoMode` and a test fixture compiled
# into the reader and ci binaries with nothing in the tree able to notice. This
# pass looks at the build a user of those profiles actually gets.
#
# staticcheck rather than a second golangci-lint run. `golangci-lint run
# --build-tags=` does not override `run.build-tags` from the config, so that
# pass loads the same eight tags and reports the same clean answer — a gate
# that runs and cannot fail. Checked by reverting both symbols and watching it
# report zero. staticcheck is what golangci wraps for this check anyway, and it
# takes the tags from the environment.
#
# What it reports is not a false positive. A symbol reachable only under a tag
# belongs in a file that declares the tag; that is the whole guarantee the
# profiles are sold on.
## lint-untagged: the unused check the tagged pass cannot see
.PHONY: lint-untagged
lint-untagged:
	staticcheck -checks=U1000 ./...

# Vulnerability scan, over the module and the toolchain it is built with.
#
# It fails closed, because govulncheck's own exit status already carries the
# distinction worth acting on: 3 when a vulnerability is *reachable* — some
# symbol this code calls, traced from a real call site — and 0 when the only
# findings sit in modules nothing here calls into. Softening that into a
# warning would make a green `make ci` mean "found something, carried on",
# which is the one thing this project's checks are not allowed to mean.
#
# The scan runs under TAGS_FULL for the reason the fuzz sweep does. With no
# tags it analyses the smallest build there is, and a vulnerability reachable
# only from code behind `write` or `mcp` would be invisible while the scan
# reported itself clean. There are no negated build constraints in this tree,
# so the full tag set is a superset of every shipped profile and one pass
# covers all four.
#
# Test files are deliberately not scanned: nothing in them ships, and a finding
# there would fail a build over code no user can reach.
#
# This is the one target that needs a network — the database is vuln.go.dev.
# An offline run fails rather than passing quietly, on the same principle as
# everything else here: a check that did not run is not a check that passed.
## vuln: scan for known vulnerabilities, in this module and the toolchain
.PHONY: vuln
vuln:
	@command -v govulncheck >/dev/null || { \
		echo "govulncheck is not installed:"; \
		echo "  go install golang.org/x/vuln/cmd/govulncheck@latest"; \
		exit 1; }
	govulncheck -tags "$(TAGS_FULL)" ./...

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
ci: fmt-check vet lint vuln test-profiles test-race build-all size

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
