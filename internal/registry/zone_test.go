package registry_test

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kmoneil/jr/internal/errs"
	"github.com/kmoneil/jr/internal/exitcode"
	"github.com/kmoneil/jr/internal/registry"
	"github.com/kmoneil/jr/internal/render"
	"github.com/kmoneil/jr/internal/site"
	"github.com/kmoneil/jr/internal/transport"
)

// Two kinds, one that declares a timestamp and one that does not, which is the
// whole of what decides whether a command has --tz.
const (
	zonedKind = "probe.zoned"
	plainKind = "probe.plain"
)

var registerZoneKinds = sync.OnceFunc(func() {
	render.RegisterSchema(zonedKind, &render.Schema{
		Element:  "event",
		Children: []render.Child{{Schema: render.Leaf("at", render.TypeTimestamp)}},
	})
	render.RegisterSchema(plainKind, &render.Schema{
		Element:  "thing",
		Children: []render.Child{{Schema: render.Leaf("name", render.TypeString)}},
	})
})

func zonedCommand() *registry.Command {
	registerZoneKinds()
	return &registry.Command{
		Path: []string{"probe", "zoned"}, NeedsJira: true,
		Outputs: []registry.Output{{Kind: zonedKind, Version: 1}},
	}
}

func plainCommand() *registry.Command {
	registerZoneKinds()
	return &registry.Command{
		Path: []string{"probe", "plain"}, NeedsJira: true,
		Outputs: []registry.Output{{Kind: plainKind, Version: 1}},
	}
}

// accountSession is a session whose only working part is the account. It
// answers /myself with the zone a test gives it and counts how often it was
// asked, and anything else a command reached for would panic.
type accountSession struct {
	registry.Session
	zone  string
	asked *int
}

func (s accountSession) Metadata(context.Context) (*site.Metadata, error) {
	return &site.Metadata{Client: s, Info: site.Info{Kind: site.Cloud}}, nil
}

func (s accountSession) Do(_ context.Context, r transport.Request) (*transport.Response, error) {
	if !strings.HasSuffix(r.Path, "/myself") {
		return &transport.Response{Status: 404, Body: []byte(`{}`)}, nil
	}
	*s.asked++
	body := `{"accountId":"acc-1","displayName":"Ada Lovelace","timeZone":"` + s.zone + `"}`
	return &transport.Response{
		Status: 200, Body: []byte(body),
		Header: map[string][]string{"Content-Type": {"application/json"}},
	}, nil
}

func withZoneFlag(value string) registry.Flags {
	flags := registry.NewFlags()
	flags.SetString(registry.ZoneFlagName, value)
	return flags
}

// TestACommandHasTheZoneFlagOnlyWhereItWritesATimestamp is the derivation
// --tz rests on.
//
// The flag respells timestamps, and on a command that writes none it could
// only be accepted and ignored, which is the thing a flag here may not be. So
// it is not declared by anybody: a command has it when a kind it emits
// declares a timestamp, and the parser, `jr schema`, the generated reference
// and the MCP tool list all read that one answer from AllFlags.
func TestACommandHasTheZoneFlagOnlyWhereItWritesATimestamp(t *testing.T) {
	has := func(c *registry.Command) bool {
		return slices.ContainsFunc(c.AllFlags(), func(f registry.Flag) bool {
			return f.Name == registry.ZoneFlagName
		})
	}

	zoned, plain := zonedCommand(), plainCommand()
	if !zoned.TakesZone() || !has(zoned) {
		t.Errorf("a command whose kind declares a timestamp has no --%s", registry.ZoneFlagName)
	}
	if plain.TakesZone() || has(plain) {
		t.Errorf("a command that writes no timestamp has --%s", registry.ZoneFlagName)
	}

	// One timestamp among several kinds is enough.
	either := plainCommand()
	either.Outputs = append(either.Outputs, registry.Output{Kind: zonedKind, Version: 1})
	if !either.TakesZone() {
		t.Error("a command that can emit a kind with a timestamp takes no zone")
	}

	// And a command can decline, which is `doctor`: it has to answer when the
	// account cannot be asked.
	fixed := zonedCommand()
	fixed.TimestampsStayUTC = true
	if fixed.TakesZone() || has(fixed) {
		t.Errorf("a command that keeps its timestamps in UTC has --%s", registry.ZoneFlagName)
	}

	// It sits beside the other derived flag and displaces neither it nor the
	// command's own.
	paged := zonedCommand()
	paged.Paginated = true
	paged.Flags = []registry.Flag{{Name: "own", Type: registry.TypeString, Usage: "x"}}
	var names []string
	for _, f := range paged.AllFlags() {
		names = append(names, f.Name)
	}
	if got := strings.Join(names, " "); got != "own limit "+registry.ZoneFlagName {
		t.Errorf("flags = %q, want the command's own, then limit, then %s",
			got, registry.ZoneFlagName)
	}
	if len(paged.Flags) != 1 {
		t.Errorf("deriving the flags changed the declaration: %v", paged.Flags)
	}
}

