package cli_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/kmoneil/jira-cli/internal/buildinfo"
	"github.com/kmoneil/jira-cli/internal/cli"
	"github.com/kmoneil/jira-cli/internal/errs"
	"github.com/kmoneil/jira-cli/internal/exitcode"
	"github.com/kmoneil/jira-cli/internal/registry"
	"github.com/kmoneil/jira-cli/internal/render"

	// The contract rules must hold for every command the binary ships, not
	// only the built-ins, so the resources are linked in here too.
	_ "github.com/kmoneil/jira-cli/internal/commands"
)

// bannedFlags are spellings this project has decided never to ship. Each is a
// shape that lets a command lie about what it did.
var bannedFlags = map[string]string{
	// A boolean negation on top of an implicit default is a coin flip every
	// time. Sorting is --sort <field> plus --order asc|desc.
	"reverse": "use --sort and --order instead",
	// The upstream API is cursor-based. An offset we cannot honor is how the
	// incumbent shipped a silent lie.
	"paginate": "use --limit, --page-size, and --page-token",
	"offset":   "the upstream API is cursor-based; use --page-token",
	"start-at": "the upstream API is cursor-based; use --page-token",
	// --plain, --raw, --csv, and --no-headers are escape hatches out of an
	// interactive layer this tool does not have. Output shape is --format.
	"plain":      "use --format",
	"raw":        "use --format",
	"csv":        "use --format tsv",
	"no-headers": "use --format",
}

func TestNoBannedFlags(t *testing.T) {
	forEachCommand(t, func(t *testing.T, c *registry.Command) {
		for _, f := range c.Flags {
			if remedy, banned := bannedFlags[f.Name]; banned {
				t.Errorf("%s declares banned flag --%s: %s", c.Name(), f.Name, remedy)
			}
		}
	})
}

// jqlReportsRatherThanRefuses names the commands that accept a raw JQL fragment
// and deliberately do not refuse a malformed one, with the reason each does not.
//
// The list is the point of the test, like notYetGating in internal/lint. A new
// command that takes --jql and forgets to validate it fails here, and cannot be
// quieted except by writing down why it is exempt.
var jqlReportsRatherThanRefuses = map[string]string{
	"jql.validate": "whether the query parses is the question it exists to " +
		"answer; refusing the input would refuse to report on it. It answers " +
		"valid=\"false\" at exit 0, which TestAMalformedQueryIsAResultNotAnError " +
		"and TestAQueryThatCannotLexCostsNoRoundTrip assert",
}

// malformedFragments are fragments no command may combine into a query it
// sends.
//
// The first two are the escape. A fragment carrying its own unbalanced
// parenthesis closes the wrapper the builder puts around it and opens a new
// group after it, and because AND binds tighter than OR,
// `project = "ENG" AND (a) OR (1=1)` reads as `(project = "ENG" AND a) OR
// (1=1)` — the project scope is gone, and the result reports itself complete.
// Both balance at zero, so counting parentheses would call them fine.
var malformedFragments = []string{
	`a) OR (1=1`,
	`a) AND (b`,
	`) OR (1=1`,
	`(project = ENG`,
	`   `,
}

