//go:build mcp

package mcp

import (
	"context"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"

	"github.com/kmoneil/jr/internal/errs"
	"github.com/kmoneil/jr/internal/registry"
	"github.com/kmoneil/jr/internal/render"
)

// FormatArgument is the name every tool exposes for choosing an output shape.
const FormatArgument = "format"

// Tool is one entry in a tools/list response.
type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

// Tools converts every registered command into a tool, except the ones that
// cannot be one.
//
// The list is the command list of this build. A reader binary advertises no
// mutating tools because it contains none — an agent introspecting the server
// sees the truth rather than a set of tools that will refuse.
func Tools(reg *registry.Registry) []Tool {
	commands := reg.All()
	out := make([]Tool, 0, len(commands))
	for _, cmd := range commands {
		if !servableAsTool(cmd) {
			continue
		}
		out = append(out, Tool{
			Name:        commandToTool(cmd.Name()),
			Description: toolDescription(cmd),
			InputSchema: inputSchema(cmd),
		})
	}
	return out
}

// servableAsTool reports whether a command can be run as a tool call.
//
// `OwnsStdout` means the command writes the byte stream itself for the life of
// the process, and this server is already writing JSON-RPC frames to that
// stream. `mcp serve` is the only such command, and calling it as a tool
// started a second server on the same stdin and stdout: the outer request never
// answered, because the outer server was blocked inside Command.Run, and the
// nested one consumed and replied to the frames that followed — from a server
// the client does not know exists. One call, and the session is gone.
//
// The predicate is `OwnsStdout` rather than the command's name, so the next
// command to set the flag inherits the refusal instead of the defect.
//
// `OwnsStdoutWhen` deliberately does *not* disqualify a command, and the two
// fields are not interchangeable here even though they read that way.
// `issue attachment download` sets it and is a perfectly good tool: with
// `output` naming a path it writes a file and returns the usual document, and
// only `output: "-"` would want the stream. That case is already refused, by
// the command's own Validate, which runs on this path and finds `inv.Stdout`
// nil — a tool call has no stdout to hand out. Hiding the whole command would
// remove a working tool to prevent a call that already fails cleanly.
// Two further classes are off the wire, and for a different reason from
// OwnsStdout: they are not operations on Jira at all.
//
// `LocalState` commands — `auth login`, `auth logout`, and the four `context`
// writers — change what this server *is*: which site it talks to, and with
// whose credential. A peer that can call them can widen its own authority,
// which makes any limit placed on it a default rather than a boundary. That is
// administrative, and the human does it with the CLI before the server starts.
//
// `auth token` is off for the sharper version of the same reason: it exists to
// print the credential. Serving it hands the secret to whatever is on the other
// end of the pipe, and no policy above that layer can matter afterwards.
//
// The *reading* siblings stay — `auth status`, `context list`, `context show`.
// They report where the server is pointed and reveal nothing, and an agent that
// cannot tell which site it is talking to is one that guesses.
//
// This is a description of the interface, not a security boundary. Authority at
// the CLI is exactly the credential, and a peer that can reach the credential
// or run its own `jr` is unaffected by anything decided here. See
// docs/build-profiles.md.
func servableAsTool(cmd *registry.Command) bool {
	switch {
	case cmd.OwnsStdout, cmd.LocalState:
		return false
	case cmd.Name() == revealsCredential:
		return false
	}
	return true
}

// revealsCredential is the one command whose whole purpose is to print the
// secret. Named rather than derived, because there is no declaration for "this
// output is the credential" and inventing one for a single command would be a
// field nothing else could ever set truthfully.
const revealsCredential = "auth.token"

// notServableReason says why a command is not a tool, because the two reasons
// need different things from the caller: one is a protocol constraint and the
// other is where the interface ends.
func notServableReason(cmd *registry.Command) string {
	switch {
	case cmd.OwnsStdout:
		return "it writes the stream this server speaks on"
	case cmd.Name() == revealsCredential:
		return "it prints the credential, which this server holds and does not disclose"
	case cmd.LocalState:
		return "it changes which site and credential this server uses, " +
			"which is done with the CLI before the server starts"
	}
	return "it cannot be called as a tool"
}

// commandToTool converts issue.list to issue_list.
//
// MCP tool names are conventionally identifier-shaped, and several clients
// reject a dot. The mapping is total and reversible, so nothing is lost.
func commandToTool(name string) string { return strings.ReplaceAll(name, ".", "_") }