// TestAZoneSettingIsOneOfThreeWordsOrAZoneThatExists holds the setting to
// what can be honoured, and makes the refusal name where the value was read,
// because a zone that is no zone is likelier a line in a context than a word
// typed just now.
func TestAZoneSettingIsOneOfThreeWordsOrAZoneThatExists(t *testing.T) {
	for _, ok := range []string{
		"", "utc", "account", "local", "Europe/Berlin", "America/Chicago",
		"UTC", "  Europe/Berlin  ",
	} {
		if err := registry.CheckZone("--tz", ok); err != nil {
			t.Errorf("%q was refused: %v", ok, err)
		}
	}

	for _, bad := range []string{"Mars/Olympus", "CEST+2", "berlin", "account,utc"} {
		err := registry.CheckZone(`context "work"`, bad)
		if err == nil {
			t.Errorf("%q was accepted as a timezone", bad)
			continue
		}
		e := errs.Coerce(err)
		if e.Code != "INVALID_TZ" || e.Exit != exitcode.Usage {
			t.Errorf("%q: refusal = %s at %v, want INVALID_TZ at %v",
				bad, e.Code, e.Exit, exitcode.Usage)
		}
		for _, want := range []string{`context "work"`, `"` + bad + `"`} {
			if !strings.Contains(e.Message, want) {
				t.Errorf("%q: message %q does not carry %s", bad, e.Message, want)
			}
		}
	}
}

// TestTheGateRefusesATypedZoneThatNamesNothing is the check before anything
// is validated or sent, on the command line and on a tool call alike, because
// both run the gate.
//
// An empty --tz is the case worth the test. It does not mean UTC: it would fall
// back to JIRA_TZ and then to the context, so somebody who typed it to mean
// "no zone" would be answered in whichever zone one of those names.
func TestTheGateRefusesATypedZoneThatNamesNothing(t *testing.T) {
	gate := func(c *registry.Command, flags registry.Flags) error {
		return registry.Gate(c, &registry.Invocation{Flags: flags})
	}

	for _, tc := range []struct{ typed, says string }{
		{"", "empty value"},
		{"   ", "empty value"},
		{"Mars/Olympus", `--tz names "Mars/Olympus"`},
	} {
		err := gate(zonedCommand(), withZoneFlag(tc.typed))
		if err == nil {
			t.Errorf("--tz %q was admitted", tc.typed)
			continue
		}
		e := errs.Coerce(err)
		if e.Code != "INVALID_TZ" || !strings.Contains(e.Message, tc.says) {
			t.Errorf("--tz %q: %s: %s, want INVALID_TZ saying %q",
				tc.typed, e.Code, e.Message, tc.says)
		}
	}

	for _, typed := range []string{"utc", "account", "local", "Europe/Berlin"} {
		if err := gate(zonedCommand(), withZoneFlag(typed)); err != nil {
			t.Errorf("--tz %s was refused: %v", typed, err)
		}
	}
	// No --tz at all is every invocation before the flag existed.
	if err := gate(zonedCommand(), registry.NewFlags()); err != nil {
		t.Errorf("an invocation with no --tz was refused: %v", err)
	}
	// A command that takes no zone may have a flag of that name which means
	// something else, as `context create` does, and it is not the gate's to
	// judge. The same value the gate refuses above passes here.
	if err := gate(plainCommand(), withZoneFlag("Mars/Olympus")); err != nil {
		t.Errorf("the gate judged a tz flag on a command that takes no zone: %v", err)
	}
}

// TestTheZoneIsSettledBeforeTheCommandRuns covers what an invocation ends up
// writing in: its own --tz, and where it has none, the default it was handed,
// which is the environment or the context on the command line and the
// server's setting for a tool call.
//
// UTC settles to nothing at all, by name or by default, which is what keeps
// the default path the bytes it always was: with no zone the writer walks
// nothing.
func TestTheZoneIsSettledBeforeTheCommandRuns(t *testing.T) {
	tokyo := time.FixedZone("the machine's", 9*60*60)
	t.Cleanup(registry.SetLocalZone(tokyo))

	for _, tc := range []struct {
		name  string
		typed string
		def   registry.ZoneDefault
		want  string
	}{
		{"nothing asked", "", registry.ZoneDefault{}, ""},
		{"utc by name", "utc", registry.ZoneDefault{}, ""},
		{"utc by default", "", registry.ZoneDefault{Setting: "utc"}, ""},
		{"a name", "Europe/Berlin", registry.ZoneDefault{}, "Europe/Berlin"},
		{"a default", "", registry.ZoneDefault{Setting: "America/Chicago"}, "America/Chicago"},
		{
			"the flag over the default", "Europe/Berlin",
			registry.ZoneDefault{Setting: "America/Chicago"},
			"Europe/Berlin",
		},
		{
			"utc typed over a default", "utc",
			registry.ZoneDefault{Setting: "America/Chicago"},
			"",
		},
		{"the machine's zone", "local", registry.ZoneDefault{}, "the machine's"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			flags := registry.NewFlags()
			if tc.typed != "" {
				flags.SetString(registry.ZoneFlagName, tc.typed)
			}
			inv := &registry.Invocation{Flags: flags}
			if err := registry.SettleZone(t.Context(), zonedCommand(), inv, tc.def); err != nil {
				t.Fatalf("settle: %v", err)
			}
			got := ""
			if inv.Zone != nil {
				got = inv.Zone.String()
			}
			if got != tc.want {
				t.Errorf("zone = %q, want %q", got, tc.want)
			}
		})
	}

	// A default that names nothing is refused, naming where it came from and
	// not the flag nobody typed.
	inv := &registry.Invocation{Flags: registry.NewFlags()}
	err := registry.SettleZone(t.Context(), zonedCommand(), inv,
		registry.ZoneDefault{Setting: "Mars/Olympus", From: `context "work"`})
	if err == nil {
		t.Fatal("a default that is no zone was settled")
	}
	if e := errs.Coerce(err); e.Code != "INVALID_TZ" ||
		!strings.Contains(e.Message, `context "work"`) {
		t.Errorf("refusal = %s: %s, want INVALID_TZ naming the context", e.Code, e.Message)
	}
}

