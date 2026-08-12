package lint_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
)

// recordedFixtures are the cassettes whose Cloud conversation has been recorded
// against a real instance, keyed by the package they live in, and which must
// stay recorded.
//
// The list is short because recording is manual: it needs a live sandbox, a
// scrub map, and somebody to read the residue report. It is not a target to
// grow for its own sake — a cassette earns a place here when its request shape
// is load-bearing enough that being wrong about it would be expensive.
//
// What this guards is regression, not coverage. Replacing a recording with a
// hand-written cassette leaves every test that replays it passing while it
// stops asserting anything about the API, and nothing else in the suite can
// tell the difference: a cassette that was written and one that was recorded
// replay identically. That is precisely how three fixtures came to encode
// requests Jira rejects — a removed createmeta route, validateQuery as a string
// where a boolean is required, a missing expand — with green tests throughout.
//
// The key is a package path rather than a resource name because
// internal/workflow is not under internal/resource, and it holds the fixture
// with the best claim to a place here: epicadd.cloud.json asserted a request
// Jira refuses on most Cloud projects for two releases, and the recording that
// replaced it is the only thing that makes the refusal beside it legible.
var recordedFixtures = map[string][]string{
	"internal/resource/project": {
		"projects.cloud.json", "project.cloud.json", "statuses.cloud.json",
		"versions-empty.cloud.json", "components-empty.cloud.json",
	},
	"internal/resource/user": {"search.cloud.json", "me.cloud.json", "user.cloud.json"},
	"internal/resource/jql":  {"parse.cloud.json", "invalid.cloud.json"},
	"internal/resource/issue": {
		"list-recorded.cloud.json",
		// Both halves of `issue history`, because the command is two
		// implementations and the deployments disagree about the endpoint, the
		// key the entries live under, and whether paging works at all. Either
		// one becoming hand-written would leave half a claim evidenced.
		"history-recorded.cloud.json",
		// The same two requests against both project styles. The symmetry is
		// the claim — that the parent field needs no branch on style — so one
		// of these quietly becoming hand-written would leave the claim
		// half-evidenced and still green.
		"parentset-classic.cloud.json", "parentclear-classic.cloud.json",
		"parentset-nextgen.cloud.json", "parentclear-nextgen.cloud.json",
	},
	"internal/resource/board": {"boards-recorded.cloud.json", "board-recorded.cloud.json"},
	"internal/resource/epic":  {"epics-recorded.cloud.json", "epic-recorded.cloud.json"},
	"internal/resource/sprint": {
		"sprints-recorded.cloud.json", "sprint-recorded.cloud.json",
		// The same sprint before and after it was started, so both renderings
		// of the date columns rest on a server rather than on a guess.
		"sprints-active-recorded.cloud.json", "sprint-active-recorded.cloud.json",
		// The expensive one. Closing a sprint cannot be undone, so this cost
		// the sandbox's only sprint and cannot be re-recorded without somebody
		// making a new one by hand.
		"close-recorded.cloud.json",
	},
	"internal/workflow": {
		"epicadd.cloud.json", "epicremove.cloud.json", "sprintadd.cloud.json",
		// The before and after of replacing the agile epic endpoint: the same
		// operation against a team-managed project, refused and then working.
		// Either one alone is half an argument.
		"epicadd-nextgen.cloud.json", "epicadd-nextgen-parent.cloud.json",
	},
}

// repoRoot is where the lint package sits relative to the module, matching the
// other tests here.
const repoRoot = "../.."

// TestRecordedFixturesStayRecorded fails if one of them is no longer evidence.
func TestRecordedFixturesStayRecorded(t *testing.T) {
	root := repoRoot

	for pkg, fixtures := range recordedFixtures {
		for _, name := range fixtures {
			path := filepath.Join(root, filepath.FromSlash(pkg), "testdata", name)

			data, err := os.ReadFile(path) //nolint:gosec // a path from the test tree.
			if err != nil {
				t.Errorf("%s/%s: %v — a recording was deleted or renamed",
					pkg, name, err)
				continue
			}
			var c struct {
				Source string `json:"source"`
			}
			if err := json.Unmarshal(data, &c); err != nil {
				t.Errorf("%s/%s: %v", pkg, name, err)
				continue
			}
			if c.Source != "recorded" {
				t.Errorf("%s/%s is source=%q, want \"recorded\" — a hand-written "+
					"cassette replays exactly like a recorded one and asserts "+
					"nothing about the API", pkg, name, c.Source)
			}
		}
	}
}

