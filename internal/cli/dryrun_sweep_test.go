//go:build write

package cli_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/kmoneil/jr/internal/cli"
	"github.com/kmoneil/jr/internal/registry"
	"github.com/kmoneil/jr/internal/render"
	"github.com/kmoneil/jr/internal/site"
	"github.com/kmoneil/jr/internal/transport"
)

// TestADryRunSendsNoWrite holds every mutating command to what --dry-run says,
// "send nothing", rather than to having the flag.
//
// TestMutatingCommandsAreSafeByConstruction checks that the flag is declared,
// and on 2026-10-01 three verbs declared it and ignored it: beside --apply,
// `issue edit`, `issue move` and `issue assign` ran the plan and wrote every
// row at exit 0. The declaration was right and the behaviour was the opposite,
// which is the shape TestEveryFlagChangesWhatTheCommandDoes was written about.
//
// So this drives every mutating command the flag sweep can drive, under
// --dry-run, with no other flag and then with each of its flags at every probe
// value, against both deployments, and fails on any request that writes. No
// probe value is a plan, so --apply is reached here only as a file that does
// not exist, which is not the mode the defect lived in: dryRunApplyCoveredBy
// names the test that drives each apply with a real plan, and a command that
// declares --apply without an entry fails.
func TestADryRunSendsNoWrite(t *testing.T) {
	requireNothingIsWritten(t)
	var driven int
	for _, c := range cli.Registry().All() {
		if !c.Mutating {
			continue
		}
		if _, plans := c.Flag("apply"); plans && dryRunApplyCoveredBy[c.Name()] == "" {
			t.Errorf("%s declares --apply and names no test that runs an apply of "+
				"a real plan under --dry-run; add it to dryRunApplyCoveredBy with "+
				"that test", c.Name())
		}
		if why := commandNotDriven(c); why != "" {
			if dryRunNotSwept[c.Name()] == "" {
				t.Errorf("%s mutates and cannot be driven here (%s); add it to "+
					"dryRunNotSwept with the test that holds its --dry-run to "+
					"sending nothing", c.Name(), why)
			}
			continue
		}
		driven++
		t.Run(c.Name(), func(t *testing.T) {
			for _, kind := range []site.Kind{site.DataCenter, site.Cloud} {
				requireNoWrite(t, kind, "no other flag", drive(t, c, kind, "", nil))
				for _, f := range c.AllFlags() {
					if f.Name == "dry-run" || f.Name == "yes" {
						// Both are already on in every run: drive sets them for a
						// mutating command unless one of them is under test.
						continue
					}
					_, values, ok := probePair(f)
					if !ok {
						// TestEveryFlagChangesWhatTheCommandDoes fails on a flag
						// with no probe value, so it is not lost by skipping here.
						continue
					}
					for _, value := range values {
						fingerprint := drive(t, c, kind, f.Name, func(flags registry.Flags) {
							setProbe(flags, f, value)
						})
						requireNoWrite(t, kind, "--"+f.Name+" "+value, fingerprint)
					}
				}
			}
		})
	}
	if driven == 0 {
		t.Fatal("no mutating command was driven, so this proves nothing about --dry-run")
	}

	for name := range dryRunApplyCoveredBy {
		if c, built := registry.Lookup(name); built {
			if _, plans := c.Flag("apply"); !plans {
				t.Errorf("%s is in dryRunApplyCoveredBy and has no --apply; "+
					"delete the entry", name)
			}
		}
	}
	for name := range dryRunNotSwept {
		if c, built := registry.Lookup(name); built && commandNotDriven(c) == "" {
			t.Errorf("%s is excused from the dry-run sweep and the sweep drives "+
				"it; delete the excuse", name)
		}
	}
}

// TestTheDryRunSweepCanFail is the negative control: a command that declares
// --dry-run and writes anyway has to leave a write where the sweep looks.
func TestTheDryRunSweepCanFail(t *testing.T) {
	leaky := &registry.Command{
		Path:    []string{"probe", "leaky"},
		Summary: "A write that ignores --dry-run",
		Flags: []registry.Flag{
			{Name: "dry-run", Type: registry.TypeBool, Usage: "read by nothing"},
		},
		Mutating:  true,
		NeedsJira: true,
		Outputs:   []registry.Output{{Kind: "probe.leaky", Version: 1}},
		Run: func(ctx context.Context, inv *registry.Invocation) (*render.Doc, error) {
			conn, _, err := inv.Jira.Connect(ctx)
			if err != nil {
				return nil, err
			}
			if _, err := conn.Do(ctx, transport.Request{
				Method: transport.MethodPut, Path: "/rest/api/2/issue/ENG-1",
			}); err != nil {
				return nil, err
			}
			return render.Record("probe.leaky", 1, render.El("probe")), nil
		},
	}
	if len(writesIn(drive(t, leaky, site.DataCenter, "", nil))) == 0 {
		t.Error("a command that writes under --dry-run left no write in the " +
			"fingerprint, so TestADryRunSendsNoWrite cannot fail")
	}
}

// dryRunApplyCoveredBy names, for every command that declares --apply, the
// test that runs an apply of a real plan under --dry-run.
var dryRunApplyCoveredBy = map[string]string{
	"issue.edit":   "TestADryRunOfAnApplySendsNothing",
	"issue.move":   "TestADryRunOfAnApplySendsNothing",
	"issue.assign": "TestADryRunOfAnApplySendsNothing",
	// The sequence's own test of the same name, in internal/workflow.
	"issue.sequence": "TestADryRunOfAnApplySendsNothing",
}

// dryRunNotSwept names every mutating command this harness cannot drive, with
// the test that holds its --dry-run to sending nothing instead.
var dryRunNotSwept = map[string]string{
	"issue.attachment.upload": "TestUploadDryRunPrintsNoFileContents",
}

// requireNoWrite fails on any write in a drive fingerprint.
func requireNoWrite(t *testing.T, kind site.Kind, with, fingerprint string) {
	t.Helper()
	for _, write := range writesIn(fingerprint) {
		t.Errorf("on %s, with %s, a dry run sent %s", kind, with, write)
	}
}

// writesIn returns every request in a drive fingerprint whose method is not a
// read.
//
// By method rather than by endpoint, which would flag a read that travels by
// POST. None does under --dry-run today; one that starts to should be named
// here with the reason, rather than the rule loosened to fit it.
func writesIn(fingerprint string) []string {
	requests, _, _ := strings.Cut(strings.TrimPrefix(fingerprint, "requests:\n"), "\ncolumns:")
	var out []string
	for line := range strings.SplitSeq(requests, "\n") {
		line = strings.TrimSpace(line)
		method, _, _ := strings.Cut(line, " ")
		switch method {
		case "", http.MethodGet, http.MethodHead, http.MethodOptions:
			continue
		}
		out = append(out, line)
	}
	return out
}
