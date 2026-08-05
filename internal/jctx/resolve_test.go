package jctx_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/kmoneil/jira-cli/internal/errs"
	"github.com/kmoneil/jira-cli/internal/exitcode"
	"github.com/kmoneil/jira-cli/internal/jctx"
)

func configWith(t *testing.T, current string, contexts map[string]jctx.Context) *jctx.Config {
	t.Helper()
	cfg, err := jctx.Load(filepath.Join(t.TempDir(), "config.toml"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	for name, ctx := range contexts {
		if err := cfg.Set(name, ctx); err != nil {
			t.Fatalf("set %s: %v", name, err)
		}
	}
	if current != "" {
		if err := cfg.Use(current); err != nil {
			t.Fatalf("use: %v", err)
		}
	}
	return cfg
}

// TestPrecedence pins the order: flag, then environment, then context. Each
// field resolves independently, so overriding one does not silently discard
// the rest of the context.
func TestPrecedence(t *testing.T) {
	cfg := configWith(t, "work", map[string]jctx.Context{
		"work": {Site: "ctx.atlassian.net", Project: "CTXPROJ", Board: "1"},
	})

	cases := []struct {
		name                 string
		over                 jctx.Overrides
		env                  map[string]string
		site, project, board string
	}{
		{
			"context only",
			jctx.Overrides{},
			nil,
			"https://ctx.atlassian.net", "CTXPROJ", "1",
		},
		{
			"env beats context",
			jctx.Overrides{},
			map[string]string{
				jctx.EnvSite: "env.atlassian.net", jctx.EnvProject: "ENVPROJ",
			},
			"https://env.atlassian.net", "ENVPROJ", "1",
		},
		{
			"flag beats env",
			jctx.Overrides{Site: "flag.atlassian.net", Project: "FLAGPROJ"},
			map[string]string{
				jctx.EnvSite: "env.atlassian.net", jctx.EnvProject: "ENVPROJ",
			},
			"https://flag.atlassian.net", "FLAGPROJ", "1",
		},
		{
			"one field overridden leaves the others alone",
			jctx.Overrides{Project: "OTHER"},
			nil,
			"https://ctx.atlassian.net", "OTHER", "1",
		},
		{
			"board from env",
			jctx.Overrides{},
			map[string]string{jctx.EnvBoard: "99"},
			"https://ctx.atlassian.net", "CTXPROJ", "99",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := jctx.Resolve(cfg, tc.over, env(tc.env))
			if err != nil {
				t.Fatalf("resolve: %v", err)
			}
			if got.Site != tc.site {
				t.Errorf("Site = %q, want %q", got.Site, tc.site)
			}
			if got.Project != tc.project {
				t.Errorf("Project = %q, want %q", got.Project, tc.project)
			}
			if got.Board != tc.board {
				t.Errorf("Board = %q, want %q", got.Board, tc.board)
			}
		})
	}
}

// TestReadOnlyIsAOneWayLatch is the safety property. A context created
// --readonly is a statement about what it is for, and an invocation that simply
// omits the flag must not quietly promote itself to read-write.
func TestReadOnlyIsAOneWayLatch(t *testing.T) {
	readOnlyCtx := configWith(t, "audit", map[string]jctx.Context{
		"audit": {Site: "acme.atlassian.net", ReadOnly: true},
	})
	writableCtx := configWith(t, "work", map[string]jctx.Context{
		"work": {Site: "acme.atlassian.net"},
	})

	cases := []struct {
		name string
		cfg  *jctx.Config
		over jctx.Overrides
		env  map[string]string
		want bool
	}{
		{"writable context, no flags", writableCtx, jctx.Overrides{}, nil, false},
		{"read-only context", readOnlyCtx, jctx.Overrides{}, nil, true},
		{
			"read-only context, flag omitted, stays read-only",
			readOnlyCtx,
			jctx.Overrides{ReadOnly: false},
			nil, true,
		},
		{
			"env alone",
			writableCtx,
			jctx.Overrides{},
			map[string]string{jctx.EnvReadOnly: "1"},
			true,
		},
		{"flag alone", writableCtx, jctx.Overrides{ReadOnly: true}, nil, true},
		{
			"env explicitly off against a read-only context stays read-only",
			readOnlyCtx,
			jctx.Overrides{},
			map[string]string{jctx.EnvReadOnly: "0"},
			true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := jctx.Resolve(tc.cfg, tc.over, env(tc.env))
			if err != nil {
				t.Fatalf("resolve: %v", err)
			}
			if got.ReadOnly != tc.want {
				t.Errorf("ReadOnly = %v, want %v", got.ReadOnly, tc.want)
			}
		})
	}
}