// unrecorded is every package-and-deployment whose cassettes are all
// hand-written, with the reason there is no recording behind any of them.
//
// It exists because TestRecordedFixturesStayRecorded ratchets in one direction
// only. That test keeps the eleven recordings from silently becoming
// hand-written, and it is a curated list, so it is structurally unable to
// notice a deployment with no recording at all — a resource can ship with zero
// evidence and every gate stays green. When this was written, all eleven
// recordings were Cloud and **nothing in the tree about Data Center rested on
// a recording**: not one of nine deployment groups, against 62 cassettes
// asserting what somebody believed Data Center does.
//
// That is the deployment where being wrong is cheapest to do and most
// expensive to find. Cloud and Data Center differ in API version, pagination
// shape, and body format, and a constructed cassette gets exactly those wrong
// quietly — `jql validate` took validateQuery as a boolean, `project list`
// never populated its lead column, `meta createmeta` called a route removed in
// Jira 9.0. All three passed for months.
//
// An entry is a debt with a name, not an exemption. It says which conversation
// nobody has evidence for and why, and the test below refuses a stale one: the
// moment a recording lands, its entry has to go, so this list can only shrink.
var unrecorded = map[string]string{
	// The four Cloud agile rows were paid off on 2026-08-10, when a
	// company-managed project with a scrum board and a sprint was added to the
	// sandbox. Every Cloud verb in this tree has now been run against a real
	// Jira, `sprint close` last and once: closing a sprint cannot be undone, so
	// that recording cost the sandbox's only iteration and re-recording it needs
	// a new sprint made by hand in the UI.

	// The nine Data Center rows were paid off on 2026-08-11. They had one cause
	// between them — the only Data Center available was production — and it
	// turned out to be a licence problem rather than an access problem.
	// Atlassian's self-serve trials ended in March 2026 and Data Center is
	// end-of-life, but the timebomb licence published for running a Data Center
	// product without the SDK still licenses a local container for three hours
	// per install. `scripts/dc` is that container, and `make dc-up dc-record`
	// is how every one of these recordings is regenerated.
	//
	// This map being empty is the state to keep it in. An entry here is a
	// conversation nobody has evidence for.
}

// TestEveryDeploymentIsBackedByARecordingOrSaysWhyNot holds each package's
// cassettes, per deployment, to having at least one recording behind them.
//
// Grouping by deployment is the whole point. A package with a recorded Cloud
// cassette and twelve written Data Center ones has evidence for one of the two
// APIs it claims to speak, and counting files or counting recordings per
// package would both call that covered.
//
// The list only shrinks: an entry naming a group that now has a recording is a
// failure, not a no-op, so paying a debt off forces the ledger to be updated
// rather than leaving a row that reads as outstanding forever.
func TestEveryDeploymentIsBackedByARecordingOrSaysWhyNot(t *testing.T) {
	groups := cassetteGroups(t)

	// A walk that matched nothing would pass silently, which for a guard is
	// worse than failing.
	if len(groups) < 15 {
		t.Fatalf("found only %d package-and-deployment groups, so this walked "+
			"the wrong tree", len(groups))
	}

	for _, key := range slices.Sorted(maps(groups)) {
		pkg, deployment, _ := strings.Cut(key, " ")
		reason, excused := unrecorded[key]

		switch {
		case groups[key] > 0 && excused:
			t.Errorf("%s has a recorded %s cassette now — delete its entry from "+
				"unrecorded, or the list reads as debt that was already paid",
				pkg, deployment)
		case groups[key] == 0 && !excused:
			t.Errorf("no %s cassette under %s is source=\"recorded\", so nothing "+
				"in this package is evidence about that API. Record one, or add "+
				"%q to unrecorded with the reason you cannot",
				deployment, pkg, key)
		case groups[key] == 0 && reason == "":
			t.Errorf("%s is excused with an empty reason; an exemption with no "+
				"argument is the drift it exists to prevent", key)
		}
	}

	for _, key := range slices.Sorted(maps(unrecorded)) {
		if _, ok := groups[key]; !ok {
			t.Errorf("unrecorded names %q and no cassette in the tree is in that "+
				"group — it was renamed or deleted", key)
		}
	}
}

