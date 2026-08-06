package issue_test

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kmoneil/jira-cli/internal/render"
	"github.com/kmoneil/jira-cli/internal/resource/issue"
)

// costDump writes the four rendered payloads somewhere a token counter can read
// them. `make cost` passes it; nothing else does.
var costDump = flag.String("cost-dump", "",
	"write the 100-issue payload in every format to this directory")

// costSummaries are the summaries Jira Cloud actually returned for the sample
// project in the sandbox, taken from list-recorded.cloud.json.
//
// They are cycled to reach a hundred rows rather than invented, because the
// thing being measured is how much framing each format wraps around a value —
// and a made-up value of a made-up length would put the answer in the hands of
// whoever made it up.
var costSummaries = []string{
	"Finalize Documentation for the Project",
	"Optimize Performance of the Application",
	"Develop Transaction History Feature",
	"Create Wallet Integration",
	"Set Up Notifications for Users",
	"Post-Launch Review and Feedback Collection",
	"Prepare for Project Launch",
	"Conduct User Testing for the Platform",
	"Implement Market Analysis Tools",
	"Implement User Authentication",
}

// costStatuses are the four the sample project uses, with their categories.
var costStatuses = []issue.Status{
	{Name: "To Do", Category: issue.CategoryToDo},
	{Name: "In Progress", Category: issue.CategoryInProgress},
	{Name: "In Review", Category: issue.CategoryInProgress},
	{Name: "Done", Category: issue.CategoryDone},
}

// costAssignees includes an empty one, because roughly a third of a real
// backlog is unassigned and an absent value is where the formats differ most:
// TSV writes nothing between two tabs, XML and JSON still spell the field.
var costAssignees = []issue.User{
	{ID: "712020:8f3a2b1c-0d4e-4a5f-9b6c-7d8e9f0a1b2c", Display: "Ada Lovelace"},
	{ID: "712020:1a2b3c4d-5e6f-4a7b-8c9d-0e1f2a3b4c5d", Display: "Grace Hopper"},
	{},
}

// costCorpus builds the payload `issue list --limit 100` would render.
//
// It goes through issue.ListDoc and issue.ListColumns, which is the point: a
// measurement of a payload assembled by the test would be a measurement of the
// test. What comes out is the document the command emits.
func costCorpus(rows int) *render.Doc {
	issues := make([]issue.Issue, 0, rows)
	for i := range rows {
		// Descending by key, which is what every query orders by.
		num := rows - i
		issues = append(issues, issue.Issue{
			Key:      fmt.Sprintf("ENG-%d", num),
			ID:       fmt.Sprintf("1%04d", num),
			Summary:  costSummaries[i%len(costSummaries)],
			Status:   costStatuses[i%len(costStatuses)],
			Assignee: costAssignees[i%len(costAssignees)],
			Updated:  fmt.Sprintf("2026-08-%02dT%02d:%02d:07Z", 1+i%28, i%24, i%60),
			Created:  fmt.Sprintf("2026-07-%02dT%02d:%02d:07Z", 1+i%28, i%24, i%60),
		})
	}
	return issue.ListDoc(&issue.ListResult{Issues: issues, Complete: true})
}

// TestFormatCostFavoursTSVForCollections is §12.2 answered with a number.
//
// The spec left the list default open and said to settle it by measuring a real
// hundred-issue payload rather than by taste. This renders one — the document
// `issue list` emits, through the command's own columns — in all four formats
// and holds the result to the relationship the default rests on.
//
// The thresholds are floors with headroom, not the measurement. `make cost`
// prints the measurement, including token counts, and docs/output-contract.md
// records it with the date and the tokenizer it was taken with. What is
// asserted here is only the claim TSV-by-default depends on: that the
// structured formats cost multiples of it for the same rows, so a change to a
// writer cannot quietly erode the premise.
func TestFormatCostFavoursTSVForCollections(t *testing.T) {
	doc := costCorpus(100)

	sizes := map[render.Format]int{}
	for _, f := range render.Formats() {
		var b strings.Builder
		if err := render.Write(&b, doc, f); err != nil {
			t.Fatalf("%s: write: %v", f, err)
		}
		sizes[f] = b.Len()
		if *costDump != "" {
			path := filepath.Join(*costDump, "issues-100."+string(f))
			if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
				t.Fatalf("dump %s: %v", path, err)
			}
		}
		t.Logf("%-4s %7d bytes", f, b.Len())
	}

	tsv := sizes[render.TSV]
	if tsv == 0 {
		t.Fatal("the TSV rendering is empty")
	}
	// Floors with headroom under what was measured on 2026-08-06 — XML 4.39x,
	// JSON 5.65x, YAML 4.15x by bytes, and 4.35x / 5.45x / 4.39x by cl100k
	// tokens. A format that fell below one of these would not be the same
	// argument any more.
	floors := map[render.Format]float64{
		render.XML:  3.0,
		render.JSON: 3.5,
		render.YAML: 3.0,
	}
	for f, floor := range floors {
		ratio := float64(sizes[f]) / float64(tsv)
		if ratio < floor {
			t.Errorf("%s is %.2fx the size of TSV, and the default assumes at "+
				"least %.1fx; if this is real, docs/output-contract.md decided "+
				"§12.2 on a measurement that no longer holds", f, ratio, floor)
		}
	}
}