// TestReadOnlyEnvIsGenerous covers a caller who wrote something other than 1.
// JIRA_READONLY=yes from someone who meant it must not silently mean
// read-write.
func TestReadOnlyEnvIsGenerous(t *testing.T) {
	cfg := configWith(t, "work", map[string]jctx.Context{
		"work": {Site: "acme.atlassian.net"},
	})

	on := []string{"1", "true", "TRUE", "yes", "y", "on", "readonly", "please"}
	for _, v := range on {
		got, err := jctx.Resolve(cfg, jctx.Overrides{},
			env(map[string]string{jctx.EnvReadOnly: v}))
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if !got.ReadOnly {
			t.Errorf("%s=%q did not enable read-only", jctx.EnvReadOnly, v)
		}
	}

	off := []string{"", "0", "false", "FALSE", "no", "off"}
	for _, v := range off {
		got, err := jctx.Resolve(cfg, jctx.Overrides{},
			env(map[string]string{jctx.EnvReadOnly: v}))
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if got.ReadOnly {
			t.Errorf("%s=%q enabled read-only", jctx.EnvReadOnly, v)
		}
	}
}

func TestCheckWritable(t *testing.T) {
	cfg := configWith(t, "audit", map[string]jctx.Context{
		"audit": {Site: "acme.atlassian.net", ReadOnly: true},
	})
	resolved, err := jctx.Resolve(cfg, jctx.Overrides{}, nil)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}

	err = resolved.CheckWritable("issue create")
	if err == nil {
		t.Fatal("a read-only context permitted a mutation")
	}
	if errs.ExitOf(err) != exitcode.Blocked {
		t.Errorf("exit = %v, want %v", errs.ExitOf(err), exitcode.Blocked)
	}
	if !strings.Contains(errs.Coerce(err).Detail, "audit") {
		t.Errorf("the error does not name the context: %q", errs.Coerce(err).Detail)
	}

	writable := configWith(t, "work", map[string]jctx.Context{
		"work": {Site: "acme.atlassian.net"},
	})
	ok, err := jctx.Resolve(writable, jctx.Overrides{}, nil)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if err := ok.CheckWritable("issue create"); err != nil {
		t.Errorf("a writable context refused a mutation: %v", err)
	}
}

func TestContextSelection(t *testing.T) {
	cfg := configWith(t, "work", map[string]jctx.Context{
		"work":     {Site: "work.atlassian.net", Project: "ENG"},
		"personal": {Site: "personal.atlassian.net", Project: "HOME"},
	})

	current, err := jctx.Resolve(cfg, jctx.Overrides{}, nil)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if current.Name != "work" || current.Project != "ENG" {
		t.Errorf("resolved %+v, want the current context", current)
	}

	// --context is a one-off override and does not change what is current.
	oneOff, err := jctx.Resolve(cfg, jctx.Overrides{Context: "personal"}, nil)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if oneOff.Name != "personal" || oneOff.Project != "HOME" {
		t.Errorf("resolved %+v, want the named context", oneOff)
	}
	if cfg.Current != "work" {
		t.Errorf("Current changed to %q", cfg.Current)
	}

	fromEnv, err := jctx.Resolve(cfg, jctx.Overrides{},
		env(map[string]string{jctx.EnvContext: "personal"}))
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if fromEnv.Name != "personal" {
		t.Errorf("%s was ignored", jctx.EnvContext)
	}
}

func TestUnknownContextIsAnError(t *testing.T) {
	cfg := configWith(t, "work", map[string]jctx.Context{
		"work": {Site: "acme.atlassian.net"},
	})
	_, err := jctx.Resolve(cfg, jctx.Overrides{Context: "nope"}, nil)
	if err == nil {
		t.Fatal("an unknown context resolved successfully")
	}
	if errs.ExitOf(err) != exitcode.NotFound {
		t.Errorf("exit = %v, want %v", errs.ExitOf(err), exitcode.NotFound)
	}
	if !strings.Contains(errs.Coerce(err).Detail, "work") {
		t.Errorf("the error does not list what is defined: %q", errs.Coerce(err).Detail)
	}
}

