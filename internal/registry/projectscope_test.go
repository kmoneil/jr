package registry_test

import (
	"strings"
	"testing"

	"github.com/kmoneil/jr/internal/errs"
	"github.com/kmoneil/jr/internal/registry"
	"github.com/kmoneil/jr/internal/render"
)

// scopeSession answers the five scope readers and nothing else. Embedding the
// interface supplies the rest as nil methods, which is the point: a test here
// that reached Connect or Metadata would panic, and none of these may.
type scopeSession struct {
	registry.Session
	projects []string
}

func (s scopeSession) Projects() []string { return s.projects }

func (s scopeSession) Project() string {
	if len(s.projects) == 1 {
		return s.projects[0]
	}
	return ""
}

func (s scopeSession) RequireProject() (string, error) {
	if p := s.Project(); p != "" {
		return p, nil
	}
	return "", errs.Usage("PROJECT_REPEATED", "the session's own refusal")
}

func (scopeSession) Board() string { return "" }

func gateFor(cmd *registry.Command, projects ...string) (*registry.ScopeWatcher, error) {
	w := registry.WatchScope(scopeSession{projects: projects})
	return w, registry.Gate(cmd, &registry.Invocation{Jira: w, Flags: registry.NewFlags()})
}

// TestGateRefusesARepeatedProjectWhereOneIsRead is the rule that makes a
// repeatable global safe to have. The flag is bound once on the root, so the
// parser takes a second --project on every command, and only a command that
// declares SeveralProjects answers for the union. Everywhere else the second
// key is refused before the command runs, with the session's own refusal.
func TestGateRefusesARepeatedProjectWhereOneIsRead(t *testing.T) {
	one := &registry.Command{Path: []string{"probe", "one"}, NeedsJira: true}
	union := &registry.Command{
		Path: []string{"probe", "union"}, NeedsJira: true, SeveralProjects: true,
	}

	_, err := gateFor(one, "OPS", "NET")
	if err == nil {
		t.Fatal("a command that reads one project was admitted with two")
	}
	if code := errs.Coerce(err).Code; code != "PROJECT_REPEATED" {
		t.Errorf("code = %s, want the session's PROJECT_REPEATED", code)
	}

	if _, err := gateFor(union, "OPS", "NET"); err != nil {
		t.Errorf("a command that takes several was refused two: %v", err)
	}
	// One project is the case every invocation before this was, on both.
	if _, err := gateFor(one, "OPS"); err != nil {
		t.Errorf("one project was refused: %v", err)
	}
	if _, err := gateFor(one); err != nil {
		t.Errorf("no project was refused: %v", err)
	}
}

// TestGateAsksAboutTheScopeWithoutReadingIt holds the gate to the watcher's
// own rule: the envelope reports the scope a command read. The gate has to
// know how many projects were given to every command, including one that was
// told exactly which issue to fetch, and if that counted as a read every
// answer would carry a project it was never computed over.
func TestGateAsksAboutTheScopeWithoutReadingIt(t *testing.T) {
	for _, cmd := range []*registry.Command{
		{Path: []string{"probe", "one"}, NeedsJira: true},
		{Path: []string{"probe", "union"}, NeedsJira: true, SeveralProjects: true},
	} {
		for _, projects := range [][]string{{"OPS"}, {"OPS", "NET"}} {
			w, _ := gateFor(cmd, projects...)
			if got := w.Scope(); !got.Empty() {
				t.Errorf("%s with %v: the gate left the scope %+v recorded, and "+
					"nothing had read one", cmd.Name(), projects, got)
			}
		}
	}
}

// TestASetOfProjectsHoldsOnlyProjectKeys is what lets the envelope write a set
// as one attribute. One --project is passed to Jira as typed and may be a
// name; a member of a set may not, because a list of values that can hold a
// space has no one way to be split.
func TestASetOfProjectsHoldsOnlyProjectKeys(t *testing.T) {
	union := &registry.Command{
		Path: []string{"probe", "union"}, NeedsJira: true, SeveralProjects: true,
	}
	_, err := gateFor(union, "OPS", "Platform Team")
	if err == nil {
		t.Fatal("a set holding a project name was admitted")
	}
	e := errs.Coerce(err)
	if e.Code != "INVALID_PROJECT_KEY" || !strings.Contains(e.Message, "Platform Team") {
		t.Errorf("refusal = %s %q, want INVALID_PROJECT_KEY naming the value", e.Code, e.Message)
	}
	// Alone, the same value is not this check's business.
	if _, err := gateFor(union, "Platform Team"); err != nil {
		t.Errorf("one --project was held to the key grammar: %v", err)
	}
}

