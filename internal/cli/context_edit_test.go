package cli_test

import (
	"strings"
	"testing"

	"github.com/kmoneil/jr/internal/exitcode"
)

// fullContext creates one with every setting populated, so a test can tell an
// edit that left things alone from one that happened to write the same values.
func fullContext(t *testing.T, env map[string]string) {
	t.Helper()
	mustRun(t, env, "context", "create", "work",
		"--site", "jira.acme.invalid",
		"--project", "ENG",
		"--board", "42",
		"--field", "Story Points",
		"--field", "Sprint",
		"--credential", "shared-key",
		"--readonly")
}

// TestEditingOneSettingLeavesTheRestAlone is the card, and the bug behind it.
// Re-stating a context through `context create` to change its project is how a
// board and a default field set get dropped without anyone noticing — which
// happened once during setup, which is why this command exists.
func TestEditingOneSettingLeavesTheRestAlone(t *testing.T) {
	env := session(t)
	fullContext(t, env)

	got := mustRun(t, env, "context", "edit", "work", "--project", "OPS")

	for _, want := range []string{
		`project="OPS"`,
		`site="https://jira.acme.invalid"`,
		`board="42"`,
		`readonly="true"`,
		`credential="shared-key"`,
		"Story Points",
		"Sprint",
	} {
		if !strings.Contains(got.stdout, want) {
			t.Errorf("editing the project lost %s:\n%s", want, got.stdout)
		}
	}

	// And it survives a reload, so the file was written and not only rendered.
	shown := mustRun(t, env, "context", "show", "work")
	if !strings.Contains(shown.stdout, `board="42"`) ||
		!strings.Contains(shown.stdout, `project="OPS"`) {
		t.Errorf("the edit did not survive a reload:\n%s", shown.stdout)
	}
}

// TestUnsetClearsWhatAnEmptyFlagCannot covers why --unset exists at all. An
// empty flag value and an absent one arrive identically, so clearing needs its
// own spelling.
func TestUnsetClearsWhatAnEmptyFlagCannot(t *testing.T) {
	env := session(t)
	fullContext(t, env)

	// The tempting way, which cannot work: this is indistinguishable from
	// passing no --board at all, so it must leave the board alone.
	kept := mustRun(t, env, "context", "edit", "work", "--board", "", "--project", "OPS")
	if !strings.Contains(kept.stdout, `board="42"`) {
		t.Errorf("an empty --board was read as a request to clear it:\n%s", kept.stdout)
	}

	got := mustRun(t, env, "context", "edit", "work",
		"--unset", "board", "--unset", "field", "--unset", "credential")
	if !strings.Contains(got.stdout, `board=""`) {
		t.Errorf("--unset board did not clear it:\n%s", got.stdout)
	}
	if strings.Contains(got.stdout, "Story Points") {
		t.Errorf("--unset field did not empty the set:\n%s", got.stdout)
	}
	// The credential falls back to the site's host rather than becoming empty,
	// which is what an unset credential means everywhere else.
	if strings.Contains(got.stdout, "shared-key") {
		t.Errorf("--unset credential did not clear it:\n%s", got.stdout)
	}
	// Nothing else moved.
	if !strings.Contains(got.stdout, `project="OPS"`) {
		t.Errorf("unsetting three things disturbed the project:\n%s", got.stdout)
	}
}

// TestUnsetReadonlyMakesAContextWritable covers the deliberate exception. The
// one-way latch governs an invocation — nothing a command does can promote
// itself — and not the configuration, which was always editable by deleting and
// re-creating the context.
func TestUnsetReadonlyMakesAContextWritable(t *testing.T) {
	env := session(t)
	fullContext(t, env)

	got := mustRun(t, env, "context", "edit", "work", "--unset", "readonly")
	if !strings.Contains(got.stdout, `readonly="false"`) {
		t.Errorf("--unset readonly did not clear it:\n%s", got.stdout)
	}

	back := mustRun(t, env, "context", "edit", "work", "--readonly")
	if !strings.Contains(back.stdout, `readonly="true"`) {
		t.Errorf("--readonly did not set it again:\n%s", back.stdout)
	}
}

