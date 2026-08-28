package registry

import (
	"slices"
	"strconv"
	"strings"

	"github.com/kmoneil/jr/internal/render"
	"github.com/kmoneil/jr/internal/transport"
)

// Effect says what a global flag reaches on the command that inherits it.
//
// It is per command-and-flag rather than per flag, because the same global
// means different things depending on where it lands: --project decides which
// slice of Jira `issue list` reports on, and decides nothing at all about
// `auth login`. Publishing one intrinsic classification would put --project in
// front of every command as though it filtered every one of them, which is the
// original defect at a larger size.
type Effect string

const (
	// EffectResult marks a global that silently narrows what the answer is:
	// which slice of a Jira the command reports on. Nothing in the result
	// document says one of these was in effect, which is what makes it the
	// attribute worth publishing — a caller who does not know --project is set
	// reads a complete, exit-0, 23%-short answer and cannot tell.
	EffectResult Effect = "result"

	// EffectProvenance marks a global that decides *which Jira* answers:
	// --site and --context. It changes the answer as thoroughly as anything
	// here and it is never silent, because every document from a command that
	// reached a site carries site="…" in its envelope. A caller can always
	// tell which one they got, so it is a different category from the one
	// above rather than a milder version of it.
	EffectProvenance Effect = "provenance"

	// EffectInvocation marks a global that changes how the command runs and
	// how its answer is delivered: the format, the transport, the gates. Run
	// the same command twice with one of these set differently and the answer
	// is the same answer, printed differently or fetched differently or
	// refused.
	EffectInvocation Effect = "invocation"
)

// Global flag names, so a command's ScopedBy cannot name one by a string that
// no longer exists.
const (
	GlobalFormat      = "format"
	GlobalDescribe    = "describe"
	GlobalContext     = "context"
	GlobalSite        = "site"
	GlobalProject     = "project"
	GlobalBoard       = "board"
	GlobalAPIVersion  = "api-version"
	GlobalCABundle    = "ca-bundle"
	GlobalReadOnly    = "readonly"
	GlobalDebug       = "debug"
	GlobalRefresh     = "refresh"
	GlobalRetries     = "retries"
	GlobalMaxRequests = "max-requests"
)

// GlobalFlags are the flags declared once and inherited by every command.
//
// They live here rather than on the cobra root because everything downstream of
// a declaration reads the registry: `jr schema`, docs/commands.md, and the MCP
// tool list. Thirteen flags declared straight onto cobra were bound, accepted,
// documented in --help, and absent from every one of those, which made
// `flags count="7"` on `jr issue activity` a count of the flags that were not
// filtering the caller's results.
//
// The usage strings are the ones the root used to carry, unchanged. Moving a
// declaration is not the moment to reword it.
func GlobalFlags() []Flag {
	return []Flag{
		{
			Name: GlobalFormat, Type: TypeEnum, Enum: render.FormatNames(),
			Usage: formatUsage(),
		},
		{
			Name: GlobalDescribe, Type: TypeBool,
			Usage: "print this command's schema instead of running it",
		},
		{
			Name: GlobalContext, Type: TypeString,
			Usage: "use this context for one invocation, without selecting it",
		},
		{
			Name: GlobalSite, Type: TypeString,
			Usage: "Jira site, overriding the context's",
		},
		{
			Name: GlobalProject, Type: TypeString,
			Usage: "project key, overriding the context's",
		},
		{
			Name: GlobalBoard, Type: TypeString,
			Usage: "board id, overriding the context's",
		},
		{
			Name: GlobalAPIVersion, Type: TypeString,
			Usage: "force the REST version, 2 or 3, and skip the deployment probe",
		},
		{
			Name: GlobalCABundle, Type: TypeString,
			Usage: "PEM file of certificates to trust in addition to the system roots",
		},
		{
			Name: GlobalReadOnly, Type: TypeBool,
			Usage: "refuse any command that would change Jira",
		},
		{
			Name: GlobalDebug, Type: TypeBool,
			Usage: "trace HTTP requests to stderr; credentials are redacted in the transport",
		},
		{
			Name: GlobalRefresh, Type: TypeBool,
			Usage: "ignore cached site metadata and probe again",
		},
		{
			Name: GlobalRetries, Type: TypeInt,
			Default: strconv.Itoa(transport.DefaultRetries),
			Usage:   "retry budget per request; exhausting it exits 8 or 9, never 0",
		},
		{
			Name: GlobalMaxRequests, Type: TypeInt, Default: "0",
			Usage: "cap total HTTP calls for this invocation; 0 means no cap",
		},
	}
}

// GlobalFlag returns the named global, and whether there is one.
func GlobalFlag(name string) (Flag, bool) {
	for _, f := range GlobalFlags() {
		if f.Name == name {
			return f, true
		}
	}
	return Flag{}, false
}

// InheritedGlobals returns the globals this command actually inherits.
//
// A command that declares a flag of its own by the same name **shadows** the
// global, and cobra already knows it: `jr meta createmeta --help` prints
// --project under Flags and omits it from Global Flags, because the local
// declaration is the one the value lands in. There is exactly one such
// collision in the tree today and this is not a workaround for it — describing
// an inherited flag the binary routes somewhere else would be the same lie as
// not describing it at all, told the other way round.
func (c *Command) InheritedGlobals() []Flag {
	declared := c.AllFlags()
	out := make([]Flag, 0, len(GlobalFlags()))
	for _, g := range GlobalFlags() {
		if slices.ContainsFunc(declared, func(f Flag) bool { return f.Name == g.Name }) {
			continue
		}
		out = append(out, g)
	}
	return out
}

// GlobalEffect reports what a global reaches on this command.
//
// ScopedBy is the only hand-written input, and it names the silent half.
// Provenance is derived, because a command either resolves a session or does
// not and there is nothing to declare: --site and --context decide which Jira
// answers `issue list` and decide nothing about `version`.
func (c *Command) GlobalEffect(name string) Effect {
	switch {
	case slices.Contains(c.ScopedBy, name):
		return EffectResult
	case c.NeedsJira && (name == GlobalSite || name == GlobalContext):
		return EffectProvenance
	default:
		return EffectInvocation
	}
}

// EffectDescriptions explains each value, for the documentation generated from
// this package. They are here rather than in the doc so the two cannot drift.
var EffectDescriptions = map[Effect]string{
	EffectResult: "narrows what this command's result set is, and nothing in " +
		"the result says so",
	EffectProvenance: "decides which Jira answers; the envelope's site " +
		"attribute reports which one did",
	EffectInvocation: "changes how the command runs or how its answer is " +
		"printed, not what the answer is",
}

// EnvFormat overrides the default output format for every command.
//
// It is here rather than in internal/cli because the flag it backs is declared
// here, and the usage string names it. A caller reads this name out of
// `jr schema`, which makes it part of the described surface.
const EnvFormat = "JIRA_FORMAT"

// formatUsage describes --format for this build, which is not the same string
// in every build: a profile without the presentational writer has one fewer
// format and no sentence about it.
func formatUsage() string {
	usage := "output format: " + strings.Join(render.FormatNames(), "|") +
		" (default: tsv for lists, xml for records"
	if name, ok := presentationalName(); ok {
		usage += "; " + name + " is for reading and is not a versioned contract"
	}
	return usage + "). " + EnvFormat + " sets it for every command"
}

// presentationalName is the presentation format this build has, if any.
func presentationalName() (string, bool) {
	for _, f := range render.Formats() {
		if render.Presentational(f) {
			return string(f), true
		}
	}
	return "", false
}
