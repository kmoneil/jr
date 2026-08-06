package lint_test

import (
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/kmoneil/jira-cli/internal/buildinfo"
)

// notYetGating names the tags that are declared but gate no code, with the
// reason each one does not yet.
//
// This list is the point of the test. "Six tags gate nothing" was a sentence in
// a document, which meant nobody found out when it stopped being true or when a
// feature shipped ungated. Here it is a fact the build states: adding the
// feature and gating it makes the test fail until the tag is removed from this
// list, and shipping the feature *without* gating it also fails, because the
// tag is still listed as gating nothing while the profile that excludes it now
// contains the code.
var notYetGating = map[string]string{
	"tui":       "internal/tui is a package doc and nothing else; there is no `jr ui`",
	"render":    "internal/adf is a stub, so there is no ADF renderer to exclude",
	"browser":   "there is no OAuth browser flow yet",
	"clipboard": "nothing copies to the clipboard yet",
}

// TestEveryTagEitherGatesCodeOrSaysWhyNot is the audit §8 asks for.
//
// A tag that excludes nothing is a profile that differs from another in name
// only, and the reader guarantee is exactly as strong as the weakest such
// claim. This does not require every tag to gate something — most of the
// features are not built — but it does require the gap to be recorded rather
// than discovered.
func TestEveryTagEitherGatesCodeOrSaysWhyNot(t *testing.T) {
	gated := filesPerTag(t)

	for _, tag := range buildinfo.KnownTags {
		files := gated[tag]
		reason, excused := notYetGating[tag]

		switch {
		case len(files) > 0 && excused:
			t.Errorf("tag %q now gates %d file(s) but is still listed as not "+
				"gating anything (%q); remove it from notYetGating",
				tag, len(files), reason)
		case len(files) == 0 && !excused:
			t.Errorf("tag %q gates no code and is not recorded as pending; "+
				"a profile without it is identical to one with it", tag)
		}
	}

	// A tag listed as pending must still be a real tag, or the list rots into
	// excuses for things that no longer exist.
	for tag := range notYetGating {
		if !slices.Contains(buildinfo.KnownTags, tag) {
			t.Errorf("notYetGating names %q, which is not a known tag", tag)
		}
	}
}

// TestTheTagsThatGateCodeGateTheRightCode pins what `write` and `mcp` exclude,
// because those two carry the guarantees the profiles are sold on.
func TestTheTagsThatGateCodeGateTheRightCode(t *testing.T) {
	gated := filesPerTag(t)

	// Every mutating verb is behind write, so a reader binary cannot contain
	// one. The count is not asserted — verbs get added — but the location is.
	for _, file := range gated["write"] {
		if !strings.HasPrefix(file, "internal/resource/") {
			t.Errorf("write gates %s, which is not a resource; a mutation "+
				"belongs in the resource it mutates", file)
		}
	}
	if len(gated["write"]) == 0 {
		t.Error("write gates nothing, so the reader guarantee is a claim")
	}

	if len(gated["mcp"]) == 0 {
		t.Error("mcp gates nothing, so `jr mcp serve` is in every build")
	}
	if len(gated["prompt"]) == 0 {
		t.Error("prompt gates nothing, so shell completion is in an agent build")
	}

	// admin is the only tag that gates code no single tag can reach: `sprint
	// close` needs write as well. Asserting it here is what keeps the
	// combination working — a file that quietly moved to one tag would still
	// satisfy the audit above, and the agent profile would gain the ability to
	// end an iteration without anything failing.
	if len(gated["admin"]) == 0 {
		t.Error("admin gates nothing, so an agent build can administer a board")
	}
	for _, file := range gated["admin"] {
		if !slices.Contains(gated["write"], file) {
			t.Errorf("admin gates %s and write does not; administering a board "+
				"is a mutation, and a build without write must not contain it", file)
		}
	}
}

// stubs are files a tag compiles that carry no behaviour, so gating them
// excludes nothing a caller could notice.
//
// internal/buildinfo holds one tag_<name>.go per tag whose whole job is to
// record that the tag is set, and internal/tui is a package doc waiting for a
// UI. Counting either would make this audit report exactly the reassurance it
// exists to withhold.
var stubs = []string{
	"internal/buildinfo/",
	"internal/tui/doc.go",
	"internal/adf/doc.go",
}

func isStub(file string) bool {
	for _, s := range stubs {
		if file == s || (strings.HasSuffix(s, "/") && strings.HasPrefix(file, s)) {
			return true
		}
	}
	return false
}

// filesPerTag returns, for each known tag, the files whose compilation depends
// on it.
//
// It asks the go tool rather than parsing source, because a constraint can be
// written several ways and only the toolchain is authoritative about which
// files a build includes.
//
// The comparison is "everything on" against "everything on but this one",
// rather than "nothing on" against "this one alone". The two differ for a file
// that needs more than one tag: `//go:build write && admin` is added by neither
// tag on its own, so turning them on one at a time attributes it to nothing and
// the audit reports both tags as gating less than they do. Removing one tag
// from a full build attributes it to both, which is what the constraint says.
func filesPerTag(t *testing.T) map[string][]string {
	t.Helper()

	all := goFiles(t, buildinfo.KnownTags)
	out := map[string][]string{}
	for _, tag := range buildinfo.KnownTags {
		without := goFiles(t, remove(buildinfo.KnownTags, tag))
		var added []string
		for file := range all {
			if without[file] {
				continue
			}
			if isStub(file) {
				continue
			}
			added = append(added, file)
		}
		sort.Strings(added)
		out[tag] = added
	}
	return out
}

// remove returns tags without one of them, leaving the original untouched.
func remove(tags []string, drop string) []string {
	out := make([]string, 0, len(tags))
	for _, tag := range tags {
		if tag != drop {
			out = append(out, tag)
		}
	}
	return out
}

// goFiles lists every file the toolchain would compile with the given tags, as
// a set of module-relative paths.
func goFiles(t *testing.T, tags []string) map[string]bool {
	t.Helper()

	args := []string{"list", "-json"}
	if len(tags) > 0 {
		args = append(args, "-tags", strings.Join(tags, ","))
	}
	args = append(args, "./...")

	cmd := exec.Command("go", args...)
	cmd.Dir = ".."
	stdout, err := cmd.Output()
	if err != nil {
		var stderr string
		if ee, ok := errors.AsType[*exec.ExitError](err); ok {
			stderr = string(ee.Stderr)
		}
		t.Fatalf("go list -tags %v: %v\n%s", tags, err, stderr)
	}

	files := map[string]bool{}
	dec := json.NewDecoder(strings.NewReader(string(stdout)))
	for {
		var p struct {
			ImportPath string
			GoFiles    []string
		}
		if err := dec.Decode(&p); err == io.EOF {
			break
		} else if err != nil {
			t.Fatalf("decode go list output: %v", err)
		}
		for _, f := range p.GoFiles {
			files[short(p.ImportPath)+"/"+f] = true
		}
	}
	if len(files) == 0 {
		t.Fatal("go list returned no files")
	}
	return files
}
