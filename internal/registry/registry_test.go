package registry_test

import (
	"slices"
	"testing"

	"github.com/kmoneil/jira-cli/internal/errs"
	"github.com/kmoneil/jira-cli/internal/exitcode"
	"github.com/kmoneil/jira-cli/internal/registry"
)

// The rules this project promises about the command surface — banned flags,
// short-flag spelling, agent safety, tag declarations — are asserted in
// internal/cli, which is where the full registry is assembled. This file tests
// the registry machinery itself.

func TestParseLimit(t *testing.T) {
	cases := []struct {
		in   string
		want registry.Limit
		ok   bool
	}{
		{"1", registry.Limit{N: 1}, true},
		{"50", registry.Limit{N: 50}, true},
		{"100000", registry.Limit{N: 100000}, true},
		{"all", registry.Limit{All: true}, true},
		{" ALL ", registry.Limit{All: true}, true},
		{"0", registry.Limit{}, false},
		{"-1", registry.Limit{}, false},
		{"", registry.Limit{}, false},
		// The incumbent's offset-shaped pagination flag. It parses as nothing
		// here, which is the point.
		{"50:2", registry.Limit{}, false},
		{"lots", registry.Limit{}, false},
	}
	for _, tc := range cases {
		got, err := registry.ParseLimit(tc.in)
		if tc.ok {
			if err != nil {
				t.Errorf("ParseLimit(%q) = %v, want %+v", tc.in, err, tc.want)
			} else if got != tc.want {
				t.Errorf("ParseLimit(%q) = %+v, want %+v", tc.in, got, tc.want)
			}
			continue
		}
		if err == nil {
			t.Errorf("ParseLimit(%q) = %+v, want an error", tc.in, got)
			continue
		}
		if errs.ExitOf(err) != exitcode.Usage {
			t.Errorf("ParseLimit(%q) exits %v, want %v", tc.in, errs.ExitOf(err), exitcode.Usage)
		}
	}
}

// TestLimitAllIsNeverSatisfied asserts --limit all keeps paging rather than
// stopping at some internal cap.
func TestLimitAllIsNeverSatisfied(t *testing.T) {
	all := registry.Limit{All: true}
	for _, n := range []int{0, 1, 100, 100000} {
		if all.Satisfied(n) {
			t.Errorf("--limit all reported satisfied at %d results", n)
		}
	}
	l := registry.Limit{N: 50}
	if l.Satisfied(49) || !l.Satisfied(50) || !l.Satisfied(51) {
		t.Error("--limit N is not satisfied exactly at N")
	}
}

func TestFlagsRoundTrip(t *testing.T) {
	f := registry.NewFlags()
	f.SetString("label", "a")
	f.SetString("label", "b")
	f.SetString("project", "ENG")
	f.SetInt("retries", 3)
	f.SetBool("dry-run", true)

	if got := f.StringSlice("label"); !slices.Equal(got, []string{"a", "b"}) {
		t.Errorf("repeated flag accumulated as %v, want [a b]", got)
	}
	if got := f.String("label"); got != "b" {
		t.Errorf("String on a repeated flag = %q, want the last value", got)
	}
	if got := f.String("project"); got != "ENG" {
		t.Errorf("String(project) = %q", got)
	}
	if got := f.Int("retries"); got != 3 {
		t.Errorf("Int(retries) = %d", got)
	}
	if !f.Bool("dry-run") {
		t.Error("Bool(dry-run) = false")
	}
	if got := f.String("absent"); got != "" {
		t.Errorf("String on an unset flag = %q, want empty", got)
	}
}

func TestRegisterRejectsDuplicates(t *testing.T) {
	r := registry.New()
	r.Register(&registry.Command{Path: []string{"issue", "list"}})

	defer func() {
		if recover() == nil {
			t.Fatal("registering a duplicate command did not panic")
		}
	}()
	r.Register(&registry.Command{Path: []string{"issue", "list"}})
}

func TestAllIsOrdered(t *testing.T) {
	r := registry.New()
	for _, p := range [][]string{
		{"issue", "list"}, {"auth", "login"}, {"issue", "get"}, {"issue", "comment", "add"},
	} {
		r.Register(&registry.Command{Path: p})
	}
	var got []string
	for _, c := range r.All() {
		got = append(got, c.Name())
	}
	want := []string{"auth.login", "issue.comment.add", "issue.get", "issue.list"}
	if !slices.Equal(got, want) {
		t.Errorf("All() = %v, want %v", got, want)
	}
}

