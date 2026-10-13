package cli_test

import (
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/kmoneil/jr/internal/cli"
	"github.com/kmoneil/jr/internal/registry"
	"github.com/kmoneil/jr/internal/render"
	"github.com/kmoneil/jr/internal/site"
)

// TestEveryCommandThatTakesAZoneCanAskTheAccount holds --tz account to
// commands that have somebody to ask.
//
// `account` is the zone on the Jira profile, so settling it is a request, and
// a command with no session has no way to make one. The flag is derived from
// the kinds a command declares, which says nothing about whether it reaches
// Jira, so the two are held together here: a command that writes a timestamp
// either talks to the site or keeps its timestamps in UTC, as `doctor` does.
func TestEveryCommandThatTakesAZoneCanAskTheAccount(t *testing.T) {
	var (
		takes  int
		doctor *registry.Command
	)
	for _, c := range cli.Registry().All() {
		if c.Name() == "doctor" {
			doctor = c
		}
		if !c.TakesZone() {
			continue
		}
		takes++
		if !c.NeedsJira {
			t.Errorf("%s has --%s and no session, so `--%s %s` on it is "+
				"NO_SESSION; give it a session or set TimestampsStayUTC",
				c.Name(), registry.ZoneFlagName, registry.ZoneFlagName, registry.ZoneAccount)
		}
	}
	// The ci profile is the smallest, and it lists and reads issues.
	if takes < 5 {
		t.Errorf("only %d commands take a zone, so this is checking nothing", takes)
	}

	if doctor == nil {
		t.Fatal("this build has no doctor, so its exemption is checking nothing")
	}
	if !render.KindHasTimestamp(doctor.Kind()) {
		t.Fatal("doctor declares no timestamp, so its exemption is checking nothing")
	}
	if doctor.TakesZone() {
		t.Error("doctor takes a zone; under `account` it could not be printed " +
			"when the credential it diagnoses is the one that is broken")
	}
}

// openTimestamp finds a timestamp declared inside an open shape, and names
// where. The zone pass descends through declared children only, so one
// declared here would be written in UTC under every zone and nothing would
// say so.
func openTimestamp(s *render.Schema, at string) string {
	if s == nil {
		return ""
	}
	at += "/" + s.Element
	if e := s.Extra; e != nil {
		switch {
		case e.Type == render.TypeTimestamp:
			return at + " (the open element's own text)"
		case e.Structured.HasTimestamp():
			return at + " (the open element's structured form)"
		}
		for _, f := range e.Attrs {
			if f.Type == render.TypeTimestamp {
				return at + " (the open element's @" + f.Name + ")"
			}
		}
	}
	for _, c := range s.Children {
		if found := openTimestamp(c.Schema, at); found != "" {
			return found
		}
	}
	return ""
}

// TestNoOpenShapeDeclaresATimestamp is the assumption the zone pass in
// internal/render rests on, held against every kind this build can emit.
//
// A kind may admit elements it does not name, a field requested with --field,
// and the pass does not look inside them. That is safe while none of them is a
// timestamp, and this is what says so. When it fails, the fix is in
// render.Schema.inZone, which has to learn the shape, and not here.
func TestNoOpenShapeDeclaresATimestamp(t *testing.T) {
	kinds := render.RegisteredKinds()
	if len(kinds) < 20 {
		t.Fatalf("only %d kinds are registered, so this is checking nothing", len(kinds))
	}
	for _, kind := range kinds {
		s, _ := render.SchemaFor(kind)
		if found := openTimestamp(s, kind); found != "" {
			t.Errorf("%s declares a timestamp the zone pass will not reach, at %s",
				kind, found)
		}
	}

	// The control: each of the three places one could hide is found.
	for name, extra := range map[string]*render.Extra{
		"text":       {Named: "x", Type: render.TypeTimestamp},
		"attribute":  {Named: "x", Attrs: []render.Field{{Name: "at", Type: render.TypeTimestamp}}},
		"structured": {Named: "x", Structured: render.Leaf("v", render.TypeTimestamp)},
	} {
		nested := &render.Schema{Element: "outer", Children: []render.Child{
			{Schema: &render.Schema{Element: "inner", Extra: extra}},
		}}
		if openTimestamp(nested, "probe") == "" {
			t.Errorf("a timestamp in an open shape's %s was not found", name)
		}
	}
	closed := &render.Schema{Element: "outer", Children: []render.Child{
		{Schema: render.Leaf("at", render.TypeTimestamp)},
	}}
	if found := openTimestamp(closed, "probe"); found != "" {
		t.Errorf("a declared timestamp was reported as open: %s", found)
	}
}