// TestRawJQLIsRefusedWhereverItIsAccepted is the finding underneath the finding
// that produced it.
//
// `jr jql explain` called jql.Validate and `jr issue list` did not, so the
// surface that describes what would be sent refused a fragment the surface that
// sends it accepted. Nothing compared them, which is why they were free to
// drift and did. This compares them: for one fragment, every command that takes
// it must reach the same verdict, by the same code.
func TestRawJQLIsRefusedWhereverItIsAccepted(t *testing.T) {
	var takesJQL []*registry.Command
	for _, c := range cli.Registry().All() {
		if slices.ContainsFunc(c.Flags, func(f registry.Flag) bool {
			return f.Name == "jql"
		}) && jqlReportsRatherThanRefuses[c.Name()] == "" {
			takesJQL = append(takesJQL, c)
		}
	}
	// Two surfaces is the whole point. One would be an agreement with itself,
	// and none would be an assertion that ran nothing.
	if len(takesJQL) < 2 {
		t.Fatalf("only %d command takes a raw --jql; this build cannot show "+
			"two surfaces agreeing", len(takesJQL))
	}

	for _, fragment := range malformedFragments {
		t.Run(fragment, func(t *testing.T) {
			verdicts := map[string][]string{}
			for _, c := range takesJQL {
				flags := registry.NewFlags()
				flags.SetString("jql", fragment)
				err := c.Validate(t.Context(), &registry.Invocation{
					Flags: flags, Limit: registry.Limit{N: registry.DefaultLimit},
					Progress: registry.NoProgress,
				})
				if err == nil {
					t.Errorf("%s accepted %q", c.Name(), fragment)
					verdicts["accepted"] = append(verdicts["accepted"], c.Name())
					continue
				}
				if got := errs.ExitOf(err); got != exitcode.Usage {
					t.Errorf("%s refused %q with exit %v, want %v",
						c.Name(), fragment, got, exitcode.Usage)
				}
				code := errs.Coerce(err).Code
				verdicts[code] = append(verdicts[code], c.Name())
			}
			if len(verdicts) > 1 {
				t.Errorf("the surfaces disagree about %q: %v", fragment, verdicts)
			}
		})
	}
}

// TestShortFlagLettersAppearInTheirNames enforces the rule that makes short
// flags guessable: no single-letter flag whose letter is not in its own name.
func TestShortFlagLettersAppearInTheirNames(t *testing.T) {
	forEachCommand(t, func(t *testing.T, c *registry.Command) {
		for _, f := range c.Flags {
			if f.Short == "" {
				continue
			}
			if len(f.Short) != 1 {
				t.Errorf("%s: --%s declares short flag %q, which is not one letter",
					c.Name(), f.Name, f.Short)
				continue
			}
			if !strings.Contains(strings.ToLower(f.Name), strings.ToLower(f.Short)) {
				t.Errorf("%s: -%s for --%s is unguessable; its letter is not in its name",
					c.Name(), f.Short, f.Name)
			}
		}
	})
}

// TestShortFlagsAreNotReusedWithDifferentMeanings asserts a short flag never
// means one thing on one command and something else on another.
func TestShortFlagsAreNotReusedWithDifferentMeanings(t *testing.T) {
	meaning, owner := map[string]string{}, map[string]string{}
	for _, c := range cli.Registry().All() {
		for _, f := range c.Flags {
			if f.Short == "" {
				continue
			}
			if prev, seen := meaning[f.Short]; seen && prev != f.Name {
				t.Errorf("-%s means --%s on %s but --%s on %s",
					f.Short, prev, owner[f.Short], f.Name, c.Name())
				continue
			}
			meaning[f.Short], owner[f.Short] = f.Name, c.Name()
		}
	}
}

// TestMutatingCommandsAreSafeByConstruction asserts the agent-safety rules:
// every mutating command can be dry-run and requires the write tag, and every
// destructive one requires --yes.
func TestMutatingCommandsAreSafeByConstruction(t *testing.T) {
	forEachCommand(t, func(t *testing.T, c *registry.Command) {
		// A command that removes something must change something, whether
		// that is Jira or a local file.
		if c.Destructive && !c.Mutating && !c.LocalState {
			t.Errorf("%s is destructive but changes neither Jira nor local state", c.Name())
		}
		// --yes is required for anything destructive, wherever it destroys.
		if c.Destructive {
			if _, has := c.Flag("yes"); !has {
				t.Errorf("%s is destructive but does not require --yes", c.Name())
			}
		}
		if !c.Mutating {
			return
		}
		if _, has := c.Flag("dry-run"); !has {
			t.Errorf("%s mutates but does not accept --dry-run", c.Name())
		}
		if !slices.Contains(c.RequiresTags, "write") {
			t.Errorf("%s mutates but does not require the write tag, "+
				"so a reader build would contain it", c.Name())
		}
		if !slices.Contains(c.ExitCodes, exitcode.Blocked) {
			t.Errorf("%s mutates but does not declare exit %d (%s), "+
				"which read-only mode produces", c.Name(), exitcode.Blocked, exitcode.Blocked)
		}
	})
}

