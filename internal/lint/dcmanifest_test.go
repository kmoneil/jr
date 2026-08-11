package lint_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// manifestPaths are the recording recipes for the Data Center rig.
//
// Two files because they describe two instances. The recorder stores
// req.URL.Path verbatim, so everything recorded under a context path carries
// the prefix and everything recorded at the root does not; running one
// manifest against the other's instance would rewrite every cassette into a
// shape nobody asked for. record.sh refuses each against the wrong one, and
// both are read here so a recording in either is covered.
var manifestPaths = []string{
	repoRoot + "/scripts/dc/manifest.tsv",
	repoRoot + "/scripts/dc/manifest-contextpath.tsv",
}

// manifestFloor is how many entries the file must carry before a clean result
// means anything, for the same reason every other walk here has a floor: a
// parser that stopped parsing reports exactly what a satisfied one reports.
const manifestFloor = 15

// manifestEntry is one line of scripts/dc/manifest.tsv.
type manifestEntry struct {
	file     string // which manifest it came from
	line     int
	group    string // a key shaped like the ones in unrecorded
	cassette string // repository-relative path
	command  string // jr arguments, or !reason
}

func (e manifestEntry) gap() bool { return strings.HasPrefix(e.command, "!") }

// TestEveryDataCenterRecordingCanBeRemade holds the manifest and the tree to
// each other.
//
// The Data Center cassettes are recordings now, which moves the risk. It is no
// longer "nobody has evidence"; it is that a recording nobody can reproduce
// rots the first time a request changes — and a request changing is exactly
// when a recording stops being evidence and starts being a fixture that
// happens to pass. The manifest is the reproduction, so the invariant is that
// every recording is named by it and every name is real.
//
// Written as a test rather than a paragraph in the README because that is the
// lesson this repository keeps re-learning: the profile counts, the error-code
// table, the schema versions in the docs were all true when written and all
// drifted, and each one only stopped drifting when something read it.
func TestEveryDataCenterRecordingCanBeRemade(t *testing.T) {
	entries := readManifest(t)

	if len(entries) < manifestFloor {
		t.Fatalf("the manifests hold %d entries between them, want at least %d — "+
			"a file moved or the parser stopped parsing", len(entries), manifestFloor)
	}

	groups := cassetteGroups(t)

	named := map[string]manifestEntry{}
	for _, e := range entries {
		if _, ok := groups[e.group]; !ok {
			t.Errorf("%s line %d names the group %q, which no cassette in "+
				"the tree belongs to. A recipe for a group that does not exist "+
				"is a recipe nobody can follow", e.file, e.line, e.group)
		}

		if !strings.HasSuffix(e.cassette, ".datacenter.json") {
			t.Errorf("%s line %d writes %q, which is not a Data Center "+
				"cassette. A recording filed under the wrong deployment is "+
				"evidence for the API it did not come from", e.file, e.line, e.cassette)
		}

		if prior, ok := named[e.cassette]; ok {
			t.Errorf("%s lines %d and %d both write %s; the second "+
				"recording silently replaces the first", e.file, prior.line, e.line, e.cassette)
		}
		named[e.cassette] = e

		if e.gap() {
			// A gap says a row's recording cannot be produced by one jr
			// invocation. It still has to say why, in the file the operator
			// reads, rather than in a plan nobody opens.
			if len(strings.TrimSpace(e.command)) < 2 {
				t.Errorf("%s line %d is a gap with no reason", e.file, e.line)
			}
			continue
		}
		if !strings.HasPrefix(e.command, "jr ") && !isJiraVerb(e.command) {
			t.Errorf("%s line %d has the command %q, which does not look "+
				"like jr arguments", e.file, e.line, e.command)
		}
	}

	// The half that catches the real failure: a recording with no recipe.
	for _, path := range recordedDataCenterCassettes(t) {
		if _, ok := named[path]; !ok {
			t.Errorf("%s is a Data Center recording that scripts/dc/manifest.tsv "+
				"does not know how to remake. Add the command that produced it, "+
				"or the next person to change that request has a fixture they "+
				"cannot re-record and no way to tell", path)
		}
	}

	// And the half that was here before: an outstanding row needs a recipe, so
	// paying it off is a command somebody can run rather than a project.
	for group := range unrecorded {
		if !strings.HasSuffix(group, " datacenter") {
			continue
		}
		if !slices.ContainsFunc(entries, func(e manifestEntry) bool { return e.group == group }) {
			t.Errorf("%q has no evidence and no manifest entry, so the ledger "+
				"says what is missing and nothing says how to get it", group)
		}
	}
}

// isJiraVerb reports whether a manifest command reads as jr arguments with the
// binary name left off, which is how every line in the file is written.
func isJiraVerb(command string) bool {
	first, _, _ := strings.Cut(command, " ")
	return first != "" && !strings.ContainsAny(first, "/.\\") && strings.ToLower(first) == first
}

func readManifest(t *testing.T) []manifestEntry {
	t.Helper()

	var out []manifestEntry
	for _, path := range manifestPaths {
		out = append(out, readOneManifest(t, path)...)
	}
	return out
}

func readOneManifest(t *testing.T, manifestPath string) []manifestEntry {
	t.Helper()

	data, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("read %s: %v", manifestPath, err)
	}

	var out []manifestEntry
	for i, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) != 3 {
			t.Errorf("%s line %d has %d tab-separated fields, want 3: %q",
				filepath.Base(manifestPath), i+1, len(fields), line)
			continue
		}
		out = append(out, manifestEntry{
			file:     filepath.Base(manifestPath),
			line:     i + 1,
			group:    strings.TrimSpace(fields[0]),
			cassette: strings.TrimSpace(fields[1]),
			command:  strings.TrimSpace(fields[2]),
		})
	}
	return out
}

// recordedDataCenterCassettes lists every Data Center cassette in the tree that
// claims to be a recording.
func recordedDataCenterCassettes(t *testing.T) []string {
	t.Helper()

	var out []string
	err := filepath.WalkDir(repoRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".datacenter.json") {
			return err //nolint:wrapcheck // the walk's own error, returned as-is.
		}
		data, readErr := os.ReadFile(path) //nolint:gosec // a path from the test tree.
		if readErr != nil {
			return nil //nolint:nilerr // unreadable is another test's business.
		}
		var c struct {
			Source string `json:"source"`
		}
		// A cassette this cannot parse is TestEveryFixtureDeclaresItsSource's
		// business, and one that is not a recording is not this test's.
		if unmarshalErr := json.Unmarshal(data, &c); unmarshalErr != nil {
			return nil //nolint:nilerr // malformed JSON is another test's business.
		}
		if c.Source != "recorded" {
			return nil
		}
		out = append(out, strings.TrimPrefix(filepath.ToSlash(path), repoRoot+"/"))
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	return out
}
