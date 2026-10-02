package project_test

import (
	"io"
	"strings"
	"testing"

	"github.com/kmoneil/jr/internal/errs"
	"github.com/kmoneil/jr/internal/registry"
	"github.com/kmoneil/jr/internal/render"
	"github.com/kmoneil/jr/internal/resource/project"
	"github.com/kmoneil/jr/internal/site"
)

// categoryFixtures names the recorded conversations, one per deployment: the
// sandbox for Cloud and the rig for Data Center. On each server one project
// is filed under "Platform"; the Cloud recording also holds two projects with
// no category at all, which is the absent case the rig's single project
// cannot show.
var categoryFixtures = map[site.Kind]struct {
	list, get, filed string
}{
	site.Cloud: {
		list: "projects-category-recorded.cloud.json",
		get:  "project-get-category-recorded.cloud.json",
		// AGL carries the category; ENG and OPS carry none.
		filed: "AGL",
	},
	site.DataCenter: {
		list:  "projects-category-recorded.datacenter.json",
		get:   "project-get-category-recorded.datacenter.json",
		filed: "ENG",
	},
}

// TestCategoryIsReadOnBothDeployments covers the decode: the field arrives as
// a full object with no expand, and a project without one omits the key
// entirely, so absence has to stay absence.
func TestCategoryIsReadOnBothDeployments(t *testing.T) {
	for kind, tc := range categoryFixtures {
		t.Run(string(kind), func(t *testing.T) {
			conn, replayer := replayConn(t, tc.list)
			client := &project.Client{Transport: conn, Site: site.Info{Kind: kind}}

			projects, err := client.List(t.Context())
			if err != nil {
				t.Fatalf("list: %v", err)
			}
			if unplayed := replayer.Unplayed(); len(unplayed) > 0 {
				t.Errorf("the catalogue was never read: %v", unplayed)
			}

			var filed, bare int
			for _, p := range projects {
				if p.Key == tc.filed {
					if p.Category != "Platform" {
						t.Errorf("%s category = %q, want Platform", p.Key, p.Category)
					}
					if p.CategoryID == "" {
						t.Errorf("%s lost its category id", p.Key)
					}
					filed++
					continue
				}
				if p.Category != "" || p.CategoryID != "" {
					t.Errorf("%s has no category and reports %q", p.Key, p.Category)
				}
				bare++
			}
			if filed != 1 {
				t.Errorf("%d projects filed under Platform, want 1", filed)
			}
			if kind == site.Cloud && bare != 2 {
				t.Errorf("%d uncategorised projects, want the recorded 2", bare)
			}
		})
	}
}

// TestCategoryKeepsTheExactNameIgnoringCase is the filter. Unlike --match it
// is not a substring search: a category is a closed set an administrator
// curates, so a fragment matching nothing is the honest answer.
func TestCategoryKeepsTheExactNameIgnoringCase(t *testing.T) {
	for _, tc := range []struct {
		name  string
		names []string
		want  string
	}{
		{"the exact name, ignoring case", []string{"platform"}, "AGL"},
		{"any of several", []string{"nonesuch", "PLATFORM"}, "AGL"},
		// A substring is not a category, and a project with no category never
		// matches anything, so neither keeps a row.
		{"a fragment", []string{"Plat"}, ""},
		{"an unknown name", []string{"Storage"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, result, _ := runListFixture(t, site.Cloud, registry.Limit{All: true},
				categoryFixtures[site.Cloud].list,
				func(f registry.Flags) {
					for _, name := range tc.names {
						f.SetString("category", name)
					}
				})
			if !result.Complete {
				t.Error("a filtered catalogue read whole was reported incomplete")
			}
			var keys []string
			lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
			for _, line := range lines[1:] {
				if line == "" {
					continue
				}
				keys = append(keys, strings.SplitN(line, "\t", 2)[0])
			}
			if got := strings.Join(keys, ","); got != tc.want {
				t.Errorf("--category %v kept %q, want %q", tc.names, got, tc.want)
			}
		})
	}
}

