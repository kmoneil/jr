package cli

import (
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"testing"

	"github.com/kmoneil/jr/internal/errs"
	"github.com/kmoneil/jr/internal/registry"
)

// TestEveryCommandParsesAnArgvAsTheCommandLineDoes holds registry.ParseArgv to
// the command line, for every command this build registers.
//
// ParseArgv exists for an argv that never reaches a shell: a step of a
// sequence, written as the command it would have been. The promise that makes
// that safe is that a step means what the same words mean typed at a prompt,
// so the comparison is against the real cobra tree, leaf by leaf, with the
// global flags merged in as they are when jr runs. Each command gets an argv
// built from its own declaration, every flag given, long and short spellings
// alternating, a repeatable flag given twice in two spellings, and positional
// arguments both before and after the flags.
func TestEveryCommandParsesAnArgvAsTheCommandLineDoes(t *testing.T) {
	a := &app{}
	a.reg = a.buildRegistry(registry.Default)

	compared := 0
	for _, rc := range a.reg.All() {
		for _, leading := range []bool{false, true} {
			argv := sampleArgv(rc, leading)
			// A fresh tree for every argv, because a parsed flag set keeps
			// what it was given and a repeatable flag would accumulate.
			leaf, rest, err := a.newRoot().Find(rc.Path)
			if err != nil || len(rest) != 0 {
				t.Fatalf("%s: the cobra tree has no leaf at its path: %v", rc.Name(), err)
			}
			if err := leaf.ParseFlags(argv); err != nil {
				t.Fatalf("%s %v: the command line refused it: %v", rc.Name(), argv, err)
			}
			wantArgs := leaf.Flags().Args()
			if err := validateFlags(leaf, rc); err != nil {
				t.Fatalf("%s %v: the command line's checks refused it: %v", rc.Name(), argv, err)
			}
			want := registry.HarvestFlags(leaf.Flags(), rc)

			got, gotArgs, err := rc.ParseArgv(argv)
			if err != nil {
				t.Errorf("%s %v: ParseArgv refused what the command line took: %v",
					rc.Name(), argv, err)
				continue
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("%s %v: the flags differ\nParseArgv:    %+v\ncommand line: %+v",
					rc.Name(), argv, got, want)
			}
			if !slices.Equal(gotArgs, wantArgs) && (len(gotArgs) > 0 || len(wantArgs) > 0) {
				t.Errorf("%s %v: the arguments differ\nParseArgv:    %q\ncommand line: %q",
					rc.Name(), argv, gotArgs, wantArgs)
			}
			compared++
		}
	}
	// Even the smallest profile registers a few dozen commands, so a handful
	// means the loop above walked a registry nobody meant it to.
	if commands := len(a.reg.All()); commands < 20 || compared != 2*commands {
		t.Fatalf("compared %d argvs over %d commands", compared, commands)
	}
}

// TestParseArgvRefusesWhatTheCommandLineRefuses is the other half: an argv the
// command line turns away is turned away here too, with the code it gets
// there.
func TestParseArgvRefusesWhatTheCommandLineRefuses(t *testing.T) {
	a := &app{}
	a.reg = a.buildRegistry(registry.Default)
	rc, ok := a.reg.Lookup("issue.list")
	if !ok {
		t.Fatal("issue list is not registered")
	}
	for _, tc := range []struct {
		name string
		argv []string
		code string
	}{
		{"an unknown flag", []string{"--no-such-flag"}, "INVALID_USAGE"},
		{"a global flag, which belongs to the root", []string{"--context", "work"}, "INVALID_USAGE"},
		{"a missing value", []string{"--status"}, "INVALID_USAGE"},
		{"an int that is not one", []string{"--page-size", "many"}, "INVALID_USAGE"},
		{"an argument the command takes none of", []string{"ENG-1"}, "INVALID_USAGE"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := rc.ParseArgv(tc.argv)
			if err == nil {
				t.Fatalf("ParseArgv(%q) was accepted", tc.argv)
			}
			if code := errs.Coerce(err).Code; code != tc.code {
				t.Errorf("ParseArgv(%q) refused as %q, want %q", tc.argv, code, tc.code)
			}
		})
	}
}

// sampleArgv builds an argv giving every flag a command declares, from the
// declaration alone, so a flag added tomorrow is covered by existing.
func sampleArgv(rc *registry.Command, leading bool) []string {
	minArgs, maxArgs := rc.ArgBounds()
	n := minArgs
	if n == 0 && maxArgs != 0 {
		n = 1
	}
	positional := make([]string, 0, n)
	for i := range n {
		positional = append(positional, fmt.Sprintf("ARG%d", i))
	}

	var argv []string
	if leading {
		argv = append(argv, positional...)
	}
	for i, f := range rc.AllFlags() {
		name := "--" + f.Name
		if f.Short != "" && i%2 == 1 {
			name = "-" + f.Short
		}
		switch {
		case f.Type == registry.TypeBool:
			argv = append(argv, name)
		case f.Type == registry.TypeInt:
			argv = append(argv, name, strconv.Itoa(i+1))
		case len(f.Enum) > 0:
			argv = append(argv, name, f.Enum[len(f.Enum)-1])
			if f.Repeatable {
				argv = append(argv, "--"+f.Name+"="+f.Enum[0])
			}
		default:
			argv = append(argv, name, fmt.Sprintf("value %d", i))
			if f.Repeatable {
				argv = append(argv, fmt.Sprintf("--%s=second %d", f.Name, i))
			}
		}
	}
	if !leading {
		argv = append(argv, positional...)
	}
	return argv
}