// utcStamp is a timestamp spelled in UTC, as a resource builds one.
var utcStamp = regexp.MustCompile(`\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(?:\.\d+)?Z`)

// writtenBy is the document part of a sweep fingerprint. The requests above
// it hold instants too, in the queries a command sent, and those are not what
// a zone changes.
func writtenBy(fingerprint string) string {
	_, rest, _ := strings.Cut(fingerprint, "\noutput: ")
	doc, _, _ := strings.Cut(rest, "\nfailed: ")
	return doc
}

// TestUnderAZoneNoTimestampIsLeftInUTC asks the question a per-command flag
// test cannot: not "did --tz change something" but "did it reach everything".
//
// The zone pass respells what a schema declares a timestamp. A resource that
// built an instant into a field declared as a string would pass every check
// there is, because a string may hold anything, and would then be the one
// value in the document still in UTC beside neighbours that had moved: the
// hand conversion the flag exists to end, now needed for one field. So every
// command this harness can answer is driven under a zone, on both deployments,
// and what it wrote may hold no instant spelled in UTC.
func TestUnderAZoneNoTimestampIsLeftInUTC(t *testing.T) {
	requireNothingIsWritten(t)
	tokyo := func(flags registry.Flags) { flags.SetString(registry.ZoneFlagName, "Asia/Tokyo") }

	var sawStamps int
	for _, c := range cli.Registry().All() {
		if commandNotDriven(c) != "" || !c.TakesZone() {
			continue
		}
		for _, kind := range []site.Kind{site.DataCenter, site.Cloud} {
			if utcStamp.MatchString(writtenBy(drive(t, c, kind, "", nil))) {
				sawStamps++
			}
			zoned := writtenBy(drive(t, c, kind, registry.ZoneFlagName, tokyo))
			if left := utcStamp.FindAllString(zoned, -1); len(left) > 0 {
				t.Errorf("%s on %s wrote %d timestamp(s) in UTC under --%s "+
					"Asia/Tokyo: %s\nSomething built an instant into a value "+
					"no schema declares a timestamp. Declare it "+
					"render.TypeTimestamp.\n%s",
					c.Name(), kind, len(left), registry.ZoneFlagName,
					strings.Join(left, ", "), zoned)
			}
		}
	}
	// A sweep over documents with no timestamp in them passes whatever the
	// writer does.
	if sawStamps < 5 {
		t.Errorf("only %d driven documents held a timestamp at all, so this "+
			"shows nothing about the ones that do", sawStamps)
	}

	// The control: an instant in a field declared as a string is found.
	const kind = "probe.undeclared-instant"
	render.RegisterSchema(kind, &render.Schema{
		Element: "probe",
		Children: []render.Child{
			{Schema: render.Leaf("declared", render.TypeTimestamp)},
			{Schema: render.Leaf("undeclared", render.TypeString)},
		},
	})
	leaky := &registry.Command{
		Path: []string{"probe", "leaky"}, Summary: "An instant in a string",
		NeedsJira: true,
		Outputs:   []registry.Output{{Kind: kind, Version: 1}},
		Run: func(context.Context, *registry.Invocation) (*render.Doc, error) {
			return render.Record(kind, 1, render.El("probe").
				Leaf("declared", "2026-05-12T09:10:00Z").
				Leaf("undeclared", "2026-05-12T09:10:00Z")), nil
		},
	}
	zoned := writtenBy(drive(t, leaky, site.Cloud, registry.ZoneFlagName, tokyo))
	if left := utcStamp.FindAllString(zoned, -1); len(left) != 1 {
		t.Errorf("the control wrote %d UTC timestamp(s) under a zone, want "+
			"exactly the undeclared one, so the sweep above cannot tell a "+
			"respelled document from an untouched one:\n%s", len(left), zoned)
	}
}