// cassetteGroups counts the recordings in each package-and-deployment group,
// keyed "<package> <deployment>". A group with cassettes and no recording is
// present with a count of zero, which is the case the test is about.
//
// The deployment comes from the cassette's own field rather than its filename,
// because the filename is a convention and the field is what the replayer
// reads.
func cassetteGroups(t *testing.T) map[string]int {
	t.Helper()
	groups := map[string]int{}

	err := filepath.WalkDir(repoRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".json") {
			return err
		}
		if !strings.Contains(path, string(filepath.Separator)+"testdata"+string(filepath.Separator)) {
			return nil
		}
		data, readErr := os.ReadFile(path) //nolint:gosec // a path from the test tree.
		if readErr != nil {
			return nil //nolint:nilerr // unreadable is TestEveryFixtureDeclaresItsSource's business.
		}
		var c struct {
			Source       string          `json:"source"`
			Deployment   string          `json:"deployment"`
			Interactions json.RawMessage `json:"interactions"`
		}
		if err := json.Unmarshal(data, &c); err != nil || len(c.Interactions) == 0 {
			return nil //nolint:nilerr // not a cassette, so not this guard's business.
		}
		if c.Deployment == "" {
			t.Errorf("%s is a cassette with no deployment, so it cannot be held "+
				"to either API's shape", path)
			return nil
		}

		rel, _ := filepath.Rel(repoRoot, path)
		pkg, _, _ := strings.Cut(filepath.ToSlash(rel), "/testdata/")
		key := pkg + " " + c.Deployment
		if _, seen := groups[key]; !seen {
			groups[key] = 0
		}
		if c.Source == "recorded" {
			groups[key]++
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	return groups
}

// TestEveryFixtureDeclaresItsSource keeps the distinction from decaying by
// default.
//
// A cassette with no source field reads as constructed, which is the safe
// reading but a silent one. Requiring the field means a new fixture states
// which kind it is rather than inheriting the weaker claim by omission — and
// somebody adding one has to decide, which is the point.
func TestEveryFixtureDeclaresItsSource(t *testing.T) {
	root := repoRoot
	var undeclared []string

	seen := 0
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".json") {
			return err
		}
		if !strings.Contains(path, string(filepath.Separator)+"testdata"+string(filepath.Separator)) {
			return nil
		}

		data, readErr := os.ReadFile(path) //nolint:gosec // a path from the test tree.
		if readErr != nil {
			// Unreadable is a separate problem from undeclared, and the tests
			// that use the file will say so more usefully than this can.
			return nil //nolint:nilerr // not this guard's business.
		}
		var c struct {
			Source       string          `json:"source"`
			Interactions json.RawMessage `json:"interactions"`
		}
		// Not every JSON file under testdata is a cassette: golden files and
		// config samples live there too, and neither has interactions.
		if err := json.Unmarshal(data, &c); err != nil || len(c.Interactions) == 0 {
			return nil //nolint:nilerr // not a cassette, so not this guard's business.
		}
		seen++
		if c.Source == "" {
			rel, _ := filepath.Rel(root, path)
			undeclared = append(undeclared, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}

	// A walk that matched nothing would pass silently, which for a guard is
	// worse than failing.
	if seen < 50 {
		t.Fatalf("found only %d cassettes, so this walked the wrong tree", seen)
	}

	if len(undeclared) > 0 {
		sort.Strings(undeclared)
		t.Errorf("%d cassette(s) do not say whether they were recorded or "+
			"written:\n  %s", len(undeclared), strings.Join(undeclared, "\n  "))
	}
}
