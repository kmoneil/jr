package render_test

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kmoneil/jr/internal/render"
)

// The kind the zone tests write. It declares a timestamp in each place a
// schema can put one, an attribute, an element's text, and a repeated child's
// attribute, beside values of other types that happen to look like one.
const zonedKind = "test.zoned"

var registerZoned = sync.OnceFunc(func() {
	render.RegisterSchema(zonedKind, &render.Schema{
		Element: "event",
		Attrs: []render.Field{
			{Name: "key", Type: render.TypeString},
			{Name: "seen", Type: render.TypeTimestamp, Optional: true},
			// A string that holds a timestamp is a string. Nothing declared it
			// one, so nothing may respell it.
			{Name: "label", Type: render.TypeString, Optional: true},
			{Name: "due", Type: render.TypeDate, Optional: true},
		},
		Children: []render.Child{
			{Schema: render.Leaf("created", render.TypeTimestamp), Optional: true},
			{Schema: render.Leaf("note", render.TypeString), Optional: true},
			{Schema: &render.Schema{
				Element: "step",
				Attrs:   []render.Field{{Name: "at", Type: render.TypeTimestamp}},
				Text:    &render.Field{Type: render.TypeString},
			}, Optional: true, Repeated: true},
		},
	})
})

// plusTwo is a zone two hours ahead of UTC. Fixed, so the test needs no zone
// database and no date on which a real zone happens to be at that offset.
var plusTwo = time.FixedZone("plus-two", 2*60*60)

func zonedEvent(key string) *render.Node {
	n := render.El("event").
		Attr("key", key).
		Attr("seen", "2026-05-12T09:10:00Z").
		Attr("label", "2026-05-12T09:10:00Z").
		Attr("due", "2026-05-12").
		Leaf("created", "2026-05-11T23:30:00Z").
		Leaf("note", "2026-05-12T09:10:00Z")
	n.Child(render.El("step").Attr("at", "2026-05-12T22:59:59Z").SetText("first"))
	n.Child(render.El("step").Attr("at", "2026-12-31T23:00:00Z").SetText("second"))
	return n
}

func zonedColumns() []render.Column {
	return []render.Column{
		{Header: "key", Path: "@key"},
		{Header: "seen", Path: "@seen"},
		{Header: "label", Path: "@label"},
		{Header: "created", Path: "created"},
	}
}

// TestAZoneRespellsEveryDeclaredTimestampAndNothingElse is issue 218 at the
// writer.
//
// Every timestamp this tool printed was UTC, and a reader in another zone
// converted them by hand, where a conversion wrong by an hour looks exactly
// like a right one. A document given a zone is written with each timestamp in
// it, offset included, so the value still names one instant and still parses.
//
// "Declared" is the whole rule. The schema says which values are timestamps,
// and a string or a date that looks like one is left as the resource built it.
// The cases are chosen to cross a boundary each: a day (23:30 becomes 01:30
// the next day), and a year.
func TestAZoneRespellsEveryDeclaredTimestampAndNothingElse(t *testing.T) {
	registerZoned()

	node := zonedEvent("E-1")
	doc := render.Record(zonedKind, 1, node)
	doc.Zone = plusTwo

	for _, format := range []render.Format{render.XML, render.JSON, render.YAML} {
		var out strings.Builder
		if err := render.Write(&out, doc, format); err != nil {
			t.Fatalf("%s: write: %v", format, err)
		}
		got := out.String()

		for _, want := range []string{
			"2026-05-12T11:10:00+02:00", // seen, an attribute
			"2026-05-12T01:30:00+02:00", // created, an element's text, a day on
			"2026-05-13T00:59:59+02:00", // a repeated child's attribute
			"2027-01-01T01:00:00+02:00", // and the second of them, a year on
		} {
			if !strings.Contains(got, want) {
				t.Errorf("%s: no %s in the output:\n%s", format, want, got)
			}
		}
		// label and note are strings and due is a date. Two values still read
		// 09:10 UTC, and they are those two strings.
		if n := strings.Count(got, "2026-05-12T09:10:00Z"); n != 2 {
			t.Errorf("%s: %d values still spelled in UTC, want the 2 strings "+
				"nothing declared a timestamp:\n%s", format, n, got)
		}
		if !strings.Contains(got, "2026-05-12") {
			t.Errorf("%s: the date was respelled:\n%s", format, got)
		}
		if strings.Contains(got, "2026-05-11T23:30:00Z") ||
			strings.Contains(got, "2026-05-12T22:59:59Z") {
			t.Errorf("%s: a declared timestamp was left in UTC:\n%s", format, got)
		}
	}

	// The document itself is as the resource built it. A cursor, a
	// precondition and a plan file all read these values after a write.
	if seen, _ := node.AttrValue("seen"); seen != "2026-05-12T09:10:00Z" {
		t.Errorf("writing in a zone changed the document: seen = %q", seen)
	}
	if created, _ := node.Lookup("created"); created != "2026-05-11T23:30:00Z" {
		t.Errorf("writing in a zone changed the document: created = %q", created)
	}
	if at, _ := node.Children[2].AttrValue("at"); at != "2026-05-12T22:59:59Z" {
		t.Errorf("writing in a zone changed a child: at = %q", at)
	}
}