// TestTheWatcherReportsOneProjectAndASetApart is the envelope's rule at its
// source. A scope of one is `project`, a scope of several is `projects`, and
// they are never both set, whichever accessor the command read it through.
func TestTheWatcherReportsOneProjectAndASetApart(t *testing.T) {
	several := registry.WatchScope(scopeSession{projects: []string{"OPS", "NET"}})
	if got := several.Projects(); strings.Join(got, " ") != "OPS NET" {
		t.Fatalf("Projects() = %q through the watcher", got)
	}
	if got := several.Scope(); got.Project != "" || strings.Join(got.Projects, " ") != "OPS NET" {
		t.Errorf("scope = %+v, want the set and no single project", got)
	}

	one := registry.WatchScope(scopeSession{projects: []string{"OPS"}})
	_ = one.Projects()
	if got := one.Scope(); got.Project != "OPS" || len(got.Projects) != 0 {
		t.Errorf("scope = %+v, want the one project and no set", got)
	}

	unread := registry.WatchScope(scopeSession{projects: []string{"OPS"}})
	if got := unread.Scope(); !got.Empty() {
		t.Errorf("scope = %+v before anything was read", got)
	}

	var none *registry.ScopeWatcher
	if got := none.Scope(); !got.Empty() {
		t.Errorf("a nil watcher reported the scope %+v", got)
	}
}

// TestRepeatableIsWhatTheCommandDoesWithASecondProject holds `jr schema` to the
// gate. --project parses a repeat everywhere, so reporting the parser's view
// would mark it repeatable on commands that refuse one.
func TestRepeatableIsWhatTheCommandDoesWithASecondProject(t *testing.T) {
	repeatable := func(c *registry.Command) bool {
		for _, f := range c.InheritedGlobals() {
			if f.Name == registry.GlobalProject {
				return f.Repeatable
			}
		}
		t.Fatalf("%s inherits no --project", c.Name())
		return false
	}
	if repeatable(&registry.Command{Path: []string{"probe", "one"}}) {
		t.Error("--project is reported repeatable on a command that reads one")
	}
	if !repeatable(&registry.Command{Path: []string{"probe", "union"}, SeveralProjects: true}) {
		t.Error("--project is not reported repeatable on a command that takes several")
	}
	// The flag itself is declared repeatable, which is what binds it as a list.
	if f, _ := registry.GlobalFlag(registry.GlobalProject); !f.Repeatable {
		t.Error("the global --project is not declared repeatable")
	}
}

// TestTheEnvelopeSpellsASetOfProjectsUnderItsOwnName is the contract a consumer
// reads. `project` holds one key and is absent for a set, so a reader written
// before sets existed never meets a list where it expected a key.
func TestTheEnvelopeSpellsASetOfProjectsUnderItsOwnName(t *testing.T) {
	doc := render.Record("probe", 1, render.El("probe"))
	doc.SetScope(render.ScopeOf([]string{"OPS", "NET", "DO"}, ""))

	var xml, json strings.Builder
	if err := render.Write(&xml, doc, render.XML); err != nil {
		t.Fatalf("xml: %v", err)
	}
	if !strings.Contains(xml.String(), ` projects="OPS NET DO"`) {
		t.Errorf("the envelope does not name the set:\n%s", xml.String())
	}
	if strings.Contains(xml.String(), ` project="`) {
		t.Errorf("a set was also written as one project:\n%s", xml.String())
	}
	if err := render.Write(&json, doc, render.JSON); err != nil {
		t.Fatalf("json: %v", err)
	}
	if !strings.Contains(json.String(), `"projects": [`) || strings.Contains(json.String(), `"project":`) {
		t.Errorf("JSON does not carry the set as a list under its own name:\n%s", json.String())
	}

	doc.SetScope(render.ScopeOf([]string{"OPS"}, ""))
	xml.Reset()
	if err := render.Write(&xml, doc, render.XML); err != nil {
		t.Fatalf("xml: %v", err)
	}
	if !strings.Contains(xml.String(), ` project="OPS"`) || strings.Contains(xml.String(), "projects=") {
		t.Errorf("one project is not written as it always was:\n%s", xml.String())
	}
}
