package lint_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/kmoneil/jira-cli/internal/auth"
	"github.com/kmoneil/jira-cli/internal/idem"
	"github.com/kmoneil/jira-cli/internal/jctx"
	"github.com/kmoneil/jira-cli/internal/site"
)

// architectureDoc publishes the mode of every file and directory this tool
// writes.
const architectureDoc = "../../docs/architecture.md"

// docModeRow matches a row of the permissions table: a backticked path, a
// description, and an octal mode. Anchored on the mode being four octal digits
// so a table elsewhere in the document cannot be read as this one.
var docModeRow = regexp.MustCompile("^\\|\\s*`([^`]+)`\\s*\\|[^|]*\\|\\s*(0[0-7]{3})\\s*\\|")

// writers drives the real write path for each documented path and returns what
// to stat.
//
// Real write paths, not os.WriteFile with the mode the test expects: the point
// is what the shipped code produces. A test that wrote the file itself would
// assert its own arithmetic and pass forever.
var writers = map[string]func(t *testing.T, root string) string{
	"$XDG_CONFIG_HOME/jr/": func(t *testing.T, root string) string {
		return filepath.Dir(saveConfig(t, root))
	},
	"$XDG_CONFIG_HOME/jr/config.toml": saveConfig,

	"$XDG_STATE_HOME/jr/": func(t *testing.T, root string) string {
		return filepath.Dir(saveCredentials(t, root))
	},
	"$XDG_STATE_HOME/jr/credentials.toml": saveCredentials,
	"$XDG_STATE_HOME/jr/idempotency.toml": saveLedger,

	"$XDG_CACHE_HOME/jr/<site>/": func(t *testing.T, root string) string {
		return filepath.Dir(saveCacheEntry(t, root))
	},
	"$XDG_CACHE_HOME/jr/<site>/<key>.json": saveCacheEntry,
}

// TestTheDocumentedModesAreTheOnesOnDisk holds the permissions table to the
// code.
//
// A mode in a document that nothing checks is a mode that was true once. This
// table had three rows and one of them was already describing the standard
// library rather than a decision: idempotency.toml was documented at 0600 and
// reached it because os.CreateTemp happens to use 0600, with no Chmod anywhere
// and nothing reading the mode back. Swapping that write for os.WriteFile would
// have moved the file to 0666&^umask with the document still saying 0600.
//
// The same failure the profile-count table had, and the same fix: read the
// document, drive the real code, compare.
func TestTheDocumentedModesAreTheOnesOnDisk(t *testing.T) {
	documented := modesFromDoc(t)
	if len(documented) == 0 {
		t.Fatalf("%s: no permissions table found; this test asserted nothing",
			architectureDoc)
	}

	for _, path := range slices.Sorted(maps(documented)) {
		want := documented[path]
		write, known := writers[path]
		if !known {
			t.Errorf("%s documents %s at %04o, and no writer here produces it.\n"+
				"Add one to `writers`, or the row is a claim nothing checks.",
				architectureDoc, path, want)
			continue
		}
		t.Run(path, func(t *testing.T) {
			got := modeOf(t, write(t, t.TempDir()))
			if got != want {
				t.Errorf("%s is %04o on disk; %s says %04o",
					path, got, architectureDoc, want)
			}
		})
	}

	// The other half: a writer with no row is a path whose mode nobody
	// published, which is how the directories went undocumented at 0755.
	for path := range writers {
		if _, ok := documented[path]; !ok {
			t.Errorf("`writers` covers %s, which %s does not document",
				path, architectureDoc)
		}
	}
}

func saveConfig(t *testing.T, root string) string {
	t.Helper()
	path := filepath.Join(root, "jr", "config.toml")
	cfg, err := jctx.Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if err := cfg.Set("work", jctx.Context{Site: "acme.atlassian.invalid"}); err != nil {
		t.Fatalf("set: %v", err)
	}
	if err := cfg.Save(); err != nil {
		t.Fatalf("save config: %v", err)
	}
	return path
}

func saveCredentials(t *testing.T, root string) string {
	t.Helper()
	path := filepath.Join(root, "jr", "credentials.toml")
	store := auth.FileStore{Path: path}
	err := store.Save("acme.atlassian.invalid", auth.Credential{
		Scheme: auth.Bearer, Secret: auth.Secret("not-a-real-token"),
	})
	if err != nil {
		t.Fatalf("save credential: %v", err)
	}
	return path
}

func saveLedger(t *testing.T, root string) string {
	t.Helper()
	path := filepath.Join(root, "jr", "idempotency.toml")
	ledger := &idem.Ledger{Path: path}
	if _, err := ledger.Claim("acme.atlassian.invalid", "k1", "issue create"); err != nil {
		t.Fatalf("claim: %v", err)
	}
	return path
}

func saveCacheEntry(t *testing.T, root string) string {
	t.Helper()
	dir := filepath.Join(root, "jr", "acme.atlassian.invalid")
	cache := &site.Cache{Dir: dir, Now: func() time.Time { return time.Unix(0, 0).UTC() }}
	if err := cache.Put("deployment", map[string]string{"type": "cloud"}); err != nil {
		t.Fatalf("cache put: %v", err)
	}
	return filepath.Join(dir, "deployment.json")
}

func modeOf(t *testing.T, path string) fs.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return info.Mode().Perm()
}

func modesFromDoc(t *testing.T) map[string]fs.FileMode {
	t.Helper()

	out := map[string]fs.FileMode{}
	for _, line := range readLines(t, architectureDoc) {
		m := docModeRow.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		n, err := strconv.ParseUint(m[2], 8, 32)
		if err != nil {
			t.Fatalf("%s: %q has an unreadable mode", architectureDoc, line)
		}
		out[m[1]] = fs.FileMode(n)
	}
	return out
}
