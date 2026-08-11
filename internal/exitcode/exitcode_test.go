package exitcode_test

import (
	"testing"

	"github.com/kmoneil/jr/internal/exitcode"
)

// TestCodesAreFrozen pins every code to its number and name.
//
// These are a public API. A code never changes meaning once shipped, so this
// table is not a restatement of the implementation — it is the contract, and a
// diff here means a major version bump.
func TestCodesAreFrozen(t *testing.T) {
	frozen := []struct {
		code exitcode.Code
		n    int
		name string
	}{
		{exitcode.OK, 0, "OK"},
		{exitcode.Error, 1, "ERROR"},
		{exitcode.Usage, 2, "USAGE"},
		{exitcode.Partial, 3, "PARTIAL"},
		{exitcode.Auth, 4, "AUTH"},
		{exitcode.NotFound, 5, "NOT_FOUND"},
		{exitcode.Permission, 6, "PERMISSION"},
		{exitcode.Conflict, 7, "CONFLICT"},
		{exitcode.RateLimit, 8, "RATE_LIMIT"},
		{exitcode.Remote, 9, "REMOTE"},
		{exitcode.Blocked, 10, "BLOCKED"},
	}
	for _, f := range frozen {
		if f.code.Int() != f.n {
			t.Errorf("%s is %d, want %d", f.name, f.code.Int(), f.n)
		}
		if f.code.Name() != f.name {
			t.Errorf("code %d is named %q, want %q", f.n, f.code.Name(), f.name)
		}
	}
	if len(exitcode.All()) != len(frozen) {
		t.Errorf("All() returns %d codes but %d are frozen; "+
			"a new code needs an entry in this table", len(exitcode.All()), len(frozen))
	}
}

func TestEveryCodeIsNamedAndDescribed(t *testing.T) {
	for _, c := range exitcode.All() {
		if c.Name() == "UNKNOWN" {
			t.Errorf("code %d has no name", c.Int())
		}
		if c.Description() == "" {
			t.Errorf("code %d (%s) has no description", c.Int(), c.Name())
		}
	}
}

func TestAllIsOrderedAndUnique(t *testing.T) {
	seen := map[exitcode.Code]bool{}
	prev := -1
	for _, c := range exitcode.All() {
		if seen[c] {
			t.Errorf("All() repeats code %d", c.Int())
		}
		if c.Int() <= prev {
			t.Errorf("All() is not in numeric order at %d", c.Int())
		}
		seen[c] = true
		prev = c.Int()
	}
}

func TestAllReturnsACopy(t *testing.T) {
	first := exitcode.All()
	first[0] = exitcode.Code(99)
	if exitcode.All()[0] != exitcode.OK {
		t.Error("All() exposes its backing array; a caller can rewrite the code table")
	}
}

func TestUnknownCodeIsNotSilentlyValid(t *testing.T) {
	c := exitcode.Code(42)
	if c.Name() != "UNKNOWN" {
		t.Errorf("an undefined code names itself %q", c.Name())
	}
}
