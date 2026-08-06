package lint_test

import (
	"encoding/json"
	"os"
	"path/filepath"
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