// TestNoContextIsUsable covers a one-off command with only --site, which is a
// legitimate way to run against a site you have not made a context for.
func TestNoContextIsUsable(t *testing.T) {
	cfg := configWith(t, "", nil)
	got, err := jctx.Resolve(cfg, jctx.Overrides{Site: "acme.atlassian.net"}, nil)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got.Name != "" {
		t.Errorf("Name = %q, want empty", got.Name)
	}
	if got.Site != "https://acme.atlassian.net" {
		t.Errorf("Site = %q", got.Site)
	}
	if _, err := got.RequireSite(); err != nil {
		t.Errorf("RequireSite: %v", err)
	}
}

// TestProjectIsNeverMandatory is the spec's rule: project defaults from the
// context and can always be omitted. Only the handful of commands that cannot
// proceed without one ask, and they exit 2 naming the flag.
func TestProjectIsNeverMandatory(t *testing.T) {
	cfg := configWith(t, "work", map[string]jctx.Context{
		"work": {Site: "acme.atlassian.net"},
	})
	resolved, err := jctx.Resolve(cfg, jctx.Overrides{}, nil)
	if err != nil {
		t.Fatalf("resolve without a project failed: %v", err)
	}
	if resolved.Project != "" {
		t.Errorf("Project = %q", resolved.Project)
	}

	_, err = resolved.RequireProject()
	if err == nil {
		t.Fatal("RequireProject succeeded with no project")
	}
	if errs.ExitOf(err) != exitcode.Usage {
		t.Errorf("exit = %v, want %v", errs.ExitOf(err), exitcode.Usage)
	}
	e := errs.Coerce(err)
	if !strings.Contains(e.Remedy, "--project") {
		t.Errorf("the remedy does not name the flag: %q", e.Remedy)
	}
	if !strings.Contains(e.Remedy, "work") {
		t.Errorf("the remedy does not name the context to fix: %q", e.Remedy)
	}
}

func TestRequireSiteAndBoard(t *testing.T) {
	empty := &jctx.Resolved{}

	if _, err := empty.RequireSite(); err == nil {
		t.Error("RequireSite succeeded with no site")
	} else if !strings.Contains(errs.Coerce(err).Remedy, "--site") {
		t.Errorf("remedy does not name the flag: %q", errs.Coerce(err).Remedy)
	}

	if _, err := empty.RequireBoard(); err == nil {
		t.Error("RequireBoard succeeded with no board")
	} else if !strings.Contains(errs.Coerce(err).Remedy, "--board") {
		t.Errorf("remedy does not name the flag: %q", errs.Coerce(err).Remedy)
	}
}

func TestFieldsOverride(t *testing.T) {
	cfg := configWith(t, "work", map[string]jctx.Context{
		"work": {Site: "acme.atlassian.net", Fields: []string{"summary", "status"}},
	})

	fromContext, err := jctx.Resolve(cfg, jctx.Overrides{}, nil)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if strings.Join(fromContext.Fields, ",") != "summary,status" {
		t.Errorf("Fields = %v", fromContext.Fields)
	}

	// Flags replace the context's fields rather than appending, so a caller
	// asking for one field gets one field.
	overridden, err := jctx.Resolve(cfg, jctx.Overrides{Fields: []string{"key"}}, nil)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if strings.Join(overridden.Fields, ",") != "key" {
		t.Errorf("Fields = %v, want only the override", overridden.Fields)
	}

	// And the result must not alias the config.
	overridden.Fields[0] = "mutated"
	again, _ := cfg.Get("work")
	if again.Fields[0] != "summary" {
		t.Errorf("the resolved fields alias the stored context: %v", again.Fields)
	}
}

func TestResolveWithNilConfig(t *testing.T) {
	got, err := jctx.Resolve(nil, jctx.Overrides{Site: "acme.atlassian.net"}, nil)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got.Site != "https://acme.atlassian.net" {
		t.Errorf("Site = %q", got.Site)
	}
	if got.CredentialRef != "acme.atlassian.net" {
		t.Errorf("CredentialRef = %q", got.CredentialRef)
	}
}

func TestResolveRejectsABadSite(t *testing.T) {
	if _, err := jctx.Resolve(nil, jctx.Overrides{Site: "ftp://acme"}, nil); err == nil {
		t.Fatal("a bad --site resolved successfully")
	}
}
