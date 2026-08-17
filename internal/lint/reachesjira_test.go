package lint_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// deployments are the two APIs this tool speaks. A package that reaches Jira
// reaches one of these, and almost always both.
var deployments = []string{"cloud", "datacenter"}

// noCassettes names a package that builds Jira requests and has no
// conversation of its own for a deployment, with the reason.
//
// It is the second question the evidence ledger could not ask.
// TestEveryDeploymentIsBackedByARecordingOrSaysWhyNot groups the cassettes
// that exist, so a package with none never forms a group and passes by being
// invisible — which is not a hypothetical: `internal/site` was exactly that
// until 2026-08-11, and it is the package where every deployment difference in
// the tool lives. Two of the three bugs this project keeps citing were in its
// code, found through resources whose own fixtures were hand-written.
//
// It is empty, and that is the state to keep it in. The one entry it opened
// with was `internal/site cloud`, and it was paid off on 2026-08-17 by
// probe-recorded.cloud.json: `jr user me --refresh` against the Cloud sandbox,
// which records the deployment probe and the account fetch in one invocation.
// It had needed the sandbox credential and nothing else, no local instance, no
// licence, no seed, which made it the cheapest outstanding row in this tree and
// also the easiest to keep not doing.
//
// Every package that builds a Jira request now has a recording behind both
// APIs it claims to speak.
var noCassettes = map[string]string{}

// TestEveryPackageThatReachesJiraHasAConversation asks whether a package that
// talks to Jira has any evidence at all, per deployment.
//
// The ledger's other question is whether the cassettes a package has are
// recordings. This one is prior to it: a package with no cassettes at all is
// absent from that grouping entirely, so "the ledger is green" and "every
// conversation is covered" were two different claims and only one of them was
// checked.
//
// Driven from the code rather than from a list. A package that reaches Jira is
// one that constructs a request for it, and that is greppable — which matters
// because a hand-maintained list of packages-that-talk-to-Jira is exactly the
// kind of list that is right when written and silently wrong two commits
// later.
func TestEveryPackageThatReachesJiraHasAConversation(t *testing.T) {
	builders := requestBuilders(t)

	// A walk that found nothing reports what a fully covered tree reports.
	if len(builders) < 6 {
		t.Fatalf("found only %d packages that build a Jira request, so this "+
			"walked the wrong tree: %v", len(builders), builders)
	}

	groups := cassetteGroups(t)

	for _, pkg := range builders {
		for _, deployment := range deployments {
			key := pkg + " " + deployment
			_, hasGroup := groups[key]
			reason, excused := noCassettes[key]

			switch {
			case hasGroup && excused:
				t.Errorf("%s has cassettes now — delete its entry from "+
					"noCassettes, or the list reads as debt that was already paid",
					key)
			case !hasGroup && !excused:
				t.Errorf("%s builds requests for Jira and has no %s cassette at "+
					"all, so nothing in this package is checked against that API. "+
					"Record one, or add %q to noCassettes with the reason you "+
					"cannot", pkg, deployment, key)
			case !hasGroup && reason == "":
				t.Errorf("%s is excused with an empty reason; an exemption with "+
					"no argument is the drift it exists to prevent", key)
			}
		}
	}

	// The list can only shrink, the same way `unrecorded` does: an entry for a
	// package that is not a request builder any more is a row nobody will
	// delete, and it would go on reading as outstanding work.
	for key := range noCassettes {
		pkg, _, _ := strings.Cut(key, " ")
		if !slices.Contains(builders, pkg) {
			t.Errorf("noCassettes names %q, and %s builds no Jira request; "+
				"delete the row", key, pkg)
		}
	}
}

// requestBuilders lists the packages that construct a request for Jira.
//
// `transport.Request{` is the spelling, and it is the whole test: the transport
// is the only thing that speaks HTTP here — an invariant with its own guard —
// so a package that reaches Jira reaches it by building one of these. Test
// files are excluded, since a fixture-driven test constructs requests in order
// to replay them, which is the coverage rather than the thing covered.
//
// Parsed rather than grepped. A comment naming `transport.Request{` would
// otherwise enrol a package that reaches nothing, and this file is full of
// such comments — including this one.
//
// `internal/transport` itself is not a builder by this rule; it is the
// mechanism, and its own contract cassettes are covered by the ledger's other
// question.
func requestBuilders(t *testing.T) []string {
	t.Helper()

	seen := map[string]bool{}
	fset := token.NewFileSet()
	err := filepath.WalkDir(repoRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err //nolint:wrapcheck // the walk's own error, returned as-is.
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, parseErr := parser.ParseFile(fset, path, nil, 0)
		if parseErr != nil {
			return nil //nolint:nilerr // an unparseable source file fails the build, not this.
		}
		if !buildsRequest(file) {
			return nil
		}
		rel, relErr := filepath.Rel(repoRoot, filepath.Dir(path))
		if relErr != nil {
			return nil //nolint:nilerr // outside the tree is not this test's business.
		}
		seen[filepath.ToSlash(rel)] = true
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}

	out := make([]string, 0, len(seen))
	for pkg := range seen {
		out = append(out, pkg)
	}
	slices.Sort(out)
	return out
}

// buildsRequest reports whether a file constructs a transport.Request.
func buildsRequest(file *ast.File) bool {
	found := false
	ast.Inspect(file, func(n ast.Node) bool {
		lit, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}
		sel, ok := lit.Type.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkg, ok := sel.X.(*ast.Ident)
		if ok && pkg.Name == "transport" && sel.Sel.Name == "Request" {
			found = true
			return false
		}
		return true
	})
	return found
}
