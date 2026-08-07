package issue_test

import (
	"encoding/json"
	"encoding/xml"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/kmoneil/jira-cli/internal/render"
	"github.com/kmoneil/jira-cli/internal/resource/issue"
)

// The parse side of §12.2.
//
// The token measurement says what a payload costs the model that reads it. This
// says what it costs the process that reads it — which for a tool built for
// scripts first is the half that runs on every invocation whether or not a
// model is involved.
//
// It decodes into typed structs for all four, rather than into a generic map
// for the self-describing formats and a struct for TSV. A generic decode
// allocates per key and would have measured that choice instead of the format.

// tsvIssue, xmlResult, docIssue: one shape per format, holding what that format
// carries. They are deliberately not one shared struct — TSV carries the five
// declared columns and the others carry the whole node, and flattening that
// difference away would hide the thing the numbers are about.
type tsvIssue struct {
	Key, Status, Assignee, Updated, Summary string
}

type xmlResult struct {
	Kind   string `xml:"kind,attr"`
	V      int    `xml:"v,attr"`
	Issues struct {
		Count    int  `xml:"count,attr"`
		Complete bool `xml:"complete,attr"`
		Issue    []struct {
			Key     string `xml:"key,attr"`
			ID      string `xml:"id,attr"`
			Summary string `xml:"summary"`
			Status  struct {
				Category string `xml:"category,attr"`
				Name     string `xml:",chardata"`
			} `xml:"status"`
			Assignee struct {
				ID      string `xml:"id,attr"`
				Display string `xml:"display,attr"`
			} `xml:"assignee"`
			Created string `xml:"created"`
			Updated string `xml:"updated"`
		} `xml:"issue"`
	} `xml:"issues"`
}

// docIssue serves JSON and YAML, whose object models are the same here.
type docResult struct {
	Kind     string `json:"kind" yaml:"kind"`
	V        int    `json:"v" yaml:"v"`
	Count    int    `json:"count" yaml:"count"`
	Complete bool   `json:"complete" yaml:"complete"`
	Issues   []struct {
		Key     string `json:"key" yaml:"key"`
		ID      string `json:"id" yaml:"id"`
		Summary string `json:"summary" yaml:"summary"`
		Status  struct {
			Category string `json:"category" yaml:"category"`
			Text     string `json:"text" yaml:"text"`
		} `json:"status" yaml:"status"`
		Assignee struct {
			ID      string `json:"id" yaml:"id"`
			Display string `json:"display" yaml:"display"`
		} `json:"assignee" yaml:"assignee"`
		Created string `json:"created" yaml:"created"`
		Updated string `json:"updated" yaml:"updated"`
	} `json:"issues" yaml:"issues"`
}

// parseTSV is the consumer half of the TSV contract: split on tabs, and undo
// the four escapes the writer applies. A consumer that skipped the unescaping
// would be faster and wrong on any summary containing a tab, which is the whole
// reason the escaping exists.
func parseTSV(s string) []tsvIssue {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) < 2 {
		return nil
	}
	out := make([]tsvIssue, 0, len(lines)-1)
	for _, line := range lines[1:] { // the header is not a row
		f := strings.Split(line, "\t")
		if len(f) != 5 {
			continue
		}
		out = append(out, tsvIssue{
			Key: unescapeTSV(f[0]), Status: unescapeTSV(f[1]),
			Assignee: unescapeTSV(f[2]), Updated: unescapeTSV(f[3]),
			Summary: unescapeTSV(f[4]),
		})
	}
	return out
}