// TestARespelledTimestampNamesTheSameInstant is the property the feature
// rests on: the zone changes how a moment is written and never which moment.
// A fraction of a second survives, and so does a timestamp nobody set.
func TestARespelledTimestampNamesTheSameInstant(t *testing.T) {
	registerZoned()

	for _, tc := range []struct{ built, want string }{
		{"2026-05-12T09:10:00Z", "2026-05-12T11:10:00+02:00"},
		{"2026-05-12T09:10:00.25Z", "2026-05-12T11:10:00.25+02:00"},
		{"", ""},
	} {
		doc := render.Record(zonedKind, 1, render.El("event").
			Attr("key", "E-1").Attr("seen", tc.built))
		doc.Zone = plusTwo

		var out strings.Builder
		if err := render.Write(&out, doc, render.XML); err != nil {
			t.Fatalf("write %q: %v", tc.built, err)
		}
		if want := `seen="` + tc.want + `"`; !strings.Contains(out.String(), want) {
			t.Errorf("built %q, want %s in:\n%s", tc.built, want, out.String())
		}
		if tc.built == "" {
			continue
		}
		was, _ := time.Parse(time.RFC3339, tc.built)
		is, err := time.Parse(time.RFC3339, tc.want)
		if err != nil || !was.Equal(is) {
			t.Errorf("%q and %q are not one instant (%v)", tc.built, tc.want, err)
		}
	}
}

// TestNoZoneWritesTheDocumentAsBuilt is the default, and the promise that
// nothing moved for a caller who asked for nothing.
//
// UTC given by name is the same bytes as no zone at all. And a kind with no
// schema has nothing declared, so a zone finds nothing to respell in it: that
// is every ad-hoc document a test builds.
func TestNoZoneWritesTheDocumentAsBuilt(t *testing.T) {
	registerZoned()

	write := func(d *render.Doc) string {
		t.Helper()
		var out strings.Builder
		if err := render.Write(&out, d, render.XML); err != nil {
			t.Fatalf("write: %v", err)
		}
		return out.String()
	}

	plain := write(render.Record(zonedKind, 1, zonedEvent("E-1")))
	if n := strings.Count(plain, "Z"); n < 6 {
		t.Fatalf("the unzoned document does not hold its timestamps in UTC:\n%s", plain)
	}

	utc := render.Record(zonedKind, 1, zonedEvent("E-1"))
	utc.Zone = time.UTC
	if got := write(utc); got != plain {
		t.Errorf("UTC by name is not the bytes of no zone:\n%s\nwant\n%s", got, plain)
	}

	adhoc := render.Record("test.zone.unregistered", 1,
		render.El("event").Attr("seen", "2026-05-12T09:10:00Z"))
	adhoc.Zone = plusTwo
	if got := write(adhoc); !strings.Contains(got, `seen="2026-05-12T09:10:00Z"`) {
		t.Errorf("a value no schema declared was respelled:\n%s", got)
	}
}

