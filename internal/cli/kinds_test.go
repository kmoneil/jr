package cli_test

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/kmoneil/jr/internal/cli"
	"github.com/kmoneil/jr/internal/exitcode"
)

// TestEveryKindHasAShapeGolden pins the shape of every output kind this build
// can emit, one file per kind and per schema version.
//
// It exists because the CLI goldens were recorded against the `ci` build and
// skipped everywhere else, so everything the write tag gates — `issue.create`,
// `issue.edit`, `issue.move`, `issue.assign`, `issue.comment.add`,
// `issue.worklog.add`, the dry-run shape — had no golden at all. Those shapes
// were asserted in unit tests, which is not the same thing: the rule is that a
// diff in a golden requires a schema version bump in the same commit, and for
// half the kinds there was no diff to notice. Six kinds went to v2 with the ADF
// conversion and were caught by the contract test rather than by a golden,
// which is luck rather than design.
//
// A shape is pinned once rather than per profile, because a kind's schema does
// not vary between builds that have it. What varies is *which* kinds a build
// emits, and that is what this test iterates: each profile compares against the
// files for its own kinds, and internal/lint asserts across all four that no
// kind is missing one.
//
// The golden is the block `jr contract` prints for that kind, verbatim. It is
// an excerpt of the shipped contract rather than a second rendering of it, so
// there is no separate description to drift.
func TestEveryKindHasAShapeGolden(t *testing.T) {
	got := run(t, nil, "--contract", "--format", "xml")
	if got.exit != exitcode.OK {
		t.Fatalf("exit = %v, stderr = %s", got.exit, got.stderr)
	}
	shapes := kindShapes(t, got.stdout)

	kinds := cli.Registry().Kinds()
	if len(kinds) == 0 {
		t.Fatal("this build emits no kinds, so this test asserts nothing")
	}
	for _, k := range kinds {
		t.Run(fmt.Sprintf("%s.v%d", k.Name, k.Version), func(t *testing.T) {
			shape, described := shapes[k.Name]
			if !described {
				t.Fatalf("`jr contract` does not describe kind %q, which this "+
					"build emits", k.Name)
			}
			if strings.TrimSpace(shape) == "" {
				t.Fatalf("kind %q has no shape in `jr contract`; a kind with no "+
					"schema is a payload render.Write holds to nothing", k.Name)
			}
			assertShapeGolden(t, k.Name, k.Version, shape)
		})
	}
}

// kindOpen matches the opening line of one kind's block in `jr contract
// --format xml`, in both the ordinary and the self-closing spelling.
var kindOpen = regexp.MustCompile(`^\s*<kind name="([^"]+)"`)

// kindShapes splits `jr contract --format xml` into one block per kind.
//
// The block excludes the <kind> line itself, whose `emitters` attribute names
// the commands in *this* build. The shape is the same in every build that has
// the kind; the emitters are not, and the per-profile contract.tsv golden is
// where they belong.
func kindShapes(t *testing.T, contract string) map[string]string {
	t.Helper()

	out := map[string]string{}
	var name string
	var block []string
	for _, line := range strings.Split(contract, "\n") {
		if name == "" {
			m := kindOpen.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			if strings.HasSuffix(strings.TrimSpace(line), "/>") {
				// A kind with no registered schema has nothing inside it.
				out[m[1]] = ""
				continue
			}
			name, block = m[1], nil
			continue
		}
		if strings.TrimSpace(line) == "</kind>" {
			out[name] = strings.Join(block, "\n") + "\n"
			name = ""
			continue
		}
		block = append(block, line)
	}
	if name != "" {
		t.Fatalf("the block for kind %q is never closed; this test reads the "+
			"contract by shape, so a reformatted document silently asserts "+
			"nothing", name)
	}
	if len(out) == 0 {
		t.Fatal("no <kind> blocks found in `jr contract --format xml`")
	}
	return out
}

// assertShapeGolden compares one kind's shape against
// testdata/kinds/<kind>.v<version>.xml.
//
// Under -update it refuses to overwrite an existing file whose content differs,
// which is what makes the version rule mechanical rather than a thing a
// reviewer has to remember. A changed shape at an unchanged version cannot be
// regenerated: the only way out is to bump the kind's version, which writes a
// new file and leaves the old one as the record of what that version was.
func assertShapeGolden(t *testing.T, kind string, version int, got string) {
	t.Helper()

	name := fmt.Sprintf("%s.v%d.xml", kind, version)
	path := filepath.Join("testdata", "kinds", name)

	if *update {
		existing, err := os.ReadFile(path)
		switch {
		case err == nil && string(existing) == got:
			return
		case err == nil:
			t.Fatalf("the shape of kind %q changed and it is still v%d.\n\n"+
				"A shape change is a change every consumer sees, so it needs a new\n"+
				"schema version in the same commit. Bump the kind's version where it\n"+
				"is declared and re-run `make golden`: that writes %s.v%d.xml and\n"+
				"leaves %s as the record of what v%d was.\n\n"+
				"--- %s ---\n%s\n--- now ---\n%s",
				kind, version, kind, version+1, name, version, name, existing, got)
		case !errors.Is(err, fs.ErrNotExist):
			t.Fatalf("read golden %s: %v", path, err)
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("create testdata dir: %v", err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatalf("write golden %s: %v", path, err)
		}
		return
	}

	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s: %v\nrun: make golden", path, err)
	}
	if got != string(want) {
		t.Errorf("the shape of kind %q does not match %s.\n"+
			"If this is deliberate, it is a contract change: bump the kind's "+
			"schema version, then run `make golden`.\n--- want ---\n%s\n--- got ---\n%s",
			kind, path, want, got)
	}
}
