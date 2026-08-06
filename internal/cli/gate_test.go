package cli_test

import (
	"context"
	"strings"
	"testing"

	"github.com/kmoneil/jira-cli/internal/cli"
	"github.com/kmoneil/jira-cli/internal/exitcode"
	"github.com/kmoneil/jira-cli/internal/registry"
	"github.com/kmoneil/jira-cli/internal/render"
)

// fakeMutating registers a command that records whether it ever ran, so a test
// can tell "refused" from "ran and failed".
func fakeMutating(ran *bool, destructive bool) *registry.Registry {
	r := registry.New()
	flags := []registry.Flag{
		{Name: "dry-run", Type: registry.TypeBool, Usage: "print the request"},
	}
	if destructive {
		flags = append(flags, registry.Flag{
			Name: "yes", Type: registry.TypeBool, Usage: "confirm",
		})
	}
	r.Register(&registry.Command{
		Path:         []string{"fake", "write"},
		Summary:      "Change something",
		Example:      "jr fake write",
		Flags:        flags,
		Mutating:     true,
		Destructive:  destructive,
		NeedsJira:    true,
		RequiresTags: nil,
		Outputs:      []registry.Output{{Kind: "fake.write", Version: 1}},
		ExitCodes:    []exitcode.Code{exitcode.Blocked},
		Run: func(context.Context, *registry.Invocation) (*render.Doc, error) {
			*ran = true
			return render.Record("fake.write", 1, render.El("done")), nil
		},
	})
	return r
}

// TestReadOnlyRefusesAMutationBeforeItRuns is the one-way latch reaching the
// thing it exists to stop. It is enforced in the CLI layer from the
// declaration, so a resource author who forgot the check cannot ship a verb
// that ignores it.
func TestReadOnlyRefusesAMutationBeforeItRuns(t *testing.T) {
	for _, how := range []struct {
		name string
		args []string
		env  map[string]string
	}{
		{"flag", []string{"--readonly"}, nil},
		{"environment", nil, map[string]string{"JIRA_READONLY": "1"}},
	} {
		t.Run(how.name, func(t *testing.T) {
			var ran bool
			args := append([]string{"fake", "write"}, how.args...)
			got := runGated(t, fakeMutating(&ran, false), how.env, args...)

			if got.exit != exitcode.Blocked {
				t.Errorf("exit = %v, want %v\nstderr: %s",
					got.exit, exitcode.Blocked, got.stderr)
			}
			if ran {
				t.Error("the command ran despite read-only mode")
			}
			if !strings.Contains(got.stderr, "READ_ONLY") {
				t.Errorf("stderr does not carry the code: %s", got.stderr)
			}
			// A refused command writes nothing at all to stdout.
			if got.stdout != "" {
				t.Errorf("stdout = %q, want nothing", got.stdout)
			}
		})
	}
}

// TestAMutationRunsWhenNothingBlocksIt is the counterpart, so the test above
// cannot pass because the command was broken for some unrelated reason.
func TestAMutationRunsWhenNothingBlocksIt(t *testing.T) {
	var ran bool
	got := runGated(t, fakeMutating(&ran, false), nil, "fake", "write")

	if got.exit != exitcode.OK {
		t.Fatalf("exit = %v, want %v\nstderr: %s", got.exit, exitcode.OK, got.stderr)
	}
	if !ran {
		t.Error("the command did not run")
	}
}

// TestDestructiveNeedsConfirmation covers the other central gate. Nothing ever
// blocks on input here, so the absence of --yes is a refusal rather than a
// question nobody can answer.
func TestDestructiveNeedsConfirmation(t *testing.T) {
	var ran bool
	got := runGated(t, fakeMutating(&ran, true), nil, "fake", "write")

	if got.exit != exitcode.Blocked {
		t.Errorf("exit = %v, want %v\nstderr: %s", got.exit, exitcode.Blocked, got.stderr)
	}
	if ran {
		t.Error("a destructive command ran without confirmation")
	}
	if !strings.Contains(got.stderr, "CONFIRMATION_REQUIRED") {
		t.Errorf("stderr does not carry the code: %s", got.stderr)
	}

	ran = false
	got = runGated(t, fakeMutating(&ran, true), nil, "fake", "write", "--yes")
	if got.exit != exitcode.OK {
		t.Fatalf("exit = %v with --yes\nstderr: %s", got.exit, got.stderr)
	}
	if !ran {
		t.Error("--yes did not let the command through")
	}
}

// TestReadOnlyOutranksAMissingConfirmation keeps the two gates from producing a
// misleading order of advice: a caller told only to pass --yes would pass it
// and be refused again.
func TestReadOnlyOutranksAMissingConfirmation(t *testing.T) {
	var ran bool
	got := runGated(t, fakeMutating(&ran, true), nil, "fake", "write", "--yes", "--readonly")

	if got.exit != exitcode.Blocked {
		t.Fatalf("exit = %v, want %v", got.exit, exitcode.Blocked)
	}
	if !strings.Contains(got.stderr, "READ_ONLY") {
		t.Errorf("with --yes given, the refusal should be about read-only: %s", got.stderr)
	}
	if ran {
		t.Error("the command ran")
	}
}

// runGated runs a command with an isolated environment plus whatever the test
// adds, and returns what reached each stream.
func runGated(
	t *testing.T, reg *registry.Registry, extra map[string]string, args ...string,
) result {
	t.Helper()
	var out, errOut strings.Builder
	env := isolate(t, nil)
	// A site is needed for the session to build at all; nothing here reaches
	// it, and the reserved TLD makes sure of that.
	env["JIRA_SITE"] = "https://jira.example.invalid"
	env["JIRA_API_TOKEN"] = "not-a-real-token"
	env["JIRA_AUTH_USER"] = "ada@example.invalid"
	for k, v := range extra {
		env[k] = v
	}
	code := cli.Main(context.Background(), args, cli.Options{
		Registry: reg,
		Stdout:   &out,
		Stderr:   &errOut,
		Getenv:   func(k string) string { return env[k] },
	})
	return result{exit: code, stdout: out.String(), stderr: errOut.String()}
}

// TestADryRunNeedsNoConfirmation covers the split between the two gates. --yes
// confirms an irreversible action; a preview is not one, and refusing to show
// the request until it has been confirmed inverts the order a caller works in.
func TestADryRunNeedsNoConfirmation(t *testing.T) {
	var ran bool
	got := runGated(t, fakeMutating(&ran, true), nil, "fake", "write", "--dry-run")

	if got.exit != exitcode.OK {
		t.Fatalf("exit = %v, want %v\nstderr: %s", got.exit, exitcode.OK, got.stderr)
	}
	if !ran {
		t.Error("a dry run of a destructive command was refused")
	}
}

// TestReadOnlyIsNotRelaxedForADryRun is the deliberate asymmetry. A missing
// --yes is a step the caller has not taken; a read-only context is a statement
// about what that context is for, and the latch stays one-way.
func TestReadOnlyIsNotRelaxedForADryRun(t *testing.T) {
	var ran bool
	got := runGated(t, fakeMutating(&ran, true), nil,
		"fake", "write", "--dry-run", "--readonly")

	if got.exit != exitcode.Blocked {
		t.Errorf("exit = %v, want %v", got.exit, exitcode.Blocked)
	}
	if ran {
		t.Error("a read-only context allowed a mutating command to run")
	}
	if !strings.Contains(got.stderr, "READ_ONLY") {
		t.Errorf("stderr does not carry the code: %s", got.stderr)
	}
}