// TestAStreamedCollectionIsWrittenInItsZone covers the rows, which reach
// stdout by two routes: TSV as each arrives, and every other format from the
// document built at Close. Both have to respell, and TSV streamed has to stay
// byte-identical to TSV buffered, which is the property streaming was adopted
// on.
func TestAStreamedCollectionIsWrittenInItsZone(t *testing.T) {
	registerZoned()

	spec := render.StreamSpec{
		Kind: zonedKind, Version: 1, Name: "events", Columns: zonedColumns(),
		Zone: plusTwo,
	}
	items := func() []*render.Node {
		return []*render.Node{zonedEvent("E-1"), zonedEvent("E-2")}
	}

	buffered := render.List(zonedKind, 1, &render.Collection{
		Name: "events", Items: items(), Columns: zonedColumns(), Complete: true,
	})
	buffered.Zone = plusTwo

	for _, format := range []render.Format{render.TSV, render.XML, render.JSON} {
		var want strings.Builder
		if err := render.Write(&want, buffered, format); err != nil {
			t.Fatalf("%s: buffered write: %v", format, err)
		}

		var got strings.Builder
		s, err := render.NewStream(&got, format, spec)
		if err != nil {
			t.Fatalf("%s: new stream: %v", format, err)
		}
		for _, item := range items() {
			if err := s.Write(item); err != nil {
				t.Fatalf("%s: write: %v", format, err)
			}
		}
		if err := s.Close(true, ""); err != nil {
			t.Fatalf("%s: close: %v", format, err)
		}

		if got.String() != want.String() {
			t.Errorf("%s: streamed differs from buffered under a zone:\n%s\nwant\n%s",
				format, got.String(), want.String())
		}
		if n := strings.Count(got.String(), "2026-05-12T11:10:00+02:00"); n != 2 {
			t.Errorf("%s: %d rows carry seen in the zone, want 2:\n%s",
				format, n, got.String())
		}
	}

	// And with no zone, a stream of a kind that has a schema writes what it
	// was given.
	var plain strings.Builder
	spec.Zone = nil
	s, err := render.NewStream(&plain, render.TSV, spec)
	if err != nil {
		t.Fatalf("new stream: %v", err)
	}
	if err := s.Write(zonedEvent("E-1")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if !strings.Contains(plain.String(), "E-1\t2026-05-12T09:10:00Z\t") {
		t.Errorf("a stream with no zone respelled a row:\n%s", plain.String())
	}

	// A streamed row of a kind with no schema is written as built, zone or
	// not: nothing declared its values.
	var adhoc strings.Builder
	a, err := render.NewStream(&adhoc, render.TSV, render.StreamSpec{
		Kind: "test.zone.unregistered", Version: 1, Name: "events",
		Columns: zonedColumns(), Zone: plusTwo,
	})
	if err != nil {
		t.Fatalf("new stream: %v", err)
	}
	if err := a.Write(zonedEvent("E-1")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if !strings.Contains(adhoc.String(), "E-1\t2026-05-12T09:10:00Z\t") {
		t.Errorf("a streamed row no schema describes was respelled:\n%s", adhoc.String())
	}
}

// TestASchemaSaysWhetherItHoldsATimestamp is what lets a caller settle the
// zone before a command runs, and skip settling it for a command that prints
// none. Each case puts the one timestamp somewhere different, because a search
// that looked in two of the three places would pass any single example.
func TestASchemaSaysWhetherItHoldsATimestamp(t *testing.T) {
	registerZoned()

	plain := func() *render.Schema {
		return &render.Schema{
			Element: "thing",
			Attrs:   []render.Field{{Name: "id", Type: render.TypeInt}},
			Text:    &render.Field{Type: render.TypeString},
			Children: []render.Child{
				{Schema: render.Leaf("name", render.TypeString)},
				{Schema: render.Leaf("due", render.TypeDate), Optional: true},
			},
		}
	}

	inAttr := plain()
	inAttr.Attrs = append(inAttr.Attrs, render.Field{Name: "at", Type: render.TypeTimestamp})

	inText := plain()
	inText.Text = &render.Field{Type: render.TypeTimestamp}

	inChild := plain()
	inChild.Children = append(inChild.Children,
		render.Child{Schema: render.Leaf("created", render.TypeTimestamp)})

	// Two levels down, in a grandchild's attribute.
	deep := plain()
	deep.Children = append(deep.Children, render.Child{Schema: &render.Schema{
		Element: "steps",
		Children: []render.Child{{Schema: &render.Schema{
			Element: "step",
			Attrs:   []render.Field{{Name: "at", Type: render.TypeTimestamp}},
		}}},
	}})

	for _, tc := range []struct {
		name   string
		schema *render.Schema
		want   bool
	}{
		{"none", plain(), false},
		{"an attribute", inAttr, true},
		{"the element's text", inText, true},
		{"a child", inChild, true},
		{"a grandchild's attribute", deep, true},
		{"no schema at all", nil, false},
	} {
		if got := tc.schema.HasTimestamp(); got != tc.want {
			t.Errorf("%s: HasTimestamp = %v, want %v", tc.name, got, tc.want)
		}
	}

	if !render.KindHasTimestamp(zonedKind) {
		t.Errorf("%s declares three timestamps and reports none", zonedKind)
	}
	if render.KindHasTimestamp("test.zone.unregistered") {
		t.Error("a kind with no schema reports a timestamp")
	}
}