// costRecord builds the document `issue get` emits: one issue with the fields a
// list does not carry, including a description with mixed content.
func costRecord() *render.Doc {
	i := issue.Issue{
		Key:      "ENG-100",
		ID:       "10100",
		Summary:  "Finalize Documentation for the Project",
		Status:   issue.Status{Name: "In Review", Category: issue.CategoryInProgress},
		Assignee: costAssignees[0],
		Reporter: costAssignees[1],
		Type:     "Task",
		Priority: "Medium",
		Project:  "ENG",
		Created:  "2026-07-14T09:12:07Z",
		Updated:  "2026-08-04T11:32:07Z",
		Labels:   []string{"docs", "release"},
		Description: "## What is missing\n\nThe transport retry section still " +
			"describes the old budget.\n\n```go\nclient.Do(req)  // counts " +
			"against --max-requests\n```\n\nSee also: a < b && c > d.",
		BodyFormat: "markdown",
	}
	return render.Record(issue.KindGet, issue.VersionGet, i.Node())
}

// TestFormatCostIsNotTheArgumentForARecord is the other side of §12.2, and the
// reason the default is per content shape rather than one format everywhere.
//
// A hundred rows is where framing compounds: the format spells every field name
// a hundred times, and TSV spells it once. One record has one of everything, so
// the multiple collapses — and what is left is that a record carries nested and
// mixed content a rectangular format has nowhere to put. The default splits on
// content shape because the saving does.
func TestFormatCostIsNotTheArgumentForARecord(t *testing.T) {
	sizes := map[render.Format]int{}
	for _, f := range render.Formats() {
		var b strings.Builder
		if err := render.Write(&b, costRecord(), f); err != nil {
			t.Fatalf("%s: write: %v", f, err)
		}
		sizes[f] = b.Len()
		if *costDump != "" {
			path := filepath.Join(*costDump, "issue-one."+string(f))
			if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
				t.Fatalf("dump %s: %v", path, err)
			}
		}
		t.Logf("%-4s %7d bytes", f, b.Len())
	}

	// The collection ratio is over 4x. If a record ever reached that, the
	// content-shape split would be costing something worth reconsidering.
	ratio := float64(sizes[render.XML]) / float64(sizes[render.TSV])
	if ratio > 2.0 {
		t.Errorf("XML is %.2fx the size of TSV for a single record, and the "+
			"content-shape default assumes the multiple collapses here; §12.2 "+
			"was decided on that", ratio)
	}
}

// TestTSVCarriesTheSameRowsAsXML is the other half of the trade, and the reason
// the saving is not free.
//
// TSV emits the declared columns and nothing else. XML carries every attribute
// and child the schema allows, so `created`, `id`, the status category, the
// assignee's account id and the labels container survive a round trip through
// XML and do not exist in the TSV at all. The number in the doc is a saving on
// five columns, not on the issue.
func TestTSVCarriesTheSameRowsAsXML(t *testing.T) {
	doc := costCorpus(100)

	var tsv, xml strings.Builder
	if err := render.Write(&tsv, doc, render.TSV); err != nil {
		t.Fatalf("tsv: %v", err)
	}
	if err := render.Write(&xml, doc, render.XML); err != nil {
		t.Fatalf("xml: %v", err)
	}

	rows := strings.Count(strings.TrimRight(tsv.String(), "\n"), "\n")
	if rows != 100 {
		t.Fatalf("the TSV has %d data rows, want 100", rows)
	}
	if got := strings.Count(xml.String(), "<issue "); got != 100 {
		t.Fatalf("the XML has %d issues, want 100", got)
	}

	// What TSV does not carry. Each of these is in the XML for every row.
	for _, dropped := range []string{"created", "712020:", "to-do"} {
		if strings.Contains(tsv.String(), dropped) {
			t.Errorf("the TSV carries %q, so this test no longer describes "+
				"what the columns leave out", dropped)
		}
		if !strings.Contains(xml.String(), dropped) {
			t.Errorf("the XML does not carry %q", dropped)
		}
	}
}