func unescapeTSV(s string) string {
	if !strings.ContainsRune(s, '\\') {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' || i+1 == len(s) {
			b.WriteByte(s[i])
			continue
		}
		i++
		switch s[i] {
		case 't':
			b.WriteByte('\t')
		case 'n':
			b.WriteByte('\n')
		case 'r':
			b.WriteByte('\r')
		case '\\':
			b.WriteByte('\\')
		default:
			b.WriteByte('\\')
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

// renderCost returns the hundred-issue payload in one format.
func renderCost(tb testing.TB, f render.Format) []byte {
	tb.Helper()
	var b strings.Builder
	if err := render.Write(&b, costCorpus(100), f); err != nil {
		tb.Fatalf("%s: write: %v", f, err)
	}
	return []byte(b.String())
}

// BenchmarkParseCost measures what it costs a consumer to read a hundred
// issues in each format. Run it with:
//
//	go test ./internal/resource/issue/ -bench ParseCost -benchmem
//
// or `make cost`, which prints it beside the token counts.
func BenchmarkParseCost(b *testing.B) {
	b.Run("tsv", func(b *testing.B) {
		payload := string(renderCost(b, render.TSV))
		b.SetBytes(int64(len(payload)))
		b.ReportAllocs()
		for b.Loop() {
			if got := parseTSV(payload); len(got) != 100 {
				b.Fatalf("parsed %d issues, want 100", len(got))
			}
		}
	})

	b.Run("xml", func(b *testing.B) {
		payload := renderCost(b, render.XML)
		b.SetBytes(int64(len(payload)))
		b.ReportAllocs()
		for b.Loop() {
			var got xmlResult
			if err := xml.Unmarshal(payload, &got); err != nil {
				b.Fatalf("unmarshal: %v", err)
			}
			if len(got.Issues.Issue) != 100 {
				b.Fatalf("parsed %d issues, want 100", len(got.Issues.Issue))
			}
		}
	})

	b.Run("json", func(b *testing.B) {
		payload := renderCost(b, render.JSON)
		b.SetBytes(int64(len(payload)))
		b.ReportAllocs()
		for b.Loop() {
			var got docResult
			if err := json.Unmarshal(payload, &got); err != nil {
				b.Fatalf("unmarshal: %v", err)
			}
			if len(got.Issues) != 100 {
				b.Fatalf("parsed %d issues, want 100", len(got.Issues))
			}
		}
	})

	b.Run("yaml", func(b *testing.B) {
		payload := renderCost(b, render.YAML)
		b.SetBytes(int64(len(payload)))
		b.ReportAllocs()
		for b.Loop() {
			var got docResult
			if err := yaml.Unmarshal(payload, &got); err != nil {
				b.Fatalf("unmarshal: %v", err)
			}
			if len(got.Issues) != 100 {
				b.Fatalf("parsed %d issues, want 100", len(got.Issues))
			}
		}
	})
}

// TestEveryFormatParsesBackToTheSameRows is what makes the benchmark honest.
//
// A parser that skipped work would be fast and would not be reading the
// payload, so each decoder is held to the values it must recover before its
// speed means anything. It also asserts the contract from the outside for the
// first time: until now nothing in this repository read `jr` output back.
func TestEveryFormatParsesBackToTheSameRows(t *testing.T) {
	want := struct{ key, status, assignee, updated, summary string }{
		key:      "ENG-100",
		status:   "To Do",
		assignee: "Ada Lovelace",
		updated:  "2026-08-01T00:00:07Z",
		summary:  "Finalize Documentation for the Project",
	}

	tsv := parseTSV(string(renderCost(t, render.TSV)))
	if len(tsv) != 100 {
		t.Fatalf("tsv: parsed %d issues, want 100", len(tsv))
	}
	if got := tsv[0]; got != (tsvIssue{
		want.key, want.status, want.assignee, want.updated, want.summary,
	}) {
		t.Errorf("tsv first row = %+v", got)
	}

	var x xmlResult
	if err := xml.Unmarshal(renderCost(t, render.XML), &x); err != nil {
		t.Fatalf("xml: %v", err)
	}
	if len(x.Issues.Issue) != 100 {
		t.Fatalf("xml: parsed %d issues, want 100", len(x.Issues.Issue))
	}
	first := x.Issues.Issue[0]
	if first.Key != want.key || first.Status.Name != want.status ||
		first.Assignee.Display != want.assignee || first.Updated != want.updated ||
		first.Summary != want.summary {
		t.Errorf("xml first issue = %+v", first)
	}
	// What XML carries and TSV does not, which is why it costs more.
	if first.ID == "" || first.Created == "" || first.Status.Category == "" ||
		first.Assignee.ID == "" {
		t.Errorf("xml first issue is missing a field TSV does not carry: %+v", first)
	}

	for _, tc := range []struct {
		format    render.Format
		unmarshal func([]byte, any) error
	}{
		{render.JSON, json.Unmarshal},
		{render.YAML, yaml.Unmarshal},
	} {
		t.Run(string(tc.format), func(t *testing.T) {
			var d docResult
			if err := tc.unmarshal(renderCost(t, tc.format), &d); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if len(d.Issues) != 100 {
				t.Fatalf("parsed %d issues, want 100", len(d.Issues))
			}
			got := d.Issues[0]
			if got.Key != want.key || got.Status.Text != want.status ||
				got.Assignee.Display != want.assignee || got.Updated != want.updated ||
				got.Summary != want.summary {
				t.Errorf("first issue = %+v", got)
			}
			// The version comes from the constant, not a literal. What this
			// test is about is that every format parses back to the same rows;
			// the version is pinned where versions are pinned, in
			// internal/cli/testdata/kinds.
			if d.Kind != "issue.list" || d.V != issue.VersionList ||
				d.Count != 100 || !d.Complete {
				t.Errorf("envelope = kind %q v%d count %d complete %v",
					d.Kind, d.V, d.Count, d.Complete)
			}
		})
	}
}