// TestEditRefusesWhatItCannotDo covers the checks that run before the config is
// touched, so a rejected edit leaves the file exactly as it was.
func TestEditRefusesWhatItCannotDo(t *testing.T) {
	env := session(t)
	fullContext(t, env)

	for _, tc := range []struct {
		name string
		args []string
		code string
	}{
		{
			// A context without a site is not a context with one fewer
			// setting; it is one that cannot be used.
			name: "unsetting the site", args: []string{"--unset", "site"},
			code: "INVALID_UNSET",
		},
		{
			name: "unsetting something that is not a setting",
			args: []string{"--unset", "colour"}, code: "INVALID_UNSET",
		},
		{
			// The label-flag lesson from issue edit: both at once has no single
			// right answer, and picking one hides the choice.
			name: "setting and clearing the same thing",
			args: []string{"--project", "OPS", "--unset", "project"},
			code: "CONFLICTING_EDIT",
		},
		{
			name: "changing nothing", args: nil, code: "NOTHING_TO_EDIT",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := run(t, env, append([]string{"context", "edit", "work"}, tc.args...)...)
			if got.exit != exitcode.Usage {
				t.Fatalf("exit = %v, want %v\n%s", got.exit, exitcode.Usage, got.stderr)
			}
			if !strings.Contains(got.stderr, tc.code) {
				t.Errorf("stderr does not carry %s:\n%s", tc.code, got.stderr)
			}
		})
	}

	// Nothing above changed the context.
	shown := mustRun(t, env, "context", "show", "work")
	if !strings.Contains(shown.stdout, `project="ENG"`) {
		t.Errorf("a refused edit changed the context:\n%s", shown.stdout)
	}
}

// TestEditingAContextThatIsNotThereSaysWhichAre covers the same refusal every
// other context command gives, so a typo lists the alternatives.
func TestEditingAContextThatIsNotThereSaysWhichAre(t *testing.T) {
	env := session(t)
	fullContext(t, env)

	got := run(t, env, "context", "edit", "typo", "--project", "OPS")
	if got.exit == exitcode.OK {
		t.Fatal("a context that does not exist was edited")
	}
	if !strings.Contains(got.stderr, "work") {
		t.Errorf("the refusal does not list what does exist:\n%s", got.stderr)
	}
}

// TestARepeatableEnumAcceptsEveryLegalValue is a regression with a date on it.
//
// pflag renders a repeated flag as "[a b]", and the binder compared that whole
// rendering against the declared set — so a repeatable enum refused every value
// including the legal ones. It was a shape the registry could declare and the
// binder could not honor, and nothing had used it until `--unset` did.
func TestARepeatableEnumAcceptsEveryLegalValue(t *testing.T) {
	env := session(t)
	fullContext(t, env)

	got := mustRun(t, env, "context", "edit", "work",
		"--unset", "board", "--unset", "project")
	if !strings.Contains(got.stdout, `board=""`) ||
		!strings.Contains(got.stdout, `project=""`) {
		t.Errorf("two repeated enum values did not both apply:\n%s", got.stdout)
	}

	// One bad value among good ones is still refused, and names the bad one
	// rather than the rendering of the whole slice.
	bad := run(t, env, "context", "edit", "work", "--unset", "board", "--unset", "colour")
	if bad.exit != exitcode.Usage {
		t.Fatalf("exit = %v, want %v", bad.exit, exitcode.Usage)
	}
	if !strings.Contains(bad.stderr, `"colour"`) {
		t.Errorf("the refusal does not name the bad value:\n%s", bad.stderr)
	}
	if strings.Contains(bad.stderr, "[board") {
		t.Errorf("the refusal echoed the slice rendering:\n%s", bad.stderr)
	}
}