// TestACommandThatTakesNoZoneSettlesNone is what makes a zone set for a whole
// shell or a whole context safe to set.
//
// A command that writes no timestamp, and one that keeps its own in UTC, are
// handed the same default every other command is. Neither may resolve it:
// `account` is a request with a credential, and `doctor` runs exactly when
// that credential is what is broken. A default that is not even a zone is
// passed over too, so a bad line in a context cannot stop `jr doctor` from
// saying so.
func TestACommandThatTakesNoZoneSettlesNone(t *testing.T) {
	fixed := zonedCommand()
	fixed.TimestampsStayUTC = true

	for _, c := range []*registry.Command{plainCommand(), fixed} {
		for _, setting := range []string{"account", "Europe/Berlin", "Mars/Olympus"} {
			asked := 0
			inv := &registry.Invocation{
				Flags: registry.NewFlags(),
				Jira:  accountSession{zone: "America/Chicago", asked: &asked},
			}
			err := registry.SettleZone(t.Context(), c, inv,
				registry.ZoneDefault{Setting: setting, From: "JIRA_TZ"})
			if err != nil {
				t.Errorf("%s under %s: refused: %v", c.Name(), setting, err)
			}
			if inv.Zone != nil {
				t.Errorf("%s under %s: settled %s", c.Name(), setting, inv.Zone)
			}
			if asked != 0 {
				t.Errorf("%s under %s: asked the site %d time(s)", c.Name(), setting, asked)
			}
		}
	}
}

// TestTheAccountsZoneIsAskedOfTheSite is `account`: the zone on the Jira
// profile, which is the zone Jira reads every date this tool sends in, so
// output in it matches input.
//
// One request, and the refusals say what to do about a zone, where the
// function they come from serves a caller reading a date and says to use a
// relative offset.
func TestTheAccountsZoneIsAskedOfTheSite(t *testing.T) {
	settle := func(session registry.Session) (*registry.Invocation, error) {
		inv := &registry.Invocation{Flags: withZoneFlag("account"), Jira: session}
		return inv, registry.SettleZone(t.Context(), zonedCommand(), inv, registry.ZoneDefault{})
	}

	asked := 0
	inv, err := settle(accountSession{zone: "America/Chicago", asked: &asked})
	if err != nil {
		t.Fatalf("settle: %v", err)
	}
	if inv.Zone == nil || inv.Zone.String() != "America/Chicago" {
		t.Errorf("zone = %v, want the account's America/Chicago", inv.Zone)
	}
	if asked != 1 {
		t.Errorf("the account was asked for %d time(s), want once", asked)
	}

	for _, tc := range []struct{ zone, code string }{
		{"", "NO_ACCOUNT_TIMEZONE"},
		{"Mars/Olympus", "UNKNOWN_ACCOUNT_TIMEZONE"},
	} {
		asked = 0
		inv, err := settle(accountSession{zone: tc.zone, asked: &asked})
		if err == nil {
			t.Errorf("an account in %q settled %v", tc.zone, inv.Zone)
			continue
		}
		e := errs.Coerce(err)
		if e.Code != tc.code {
			t.Errorf("account in %q: code = %s, want %s", tc.zone, e.Code, tc.code)
		}
		if !strings.Contains(e.Remedy, "--tz") || strings.Contains(e.Remedy, "-7d") {
			t.Errorf("account in %q: the remedy is the one for a date, not a "+
				"zone: %q", tc.zone, e.Remedy)
		}
		if strings.Contains(e.Detail, "date") {
			t.Errorf("account in %q: the detail is the one for a date: %q", tc.zone, e.Detail)
		}
	}

	// With no session there is nobody to ask, and that is said, not guessed
	// around.
	if _, err := settle(nil); err == nil || errs.Coerce(err).Code != "NO_SESSION" {
		t.Errorf("account with no session: %v, want NO_SESSION", err)
	}
}
