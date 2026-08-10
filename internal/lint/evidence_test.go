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

// recordedResources are the resources whose Cloud conversation has been
// recorded against a real instance, and must stay recorded.
//
// The list is short because recording is manual: it needs a live sandbox, a
// scrub map, and somebody to read the residue report. It is not a target to
// grow for its own sake — a resource earns a place here when its request shape
// is load-bearing enough that being wrong about it would be expensive.
//
// What this guards is regression, not coverage. Replacing a recording with a
// hand-written cassette leaves every test that replays it passing while it
// stops asserting anything about the API, and nothing else in the suite can
// tell the difference: a cassette that was written and one that was recorded
// replay identically. That is precisely how three fixtures came to encode
// requests Jira rejects — a removed createmeta route, validateQuery as a string
// where a boolean is required, a missing expand — with green tests throughout.
var recordedResources = map[string][]string{
	"project": {
		"projects.cloud.json", "project.cloud.json", "statuses.cloud.json",
		"versions-empty.cloud.json", "components-empty.cloud.json",
	},
	"user":  {"search.cloud.json", "me.cloud.json", "user.cloud.json"},
	"jql":   {"parse.cloud.json", "invalid.cloud.json"},
	"issue": {"list-recorded.cloud.json"},
}

// repoRoot is where the lint package sits relative to the module, matching the
// other tests here.
const repoRoot = "../.."

// TestRecordedFixturesStayRecorded fails if one of them is no longer evidence.
func TestRecordedFixturesStayRecorded(t *testing.T) {
	root := repoRoot

	for resource, fixtures := range recordedResources {
		for _, name := range fixtures {
			path := filepath.Join(root, "internal", "resource", resource, "testdata", name)

			data, err := os.ReadFile(path) //nolint:gosec // a path from the test tree.
			if err != nil {
				t.Errorf("%s/%s: %v — a recording was deleted or renamed",
					resource, name, err)
				continue
			}
			var c struct {
				Source string `json:"source"`
			}
			if err := json.Unmarshal(data, &c); err != nil {
				t.Errorf("%s/%s: %v", resource, name, err)
				continue
			}
			if c.Source != "recorded" {
				t.Errorf("%s/%s is source=%q, want \"recorded\" — a hand-written "+
					"cassette replays exactly like a recorded one and asserts "+
					"nothing about the API", resource, name, c.Source)
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
	"internal/resource/board cloud": "the Cloud sandbox has no scrum board, so " +
		"there is no board to read; blocked on creating one, which is UI work " +
		"and not code",
	"internal/resource/epic cloud": "same board that internal/resource/board " +
		"needs — an epic is read through the agile API and the sandbox has no " +
		"agile content",
	"internal/resource/sprint cloud": "same board again; without a sprint there " +
		"is nothing for `sprint list` or `sprint get` to answer with",
	"internal/workflow cloud": "the agile write verbs need a board with issues " +
		"to move between containers, and `sprint close` ends a real iteration",

	// Every Data Center row has one cause, written out once per row rather than
	// once for the group, because a row is what somebody deletes and the reason
	// has to travel with it.
	"internal/resource/board datacenter":   dataCenterBlocked,
	"internal/resource/epic datacenter":    dataCenterBlocked,
	"internal/resource/issue datacenter":   dataCenterBlocked,
	"internal/resource/jql datacenter":     dataCenterBlocked,
	"internal/resource/project datacenter": dataCenterBlocked,
	"internal/resource/sprint datacenter":  dataCenterBlocked,
	"internal/resource/user datacenter":    dataCenterBlocked,
	"internal/transport datacenter":        dataCenterBlocked,
	"internal/workflow datacenter":         dataCenterBlocked,
}

// dataCenterBlocked is why no Data Center conversation in this tree is
// evidence. The only Data Center instance available is production, and
// recording against production is refused; a local container with an
// evaluation licence is the way out, and the agile endpoints need Jira
// Software rather than Core.
const dataCenterBlocked = "the only Data Center instance available is " +
	"production and recording against it is refused; needs a local container"

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
