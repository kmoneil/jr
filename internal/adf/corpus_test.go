package adf_test

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kmoneil/jr/internal/adf"
	"github.com/kmoneil/jr/internal/errs"
)

var update = flag.Bool("update", false, "rewrite golden files instead of comparing against them")

// corpusEntry is one document from testdata/corpus.json.
//
// Every ADF here was produced by Jira Cloud: the generator posted a document to
// the sandbox and read back what was stored, so the corpus is evidence about
// the API rather than about what somebody believed the API does. The entries
// Jira refused carry what was sent instead, because "Jira will not store this"
// is the same kind of evidence and the reason several of these constructs are
// refused by FromMarkdown before a request is made.
type corpusEntry struct {
	Name string          `json:"name"`
	ADF  json.RawMessage `json:"adf,omitempty"`
	Sent json.RawMessage `json:"sent,omitempty"`
	// RefusedByJira marks a document the server would not store.
	RefusedByJira bool `json:"refused-by-jira,omitempty"`
}

func loadCorpus(t *testing.T) []corpusEntry {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "corpus.json"))
	if err != nil {
		t.Fatalf("read corpus: %v", err)
	}
	var entries []corpusEntry
	if err := json.Unmarshal(raw, &entries); err != nil {
		t.Fatalf("parse corpus: %v", err)
	}
	return entries
}

// TestCorpusMeetsItsSize is the §10 gate, held to a number rather than to a
// memory of having collected enough. Documents Jira refused do not count: they
// exercise nothing in the converter.
func TestCorpusMeetsItsSize(t *testing.T) {
	const want = 200

	stored := 0
	for _, e := range loadCorpus(t) {
		if len(e.ADF) > 0 {
			stored++
		}
	}
	if stored < want {
		t.Errorf("the corpus holds %d documents Jira stored, want at least %d\n"+
			"regenerate it against the sandbox rather than writing more by hand",
			stored, want)
	}
}

// TestEveryCorpusDocumentConvertsOrIsRefusedByName is the whole read-side
// guarantee, over documents a real server produced.
//
// There are exactly two acceptable outcomes for any document Jira will store:
// markdown, or a refusal naming the construct. A third — a panic, a different
// error, or markdown that silently lost something — is the failure this
// package exists to prevent, and no hand-written fixture would find it.
func TestEveryCorpusDocumentConvertsOrIsRefusedByName(t *testing.T) {
	var golden strings.Builder

	for _, e := range loadCorpus(t) {
		if len(e.ADF) == 0 {
			continue
		}
		t.Run(e.Name, func(t *testing.T) {
			doc, err := adf.Parse(e.ADF)
			if err != nil {
				t.Fatalf("a document Jira stored did not parse: %v\n%s", err, e.ADF)
			}
			got, err := adf.ToMarkdown(doc)
			if err == nil {
				golden.WriteString("=== " + e.Name + "\n" + got + "\n\n")
				return
			}

			structured := errs.Coerce(err)
			if structured.Code != "ADF_UNREPRESENTABLE" {
				t.Fatalf("code = %q, want ADF_UNREPRESENTABLE: %v", structured.Code, err)
			}
			if !strings.Contains(structured.Remedy, "--raw-body") {
				t.Errorf("remedy %q leaves the caller with no way to read the body",
					structured.Remedy)
			}
			golden.WriteString("=== " + e.Name + "\n!! " + structured.Message + "\n\n")
		})
	}

	assertCorpusGolden(t, "corpus.golden", golden.String())
}

// TestNothingThisToolWritesIsSomethingItCannotRead closes the gap the golden
// above leaves open.
//
// The golden records what ToMarkdown produces and would not move if the markdown
// in it stopped being readable, because nothing in that test reads it back. Two
// documents sat in exactly that state for a day: a link whose text is inline
// code, which this tool wrote and then refused, because the check that refuses
// emphasis on inline code refused a link as well. Jira stores that combination,
// which is why those two entries carry an `adf` payload.
//
// A document Jira stored, converted and handed back, has to be one a caller can
// send again. That is the whole promise of the markdown body format, and it is
// the round trip a person actually performs: read an issue, edit the body, send
// it back.
func TestNothingThisToolWritesIsSomethingItCannotRead(t *testing.T) {
	for _, e := range loadCorpus(t) {
		if len(e.ADF) == 0 {
			continue
		}
		t.Run(e.Name, func(t *testing.T) {
			doc, err := adf.Parse(e.ADF)
			if err != nil {
				t.Fatalf("a document Jira stored did not parse: %v", err)
			}
			markdown, err := adf.ToMarkdown(doc)
			if err != nil {
				// Refused on the way out is the designed answer for a construct
				// ADF has and markdown does not. The golden above covers it.
				return
			}
			if _, err := adf.FromMarkdown(markdown); err != nil {
				t.Errorf("this tool wrote markdown it cannot read: %v\n--- wrote ---\n%s",
					err, markdown)
			}
		})
	}
}

// TestWhatJiraRefusedIsRefusedBeforeTheRequest keeps the two halves honest.
//
// Where the corpus records a document the server would not store and this
// package can produce the markdown for it, FromMarkdown has to refuse it here.
// Otherwise a caller gets Jira's "INVALID_INPUT; comment: INVALID_INPUT",
// which names neither the node nor where it was.
func TestWhatJiraRefusedIsRefusedBeforeTheRequest(t *testing.T) {
	refused := map[string]string{
		"marks: strong + code":       "**`x`**",
		"marks: em + code":           "*`x`*",
		"marks: code + strike":       "~~`x`~~",
		"nested blockquote":          "> > deep",
		"panel info holding a table": "> [!INFO]\n> | a |\n> | --- |",
	}

	found := 0
	for _, e := range loadCorpus(t) {
		markdown, covered := refused[e.Name]
		if !covered {
			continue
		}
		found++
		if !e.RefusedByJira {
			t.Errorf("%q is recorded as stored; this test's premise is stale", e.Name)
			continue
		}
		t.Run(e.Name, func(t *testing.T) {
			if _, err := adf.FromMarkdown(markdown); err == nil {
				t.Errorf("FromMarkdown built a document Jira will not store")
			}
		})
	}
	// A renamed corpus entry would otherwise leave this test running over
	// nothing and passing, which is the shape of guard this project keeps
	// finding on the wrong end of.
	if found != len(refused) {
		t.Errorf("matched %d of %d corpus entries; the names have drifted", found, len(refused))
	}
}

// assertCorpusGolden compares the whole conversion against testdata, so a
// change in how any of 247 real documents converts shows up as a diff rather
// than as nothing at all.
func assertCorpusGolden(t *testing.T, name, got string) {
	t.Helper()

	path := filepath.Join("testdata", name)
	if *update {
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
		t.Errorf("the corpus converts differently than %s records.\n"+
			"If that is intended, run `make golden` and read the diff: it is "+
			"what every consumer of a Cloud body will see.", path)
	}
}