// toolToCommand is the inverse.
func toolToCommand(name string) string { return strings.ReplaceAll(name, "_", ".") }

// toolDescription gives the model what it needs to choose and use the tool:
// what it does, whether it changes anything, and what it returns.
func toolDescription(cmd *registry.Command) string {
	var b strings.Builder
	b.WriteString(cmd.Summary)
	b.WriteString(".\n\n")
	if cmd.Description != "" {
		b.WriteString(cmd.Description)
		b.WriteString("\n\n")
	}

	fmt.Fprintf(&b, "Returns output of kind %s (schema v%d).\n",
		cmd.Kind(), cmd.KindVersion())
	switch {
	case cmd.Mutating:
		b.WriteString("This CHANGES Jira.\n")
	case cmd.LocalState:
		b.WriteString("This changes local configuration, not Jira.\n")
	default:
		b.WriteString("This only reads.\n")
	}
	if cmd.Destructive {
		b.WriteString("This is DESTRUCTIVE and requires yes=true.\n")
	}
	if cmd.Paginated {
		b.WriteString("A result cut short by limit is reported as incomplete " +
			"and carries a page token to resume from; it is never silently truncated.\n")
	}
	return strings.TrimSpace(b.String())
}

// inputSchema builds the JSON Schema for a tool's arguments.
func inputSchema(cmd *registry.Command) map[string]any {
	properties := map[string]any{}
	var required []string

	for _, arg := range cmd.Args {
		properties[arg.Name] = map[string]any{
			"type":        "string",
			"description": arg.Usage,
		}
		if arg.Required {
			required = append(required, arg.Name)
		}
	}

	// AllFlags carries --limit for a paginated command, with the default this
	// command actually uses. Spelling it out here instead read DefaultLimit
	// directly, so `jr schema` advertised "(default 50)" over MCP and answered
	// with fifty of sixty-two commands, while the same tool on the CLI
	// defaulted to "all" and answered with all of them.
	for _, flag := range cmd.AllFlags() {
		properties[flag.Name] = flagSchema(flag)
		if flag.Required {
			required = append(required, flag.Name)
		}
	}

	properties[FormatArgument] = map[string]any{
		"type":        "string",
		"enum":        render.FormatNames(),
		"description": "output format; defaults to tsv for lists and xml for records",
	}

	schema := map[string]any{
		"type":       "object",
		"properties": properties,
		// Refusing an unknown argument is the same rule as the CLI's: a
		// misspelled option that is silently ignored is a request that did
		// something other than what was asked.
		"additionalProperties": false,
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

func flagSchema(flag registry.Flag) map[string]any {
	schema := map[string]any{"description": flag.Usage}

	switch {
	case flag.Repeatable:
		schema["type"] = "array"
		schema["items"] = map[string]any{"type": "string"}
	case flag.Type == registry.TypeBool:
		schema["type"] = "boolean"
	case flag.Type == registry.TypeInt:
		schema["type"] = "integer"
	default:
		schema["type"] = "string"
	}
	if len(flag.Enum) > 0 {
		schema["enum"] = flag.Enum
	}
	if flag.Default != "" {
		schema["default"] = flag.Default
	}
	return schema
}

// invocation turns a tool call's arguments into what a command expects.
//
// An argument the tool did not advertise is refused rather than ignored. The
// schema says additionalProperties is false, and a server that then accepted
// them would be describing itself inaccurately — which is the drift this whole
// design exists to prevent.
func (s *Server) invocation(
	ctx context.Context, cmd *registry.Command, args map[string]any,
) (*registry.Invocation, render.Format, error) {
	if err := rejectUnknownArguments(cmd, args); err != nil {
		return nil, "", err
	}

	flags, err := resolveFlags(cmd, args)
	if err != nil {
		return nil, "", err
	}
	positional, err := resolvePositional(cmd, args)
	if err != nil {
		return nil, "", err
	}
	limit, err := resolveLimit(cmd, args)
	if err != nil {
		return nil, "", err
	}

	inv := &registry.Invocation{
		Args:  positional,
		Flags: flags,
		Limit: limit,
		// A tool call has no terminal and no stderr worth writing to, so
		// nothing reports progress and nothing writes a diagnostic anywhere
		// but into the reply.
		Stderr:   io.Discard,
		Progress: registry.NoProgress,
	}

	if cmd.NeedsJira {
		if inv.Jira, err = s.session(); err != nil {
			return nil, "", err
		}
	}

	// Read-only and confirmation, from the declaration, in the same place and
	// the same order the CLI applies them: before Validate and before any
	// network call. A tool call is the one path where nobody is watching, so
	// this is where §6's guarantees are worth the most and where they were
	// missing — the gate lived in the CLI wrapper and this layer calls
	// Command.Run directly.
	if err := registry.Gate(cmd, inv); err != nil {
		return nil, "", err
	}

	if cmd.Validate != nil {
		if err := cmd.Validate(ctx, inv); err != nil {
			return nil, "", err
		}
	}

	format, err := resolveFormat(cmd, args)
	if err != nil {
		return nil, "", err
	}
	return inv, format, nil
}

// rejectUnknownArguments refuses an argument the tool did not advertise.
func rejectUnknownArguments(cmd *registry.Command, args map[string]any) error {
	known := map[string]bool{FormatArgument: true}
	for _, arg := range cmd.Args {
		known[arg.Name] = true
	}
	for _, flag := range cmd.AllFlags() {
		known[flag.Name] = true
	}
	for name := range args {
		if !known[name] {
			return errs.Usage("UNKNOWN_ARGUMENT",
				"%s has no argument %q", commandToTool(cmd.Name()), name).
				WithRemedy("call tools/list for the arguments this tool accepts")
		}
	}
	return nil
}

func resolveFlags(cmd *registry.Command, args map[string]any) (registry.Flags, error) {
	flags := registry.NewFlags()
	for _, flag := range cmd.AllFlags() {
		raw, ok := args[flag.Name]
		if !ok {
			continue
		}
		if err := setFlag(flags, flag, raw); err != nil {
			return registry.Flags{}, err
		}
	}
	return flags, nil
}

func resolvePositional(cmd *registry.Command, args map[string]any) ([]string, error) {
	out := make([]string, 0, len(cmd.Args))
	for _, arg := range cmd.Args {
		raw, ok := args[arg.Name]
		if !ok {
			if arg.Required {
				return nil, errs.Usage("MISSING_ARGUMENT",
					"%s requires %q", commandToTool(cmd.Name()), arg.Name)
			}
			continue
		}
		text, err := asString(arg.Name, raw)
		if err != nil {
			return nil, err
		}
		out = append(out, text)
	}
	return out, nil
}

// resolveLimit reads the limit argument, defaulting the way the command's own
// declaration does.
//
// It used to start from registry.DefaultLimit unconditionally, which ignored
// DefaultsToAll and made `jr schema` over MCP return fifty of sixty-two
// commands with complete="false": every sprint and user command invisible to
// an agent introspecting the binary through the interface built for it. Parsing
// the declared default is what keeps one answer for one question.
func resolveLimit(cmd *registry.Command, args map[string]any) (registry.Limit, error) {
	limit, err := registry.ParseLimit(cmd.LimitFlag().Default)
	if err != nil {
		return registry.Limit{}, err
	}
	raw, ok := args["limit"]
	if !ok {
		return limit, nil
	}
	text, err := asString("limit", raw)
	if err != nil {
		return limit, err
	}
	return registry.ParseLimit(text)
}

func (s *Server) session() (registry.Session, error) {
	if s.opt.Session == nil {
		return nil, errs.Runtime("NO_SESSION", "this server has no way to reach Jira")
	}
	return s.opt.Session()
}

// resolveFormat picks the output shape, defaulting the same way the CLI does:
// TSV for a collection, XML for a record.
func resolveFormat(cmd *registry.Command, args map[string]any) (render.Format, error) {
	if raw, ok := args[FormatArgument]; ok {
		text, err := asString(FormatArgument, raw)
		if err != nil {
			return "", err
		}
		return render.ParseFormat(text)
	}
	if cmd.Streams() {
		return render.TSV, nil
	}
	return render.XML, nil
}

func setFlag(flags registry.Flags, flag registry.Flag, raw any) error {
	if flag.Repeatable {
		return setRepeatable(flags, flag, raw)
	}
	return setScalar(flags, flag, raw)
}

func setRepeatable(flags registry.Flags, flag registry.Flag, raw any) error {
	values, ok := raw.([]any)
	if !ok {
		// A single value where a list was advertised is a common and harmless
		// mistake; accept it rather than refusing over shape.
		values = []any{raw}
	}
	for _, v := range values {
		text, err := asString(flag.Name, v)
		if err != nil {
			return err
		}
		flags.SetString(flag.Name, text)
	}
	return nil
}

func setScalar(flags registry.Flags, flag registry.Flag, raw any) error {
	switch flag.Type {
	case registry.TypeBool:
		b, ok := raw.(bool)
		if !ok {
			return errs.Usage("INVALID_ARGUMENT", "%q expects true or false", flag.Name).
				WithDetail("got %T", raw)
		}
		flags.SetBool(flag.Name, b)
	case registry.TypeInt:
		f, ok := raw.(float64)
		if !ok {
			return errs.Usage("INVALID_ARGUMENT", "%q expects a number", flag.Name).
				WithDetail("got %T", raw)
		}
		n, err := wholeNumber(flag.Name, f)
		if err != nil {
			return err
		}
		flags.SetInt(flag.Name, n)
	default:
		text, err := asString(flag.Name, raw)
		if err != nil {
			return err
		}
		if len(flag.Enum) > 0 && !contains(flag.Enum, text) {
			return errs.Usage("INVALID_ARGUMENT",
				"%q does not accept %q", flag.Name, text).
				WithDetail("valid values: %s", strings.Join(flag.Enum, ", "))
		}
		flags.SetString(flag.Name, text)
	}
	return nil
}

// wholeNumber converts a JSON number to an int, refusing what the conversion
// cannot represent.
//
// JSON has one numeric type and Go decodes it as float64, so `int(f)` was
// doing three things silently. An out-of-range value is implementation-defined
// by the Go spec — on amd64 `int(1e30)` is the most negative int64 — and NaN is
// too. A fraction was truncated, so `page-size: 2.7` meant 2 and said nothing.
//
// Nothing reachable today turns that into a defect: `--page-size` is the only
// registry int flag in the tree, and resolvePageSize range-checks it, so an
// overflowed value is refused one layer down with a message about page size.
// That is the reason to fix it rather than the reason not to — the check that
// currently saves it belongs to a different flag's validation, and the next int
// flag inherits the narrowing without inheriting the rescue.
//
// The bound is float64's exact-integer range rather than math.MaxInt. Beyond
// 2^53 a float64 cannot distinguish adjacent integers, so a value that
// converted "successfully" would be a number the peer did not send. Refusing is
// the honest answer, and no flag this tool has is anywhere near it.
//
// # What this cannot catch, and why that is not fixed here
//
// 2^53+1 arrives as 2^53. json.Unmarshal decoded it into a float64 before this
// function existed, so the rounding happened at a layer above and the value
// here is one the peer could legitimately have sent. There is no check that
// distinguishes them.
//
// Catching it means decoding with json.Number and parsing each argument from
// its literal text — which changes every numeric path in this file, including
// asString's float64 case, for a magnitude no flag in this tool approaches:
// --page-size is the only registry int flag and its ceiling is three digits.
// Written down rather than done, so the gap is a decision instead of a
// discovery, and so the argument is here when a flag ever does need that range.
func wholeNumber(name string, f float64) (int, error) {
	const exactIntegerRange = 1 << 53

	refuse := func(why string) error {
		return errs.Usage("INVALID_ARGUMENT", "%q expects a whole number", name).
			WithDetail("%s", why).
			WithRemedy("pass an integer within ±%d", exactIntegerRange)
	}

	switch {
	case math.IsNaN(f):
		return 0, refuse("got NaN")
	case math.IsInf(f, 0):
		return 0, refuse("got infinity")
	case f != math.Trunc(f):
		return 0, refuse(fmt.Sprintf("got %v, which is not whole", f))
	case f > exactIntegerRange || f < -exactIntegerRange:
		return 0, refuse(fmt.Sprintf("got %v, which is out of range", f))
	}
	return int(f), nil
}

// asString accepts the JSON scalars a model is likely to produce for something
// the schema calls a string, rather than refusing over a type it got nearly
// right.
func asString(name string, raw any) (string, error) {
	switch v := raw.(type) {
	case string:
		return v, nil
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64), nil
	case bool:
		return strconv.FormatBool(v), nil
	default:
		return "", errs.Usage("INVALID_ARGUMENT", "%q expects a string", name).
			WithDetail("got %T", raw)
	}
}

func contains(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}
