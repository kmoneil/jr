package lint_test

import (
	"path/filepath"
	"regexp"
	"strconv"
	"testing"

	"github.com/kmoneil/jira-cli/internal/registry"

	// Every resource, so registry.Default holds the whole surface. The blank
	// imports are how the other lint tests reach it too.
	_ "github.com/kmoneil/jira-cli/internal/commands"
)

// versionedDocs are the documents that print a worked example of a result
// envelope, and therefore print a schema version.
var versionedDocs = []string{
	"../../docs/output-contract.md",
	"../../README.md",
}

// docEnvelope matches an XML envelope in prose: `<result kind="issue.list" v="3">`.
var docEnvelope = regexp.MustCompile(`kind="([a-z][a-z.]*)" v="(\d+)"`)

// docJSONEnvelope matches the JSON spelling of the same thing, which is two
// lines apart and so is matched a line at a time by the caller.
var (
	docJSONKind = regexp.MustCompile(`^\s*"kind":\s*"([a-z][a-z.]*)",`)
	docJSONV    = regexp.MustCompile(`^\s*"v":\s*(\d+),`)
)

// TestTheDocumentedSchemaVersionsAreCurrent holds every worked example in the
// documentation to the version the binary actually emits.
//
// It was written after `--url` bumped `issue.list` and `issue.get`, and the
// examples in both documents turned out to have been stale for two bumps
// already: the contract said `issue.list v="1"` against a real v2, and the
// README said `issue.get v="1"` against a real v3. Nothing was wrong with the
// process — `make golden` pins the *shape* of every kind and refuses a changed
// shape at an unchanged version — but nothing pinned the number where a reader
// actually looks for it.
//
// That matters more here than a stale example usually would. This document is
// the thing a consumer pins against, and its first instruction is to dispatch
// on `kind` and branch on `v`. An example printing a version that no build has
// ever emitted teaches the wrong constant to whoever copies it.
func TestTheDocumentedSchemaVersionsAreCurrent(t *testing.T) {
	current := currentKindVersions(t)

	for _, doc := range versionedDocs {
		checked := 0
		var kind string
		for i, line := range readLines(t, doc) {
			for _, m := range docEnvelope.FindAllStringSubmatch(line, -1) {
				checkVersion(t, doc, i+1, m[1], m[2], current)
				checked++
			}
			// The JSON envelope names the kind and the version on separate
			// lines, so the kind is carried forward to the line that has the
			// number.
			if m := docJSONKind.FindStringSubmatch(line); m != nil {
				kind = m[1]
				continue
			}
			if m := docJSONV.FindStringSubmatch(line); m != nil && kind != "" {
				checkVersion(t, doc, i+1, kind, m[1], current)
				checked++
				kind = ""
			}
		}
		if checked == 0 {
			t.Errorf("%s has no versioned example, so this test read it and "+
				"asserted nothing; either the examples moved or the pattern "+
				"stopped matching them", filepath.Base(doc))
		}
	}
}

// checkVersion compares one documented pair against the registry.
func checkVersion(
	t *testing.T, doc string, line int, kind, version string, current map[string]int,
) {
	t.Helper()

	want, known := current[kind]
	if !known {
		t.Errorf("%s:%d documents kind %q, which no command emits",
			doc, line, kind)
		return
	}
	got, err := strconv.Atoi(version)
	if err != nil {
		t.Fatalf("%s:%d has an unreadable version %q", doc, line, version)
	}
	if got != want {
		t.Errorf("%s:%d shows %s at v%d and the binary emits v%d",
			doc, line, kind, got, want)
	}
}

// currentKindVersions is what the registry says each kind is at.
//
// registry.Kinds is the same source `jr contract` prints from, so the document
// is compared against the machine-readable contract rather than against a
// second reading of the source. A kind declared at two versions is already a
// registry-test failure and is not re-checked here.
func currentKindVersions(t *testing.T) map[string]int {
	t.Helper()

	out := map[string]int{}
	for _, k := range registry.Kinds() {
		out[k.Name] = k.Version
	}
	if len(out) == 0 {
		t.Fatal("the registry reports no kinds at all")
	}
	return out
}
