package cli_test

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/kmoneil/jr/internal/cli"
	"github.com/kmoneil/jr/internal/exitcode"

	// Every command the binary ships, not only the built-ins.
	_ "github.com/kmoneil/jr/internal/commands"
)

// truncationProofs names, for every command that declares exit 3, the test that
// drives it past its limit and checks it says so.
//
// A declared exit code is a claim, and this is the only thing in the tree that
// asks whether the claim is true. `user list` declared exit 3 for the whole life
// of the command and could not produce it: the caller's limit reached the server
// as maxResults, so the comparison that would have set complete="false" was
// testing a number the request had already enforced. Every test passed. The
// fixtures were honest. Nothing was in a position to notice, because nothing was
// looking at the set of commands as a set.
//
// A manifest rather than a driven test, deliberately. Driving an arbitrary
// command from here means a session, a cassette, and a project or board for the
// ones that need them — all of which live in the resource's own test package
// beside its fixtures. What can be checked centrally is that every command has
// a proof and that the proof still exists, which catches the two ways this
// decays: a new paginated command written without one, and a rename or deletion
// that quietly removes one.
//
// The entry is a path and a test function. Adding a command here without
// writing the test does not compile away — the file and the function are both
// checked below.
var truncationProofs = map[string]string{
	// Fetch-all listings. Each holds the whole set before trimming, so a limit
	// below the fixture's row count exercises the branch.
	"board.list":       "internal/resource/board/board_test.go:TestListTruncatesAndSaysSo",
	"epic.list":        "internal/resource/epic/epic_test.go:TestListTruncatesAndSaysSo",
	"sprint.list":      "internal/resource/sprint/sprint_test.go:TestListTruncatesAndSaysSo",
	"project.list":     "internal/resource/project/project_test.go:TestListTruncatesAndSaysSo",
	"field.list":       "internal/resource/field/field_test.go:TestLimitTruncatesAndSaysSo",
	"meta.transitions": "internal/resource/meta/meta_test.go:TestLimitTruncatesAndSaysSo",
	"meta.createmeta":  "internal/resource/meta/meta_test.go:TestLimitTruncatesAndSaysSo",

	// Paged subresources. These stop when the limit is reached rather than
	// trimming afterwards, so the proof has to drive the paging.
	"issue.list":         "internal/resource/issue/issue_test.go:TestLimitTruncatesAndSaysSo",
	"issue.comment.list": "internal/resource/issue/comment_test.go:TestCommentListTruncatesAndSaysSo",
	"issue.link.list":    "internal/resource/issue/link_worklog_test.go:TestLinkListTruncatesAndSaysSo",
	"issue.worklog.list": "internal/resource/issue/link_worklog_test.go:TestWorklogListTruncatesAndSaysSo",

	"issue.attachment.list": "internal/resource/issue/attachment_test.go:TestAttachmentListTruncatesAndSaysSo",
	"project.components":    "internal/resource/project/project_test.go:TestProjectPartsTruncateAndSaySo",
	"project.versions":      "internal/resource/project/project_test.go:TestProjectPartsTruncateAndSaySo",
	"project.statuses":      "internal/resource/project/project_test.go:TestProjectPartsTruncateAndSaySo",

	// Bounded by the server rather than trimmed here, which is why its proof
	// looks different: the fixture answers the probe in full instead of the
	// limit being set below the row count.
	"user.list": "internal/resource/user/user_test.go:TestAFullPageIsNeverReportedComplete",

	// Not collections. `issue get` is a record that can hold part of a paged
	// comment thread, which is the only way a record is ever incomplete.
	"issue.get": "internal/resource/issue/issue_test.go:TestATruncatedThreadIsNeverReportedAsComplete",

	// Local, so they need no cassette — and both prove it end to end through
	// cli.Main, which is the stronger form: the real exit code and the real
	// stderr, which is the pair a script actually checks.
	"context.list": "internal/cli/auth_context_test.go:TestContextListTruncatesAndSaysSo",
	"schema":       "internal/cli/cli_test.go:TestSchemaIsCompleteWithNoFlags",
}

// funcDecl matches a test function declaration by name.
func funcDecl(name string) *regexp.Regexp {
	return regexp.MustCompile(`(?m)^func ` + regexp.QuoteMeta(name) + `\(`)
}

// TestEveryCommandDeclaringPartialCanProduceIt is the set-level question no
// per-resource test is in a position to ask.
func TestEveryCommandDeclaringPartialCanProduceIt(t *testing.T) {
	const repoRoot = "../.."

	var declaring []string
	registered := map[string]bool{}
	for _, c := range cli.Registry().All() {
		registered[c.Name()] = true
		if slices.Contains(c.ExitCodes, exitcode.Partial) {
			declaring = append(declaring, c.Name())
		}
	}
	if len(declaring) == 0 {
		t.Fatal("this build declares exit 3 nowhere; the enumeration is broken")
	}

	for _, name := range declaring {
		t.Run(name, func(t *testing.T) {
			proof, ok := truncationProofs[name]
			if !ok {
				t.Fatalf("%s declares exit %d (%s) and no test proves it can emit one.\n"+
					"Write one that drives the command past its limit and asserts "+
					"complete is false, then name it in truncationProofs. If the "+
					"command cannot truncate, it should not declare the code.",
					name, exitcode.Partial, exitcode.Partial)
			}
			file, fn, found := strings.Cut(proof, ":")
			if !found {
				t.Fatalf("truncationProofs[%q] = %q, want path:TestName", name, proof)
			}
			data, err := os.ReadFile(filepath.Join(repoRoot, file)) //nolint:gosec // a path from the test tree.
			if err != nil {
				t.Fatalf("%s names %s as its proof and the file is gone: %v", name, file, err)
			}
			if !funcDecl(fn).Match(data) {
				t.Errorf("%s names %s in %s as its proof, and no such test exists there — "+
					"it was renamed or deleted, and nothing else would have said so",
					name, fn, file)
			}
		})
	}

	// The manifest must not outlive the commands. An entry for a command that
	// has stopped declaring exit 3 reads as coverage and is not.
	//
	// Only for commands this build registers. Every entry happens to be a read
	// present in all four profiles today, so a flat check passes — but the
	// first paginated command behind a tag would fail this under the ci build
	// for being absent rather than for being wrong, and the fix would look like
	// deleting a live entry.
	for name := range truncationProofs {
		if registered[name] && !slices.Contains(declaring, name) {
			t.Errorf("truncationProofs names %q, which no longer declares exit %d",
				name, exitcode.Partial)
		}
	}
}
