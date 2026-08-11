package cli

import (
	"testing"

	"github.com/spf13/cobra"

	"github.com/kmoneil/jr/internal/registry"
)

// TestHarvestTellsAnExplicitZeroFromAnAbsentFlag pins the mechanism the
// --page-size bound rests on.
//
// At this layer a flag's zero value and its absence were the same value:
// harvest wrote every declared flag with whatever pflag reported, so
// `f.ints["page-size"]` was 0 whether the caller typed `--page-size 0` or never
// mentioned it, and Flags.Int returns an int rather than an (int, bool). So
// `--page-size 0` was accepted and meant 50, against a flag whose own help says
// 1 to 100.
//
// It is an internal test because the question is about harvest specifically.
// A registry.Flags built by hand in a test says whatever the test says; only
// the binding layer knows what the caller actually typed, and asserting this
// through a hand-built Flags would be asserting the test's own setup.
func TestHarvestTellsAnExplicitZeroFromAnAbsentFlag(t *testing.T) {
	for _, tc := range []struct {
		name    string
		args    []string
		wantSet bool
		wantInt int
	}{
		{"omitted", nil, false, 0},
		{"explicit zero", []string{"--page-size", "0"}, true, 0},
		{"explicit and legal", []string{"--page-size", "25"}, true, 25},
	} {
		t.Run(tc.name, func(t *testing.T) {
			flags := harvestArgs(t, tc.args)

			if got := flags.WasSet("page-size"); got != tc.wantSet {
				t.Errorf("WasSet = %v, want %v", got, tc.wantSet)
			}
			// The effective value is unchanged in all three cases. A reader
			// that does not care whether the caller typed the flag must not
			// start seeing something different because this exists.
			if got := flags.Int("page-size"); got != tc.wantInt {
				t.Errorf("Int = %d, want %d", got, tc.wantInt)
			}
		})
	}
}

// TestADeclaredDefaultIsStillReadableAsItsValue is the regression this could
// most easily have caused.
//
// Three flags in the tree declare a non-empty default — --body-format,
// --changed-field, --order — and every reader of them goes through
// Flags.String. Recording explicitness by *skipping* the write for an unchanged
// flag would have been the obvious implementation and would have turned all
// three into the empty string, silently, in every invocation that did not name
// them.
func TestADeclaredDefaultIsStillReadableAsItsValue(t *testing.T) {
	rc := &registry.Command{
		Path: []string{"probe"},
		Flags: []registry.Flag{
			{Name: "order", Type: registry.TypeString, Default: "asc"},
			{Name: "page-size", Type: registry.TypeInt, Default: "50"},
			{Name: "loud", Type: registry.TypeBool, Default: "true"},
		},
	}
	flags := harvestCommand(t, rc, nil)

	if got := flags.String("order"); got != "asc" {
		t.Errorf("String = %q, want the declared default %q", got, "asc")
	}
	if got := flags.Int("page-size"); got != 50 {
		t.Errorf("Int = %d, want the declared default 50", got)
	}
	if !flags.Bool("loud") {
		t.Error("Bool = false, want the declared default true")
	}
	for _, name := range []string{"order", "page-size", "loud"} {
		if flags.WasSet(name) {
			t.Errorf("--%s reads as the caller's choice, and nobody typed it", name)
		}
	}
}

// harvestArgs binds the real `issue list` declaration and parses args against
// it, so this test moves whenever that command's flags do.
func harvestArgs(t *testing.T, args []string) registry.Flags {
	t.Helper()
	rc, ok := Registry().Lookup("issue.list")
	if !ok {
		t.Fatal("issue list is not registered")
	}
	return harvestCommand(t, rc, args)
}

func harvestCommand(t *testing.T, rc *registry.Command, args []string) registry.Flags {
	t.Helper()
	cc := &cobra.Command{Use: "probe"}
	collect := bindFlags(cc, rc)
	if err := cc.Flags().Parse(args); err != nil {
		t.Fatalf("parse %v: %v", args, err)
	}
	return collect(cc)
}