// TestLocalStateCommandsExistInEveryBuild is the counterpart to the write tag.
// A build that could not create a context or store a credential could not be
// configured at all, which would make the reader profile useless rather than
// safe.
func TestLocalStateCommandsExistInEveryBuild(t *testing.T) {
	forEachCommand(t, func(t *testing.T, c *registry.Command) {
		if !c.LocalState {
			return
		}
		if c.Mutating {
			t.Errorf("%s is marked both LocalState and Mutating; "+
				"LocalState is for local files, Mutating is for Jira", c.Name())
		}
		if slices.Contains(c.RequiresTags, "write") {
			t.Errorf("%s writes local state but requires the write tag, "+
				"so a reader build could not configure itself", c.Name())
		}
	})
}

// TestReaderBuildCannotMutate is the compile-out guarantee, checked against
// whatever tags this build actually has.
func TestReaderBuildCannotMutate(t *testing.T) {
	if buildinfo.CanWrite() {
		t.Skip("this build has the write tag")
	}
	for _, c := range cli.Registry().All() {
		if c.Mutating {
			t.Errorf("%s mutates but is present in a build without the write tag", c.Name())
		}
	}
}

// TestPaginatedCommandsDeclarePartial asserts a command that can truncate says
// so, because a caller pins the exit codes it handles.
func TestPaginatedCommandsDeclarePartial(t *testing.T) {
	forEachCommand(t, func(t *testing.T, c *registry.Command) {
		if c.Paginated && !slices.Contains(c.ExitCodes, exitcode.Partial) {
			t.Errorf("%s is paginated but does not declare exit %d (%s)",
				c.Name(), exitcode.Partial, exitcode.Partial)
		}
	})
}

// TestCommandsDeclareTheirTags asserts the compile-out contract: a command is
// present only in a build with every tag it needs, and it never names a tag
// this project does not define.
func TestCommandsDeclareTheirTags(t *testing.T) {
	forEachCommand(t, func(t *testing.T, c *registry.Command) {
		for _, tag := range c.RequiresTags {
			if !slices.Contains(buildinfo.KnownTags, tag) {
				t.Errorf("%s requires undocumented tag %q; add it to buildinfo.KnownTags",
					c.Name(), tag)
			}
		}
		if missing := buildinfo.MissingTags(c.RequiresTags); len(missing) > 0 {
			t.Errorf("%s is registered in a build missing its required tag(s) %v; "+
				"move its registration into a file gated on those tags",
				c.Name(), missing)
		}
	})
}

// TestCommandsDeclareTheirOutput asserts every command names the payload shape
// it emits, at a version, so `jr --contract` is complete.
func TestCommandsDeclareTheirOutput(t *testing.T) {
	forEachCommand(t, func(t *testing.T, c *registry.Command) {
		// A command that owns stdout writes a stream rather than a result
		// document, so it has no kind to declare.
		if !c.EmitsDocument() {
			if len(c.Outputs) > 0 {
				t.Errorf("%s owns stdout but also declares an output kind; "+
					"rendering one would put an unparseable frame on the wire", c.Name())
			}
			return
		}
		if len(c.Outputs) == 0 {
			t.Errorf("%s declares no output kind", c.Name())
			return
		}
		seen := map[string]bool{}
		for _, o := range c.Outputs {
			switch {
			case o.Kind == "":
				t.Errorf("%s declares an output with no kind", c.Name())
			case o.Version < 1:
				t.Errorf("%s declares kind %q with no schema version", c.Name(), o.Kind)
			case seen[o.Kind]:
				t.Errorf("%s declares kind %q twice", c.Name(), o.Kind)
			}
			seen[o.Kind] = true
		}
		for _, o := range c.Outputs[1:] {
			if o.When == "" {
				t.Errorf("%s declares kind %q as an alternative output "+
					"without saying what selects it", c.Name(), o.Kind)
			}
		}
	})
}

// TestStdoutOwnersEmitNothingElse is the rule a live run caught and the tests
// had missed: a protocol server cannot also emit a result document, because the
// document lands on the wire as a frame its peer cannot parse.
func TestStdoutOwnersEmitNothingElse(t *testing.T) {
	forEachCommand(t, func(t *testing.T, c *registry.Command) {
		if !c.OwnsStdout {
			return
		}
		if c.Streams() {
			t.Errorf("%s owns stdout and also streams a collection to it", c.Name())
		}
		if c.Paginated {
			t.Errorf("%s owns stdout but is marked paginated", c.Name())
		}
	})
}