func TestKindsAggregatesEmitters(t *testing.T) {
	r := registry.New()
	r.Register(&registry.Command{
		Path:    []string{"issue", "list"},
		Outputs: []registry.Output{{Kind: "issue.list", Version: 1}},
	})
	r.Register(&registry.Command{
		Path: []string{"issue", "search"},
		Outputs: []registry.Output{
			{Kind: "issue.list", Version: 1},
			{Kind: "issue.get", Version: 2, When: "exactly one match"},
		},
	})

	kinds := r.Kinds()
	if len(kinds) != 2 {
		t.Fatalf("Kinds() returned %d kinds, want 2: %+v", len(kinds), kinds)
	}
	if kinds[0].Name != "issue.get" || kinds[0].Version != 2 {
		t.Errorf("kinds[0] = %+v, want issue.get v2", kinds[0])
	}
	if kinds[1].Name != "issue.list" || kinds[1].Version != 1 {
		t.Errorf("kinds[1] = %+v, want issue.list v1", kinds[1])
	}
	if !slices.Equal(kinds[1].Emitters, []string{"issue.list", "issue.search"}) {
		t.Errorf("issue.list emitters = %v", kinds[1].Emitters)
	}
}

func TestEmitsOnlyAcceptsDeclaredShapes(t *testing.T) {
	c := &registry.Command{
		Path: []string{"schema"},
		Outputs: []registry.Output{
			{Kind: "schema.commands", Version: 1},
			{Kind: "schema.command", Version: 1, When: "a command name is given"},
		},
	}
	if !c.Emits("schema.commands", 1) || !c.Emits("schema.command", 1) {
		t.Error("Emits rejected a declared shape")
	}
	if c.Emits("schema.commands", 2) {
		t.Error("Emits accepted a declared kind at an undeclared version")
	}
	if c.Emits("issue.list", 1) {
		t.Error("Emits accepted an undeclared kind")
	}
	if got := c.Kind(); got != "schema.commands" {
		t.Errorf("Kind() = %q, want the first declared output", got)
	}
}

func TestArgBounds(t *testing.T) {
	cases := []struct {
		name             string
		args             []registry.Arg
		wantMin, wantMax int
		spec             string
	}{
		{"none", nil, 0, 0, ""},
		{
			"one required",
			[]registry.Arg{{Name: "key", Required: true}},
			1, 1, "<key>",
		},
		{
			"required then optional",
			[]registry.Arg{{Name: "key", Required: true}, {Name: "body"}},
			1, 2, "<key> [body]",
		},
		{
			"variadic",
			[]registry.Arg{{Name: "key", Required: true}, {Name: "label", Variadic: true}},
			1, -1, "<key> [label...]",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &registry.Command{Path: []string{"x"}, Args: tc.args}
			gotMin, gotMax := c.ArgBounds()
			if gotMin != tc.wantMin || gotMax != tc.wantMax {
				t.Errorf("ArgBounds() = (%d, %d), want (%d, %d)",
					gotMin, gotMax, tc.wantMin, tc.wantMax)
			}
			if got := c.ArgSpec(); got != tc.spec {
				t.Errorf("ArgSpec() = %q, want %q", got, tc.spec)
			}
		})
	}
}

func TestAllExitCodesIncludesTheUniversalOnes(t *testing.T) {
	c := &registry.Command{
		Path:      []string{"issue", "get"},
		ExitCodes: []exitcode.Code{exitcode.NotFound, exitcode.Auth, exitcode.NotFound},
	}
	want := []exitcode.Code{
		exitcode.OK, exitcode.Error, exitcode.Usage, exitcode.Auth, exitcode.NotFound,
	}
	if got := c.AllExitCodes(); !slices.Equal(got, want) {
		t.Errorf("AllExitCodes() = %v, want %v", got, want)
	}
}

func TestFilterByPrefix(t *testing.T) {
	cmds := []*registry.Command{
		{Path: []string{"issue", "list"}},
		{Path: []string{"issue", "comment", "add"}},
		{Path: []string{"issuetype", "list"}},
		{Path: []string{"epic", "list"}},
	}
	var got []string
	for _, c := range registry.Filter(cmds, "issue") {
		got = append(got, c.Name())
	}
	// "issuetype.list" must not match the "issue" prefix: a prefix is a path
	// boundary, not a string boundary.
	want := []string{"issue.list", "issue.comment.add"}
	if !slices.Equal(got, want) {
		t.Errorf("Filter(cmds, \"issue\") = %v, want %v", got, want)
	}
}