// TestABlankCategoryIsRefused mirrors the blank-match refusal: nothing is
// filed under "", so `--category "$TEAM"` with TEAM unset must refuse rather
// than answer an empty catalogue as if it had been filtered.
func TestABlankCategoryIsRefused(t *testing.T) {
	cmd, _ := registry.Lookup("project.list")
	for _, name := range []string{"", "  "} {
		flags := registry.NewFlags()
		flags.SetString("category", name)
		err := cmd.Validate(t.Context(), &registry.Invocation{Flags: flags})
		if err == nil {
			t.Errorf("--category %q was accepted", name)
			continue
		}
		if code := errs.Coerce(err).Code; code != "EMPTY_QUERY" {
			t.Errorf("--category %q refused as %s, want EMPTY_QUERY", name, code)
		}
	}
}

// TestWithCategoryAddsTheColumn covers the ask. The default table keeps its
// four columns, because adding one to the default set is a breaking change;
// the fifth appears only when asked for, through the same ColumnsFor hook the
// CLI and MCP both read.
func TestWithCategoryAddsTheColumn(t *testing.T) {
	cmd, ok := registry.Lookup("project.list")
	if !ok {
		t.Fatal("project list is not registered")
	}

	for _, tc := range []struct {
		name, header string
		with         bool
	}{
		{"without the flag", "key\tname\ttype\tlead", false},
		{"with the flag", "key\tname\ttype\tlead\tcategory", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			conn, _ := replayConn(t, categoryFixtures[site.Cloud].list)
			flags := registry.NewFlags()
			if tc.with {
				flags.SetBool("with-category", true)
			}
			inv := &registry.Invocation{
				Jira:  &stubSession{conn: conn, kind: site.Cloud},
				Flags: flags, Limit: registry.Limit{All: true},
				Stderr: io.Discard, Progress: registry.NoProgress,
			}

			var buf strings.Builder
			stream, err := render.NewStream(&buf, render.TSV, render.StreamSpec{
				Kind: cmd.Kind(), Version: cmd.KindVersion(),
				Name: cmd.CollectionName, Columns: cmd.ColumnsFor(inv),
			})
			if err != nil {
				t.Fatalf("stream: %v", err)
			}
			result, err := cmd.Stream(t.Context(), inv, stream)
			if err != nil {
				t.Fatalf("run: %v", err)
			}
			if err := stream.Close(result.Complete, result.NextPageToken); err != nil {
				t.Fatalf("close: %v", err)
			}

			lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
			if lines[0] != tc.header {
				t.Errorf("header = %q, want %q", lines[0], tc.header)
			}
			if !tc.with {
				return
			}
			// The filed project fills the cell and the bare ones leave it
			// empty rather than invented.
			for _, line := range lines[1:] {
				cells := strings.Split(line, "\t")
				key, got := cells[0], cells[len(cells)-1]
				want := ""
				if key == categoryFixtures[site.Cloud].filed {
					want = "Platform"
				}
				if got != want {
					t.Errorf("%s category cell = %q, want %q", key, got, want)
				}
			}
		})
	}
}

// TestProjectGetCarriesTheCategory covers the record. The schema is shared
// with the listing, so the fetched project reports the same element on the
// same terms: present with the server's id and name, absent when the project
// has none.
func TestProjectGetCarriesTheCategory(t *testing.T) {
	cmd, ok := registry.Lookup("project.get")
	if !ok {
		t.Fatal("project get is not registered")
	}

	for kind, tc := range categoryFixtures {
		t.Run(string(kind), func(t *testing.T) {
			conn, replayer := replayConn(t, tc.get)
			doc, err := cmd.Run(t.Context(), &registry.Invocation{
				Jira: &stubSession{conn: conn, kind: kind},
				Args: []string{tc.filed}, Flags: registry.NewFlags(),
				Stderr: io.Discard, Progress: registry.NoProgress,
			})
			if err != nil {
				t.Fatalf("run: %v", err)
			}
			if unplayed := replayer.Unplayed(); len(unplayed) > 0 {
				t.Errorf("the project was never read: %v", unplayed)
			}

			category, ok := doc.Record.ChildNamed("category")
			if !ok {
				t.Fatal("the record carries no category")
			}
			if category.Text != "Platform" {
				t.Errorf("category = %q, want Platform", category.Text)
			}
			if id, _ := category.AttrValue("id"); id == "" {
				t.Error("the category lost its id")
			}
		})
	}
}