// TestKindVersionsAreConsistent asserts two commands never claim different
// schema versions for the same kind.
func TestKindVersionsAreConsistent(t *testing.T) {
	version, owner := map[string]int{}, map[string]string{}
	for _, c := range cli.Registry().All() {
		for _, o := range c.Outputs {
			if prev, seen := version[o.Kind]; seen && prev != o.Version {
				t.Errorf("kind %q is v%d on %s but v%d on %s",
					o.Kind, prev, owner[o.Kind], o.Version, c.Name())
				continue
			}
			version[o.Kind], owner[o.Kind] = o.Version, c.Name()
		}
	}
}

// TestCommandsAreDescribed asserts the self-description is usable without
// external documentation.
func TestCommandsAreDescribed(t *testing.T) {
	forEachCommand(t, func(t *testing.T, c *registry.Command) {
		if c.Summary == "" {
			t.Errorf("%s has no summary", c.Name())
		}
		if strings.HasSuffix(c.Summary, ".") {
			t.Errorf("%s: summary should not end with a period: %q", c.Name(), c.Summary)
		}
		if c.Example == "" {
			t.Errorf("%s has no example invocation", c.Name())
		}
		switch {
		case c.Run == nil && c.Stream == nil:
			t.Errorf("%s has no implementation", c.Name())
		case c.Run != nil && c.Stream != nil:
			t.Errorf("%s declares both Run and Stream; Run emits a record, "+
				"Stream emits a collection", c.Name())
		}
		for _, f := range c.Flags {
			if f.Usage == "" {
				t.Errorf("%s: --%s has no usage string", c.Name(), f.Name)
			}
			if f.Type == registry.TypeEnum && len(f.Enum) == 0 {
				t.Errorf("%s: --%s is an enum with no values", c.Name(), f.Name)
			}
			if f.Type != registry.TypeEnum && len(f.Enum) > 0 {
				t.Errorf("%s: --%s declares values but is typed %q", c.Name(), f.Name, f.Type)
			}
		}
	})
}

// TestStreamingCommandsDeclareTheirCollection asserts a streaming command says
// what its rows look like.
//
// The stream is opened — and for TSV its header written — before the first page
// lands, so the container name and columns have to be on the declaration rather
// than arriving with the data.
func TestStreamingCommandsDeclareTheirCollection(t *testing.T) {
	forEachCommand(t, func(t *testing.T, c *registry.Command) {
		if !c.Streams() {
			if c.CollectionName != "" || len(c.Columns) > 0 {
				t.Errorf("%s describes a collection but does not stream one", c.Name())
			}
			return
		}
		if c.CollectionName == "" {
			t.Errorf("%s streams but names no container element", c.Name())
		}
		if len(c.Columns) == 0 {
			t.Errorf("%s streams but declares no columns, so TSV would have "+
				"nothing to emit", c.Name())
		}
		// A streaming command emits a collection, which is paginated by
		// definition and can therefore be truncated.
		if !c.Paginated {
			t.Errorf("%s streams a collection but is not marked paginated", c.Name())
		}
	})
}

// TestArgsAreWellFormed asserts required positionals come first and only the
// last one is variadic, so the usage line is unambiguous.
func TestArgsAreWellFormed(t *testing.T) {
	forEachCommand(t, func(t *testing.T, c *registry.Command) {
		optionalSeen := false
		for i, a := range c.Args {
			if a.Name == "" {
				t.Errorf("%s: positional argument %d has no name", c.Name(), i)
			}
			if a.Usage == "" {
				t.Errorf("%s: positional argument %q has no usage string", c.Name(), a.Name)
			}
			if a.Required && optionalSeen {
				t.Errorf("%s: required argument %q follows an optional one", c.Name(), a.Name)
			}
			if !a.Required {
				optionalSeen = true
			}
			if a.Variadic && i != len(c.Args)-1 {
				t.Errorf("%s: variadic argument %q is not last", c.Name(), a.Name)
			}
		}
	})
}

// TestNamesAreNounVerb asserts the command surface stays uniform.
func TestNamesAreNounVerb(t *testing.T) {
	forEachCommand(t, func(t *testing.T, c *registry.Command) {
		for _, seg := range c.Path {
			if seg == "" {
				t.Errorf("%s has an empty path segment", c.Name())
			}
			if seg != strings.ToLower(seg) {
				t.Errorf("%s: path segment %q is not lowercase", c.Name(), seg)
			}
			if strings.ContainsAny(seg, " ._") {
				t.Errorf("%s: path segment %q should be a single kebab-case word", c.Name(), seg)
			}
		}
	})
}

// TestBuildDeclaresOnlyDocumentedTags catches a tag file added without
// documenting what the tag means.
func TestBuildDeclaresOnlyDocumentedTags(t *testing.T) {
	if unknown := buildinfo.UnknownTags(); len(unknown) > 0 {
		t.Errorf("build enables undocumented tag(s) %v; add them to buildinfo.KnownTags",
			unknown)
	}
	for _, tag := range buildinfo.KnownTags {
		if buildinfo.TagDescriptions[tag] == "" {
			t.Errorf("tag %q has no description", tag)
		}
	}
}

// forEachCommand runs check against every command in this build. A build with
// no commands is itself a failure: the registry is what the binary is.
func forEachCommand(t *testing.T, check func(*testing.T, *registry.Command)) {
	t.Helper()
	cmds := cli.Registry().All()
	if len(cmds) == 0 {
		t.Fatal("this build registers no commands")
	}
	for _, c := range cmds {
		t.Run(c.Name(), func(t *testing.T) { check(t, c) })
	}
}

// TestEveryKindDeclaresItsShape closes the gap §3.5 left open: `jr contract`
// used to report a kind's name, version, and emitters, which is enough to pin a
// version and not enough to verify a response against it.
//
// A kind with no schema is not merely undocumented. render.Doc.Validate skips
// the conformance check when it finds none, so an unregistered kind is a
// payload nothing holds to any shape at all.
func TestEveryKindDeclaresItsShape(t *testing.T) {
	for _, k := range cli.Registry().Kinds() {
		if _, ok := render.SchemaFor(k.Name); !ok {
			t.Errorf("kind %q has no schema; a consumer can pin it and cannot "+
				"verify it, and nothing checks what this build emits for it",
				k.Name)
		}
	}
}

// TestEverySchemaBelongsToAKind is the other direction. A schema for a kind no
// command emits is a shape published for a payload that does not exist.
func TestEverySchemaBelongsToAKind(t *testing.T) {
	emitted := map[string]bool{}
	for _, k := range cli.Registry().Kinds() {
		emitted[k.Name] = true
	}
	for _, kind := range render.RegisteredKinds() {
		if !emitted[kind] {
			t.Errorf("a schema is registered for kind %q, which no command in "+
				"this build emits", kind)
		}
	}
}

// TestEveryColumnNamesAValue is the test the TSV writer already claimed
// existed.
//
// writeTSV resolves each column against the item and takes an empty string when
// the path leads nowhere, with a comment saying the contract tests assert the
// path is resolvable. They did not. `project statuses` declared a column over a
// list element, and because a container resolves to its own (empty) text, every
// row on both deployments carried a blank cell — through a convergence test, a
// golden file, and a per-deployment command test, none of which ever looked at
// a cell.
//
// A column that cannot show what its header says is the same defect as a flag
// that cannot do what it says, and this repository does not ship either.
func TestEveryColumnNamesAValue(t *testing.T) {
	for _, cmd := range registry.All() {
		if len(cmd.Columns) == 0 {
			continue
		}
		schema, ok := render.SchemaFor(cmd.Kind())
		if !ok || schema == nil {
			continue // Kinds without a registered schema are a separate check.
		}
		t.Run(strings.Join(cmd.Path, "."), func(t *testing.T) {
			for _, col := range cmd.Columns {
				if err := schema.ResolveColumn(col.Path); err != nil {
					t.Errorf("column %q: %v", col.Header, err)
				}
			}
		})
	}
}
